package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The transaction runner is the most important code in this package: it exists
// so that the 4 ignored tx.Commit() errors and the 16 hand-rolled
// Begin/Commit/Rollback blocks in the handlers become impossible to write.
// Testing it needs a database that can be told to fail a commit on demand,
// which a real Postgres cannot easily be. These tests therefore drive InTx
// through a fake database/sql driver that records every Begin, Commit and
// Rollback. Behaviour against a real server is covered in integration_test.go.

// fakeDB is the shared state behind a registered fake driver.
type fakeDB struct {
	mu         sync.Mutex
	begins     int
	commits    int
	rollbacks  int
	commitErrs []error // popped per Commit; empty means success
	queryErr   error   // returned by every query when set
}

func (f *fakeDB) counts() (begins, commits, rollbacks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.begins, f.commits, f.rollbacks
}

func (f *fakeDB) nextCommitErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	if len(f.commitErrs) == 0 {
		return nil
	}
	err := f.commitErrs[0]
	f.commitErrs = f.commitErrs[1:]
	return err
}

type fakeDriver struct{ db *fakeDB }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

type fakeConn struct{ db *fakeDB }

var (
	_ driver.Conn           = (*fakeConn)(nil)
	_ driver.ConnBeginTx    = (*fakeConn)(nil)
	_ driver.QueryerContext = (*fakeConn)(nil)
	_ driver.ExecerContext  = (*fakeConn)(nil)
)

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: Prepare not supported")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.db.mu.Lock()
	c.db.begins++
	c.db.mu.Unlock()
	return &fakeTx{db: c.db}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	c.db.mu.Lock()
	err := c.db.queryErr
	c.db.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &fakeRows{}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	c.db.mu.Lock()
	err := c.db.queryErr
	c.db.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

type fakeTx struct{ db *fakeDB }

func (t *fakeTx) Commit() error { return t.db.nextCommitErr() }

func (t *fakeTx) Rollback() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	t.db.rollbacks++
	return nil
}

type fakeRows struct{ done bool }

