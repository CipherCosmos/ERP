package testsupport_test

import (
	"context"
	"strings"
	"testing"

	"gbos/internal/testsupport"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests assert that the database itself enforces the business
// invariants, rather than trusting application code to remember them. Each one
// corresponds to a defect found in the SQLite implementation, where the schema
// had no CHECK constraints at all and status columns were free text.

func TestSchemaAppliesAndSeedsNothing(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()

	var tables int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&tables); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if tables < 30 {
		t.Errorf("expected at least 30 tables, got %d", tables)
	}

	// A fresh database must be empty. The SQLite path seeded demo rows on
	// first run, which is how published credentials reached production.
	for _, table := range []string{"users", "stores", "products", "invoices"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s should be empty in a fresh database, got %d rows", table, n)
		}
	}
}

func TestMoneyColumnsAreExactNumeric(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()

	// Any float column is a regression: float money is what produced the
	// half-paise drift and the never-settling credit entries.
	rows, err := pool.Query(ctx,
		`SELECT table_name, column_name, data_type
		   FROM information_schema.columns
		  WHERE table_schema = 'public'
		    AND data_type IN ('real', 'double precision')`)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	defer rows.Close()

	var offenders []string
	for rows.Next() {
		var table, column, kind string
		if err := rows.Scan(&table, &column, &kind); err != nil {
			t.Fatalf("scan: %v", err)
		}
		offenders = append(offenders, table+"."+column+" ("+kind+")")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("floating point columns must not exist: %s", strings.Join(offenders, ", "))
	}
}

func TestStockQuantityCannotGoNegative(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()
	storeID, productID := seedCatalogue(t, pool)

	_, err := pool.Exec(ctx,
		`INSERT INTO stock (product_id, store_id, batch_no, quantity) VALUES ($1, $2, 'B1', -1)`,
		productID, storeID)
	if err == nil {
		t.Fatal("expected the database to reject negative stock")
	}
	if !strings.Contains(err.Error(), "violates check constraint") {
		t.Errorf("expected a check constraint violation, got: %v", err)
	}
}

func TestStockUpsertMergesInsteadOfDuplicating(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()
	storeID, productID := seedCatalogue(t, pool)

	// batch_no used to be nullable inside the unique index. Because NULLs are
	// distinct, this ON CONFLICT silently inserted a second row instead of
	// adding to the first, fragmenting on-hand quantity across rows.
	const upsert = `
		INSERT INTO stock (product_id, store_id, batch_no, quantity)
		VALUES ($1, $2, '', $3)
		ON CONFLICT (product_id, store_id, batch_no)
		DO UPDATE SET quantity = stock.quantity + excluded.quantity`

	for _, qty := range []string{"10", "5"} {
		if _, err := pool.Exec(ctx, upsert, productID, storeID, qty); err != nil {
			t.Fatalf("upsert %s: %v", qty, err)
		}
	}

	var rowCount int
	var total string
	if err := pool.QueryRow(ctx,
		`SELECT count(*), coalesce(sum(quantity), 0)::text FROM stock WHERE product_id = $1`,
		productID).Scan(&rowCount, &total); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("expected the two receipts to merge into 1 row, got %d", rowCount)
	}
	if !strings.HasPrefix(total, "15") {
		t.Errorf("expected a merged quantity of 15, got %s", total)
	}
}

