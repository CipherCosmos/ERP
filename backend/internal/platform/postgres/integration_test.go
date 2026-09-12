package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests need a real Postgres. They are skipped unless TEST_DATABASE_URL
// is set, so that `go test ./...` stays hermetic on a laptop with no server.
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/gbos_test?sslmode=disable \
//	    go test ./internal/platform/postgres/... -race
//
// The containerised harness that supplies that variable in CI is another
// workstream's; nothing here starts a container.
//
// The unit tests in tx_test.go cover the runner's control flow against a fake
// driver. What these add is the behaviour only a real server produces: actual
// SQLSTATEs from actual constraints, and real serialization conflicts.

// requirePostgres returns a connected *DB, or skips.
func requirePostgres(t *testing.T) *DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping Postgres integration tests")
	}

	// driver.go registers "pgx" as a side effect of importing this package, so
	// this only trips if DATABASE_DRIVER names something else.
	driverName := os.Getenv("DATABASE_DRIVER")
	if driverName == "" {
		driverName = DefaultDriverName
	}
	if !slices.Contains(sql.Drivers(), driverName) {
		t.Fatalf("no database/sql driver registered as %q; registered drivers: %v",
			driverName, sql.Drivers())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := Open(ctx, Config{DSN: dsn, DriverName: driverName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// setupSchema creates a throwaway schema with the constraints these tests
// exercise, and drops it afterwards.
func setupSchema(t *testing.T, db *DB) string {
	t.Helper()
	ctx := context.Background()

	schema := fmt.Sprintf("gbos_test_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})

	stmts := []string{
		`CREATE TABLE ` + schema + `.categories (
			id   int PRIMARY KEY,
			name text NOT NULL
		)`,
		`CREATE TABLE ` + schema + `.products (
			id          int PRIMARY KEY,
			sku         text NOT NULL,
			category_id int REFERENCES ` + schema + `.categories(id),
			qty         int NOT NULL DEFAULT 0,
			CONSTRAINT products_sku_key UNIQUE (sku),
			CONSTRAINT products_qty_non_negative CHECK (qty >= 0)
		)`,
		`INSERT INTO ` + schema + `.categories (id, name) VALUES (1, 'Grocery')`,
		`INSERT INTO ` + schema + `.products (id, sku, category_id, qty)
		 VALUES (1, 'ABC-123', 1, 10)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	return schema
}

func TestIntegrationPing(t *testing.T) {
	db := requirePostgres(t)

	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestIntegrationPingHonoursContext checks the claim the package doc makes:
// every call takes a context and a cancelled one stops the work. None of the
// 219 existing query sites can do this.
func TestIntegrationPingHonoursContext(t *testing.T) {
	db := requirePostgres(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := db.Ping(ctx)
	if err == nil {
		t.Fatal("Ping succeeded on a cancelled context")
	}
	if !errors.Is(err, ErrCanceled) {
		t.Errorf("Ping = %v, want ErrCanceled", err)
	}
}

func TestIntegrationOpenBadDSN(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := Open(ctx, Config{
		DSN: "postgres://nobody:wrong@127.0.0.1:1/nonexistent?sslmode=disable",
	})
	if err == nil {
		t.Fatal("Open returned a usable pool for an unreachable database")
	}
}

func TestIntegrationEmptyDSN(t *testing.T) {
	_, err := Open(context.Background(), Config{})
	if err == nil {
		t.Fatal("Open with an empty DSN returned no error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error %q should point at DATABASE_URL", err)
	}
}

func TestIntegrationInTxCommits(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)
	ctx := context.Background()

	err := db.InTx(ctx, func(ctx context.Context, tx Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO `+schema+`.products (id, sku, category_id) VALUES (2, 'NEW-1', 1)`)
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+schema+`.products WHERE sku = 'NEW-1'`).Scan(&count); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count != 1 {
		t.Fatalf("committed row count = %d, want 1", count)
	}
}

func TestIntegrationInTxRollsBack(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)
	ctx := context.Background()

	sentinel := errors.New("abort")
	err := db.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+schema+`.products (id, sku, category_id) VALUES (3, 'GONE-1', 1)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx = %v, want %v", err, sentinel)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+schema+`.products WHERE sku = 'GONE-1'`).Scan(&count); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back row is still present (count = %d)", count)
	}
}

func TestIntegrationInTxPanicRollsBack(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)
	ctx := context.Background()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("the panic did not propagate")
			}
		}()
		_ = db.InTx(ctx, func(ctx context.Context, tx Tx) error {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO `+schema+`.products (id, sku, category_id) VALUES (4, 'PANIC-1', 1)`); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+schema+`.products WHERE sku = 'PANIC-1'`).Scan(&count); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count != 0 {
		t.Fatalf("a panicking transaction left its row behind (count = %d)", count)
	}
}

