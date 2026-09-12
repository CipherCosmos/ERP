package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors. Every error leaving this package is wrapped so that callers
// can classify it with errors.Is without importing a driver package or, worse,
// string-matching a driver message.
//
// These are deliberately transport-agnostic: internal/platform/httperr owns the
// translation to HTTP status codes.
var (
	// ErrNotFound means the row a query required does not exist. Produced by
	// sql.ErrNoRows and pgx.ErrNoRows.
	ErrNotFound = errors.New("record not found")

	// ErrConflict means the write collided with an existing row: a unique or
	// exclusion constraint rejected it. SQLSTATE 23505, 23P01.
	ErrConflict = errors.New("record already exists")

	// ErrReference means a foreign key was violated. This covers both
	// directions: inserting a row that points at a parent that does not exist,
	// and deleting a parent that children still point at. SQLSTATE 23503.
	ErrReference = errors.New("referenced record is missing or still in use")

	// ErrConstraint means a business rule encoded in the schema rejected the
	// row: a CHECK constraint or a NOT NULL column. SQLSTATE 23514, 23502.
	ErrConstraint = errors.New("value violates a database constraint")

	// ErrSerialization means the transaction lost a concurrency race and can be
	// retried verbatim. SQLSTATE 40001, 40P01. See IsRetryable.
	ErrSerialization = errors.New("transaction serialization failure")

	// ErrLockTimeout means a lock could not be acquired in time, typically from
	// SELECT ... FOR UPDATE NOWAIT or an exhausted lock_timeout. SQLSTATE 55P03.
	ErrLockTimeout = errors.New("could not acquire lock")

	// ErrTimeout means the statement was cancelled, usually by statement_timeout
	// or by the caller's context. SQLSTATE 57014.
	ErrTimeout = errors.New("query timed out")

	// ErrCanceled means the caller's context was cancelled before the query
	// finished. The client has usually gone away.
	ErrCanceled = errors.New("query canceled")

	// ErrInvalidInput means the value could not be coerced to the column type:
	// bad numeric text, out-of-range timestamp, malformed UUID. SQLSTATE 22xxx.
	ErrInvalidInput = errors.New("invalid value for column")

	// ErrUnavailable means the database could not be reached or refused work.
	// SQLSTATE 08xxx, 53300, 57P01-57P03.
	ErrUnavailable = errors.New("database unavailable")

	// ErrInsufficientPrivilege means the connected role lacks rights on the
	// object. SQLSTATE 42501. Always an operator bug, never an end-user one.
	ErrInsufficientPrivilege = errors.New("insufficient database privilege")
)

// Error is the concrete error this package returns. It carries the SQLSTATE and
// the constraint/table/column names so a repository can turn
// "products_sku_key" into "that SKU is already in use", while Kind stays
// coarse enough for httperr to pick a status code.
//
// Detail and the wrapped driver error hold schema information. They are safe to
// log and must never be written to an HTTP response; httperr enforces that by
// never calling Error() on the way out.
type Error struct {
	// Kind is one of the sentinels above. errors.Is(err, ErrConflict) matches.
	Kind error

	// Code is the five-character SQLSTATE, empty for non-Postgres errors.
	Code string

	// Constraint is the name of the constraint or index that rejected the write.
	// This is the field callers use to produce a specific message.
	Constraint string

	// Table and Column name the relation involved, when the server reports them.
	Table  string
	Column string

	// Detail is the server's DETAIL line. It frequently echoes the offending
	// values, so it is for logs only.
	Detail string

	// err is the original driver error.
	err error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := e.Kind.Error()
	if e.Code != "" {
		msg = fmt.Sprintf("%s (SQLSTATE %s", msg, e.Code)
		if e.Constraint != "" {
			msg += ", constraint " + e.Constraint
		}
		if e.Table != "" {
			msg += ", table " + e.Table
		}
		msg += ")"
	}
	if e.err != nil {
		msg += ": " + e.err.Error()
	}
	return msg
}

// Unwrap returns both the Kind sentinel and the original driver error so that
// errors.Is matches either one.
func (e *Error) Unwrap() []error {
	if e == nil {
		return nil
	}
	if e.err == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.err}
}

