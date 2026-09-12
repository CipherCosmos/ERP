package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestMapSQLState is the mapping table, asserted. Every SQLSTATE this package
// claims to translate is listed here; a change to kindForSQLState that is not
// reflected here fails.
func TestMapSQLState(t *testing.T) {
	tests := []struct {
		code string
		name string
		want error
	}{
		{"23505", "unique_violation", ErrConflict},
		{"23P01", "exclusion_violation", ErrConflict},
		{"23503", "foreign_key_violation", ErrReference},
		{"23514", "check_violation", ErrConstraint},
		{"23502", "not_null_violation", ErrConstraint},
		{"40001", "serialization_failure", ErrSerialization},
		{"40P01", "deadlock_detected", ErrSerialization},
		{"55P03", "lock_not_available", ErrLockTimeout},
		{"55006", "object_in_use", ErrLockTimeout},
		{"57014", "query_canceled", ErrTimeout},
		{"53300", "too_many_connections", ErrUnavailable},
		{"53200", "out_of_memory", ErrUnavailable},
		{"57P01", "admin_shutdown", ErrUnavailable},
		{"57P02", "crash_shutdown", ErrUnavailable},
		{"57P03", "cannot_connect_now", ErrUnavailable},
		{"42501", "insufficient_privilege", ErrInsufficientPrivilege},

		// Class-level fallbacks for codes not enumerated above.
		{"08006", "connection_failure (class 08)", ErrUnavailable},
		{"08003", "connection_does_not_exist (class 08)", ErrUnavailable},
		{"22003", "numeric_value_out_of_range (class 22)", ErrInvalidInput},
		{"22P02", "invalid_text_representation (class 22)", ErrInvalidInput},
		{"23001", "restrict_violation (class 23)", ErrConstraint},
		{"40003", "statement_completion_unknown (class 40)", ErrSerialization},
		{"53100", "disk_full (class 53)", ErrUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.code+"_"+tt.name, func(t *testing.T) {
			err := MapError(&pgconn.PgError{Code: tt.code, Message: "boom"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("MapError(SQLSTATE %s) = %v, want errors.Is(..., %v)", tt.code, err, tt.want)
			}
			if got := SQLState(err); got != tt.code {
				t.Errorf("SQLState = %q, want %q", got, tt.code)
			}
		})
	}
}

// TestUnclassifiedCodesStayGeneric checks that a SQLSTATE we deliberately do
// not translate does not accidentally match a sentinel — a syntax error in our
// own SQL must not be reported to a client as a conflict.
func TestUnclassifiedCodesStayGeneric(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrNotFound", ErrNotFound},
		{"ErrConflict", ErrConflict},
		{"ErrReference", ErrReference},
		{"ErrConstraint", ErrConstraint},
		{"ErrSerialization", ErrSerialization},
		{"ErrLockTimeout", ErrLockTimeout},
		{"ErrTimeout", ErrTimeout},
		{"ErrInvalidInput", ErrInvalidInput},
		{"ErrUnavailable", ErrUnavailable},
	}

	for _, code := range []string{"42601", "42P01", "XX000", "44000"} {
		err := MapError(&pgconn.PgError{Code: code, Message: "boom"})
		if err == nil {
			t.Fatalf("MapError(SQLSTATE %s) returned nil", code)
		}
		for _, s := range sentinels {
			if errors.Is(err, s.err) {
				t.Errorf("SQLSTATE %s matched %s; it should stay unclassified", code, s.name)
			}
		}
	}
}

func TestMapNoRows(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"sql.ErrNoRows", sql.ErrNoRows},
		{"pgx.ErrNoRows", pgx.ErrNoRows},
		{"wrapped", fmt.Errorf("load product: %w", sql.ErrNoRows)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := MapError(tt.err); !errors.Is(err, ErrNotFound) {
				t.Fatalf("MapError(%v) = %v, want ErrNotFound", tt.err, err)
			}
		})
	}
}

func TestMapContextErrors(t *testing.T) {
	if err := MapError(context.Canceled); !errors.Is(err, ErrCanceled) {
		t.Errorf("MapError(context.Canceled) = %v, want ErrCanceled", err)
	}
	if err := MapError(context.DeadlineExceeded); !errors.Is(err, ErrTimeout) {
		t.Errorf("MapError(context.DeadlineExceeded) = %v, want ErrTimeout", err)
	}
}

func TestMapNilAndUnknown(t *testing.T) {
	if err := MapError(nil); err != nil {
		t.Errorf("MapError(nil) = %v, want nil", err)
	}

	// An error from outside the driver passes through untouched, so a caller
	// cannot mistake it for a classified failure.
	plain := errors.New("something else entirely")
	if got := MapError(plain); !errors.Is(got, plain) {
		t.Errorf("MapError(plain) = %v, want the original error", got)
	}
}

