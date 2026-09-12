package audit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "gbos/internal/domain/audit"
	platform "gbos/internal/platform/postgres"
	repo "gbos/internal/repo/postgres"
	service "gbos/internal/service/audit"
	"gbos/internal/testsupport"

	"github.com/google/uuid"
)

type fixture struct {
	svc     *service.Service
	db      *platform.DB
	storeID uuid.UUID
	userID  uuid.UUID
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := testsupport.OpenDB(t)

	f := fixture{svc: service.New(db, repo.NewAudit()), db: db}
	f.storeID = seedStore(t, db, "Test Shop")
	f.userID = seedUser(t, db, f.storeID, "keeper")
	return f
}

func seedStore(t *testing.T, db *platform.DB, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO stores (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed store %s: %v", name, err)
	}
	return id
}

func seedUser(t *testing.T, db *platform.DB, storeID uuid.UUID, username string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, role, store_id)
		 VALUES ($1, 'not-a-real-hash', 'manager', $2) RETURNING id`, username, storeID).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	return id
}

// TestEntryIsRecordedWithAUUID pins the identifier. One legacy call site built
// the id as "AUD-" plus a nanosecond clock reading; the column is now a uuid
// and rejects that outright, so a regression is a failed insert rather than a
// cosmetic difference.
func TestEntryIsRecordedWithAUUID(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	entry, err := f.svc.Record(ctx, domain.NewEntryInput{
		UserID:    f.userID,
		StoreID:   f.storeID,
		Action:    "api.POST /api/v1/products",
		TableName: "products",
		RecordID:  "abc-123",
		NewValue:  `{"name":"Rice"}`,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if entry.ID == uuid.Nil {
		t.Fatal("the entry was written without an identifier")
	}

	entries, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("listed %d entries, want 1", len(entries))
	}
	if entries[0].ID != entry.ID {
		t.Errorf("listed %s, recorded %s", entries[0].ID, entry.ID)
	}
	// The trail names people. A column of raw user ids is not readable by
	// anybody who has to act on it.
	if entries[0].Username != "keeper" {
		t.Errorf("username = %q, want keeper", entries[0].Username)
	}
	if entries[0].TableName != "products" {
		t.Errorf("table_name = %q, want products", entries[0].TableName)
	}
}

func TestTrailIsFilterableByUserAndTable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	other := seedUser(t, f.db, f.storeID, "second-keeper")

	for _, in := range []domain.NewEntryInput{
		{UserID: f.userID, StoreID: f.storeID, Action: "a", TableName: "products"},
		{UserID: f.userID, StoreID: f.storeID, Action: "b", TableName: "invoices"},
		{UserID: other, StoreID: f.storeID, Action: "c", TableName: "products"},
	} {
		if _, err := f.svc.Record(ctx, in); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	byTable, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID, TableName: "products"})
	if err != nil {
		t.Fatalf("list by table: %v", err)
	}
	if len(byTable) != 2 {
		t.Errorf("table filter returned %d entries, want 2", len(byTable))
	}

	byUser, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID, UserID: &other})
	if err != nil {
		t.Fatalf("list by user: %v", err)
	}
	if len(byUser) != 1 {
		t.Errorf("user filter returned %d entries, want 1", len(byUser))
	}
}

// TestAnotherStoresTrailIsNotVisible pins the store boundary: one shop's
// record of who did what is not another shop's to read.
func TestAnotherStoresTrailIsNotVisible(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	otherStore := seedStore(t, f.db, "Somebody Elses Shop")
	otherUser := seedUser(t, f.db, otherStore, "their-keeper")

	if _, err := f.svc.Record(ctx, domain.NewEntryInput{
		UserID: f.userID, StoreID: f.storeID, Action: "ours",
	}); err != nil {
		t.Fatalf("record ours: %v", err)
	}
	if _, err := f.svc.Record(ctx, domain.NewEntryInput{
		UserID: otherUser, StoreID: otherStore, Action: "theirs",
	}); err != nil {
		t.Fatalf("record theirs: %v", err)
	}

	entries, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "ours" {
		t.Errorf("listed %d entries — the other shop's trail leaked in", len(entries))
	}
}

func TestTrailListingIsBounded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := f.svc.Record(ctx, domain.NewEntryInput{
			UserID: f.userID, StoreID: f.storeID, Action: "api.POST /x",
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	entries, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID, Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("limit 2 returned %d entries", len(entries))
	}

	entries, err = f.svc.List(ctx, domain.Filter{StoreID: f.storeID, Limit: 1_000_000})
	if err != nil {
		t.Fatalf("list with a large limit: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("clamped listing returned %d entries, want all 5", len(entries))
	}
}

func TestTrailNeedsAStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	if _, err := f.svc.List(context.Background(), domain.Filter{}); !errors.Is(err, service.ErrStoreRequired) {
		t.Errorf("expected ErrStoreRequired, got %v", err)
	}
}

func TestAnEntryWithoutAnActorIsRejected(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	for name, in := range map[string]domain.NewEntryInput{
		"no user":   {StoreID: f.storeID, Action: "a"},
		"no store":  {UserID: f.userID, Action: "a"},
		"no action": {UserID: f.userID, StoreID: f.storeID},
	} {
		if _, err := f.svc.Record(ctx, in); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s: expected ErrValidation, got %v", name, err)
		}
	}
}

// TestRecordRequestStoresARedactedBody is the middleware's path through the
// service. The body reaching it is already redacted, and this checks that the
// service stores it whole rather than dropping the column.
func TestRecordRequestStoresARedactedBody(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	body := domain.RedactBody([]byte(`{"username":"till1","password":"hunter2"}`))
	if err := f.svc.RecordRequest(ctx, f.userID, f.storeID, "register", "auth", "", body); err != nil {
		t.Fatalf("record request: %v", err)
	}

	entries, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("listed %d entries, want 1", len(entries))
	}
	stored := entries[0].NewValue
	if strings.Contains(stored, "hunter2") {
		t.Errorf("the stored body carries the password: %s", stored)
	}
	if !strings.Contains(stored, "[redacted]") {
		t.Errorf("the password field was dropped rather than redacted: %s", stored)
	}
	if !strings.Contains(stored, "till1") {
		t.Errorf("redaction removed the non-sensitive fields too: %s", stored)
	}
	if entries[0].TableName != "auth" {
		t.Errorf("table_name = %q, want auth", entries[0].TableName)
	}
}

// TestRecordRequestFilesEntriesUnderTheirResource is what makes the trail
// answerable to the question it is kept for: everything that touched one
// invoice. Every entry used to be filed under a single constant, so both the
// screen's filter and the (table_name, record_id) index matched nothing.
func TestRecordRequestFilesEntriesUnderTheirResource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	const invoiceID = "8ac91f22-0000-4000-8000-000000000001"
	for _, r := range []struct{ action, table, record string }{
		{"create", "invoices", ""},
		{"void", "invoices", invoiceID},
		{"create", "expenses", ""},
	} {
		if err := f.svc.RecordRequest(ctx, f.userID, f.storeID,
			r.action, r.table, r.record, `{}`); err != nil {
			t.Fatalf("record %s %s: %v", r.table, r.action, err)
		}
	}

	invoiceEntries, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID, TableName: "invoices"})
	if err != nil {
		t.Fatalf("list invoices: %v", err)
	}
	if len(invoiceEntries) != 2 {
		t.Fatalf("filtering on invoices listed %d entries, want 2", len(invoiceEntries))
	}
	for _, e := range invoiceEntries {
		if e.TableName != "invoices" {
			t.Errorf("filter returned a %q entry", e.TableName)
		}
	}
	if got := invoiceEntries[0].RecordID; got != invoiceID {
		// Most recent first, so the void is at the head.
		t.Errorf("the void entry names record %q, want %q", got, invoiceID)
	}

	tables, err := f.svc.Tables(ctx, f.storeID)
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) != 2 || tables[0] != "expenses" || tables[1] != "invoices" {
		t.Errorf("Tables() = %v, want [expenses invoices]", tables)
	}
}

// TestAnOversizedBodyIsTruncated keeps a caller from filling the trail table
// from a browser.
func TestAnOversizedBodyIsTruncated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	huge := `{"note":"` + strings.Repeat("x", domain.MaxStoredBytes*2) + `"}`
	if err := f.svc.RecordRequest(ctx, f.userID, f.storeID, "create", "invoices", "", huge); err != nil {
		t.Fatalf("record request: %v", err)
	}

	entries, err := f.svc.List(ctx, domain.Filter{StoreID: f.storeID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("listed %d entries, want 1", len(entries))
	}
	if len(entries[0].NewValue) > domain.MaxStoredBytes+64 {
		t.Errorf("stored %d bytes, want at most about %d", len(entries[0].NewValue), domain.MaxStoredBytes)
	}
}