// MapError converts a driver error into one of this package's sentinels.
//
// It returns nil for nil, returns *Error unchanged if err was already mapped,
// and falls back to returning err untouched when it recognises nothing — an
// unmapped error is still an error, and httperr turns anything it does not
// recognise into a generic 500.
func MapError(err error) error {
	if err == nil {
		return nil
	}

	// Already mapped; do not double-wrap.
	var mapped *Error
	if errors.As(err, &mapped) {
		return err
	}

	// Row-not-found. pgx.ErrNoRows already wraps sql.ErrNoRows, but check both
	// so this does not depend on that implementation detail.
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return &Error{Kind: ErrNotFound, err: err}
	}

	// Context errors are checked before the driver error: a cancelled context
	// often surfaces as both, and the caller's intent is the more useful signal.
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: ErrCanceled, err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrTimeout, err: err}
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return &Error{
			Kind:       kindForSQLState(pgErr.Code),
			Code:       pgErr.Code,
			Constraint: pgErr.ConstraintName,
			Table:      pgErr.TableName,
			Column:     pgErr.ColumnName,
			Detail:     pgErr.Detail,
			err:        err,
		}
	}

	// Pool exhausted / server closed the socket mid-flight.
	if errors.Is(err, sql.ErrConnDone) || errors.Is(err, pgx.ErrTxClosed) {
		return &Error{Kind: ErrUnavailable, err: err}
	}

	return err
}

// kindForSQLState maps a SQLSTATE to a sentinel. Class-level fallbacks handle
// the codes we have not enumerated, so an unfamiliar 08xxx still reads as
// "unavailable" rather than as a generic 500.
func kindForSQLState(code string) error {
	switch code {
	// Class 23 — integrity constraint violation.
	case "23505": // unique_violation
		return ErrConflict
	case "23P01": // exclusion_violation
		return ErrConflict
	case "23503": // foreign_key_violation
		return ErrReference
	case "23514": // check_violation
		return ErrConstraint
	case "23502": // not_null_violation
		return ErrConstraint

	// Class 40 — transaction rollback. Both are safe to retry.
	case "40001": // serialization_failure
		return ErrSerialization
	case "40P01": // deadlock_detected
		return ErrSerialization

	// Locking and cancellation.
	case "55P03": // lock_not_available
		return ErrLockTimeout
	case "55006": // object_in_use
		return ErrLockTimeout
	case "57014": // query_canceled
		return ErrTimeout

	// Resource and availability.
	case "53300": // too_many_connections
		return ErrUnavailable
	case "53200": // out_of_memory
		return ErrUnavailable
	case "57P01", "57P02", "57P03": // admin_shutdown, crash_shutdown, cannot_connect_now
		return ErrUnavailable

	case "42501": // insufficient_privilege
		return ErrInsufficientPrivilege
	}

	// Class-level fallbacks.
	if len(code) >= 2 {
		switch code[:2] {
		case "08": // connection exception
			return ErrUnavailable
		case "22": // data exception
			return ErrInvalidInput
		case "23": // integrity constraint violation
			return ErrConstraint
		case "40": // transaction rollback
			return ErrSerialization
		case "53": // insufficient resources
			return ErrUnavailable
		}
	}

	// Anything else (42xxx syntax errors, 44xxx, XX000 internal) is a bug in our
	// SQL, not something the caller can act on. Leave it unclassified so httperr
	// reports a generic 500 and the real error reaches the log.
	return errUnclassified
}

// errUnclassified is the Kind for a Postgres error we deliberately do not
// translate. It is unexported so no caller can branch on it; they get a 500.
var errUnclassified = errors.New("database error")

// IsRetryable reports whether err is a transient concurrency failure that can
// be resolved by running the same transaction again. Only serialization
// failures and deadlocks qualify: both leave the database untouched.
func IsRetryable(err error) bool {
	return errors.Is(err, ErrSerialization)
}

// ConstraintName returns the name of the constraint or index that produced err,
// or "" if err did not come from a constraint violation.
//
// Use it to turn a generic conflict into a specific message:
//
//	if errors.Is(err, postgres.ErrConflict) {
//	    switch postgres.ConstraintName(err) {
//	    case "products_sku_key":
//	        return httperr.Conflict("a product with that SKU already exists")
//	    }
//	}
func ConstraintName(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Constraint
	}
	return ""
}

// SQLState returns the five-character SQLSTATE behind err, or "" if err did not
// originate from the Postgres server.
func SQLState(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