// TestMapIsIdempotent guards against double-wrapping when an already-mapped
// error passes through a second repository layer.
func TestMapIsIdempotent(t *testing.T) {
	once := MapError(&pgconn.PgError{Code: "23505", ConstraintName: "products_sku_key"})
	twice := MapError(once)

	if once != twice { //nolint:errorlint // identity is exactly what is asserted
		t.Fatal("MapError re-wrapped an already-mapped error")
	}
	if got := ConstraintName(twice); got != "products_sku_key" {
		t.Errorf("ConstraintName after double map = %q, want %q", got, "products_sku_key")
	}
}

func TestConstraintDetailIsPreserved(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:           "23505",
		Message:        "duplicate key value violates unique constraint",
		Detail:         "Key (sku)=(ABC-123) already exists.",
		ConstraintName: "products_sku_key",
		TableName:      "products",
		ColumnName:     "sku",
	}
	err := MapError(pgErr)

	var mapped *Error
	if !errors.As(err, &mapped) {
		t.Fatal("MapError did not return a *Error")
	}
	if mapped.Constraint != "products_sku_key" {
		t.Errorf("Constraint = %q, want %q", mapped.Constraint, "products_sku_key")
	}
	if mapped.Table != "products" {
		t.Errorf("Table = %q, want %q", mapped.Table, "products")
	}
	if mapped.Column != "sku" {
		t.Errorf("Column = %q, want %q", mapped.Column, "sku")
	}
	if mapped.Detail != "Key (sku)=(ABC-123) already exists." {
		t.Errorf("Detail = %q", mapped.Detail)
	}

	// Helpers reach through arbitrary wrapping.
	wrapped := fmt.Errorf("create product: %w", err)
	if got := ConstraintName(wrapped); got != "products_sku_key" {
		t.Errorf("ConstraintName(wrapped) = %q", got)
	}
	if got := SQLState(wrapped); got != "23505" {
		t.Errorf("SQLState(wrapped) = %q", got)
	}
}

// TestErrorUnwrapsToBothKindAndCause checks the Unwrap() []error contract: a
// caller can match the sentinel, and can still reach the driver error for logs.
func TestErrorUnwrapsToBothKindAndCause(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23503", ConstraintName: "sale_items_product_id_fkey"}
	err := MapError(pgErr)

	if !errors.Is(err, ErrReference) {
		t.Error("mapped error does not match its Kind sentinel")
	}
	var target *pgconn.PgError
	if !errors.As(err, &target) {
		t.Fatal("mapped error does not unwrap to the original *pgconn.PgError")
	}
	if target.ConstraintName != "sale_items_product_id_fkey" {
		t.Errorf("unwrapped constraint = %q", target.ConstraintName)
	}
}

// TestErrorMessageCarriesDiagnostics documents that Error() is for logs: it is
// expected to contain the constraint and table. httperr never sends it to a
// client, which is the invariant that keeps this safe.
func TestErrorMessageCarriesDiagnostics(t *testing.T) {
	err := MapError(&pgconn.PgError{
		Code:           "23505",
		Message:        "duplicate key",
		ConstraintName: "products_sku_key",
		TableName:      "products",
	})
	msg := err.Error()
	for _, want := range []string{"23505", "products_sku_key", "products"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	retryable := []string{"40001", "40P01"}
	for _, code := range retryable {
		if err := MapError(&pgconn.PgError{Code: code}); !IsRetryable(err) {
			t.Errorf("SQLSTATE %s should be retryable", code)
		}
	}

	notRetryable := []error{
		MapError(&pgconn.PgError{Code: "23505"}),
		MapError(&pgconn.PgError{Code: "23503"}),
		MapError(&pgconn.PgError{Code: "55P03"}),
		MapError(sql.ErrNoRows),
		errors.New("plain"),
		nil,
	}
	for _, err := range notRetryable {
		if IsRetryable(err) {
			t.Errorf("%v should not be retryable", err)
		}
	}
}

func TestHelpersOnUnrelatedErrors(t *testing.T) {
	plain := errors.New("nothing to do with postgres")
	if got := ConstraintName(plain); got != "" {
		t.Errorf("ConstraintName = %q, want empty", got)
	}
	if got := SQLState(plain); got != "" {
		t.Errorf("SQLState = %q, want empty", got)
	}
	if got := ConstraintName(nil); got != "" {
		t.Errorf("ConstraintName(nil) = %q, want empty", got)
	}
}
