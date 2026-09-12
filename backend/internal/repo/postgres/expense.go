package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/expense"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// expenseDefaultLimit matches what the expenses screen has always asked for.
const expenseDefaultLimit = 50

// expenseMaxLimit is the ceiling a caller cannot exceed. The handler this
// replaces built its LIMIT with string concatenation from an unvalidated query
// parameter; here the limit is clamped and then bound.
const expenseMaxLimit = 500

// Expense reads and writes expense categories and expenses.
type Expense struct{}

// NewExpense returns an expense repository.
func NewExpense() *Expense { return &Expense{} }

// expenseDate renders a business date as an unambiguous 'YYYY-MM-DD' literal
// for a `$n::date` bound parameter.
//
// Binding a time.Time directly would send a timestamptz, and comparing the
// DATE column against a timestamptz promotes the column — which drags the
// session timezone into a date-only comparison and stops the comparison using
// idx_expenses_store_date.
func expenseDate(t time.Time) string { return t.Format(expense.DateLayout) }

func expenseClampLimit(limit int) int {
	if limit <= 0 {
		return expenseDefaultLimit
	}
	if limit > expenseMaxLimit {
		return expenseMaxLimit
	}
	return limit
}

// ListCategories returns the active expense categories in display order.
func (r *Expense) ListCategories(ctx context.Context, q platform.Querier) ([]expense.Category, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, name, is_active FROM expense_categories WHERE is_active ORDER BY name`)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	categories := make([]expense.Category, 0)
	for rows.Next() {
		var c expense.Category
		if err := rows.Scan(&c.ID, &c.Name, &c.IsActive); err != nil {
			// Never skip a row that failed to scan. The handler this replaces
			// discarded the scan error and appended whatever the zero values
			// happened to be.
			return nil, fmt.Errorf("scan expense category: %w", platform.MapError(err))
		}
		categories = append(categories, c)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return categories, nil
}

// CreateCategory inserts an expense category.
func (r *Expense) CreateCategory(ctx context.Context, q platform.Querier, c expense.Category) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO expense_categories (id, name, is_active) VALUES ($1, $2, $3)`,
		c.ID, c.Name, c.IsActive)
	return platform.MapError(err)
}

// expenseColumns is the read model the listing returns.
const expenseColumns = `e.id, e.store_id, e.category_id, ec.name, e.amount,
	COALESCE(e.description, ''), e.expense_date, e.user_id, e.created_at`

// List returns a store's expenses, most recent first.
func (r *Expense) List(ctx context.Context, q platform.Querier, f expense.Filter) ([]expense.Expense, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ` + expenseColumns + `
		  FROM expenses e
		  JOIN expense_categories ec ON ec.id = e.category_id
		 WHERE e.store_id = $1`)
	args := []any{f.StoreID}

	if f.CategoryID != nil {
		args = append(args, *f.CategoryID)
		fmt.Fprintf(&sb, ` AND e.category_id = $%d`, len(args))
	}
	if f.From != nil {
		args = append(args, expenseDate(*f.From))
		fmt.Fprintf(&sb, ` AND e.expense_date >= $%d::date`, len(args))
	}
	if f.To != nil {
		args = append(args, expenseDate(*f.To))
		fmt.Fprintf(&sb, ` AND e.expense_date <= $%d::date`, len(args))
	}

	args = append(args, expenseClampLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY e.expense_date DESC, e.created_at DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	expenses := make([]expense.Expense, 0)
	for rows.Next() {
		var e expense.Expense
		if err := rows.Scan(&e.ID, &e.StoreID, &e.CategoryID, &e.CategoryName, &e.Amount,
			&e.Description, &e.ExpenseDate, &e.UserID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan expense: %w", platform.MapError(err))
		}
		expenses = append(expenses, e)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return expenses, nil
}

// Create inserts an expense and returns it carrying the timestamp the database
// assigned, so the caller's copy and the stored row agree. Returning the
// caller's own struct instead reports created_at as year 0001, which is not a
// time anything happened.
func (r *Expense) Create(ctx context.Context, q platform.Querier, e expense.Expense) (expense.Expense, error) {
	err := q.QueryRowContext(ctx,
		`INSERT INTO expenses (id, store_id, category_id, amount, description, expense_date, user_id)
		 VALUES ($1, $2, $3, $4, $5, $6::date, $7)
		 RETURNING created_at`,
		e.ID, e.StoreID, e.CategoryID, e.Amount, nullIfBlank(e.Description),
		expenseDate(e.ExpenseDate), e.UserID).Scan(&e.CreatedAt)
	if err != nil {
		return expense.Expense{}, platform.MapError(err)
	}
	return e, nil
}

// Summarise returns what a store spent under each category over a window.
//
// The GROUP BY names ec.id as well as ec.name so that two categories sharing a
// name stay two lines rather than silently merging.
//
// There is deliberately no HAVING clause. Postgres resolves HAVING against the
// input columns and not the select list, so `HAVING total > 0` — which SQLite
// accepts — is a "column does not exist" error here; the same trap bit two
// other aggregates during this port. A category with no spending in the window
// simply does not appear, because the JOIN produces no row for it.
func (r *Expense) Summarise(ctx context.Context, q platform.Querier, f expense.Filter) ([]expense.CategoryTotal, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT ec.id, ec.name, COALESCE(SUM(e.amount), 0)
		  FROM expenses e
		  JOIN expense_categories ec ON ec.id = e.category_id
		 WHERE e.store_id = $1`)
	args := []any{f.StoreID}

	if f.CategoryID != nil {
		args = append(args, *f.CategoryID)
		fmt.Fprintf(&sb, ` AND e.category_id = $%d`, len(args))
	}
	if f.From != nil {
		args = append(args, expenseDate(*f.From))
		fmt.Fprintf(&sb, ` AND e.expense_date >= $%d::date`, len(args))
	}
	if f.To != nil {
		args = append(args, expenseDate(*f.To))
		fmt.Fprintf(&sb, ` AND e.expense_date <= $%d::date`, len(args))
	}
	sb.WriteString(` GROUP BY ec.id, ec.name ORDER BY SUM(e.amount) DESC, ec.name`)

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	totals := make([]expense.CategoryTotal, 0)
	for rows.Next() {
		var t expense.CategoryTotal
		if err := rows.Scan(&t.CategoryID, &t.Category, &t.Total); err != nil {
			return nil, fmt.Errorf("scan expense total: %w", platform.MapError(err))
		}
		totals = append(totals, t)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return totals, nil
}

// CategoryExists reports whether a category is on file and still active, so
// the service can say "no such category" rather than letting the foreign key
// produce an unattributable 422.
func (r *Expense) CategoryExists(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM expense_categories WHERE id = $1 AND is_active)`, id).Scan(&exists)
	if err != nil {
		return false, platform.MapError(err)
	}
	return exists, nil
}
