package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Tx is a handle to an open transaction. It is the Querier surface and nothing
// more: Commit and Rollback are deliberately absent, because InTx owns the
// lifecycle. A repository handed a Tx cannot end the transaction out from under
// its caller.
type Tx interface {
	Querier
}

// Common transaction options.
var (
	// ReadOnly marks a transaction that only reads. Postgres can skip some
	// bookkeeping, and it turns an accidental write into an error.
	ReadOnly = &sql.TxOptions{ReadOnly: true}

	// Serializable is the isolation level for logic that reads a value and
	// writes a decision based on it — the credit-limit check, stock
	// reservation, invoice numbering. Pair it with InTxRetry: at this level
	// Postgres reports 40001 rather than producing a wrong answer.
	Serializable = &sql.TxOptions{Isolation: sql.LevelSerializable}

	// ReadCommitted is the Postgres default, stated explicitly.
	ReadCommitted = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
)

// txKey is the context key carrying the ambient transaction.
type txKey struct{}

// tx implements Tx over a *sql.Tx, mapping driver errors on the way out.
type tx struct {
	tx    *sql.Tx
	owner *DB
}

func (t *tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := t.tx.ExecContext(ctx, query, args...)
	return res, MapError(err)
}

func (t *tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	return rows, MapError(err)
}

func (t *tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

// TxFromContext returns the transaction InTx placed on ctx, if any. It lets a
// helper deep in a call stack join the ambient transaction without threading a
// Tx through every signature.
func TxFromContext(ctx context.Context) (Tx, bool) {
	t, ok := ctx.Value(txKey{}).(*tx)
	return t, ok
}

// InTx runs fn inside a transaction at the default isolation level.
//
// It begins the transaction, calls fn, and then:
//
//   - fn returns nil     -> commit, and return the commit error if it fails
//   - fn returns an error -> roll back, and return fn's error
//   - fn panics           -> roll back, then re-panic with the original value
//
// The commit error is returned, not discarded. A transaction that reaches
// COMMIT can still fail — deferred constraints fire, the connection drops, a
// serialization check fails — and silently dropping that error reports success
// for writes that never landed.
//
// The panic is re-raised rather than converted to an error, so a nil-map
// dereference in a handler stays a panic with its original stack for the
// recovery middleware to log, instead of being laundered into a plain 500.
//
// fn receives a context carrying the transaction. If fn (or anything it calls)
// invokes InTx again on the same DB, that nested call joins this transaction
// instead of opening a second one. That is not just tidiness: opening a second
// transaction while holding the first takes a second connection from a bounded
// pool, and enough concurrent requests doing so deadlock the pool with every
// connection waiting on a connection.
//
// Usage:
//
//	err := db.InTx(ctx, func(ctx context.Context, tx postgres.Tx) error {
//	    if _, err := tx.ExecContext(ctx, `UPDATE ...`); err != nil {
//	        return err // rolled back
//	    }
//	    return nil // committed
//	})
func (db *DB) InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	return db.InTxOpts(ctx, nil, fn)
}

// InTxOpts is InTx with an explicit isolation level or read-only flag.
func (db *DB) InTxOpts(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context, tx Tx) error) error {
	if fn == nil {
		return errors.New("postgres: InTx called with nil function")
	}

	// Join an ambient transaction rather than opening a nested one. Committing
	// is the outer call's job, so this path neither commits nor rolls back: an
	// error returned here propagates up and the outer InTx rolls back.
	if existing, ok := ctx.Value(txKey{}).(*tx); ok && existing.owner == db {
		return fn(ctx, existing)
	}

	sqlTx, err := db.db.BeginTx(ctx, opts)
	if err != nil {
		return MapError(err)
	}

	t := &tx{tx: sqlTx, owner: db}
	txCtx := context.WithValue(ctx, txKey{}, t)

	committed := false
	defer func() {
		p := recover()
		if p == nil {
			if !committed {
				// Safety net. The error path below has usually rolled back
				// already; Rollback on a finished transaction returns
				// sql.ErrTxDone, which is not a failure.
				_ = sqlTx.Rollback()
			}
			return
		}
		// Roll back before re-panicking, so the panic does not leave a
		// transaction open holding locks until the connection is reaped.
		_ = sqlTx.Rollback()
		panic(p)
	}()

	if fnErr := fn(txCtx, t); fnErr != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			// Report both: the rollback failure usually means the connection
			// died, which is a different incident from whatever fn hit.
			return errors.Join(fnErr, fmt.Errorf("postgres: rollback: %w", MapError(rbErr)))
		}
		return fnErr
	}

	if err := sqlTx.Commit(); err != nil {
		return MapError(err)
	}
	committed = true
	return nil
}

// DefaultTxAttempts is the attempt count InTxRetry uses when given a
// non-positive value.
const DefaultTxAttempts = 3

// InTxRetry runs fn in a transaction, retrying the whole transaction when
// Postgres reports a serialization failure or a deadlock (SQLSTATE 40001 and
// 40P01). Both leave the database unchanged, so replaying fn is safe.
//
// This is what makes Serializable usable. Under that isolation level Postgres
// refuses to produce a wrong answer and returns 40001 instead, which means
// every serializable transaction needs a retry loop to be correct.
//
// fn must be replayable: it will run more than once. Do not send email, charge
// a card, or mutate shared memory inside it.
//
// Retries stop early if ctx is done. Non-retryable errors are returned on the
// first attempt.
func (db *DB) InTxRetry(ctx context.Context, opts *sql.TxOptions, attempts int, fn func(ctx context.Context, tx Tx) error) error {
	if attempts <= 0 {
		attempts = DefaultTxAttempts
	}

	// A nested call is already inside someone else's transaction; retrying
	// would replay only part of it. Run once and let the outer runner decide.
	if _, ok := ctx.Value(txKey{}).(*tx); ok {
		return db.InTxOpts(ctx, opts, fn)
	}

	var err error
	for attempt := range attempts {
		if attempt > 0 {
			// Small escalating backoff so concurrent losers do not collide
			// again immediately.
			delay := time.Duration(attempt) * 10 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(err, MapError(ctx.Err()))
			case <-timer.C:
			}
		}

		err = db.InTxOpts(ctx, opts, fn)
		if err == nil {
			return nil
		}
		if !IsRetryable(err) {
			return err
		}
	}
	return fmt.Errorf("postgres: transaction failed after %d attempts: %w", attempts, err)
}