func TestInvoiceStatusRejectsArbitraryValues(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()
	storeID, _ := seedCatalogue(t, pool)

	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, role, store_id)
		 VALUES ('probe', 'x', 'owner', $1) RETURNING id`, storeID).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// The handler used to bind a client-supplied status straight into this
	// column, so a caller could post an already-voided invoice that still
	// deducted stock. The enum makes that unrepresentable.
	_, err := pool.Exec(ctx,
		`INSERT INTO invoices (invoice_no, store_id, user_id, invoice_date, status)
		 VALUES ('INV/T1/00001', $1, $2, CURRENT_DATE, 'not_a_real_status')`,
		storeID, userID)
	if err == nil {
		t.Fatal("expected the database to reject an unknown invoice status")
	}
	if !strings.Contains(err.Error(), "invalid input value for enum") {
		t.Errorf("expected an enum violation, got: %v", err)
	}
}

func TestTwoStoresCanRunTheSameInvoiceSeries(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()

	// Terminal codes are unique per store, so both shops can name a counter
	// 'T1'. If invoice_no were globally unique, the second store's very first
	// sale would fail on a unique violation — a real sale rejected the day the
	// business opens its second branch.
	var firstNo string
	for _, storeName := range []string{"Shop One", "Shop Two"} {
		var storeID, userID, terminalID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO stores (name) VALUES ($1) RETURNING id`, storeName).Scan(&storeID); err != nil {
			t.Fatalf("seed store %s: %v", storeName, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, password_hash, role, store_id)
			 VALUES ($1, 'x', 'cashier', $2) RETURNING id`,
			"cashier-"+storeName, storeID).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T1', 'Counter 1') RETURNING id`,
			storeID).Scan(&terminalID); err != nil {
			t.Fatalf("seed terminal for %s: %v", storeName, err)
		}

		firstNo = "INV/T1/00001"
		if _, err := pool.Exec(ctx,
			`INSERT INTO invoices (invoice_no, store_id, user_id, terminal_id, invoice_date)
			 VALUES ($1, $2, $3, $4, CURRENT_DATE)`,
			firstNo, storeID, userID, terminalID); err != nil {
			t.Fatalf("%s could not issue %s: %v", storeName, firstNo, err)
		}

		// Within one store the number must still be unique.
		_, err := pool.Exec(ctx,
			`INSERT INTO invoices (invoice_no, store_id, user_id, terminal_id, invoice_date)
			 VALUES ($1, $2, $3, $4, CURRENT_DATE)`,
			firstNo, storeID, userID, terminalID)
		if err == nil {
			t.Fatalf("%s was allowed to reuse %s within the same store", storeName, firstNo)
		}
		if !strings.Contains(err.Error(), "duplicate key value") {
			t.Errorf("expected a unique violation, got: %v", err)
		}
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM invoices WHERE invoice_no = $1`, firstNo).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("expected both stores to hold %s, found %d", firstNo, n)
	}
}

func TestCreditEntryCannotBeOverpaid(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()
	storeID, _ := seedCatalogue(t, pool)

	var customerID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO customers (name, store_id) VALUES ('Probe Customer', $1) RETURNING id`,
		storeID).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO credit_entries (customer_id, amount, type, paid_amount, status)
		 VALUES ($1, 100.00, 'issue', 150.00, 'partial')`, customerID)
	if err == nil {
		t.Fatal("expected the database to reject paid_amount exceeding amount")
	}
	if !strings.Contains(err.Error(), "violates check constraint") {
		t.Errorf("expected a check constraint violation, got: %v", err)
	}
}

func TestDocumentSequenceAllocatesWithoutGaps(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()
	storeID, _ := seedCatalogue(t, pool)

	var terminalID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO terminals (store_id, code, name) VALUES ($1, 'T1', 'Counter 1') RETURNING id`,
		storeID).Scan(&terminalID); err != nil {
		t.Fatalf("seed terminal: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_sequences (store_id, terminal_id, doc_type, fin_year, next_value)
		 VALUES ($1, $2, 'invoice', '2026-27', 1)`, storeID, terminalID); err != nil {
		t.Fatalf("seed sequence: %v", err)
	}

	// A real Postgres SEQUENCE is non-transactional and leaves gaps on
	// rollback, which is exactly what CGST Rule 46(b) forbids. A counter row
	// locked inside the transaction rolls back with it.
	allocate := func() int64 {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)

		var n int64
		if err := tx.QueryRow(ctx,
			`UPDATE document_sequences SET next_value = next_value + 1
			  WHERE store_id = $1 AND terminal_id = $2 AND doc_type = 'invoice' AND fin_year = '2026-27'
			  RETURNING next_value - 1`, storeID, terminalID).Scan(&n); err != nil {
			t.Fatalf("allocate: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return n
	}

	for want := int64(1); want <= 3; want++ {
		if got := allocate(); got != want {
			t.Fatalf("expected consecutive number %d, got %d", want, got)
		}
	}
}

// TestIdempotencyKeyRejectsReplayWithinAStore checks both halves of the key's
// scope at the schema level: the same key twice in one shop is refused, and the
// same key in two shops is not.
//
// The key is only unique to whoever generates it. A till numbers its requests
// from its own counter, so "till-1-0001" is the first key every shop ever sends,
// and a globally unique index would refuse the second shop's first sale.
func TestIdempotencyKeyRejectsReplayWithinAStore(t *testing.T) {
	t.Parallel()
	pool := testsupport.Pool(t)
	ctx := context.Background()

	first, _ := seedCatalogue(t, pool)
	var second string
	if err := pool.QueryRow(ctx,
		`INSERT INTO stores (name) VALUES ('Second Shop') RETURNING id`).Scan(&second); err != nil {
		t.Fatalf("seed the second store: %v", err)
	}

	const key = "till-1-0001"
	insert := `INSERT INTO processed_requests (store_id, key, request_path, response_status)
	           VALUES ($1, $2, '/api/v1/invoices', 201)`

	if _, err := pool.Exec(ctx, insert, first, key); err != nil {
		t.Fatalf("the first shop's write: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, first, key); err == nil {
		t.Error("the same key twice in one shop must be refused")
	} else if !strings.Contains(err.Error(), "duplicate key value") {
		t.Errorf("expected a unique violation, got: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, second, key); err != nil {
		t.Errorf("the second shop's identical key must be allowed, got: %v", err)
	}

	// And a row that names no shop is not a row, because every request names one.
	if _, err := pool.Exec(ctx,
		`INSERT INTO processed_requests (key, request_path, response_status)
		 VALUES ('unattributed', '/api/v1/invoices', 201)`); err == nil {
		t.Error("a processed request with no store must be refused")
	}
}

// seedCatalogue inserts the minimum rows needed to hang stock off, and returns
// the store and product ids.
func seedCatalogue(t *testing.T, pool *pgxpool.Pool) (storeID, productID string) {
	t.Helper()
	ctx := context.Background()

	if err := pool.QueryRow(ctx,
		`INSERT INTO stores (name) VALUES ('Probe Store') RETURNING id`).Scan(&storeID); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	// The units migration seeds the starter set and units_of_measure.code is
	// unique, so 'NOS' is read rather than inserted.
	var uomID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM units_of_measure WHERE code = 'NOS'`).Scan(&uomID); err != nil {
		t.Fatalf("read the seeded piece unit: %v", err)
	}
	var categoryID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO categories (name) VALUES ('Probe Category') RETURNING id`).Scan(&categoryID); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (name, category_id, uom_id) VALUES ('Probe Product', $1, $2) RETURNING id`,
		categoryID, uomID).Scan(&productID); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return storeID, productID
}