// TestIntegrationRealSQLStates is the mapping table checked against a real
// server, so that a hand-written SQLSTATE constant cannot drift from what
// Postgres actually reports.
func TestIntegrationRealSQLStates(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)
	ctx := context.Background()

	tests := []struct {
		name           string
		stmt           string
		want           error
		wantConstraint string
	}{
		{
			name:           "unique violation",
			stmt:           `INSERT INTO ` + schema + `.products (id, sku) VALUES (99, 'ABC-123')`,
			want:           ErrConflict,
			wantConstraint: "products_sku_key",
		},
		{
			name: "foreign key violation",
			stmt: `INSERT INTO ` + schema + `.products (id, sku, category_id) VALUES (98, 'FK-1', 4242)`,
			want: ErrReference,
		},
		{
			name:           "check violation",
			stmt:           `INSERT INTO ` + schema + `.products (id, sku, qty) VALUES (97, 'CHK-1', -5)`,
			want:           ErrConstraint,
			wantConstraint: "products_qty_non_negative",
		},
		{
			name: "not null violation",
			stmt: `INSERT INTO ` + schema + `.products (id, sku) VALUES (96, NULL)`,
			want: ErrConstraint,
		},
		{
			name: "invalid text representation",
			stmt: `SELECT 'not-a-number'::int`,
			want: ErrInvalidInput,
		},
		{
			name: "undefined table stays unclassified",
			stmt: `SELECT * FROM ` + schema + `.no_such_table`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, tt.stmt)
			if err == nil {
				t.Fatal("statement unexpectedly succeeded")
			}

			if tt.want == nil {
				for _, sentinel := range []error{ErrConflict, ErrReference, ErrConstraint, ErrNotFound} {
					if errors.Is(err, sentinel) {
						t.Errorf("error matched %v; it should stay unclassified", sentinel)
					}
				}
				return
			}

			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v (SQLSTATE %s), want %v", err, SQLState(err), tt.want)
			}
			if tt.wantConstraint != "" {
				if got := ConstraintName(err); got != tt.wantConstraint {
					t.Errorf("ConstraintName = %q, want %q", got, tt.wantConstraint)
				}
			}
		})
	}
}

func TestIntegrationNoRows(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)
	ctx := context.Background()

	var sku string
	err := MapError(db.QueryRowContext(ctx,
		`SELECT sku FROM `+schema+`.products WHERE id = -1`).Scan(&sku))

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestIntegrationSerializationRetry drives two serializable transactions into a
// genuine 40001 and checks that InTxRetry resolves it. This is the credit-limit
// and stock-reservation pattern: read a total, then write a decision based on
// it.
func TestIntegrationSerializationRetry(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)
	ctx := context.Background()

	const workers = 4
	var wg sync.WaitGroup
	errs := make([]error, workers)

	// Each worker reads the current qty and writes qty-1 based on what it read.
	// Under SERIALIZABLE that is exactly the pattern Postgres refuses to get
	// wrong, so the losers get 40001 and must be replayed.
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			errs[w] = db.InTxRetry(ctx, Serializable, 10, func(ctx context.Context, tx Tx) error {
				var qty int
				if err := MapError(tx.QueryRowContext(ctx,
					`SELECT qty FROM `+schema+`.products WHERE id = 1`).Scan(&qty)); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx,
					`UPDATE `+schema+`.products SET qty = $1 WHERE id = 1`, qty-1)
				return err
			})
		}(w)
	}
	wg.Wait()

	for w, err := range errs {
		if err != nil {
			t.Errorf("worker %d: %v", w, err)
		}
	}

	var qty int
	if err := db.QueryRowContext(ctx,
		`SELECT qty FROM `+schema+`.products WHERE id = 1`).Scan(&qty); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// Every transaction must have taken effect exactly once. A lost update
	// would leave qty above this.
	if want := 10 - workers; qty != want {
		t.Fatalf("qty = %d, want %d: an update was lost", qty, want)
	}
}

// TestIntegrationReadOnlyTxRejectsWrites checks that the ReadOnly option is
// actually passed through to BeginTx.
func TestIntegrationReadOnlyTxRejectsWrites(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)

	err := db.InTxOpts(context.Background(), ReadOnly, func(ctx context.Context, tx Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO `+schema+`.products (id, sku) VALUES (95, 'RO-1')`)
		return err
	})
	if err == nil {
		t.Fatal("a write succeeded inside a read-only transaction")
	}
}

// TestIntegrationNestedTxSharesConnection checks the pool-deadlock guard
// against a real pool: with a single connection, a nested InTx that opened its
// own transaction would block forever.
func TestIntegrationNestedTxSharesConnection(t *testing.T) {
	db := requirePostgres(t)
	schema := setupSchema(t, db)

	db.SQL().SetMaxOpenConns(1)
	t.Cleanup(func() { db.SQL().SetMaxOpenConns(db.Config().MaxOpenConns) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := db.InTx(ctx, func(ctx context.Context, tx Tx) error {
		return db.InTx(ctx, func(ctx context.Context, tx Tx) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO `+schema+`.products (id, sku) VALUES (94, 'NEST-1')`)
			return err
		})
	})
	if err != nil {
		t.Fatalf("nested InTx on a single-connection pool: %v", err)
	}
}
