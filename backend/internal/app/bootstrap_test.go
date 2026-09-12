package app_test

import (
	"context"
	"sync"
	"testing"

	"gbos/internal/app"
	repo "gbos/internal/repo/postgres"
	authsvc "gbos/internal/service/auth"
	"gbos/internal/testsupport"
)

func TestBootstrapCreatesASignableInAccount(t *testing.T) {
	t.Parallel()
	db := testsupport.OpenDB(t)
	ctx := context.Background()

	got, err := app.EnsureBootstrap(ctx, db)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !got.Created {
		t.Fatal("nothing was created on an empty database")
	}
	if !got.Generated || got.Password == "" {
		t.Fatal("no password was generated to sign in with")
	}

	// The point of bootstrapping is that somebody can get in. Prove it by
	// actually signing in rather than checking a row exists.
	auth, err := authsvc.New(db, repo.NewUser(), authsvc.Config{Secret: "bootstrap-test-secret-value-32-chars"})
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	token, user, err := auth.Login(ctx, got.Username, got.Password)
	if err != nil {
		t.Fatalf("sign in as the bootstrapped owner: %v", err)
	}
	if token == "" {
		t.Error("no token issued")
	}
	if user.Role != "owner" {
		t.Errorf("role = %q, want owner", user.Role)
	}

	// A counter must exist, or the shop cannot number an invoice and so
	// cannot sell anything.
	var terminals int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM terminals WHERE store_id = $1`, user.StoreID).Scan(&terminals); err != nil {
		t.Fatalf("count terminals: %v", err)
	}
	if terminals != 1 {
		t.Errorf("%d terminals created, want 1", terminals)
	}
}

func TestBootstrapPlantsNoDemoData(t *testing.T) {
	t.Parallel()
	db := testsupport.OpenDB(t)
	ctx := context.Background()

	if _, err := app.EnsureBootstrap(ctx, db); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// The seeding this replaces planted three products, a wholesale customer,
	// a supplier, and cashier/helper accounts whose passwords were published
	// in the repository. A shop's catalogue is its own.
	for _, table := range []string{"products", "customers", "vendors", "stock", "categories"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d demo rows, want none", table, n)
		}
	}

	var users int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("%d users created, want only the owner", users)
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	t.Parallel()
	db := testsupport.OpenDB(t)
	ctx := context.Background()

	first, err := app.EnsureBootstrap(ctx, db)
	if err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if !first.Created {
		t.Fatal("the first run created nothing")
	}

	// It runs on every start, so a second call must be a no-op — not a second
	// owner, and certainly not a second password nobody was told about.
	second, err := app.EnsureBootstrap(ctx, db)
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if second.Created {
		t.Error("the second run created another account")
	}

	var users int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("%d users exist after two runs, want 1", users)
	}
}

func TestConcurrentBootstrapsCreateOneOwner(t *testing.T) {
	t.Parallel()
	db := testsupport.OpenDB(t)
	ctx := context.Background()

	// Two instances starting together would both find the database empty.
	// There is no row to lock, so the advisory lock is what settles it.
	const racers = 4
	var wg sync.WaitGroup
	results := make([]app.Bootstrap, racers)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = app.EnsureBootstrap(ctx, db)
		}(i)
	}
	wg.Wait()

	created := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
		if results[i].Created {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d racers created an owner, want exactly 1", created)
	}

	var users int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("%d users exist, want 1", users)
	}
}