func (r *fakeRows) Columns() []string { return []string{"n"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}

var driverSeq atomic.Int64

// newFakeDB registers a fresh driver and returns a *DB backed by it, plus the
// recorder. Each call gets its own driver name so tests cannot interfere.
func newFakeDB(t *testing.T) (*DB, *fakeDB) {
	t.Helper()

	rec := &fakeDB{}
	name := fmt.Sprintf("gbos-fake-%d", driverSeq.Add(1))
	sql.Register(name, fakeDriver{db: rec})

	sqlDB, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	return New(sqlDB), rec
}

func TestInTxCommits(t *testing.T) {
	db, rec := newFakeDB(t)

	var ran bool
	err := db.InTx(context.Background(), func(ctx context.Context, tx Tx) error {
		ran = true
		if _, err := tx.ExecContext(ctx, "UPDATE products SET qty = qty - 1"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
	if !ran {
		t.Fatal("fn was not called")
	}

	begins, commits, rollbacks := rec.counts()
	if begins != 1 || commits != 1 || rollbacks != 0 {
		t.Fatalf("begins=%d commits=%d rollbacks=%d, want 1/1/0", begins, commits, rollbacks)
	}
}

// TestInTxRollsBackOnError is the defect this package removes: the handlers
// hand-roll transactions with no `defer tx.Rollback()` anywhere in the repo, so
// an early return leaks an open transaction holding locks.
func TestInTxRollsBackOnError(t *testing.T) {
	db, rec := newFakeDB(t)

	sentinel := errors.New("business rule failed")
	err := db.InTx(context.Background(), func(context.Context, Tx) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx = %v, want the error fn returned", err)
	}

	begins, commits, rollbacks := rec.counts()
	if begins != 1 || commits != 0 || rollbacks != 1 {
		t.Fatalf("begins=%d commits=%d rollbacks=%d, want 1/0/1", begins, commits, rollbacks)
	}
}

// TestInTxReturnsCommitError covers the 4 ignored `tx.Commit()` errors the
// audit found. A transaction that fails at COMMIT wrote nothing; reporting
// success would tell the caller their sale was recorded when it was not.
func TestInTxReturnsCommitError(t *testing.T) {
	db, rec := newFakeDB(t)
	rec.commitErrs = []error{&pgconn.PgError{Code: "23505", ConstraintName: "sales_invoice_no_key"}}

	err := db.InTx(context.Background(), func(context.Context, Tx) error { return nil })
	if err == nil {
		t.Fatal("InTx returned nil despite a failing commit")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("InTx = %v, want a mapped ErrConflict", err)
	}
	if got := ConstraintName(err); got != "sales_invoice_no_key" {
		t.Errorf("ConstraintName = %q, want %q", got, "sales_invoice_no_key")
	}

	if _, commits, _ := rec.counts(); commits != 1 {
		t.Errorf("commits = %d, want 1", commits)
	}
}

// TestInTxRePanicsAfterRollback checks that a panic stays a panic. Converting
// it to an error would discard the stack and hide the bug behind a plain 500.
func TestInTxRePanicsAfterRollback(t *testing.T) {
	db, rec := newFakeDB(t)

	panicValue := "nil map write"

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("InTx swallowed the panic")
			}
			if r != panicValue {
				t.Fatalf("recovered %v, want the original value %v", r, panicValue)
			}
		}()

		_ = db.InTx(context.Background(), func(context.Context, Tx) error {
			panic(panicValue)
		})
	}()

	begins, commits, rollbacks := rec.counts()
	if begins != 1 || commits != 0 || rollbacks != 1 {
		t.Fatalf("begins=%d commits=%d rollbacks=%d, want 1/0/1", begins, commits, rollbacks)
	}
}

// TestInTxNestedJoinsOuter checks that a service calling a repository that also
// opens a transaction does not take a second connection. On a bounded pool,
// enough concurrent requests doing that deadlock every connection against
// every other.
func TestInTxNestedJoinsOuter(t *testing.T) {
	db, rec := newFakeDB(t)

	var outerTx, innerTx Tx
	err := db.InTx(context.Background(), func(ctx context.Context, tx Tx) error {
		outerTx = tx
		return db.InTx(ctx, func(ctx context.Context, tx Tx) error {
			innerTx = tx
			return nil
		})
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
	if outerTx != innerTx {
		t.Error("nested InTx did not receive the outer transaction")
	}

	begins, commits, rollbacks := rec.counts()
	if begins != 1 || commits != 1 || rollbacks != 0 {
		t.Fatalf("begins=%d commits=%d rollbacks=%d, want 1/1/0", begins, commits, rollbacks)
	}
}

// TestInTxNestedErrorRollsBackOuter checks that a nested failure aborts the
// whole unit of work rather than only its own part.
func TestInTxNestedErrorRollsBackOuter(t *testing.T) {
	db, rec := newFakeDB(t)

	sentinel := errors.New("inner failed")
	err := db.InTx(context.Background(), func(ctx context.Context, tx Tx) error {
		return db.InTx(ctx, func(context.Context, Tx) error { return sentinel })
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx = %v, want %v", err, sentinel)
	}

	begins, commits, rollbacks := rec.counts()
	if begins != 1 || commits != 0 || rollbacks != 1 {
		t.Fatalf("begins=%d commits=%d rollbacks=%d, want 1/0/1", begins, commits, rollbacks)
	}
}

func TestTxFromContext(t *testing.T) {
	db, _ := newFakeDB(t)

	if _, ok := TxFromContext(context.Background()); ok {
		t.Error("TxFromContext found a transaction on a bare context")
	}

	err := db.InTx(context.Background(), func(ctx context.Context, tx Tx) error {
		got, ok := TxFromContext(ctx)
		if !ok {
			return errors.New("TxFromContext did not find the ambient transaction")
		}
		if got != tx {
			return errors.New("TxFromContext returned a different transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInTxNilFunc(t *testing.T) {
	db, rec := newFakeDB(t)

	if err := db.InTx(context.Background(), nil); err == nil {
		t.Fatal("InTx with a nil function returned no error")
	}
	if begins, _, _ := rec.counts(); begins != 0 {
		t.Errorf("begins = %d, want 0: nothing should have been opened", begins)
	}
}

// TestInTxPropagatesQueryError checks that an error from inside the transaction
// is mapped, so callers can classify it without touching the driver.
func TestInTxPropagatesQueryError(t *testing.T) {
	db, rec := newFakeDB(t)
	rec.queryErr = &pgconn.PgError{Code: "23503", ConstraintName: "sale_items_product_id_fkey"}

	err := db.InTx(context.Background(), func(ctx context.Context, tx Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO sale_items ...")
		return err
	})
	if !errors.Is(err, ErrReference) {
		t.Fatalf("InTx = %v, want ErrReference", err)
	}
	if _, _, rollbacks := rec.counts(); rollbacks != 1 {
		t.Errorf("rollbacks = %d, want 1", rollbacks)
	}
}

func TestInTxOptsPassesIsolation(t *testing.T) {
	db, rec := newFakeDB(t)

	err := db.InTxOpts(context.Background(), ReadOnly, func(context.Context, Tx) error { return nil })
	if err != nil {
		t.Fatalf("InTxOpts: %v", err)
	}
	if begins, commits, _ := rec.counts(); begins != 1 || commits != 1 {
		t.Fatalf("begins=%d commits=%d, want 1/1", begins, commits)
	}
}

// TestInTxRetrySucceedsAfterSerializationFailure covers the reason Serializable
// is usable at all: Postgres reports 40001 instead of producing a wrong answer,
// so the transaction must be replayed.
func TestInTxRetrySucceedsAfterSerializationFailure(t *testing.T) {
	db, rec := newFakeDB(t)
	rec.commitErrs = []error{
		&pgconn.PgError{Code: "40001"},
		&pgconn.PgError{Code: "40P01"},
		nil,
	}

	attempts := 0
	err := db.InTxRetry(context.Background(), Serializable, 3, func(context.Context, Tx) error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("InTxRetry: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("fn ran %d times, want 3", attempts)
	}

	begins, commits, _ := rec.counts()
	if begins != 3 || commits != 3 {
		t.Fatalf("begins=%d commits=%d, want 3/3", begins, commits)
	}
}

func TestInTxRetryGivesUp(t *testing.T) {
	db, rec := newFakeDB(t)
	rec.commitErrs = []error{
		&pgconn.PgError{Code: "40001"},
		&pgconn.PgError{Code: "40001"},
		&pgconn.PgError{Code: "40001"},
	}

	err := db.InTxRetry(context.Background(), Serializable, 3, func(context.Context, Tx) error { return nil })
	if err == nil {
		t.Fatal("InTxRetry returned nil after exhausting its attempts")
	}
	if !errors.Is(err, ErrSerialization) {
		t.Errorf("InTxRetry = %v, want it to still match ErrSerialization", err)
	}
	if begins, _, _ := rec.counts(); begins != 3 {
		t.Errorf("begins = %d, want 3", begins)
	}
}

// TestInTxRetryDoesNotRetryOtherErrors checks that a unique violation is
// returned immediately: replaying it would only fail the same way.
func TestInTxRetryDoesNotRetryOtherErrors(t *testing.T) {
	db, rec := newFakeDB(t)
	rec.commitErrs = []error{&pgconn.PgError{Code: "23505"}}

	err := db.InTxRetry(context.Background(), nil, 3, func(context.Context, Tx) error { return nil })
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("InTxRetry = %v, want ErrConflict", err)
	}
	if begins, _, _ := rec.counts(); begins != 1 {
		t.Errorf("begins = %d, want 1: a conflict must not be retried", begins)
	}
}

func TestInTxRetryStopsOnCancelledContext(t *testing.T) {
	db, rec := newFakeDB(t)
	rec.commitErrs = []error{
		&pgconn.PgError{Code: "40001"},
		&pgconn.PgError{Code: "40001"},
		&pgconn.PgError{Code: "40001"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	err := db.InTxRetry(ctx, Serializable, 5, func(context.Context, Tx) error {
		cancel() // abandon the request mid-flight
		return nil
	})
	if err == nil {
		t.Fatal("InTxRetry returned nil despite a cancelled context")
	}
	if begins, _, _ := rec.counts(); begins > 2 {
		t.Errorf("begins = %d: retries continued after cancellation", begins)
	}
}

func TestPing(t *testing.T) {
	db, _ := newFakeDB(t)

	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingReportsFailure(t *testing.T) {
	db, rec := newFakeDB(t)
	rec.queryErr = &pgconn.PgError{Code: "57P03"} // cannot_connect_now

	err := db.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping returned nil against an unavailable server")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ping = %v, want ErrUnavailable", err)
	}
}

// TestInTxConcurrent runs the runner from many goroutines under -race.
func TestInTxConcurrent(t *testing.T) {
	db, rec := newFakeDB(t)
	db.SQL().SetMaxOpenConns(4)

	const workers = 16
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			err := db.InTx(context.Background(), func(ctx context.Context, tx Tx) error {
				if _, err := tx.ExecContext(ctx, "UPDATE stock SET qty = qty - 1"); err != nil {
					return err
				}
				// Nest, to exercise the ambient-transaction path concurrently.
				return db.InTx(ctx, func(ctx context.Context, tx Tx) error {
					_, err := tx.ExecContext(ctx, "INSERT INTO stock_moves ...")
					return err
				})
			})
			if err != nil {
				t.Errorf("worker %d: %v", w, err)
			}
		}(w)
	}
	wg.Wait()

	begins, commits, rollbacks := rec.counts()
	if begins != workers || commits != workers || rollbacks != 0 {
		t.Fatalf("begins=%d commits=%d rollbacks=%d, want %d/%d/0",
			begins, commits, rollbacks, workers, workers)
	}
}
