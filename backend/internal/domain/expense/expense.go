// Package expense holds what a shop spends running itself: the headings it
// spends under, and the individual payments recorded against them.
//
// Nothing here talks to a database or to HTTP. Expenses are the deduction side
// of the profit and loss report, so an amount that should never have been
// accepted is a misstated trading result rather than a cosmetic defect — which
// is why the validation lives on the types rather than in whichever handler
// happened to remember it.
package expense

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gbos/internal/domain/money"

	"github.com/google/uuid"
)

// ErrValidation is the sentinel for a rejected value. Callers test with
// errors.Is; the message carries the detail worth showing a user.
var ErrValidation = errors.New("validation failed")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrValidation}, args...)...)
}

// DateLayout is the form an expense date takes in a query parameter.
const DateLayout = "2006-01-02"

// Category is a heading a shop spends under: rent, wages, electricity.
//
// Nothing seeds this table. What counts as a category is the shop's own
// business, and a fixed list invented here would be wrong for most shops and
// awkward to remove for all of them, so a shop defines its own.
type Category struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IsActive bool      `json:"is_active"`
}

// NewCategory validates and returns a category ready to persist.
func NewCategory(name string) (Category, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Category{}, invalid("category name is required")
	}
	if len(name) > 200 {
		return Category{}, invalid("category name is longer than 200 characters")
	}
	return Category{ID: uuid.New(), Name: name, IsActive: true}, nil
}

// Expense is one payment out, recorded against a category and a store.
type Expense struct {
	ID         uuid.UUID `json:"id"`
	StoreID    uuid.UUID `json:"store_id"`
	CategoryID uuid.UUID `json:"category_id"`
	// CategoryName rides along on the read model so the listing screen renders
	// a row without holding a second lookup table of its own.
	CategoryName string      `json:"category_name"`
	Amount       money.Money `json:"amount"`
	Description  string      `json:"description"`
	ExpenseDate  time.Time   `json:"expense_date"`
	UserID       uuid.UUID   `json:"user_id"`
	CreatedAt    time.Time   `json:"created_at"`
}

// NewExpenseInput is the caller-supplied shape for recording an expense.
type NewExpenseInput struct {
	StoreID     uuid.UUID
	CategoryID  uuid.UUID
	Amount      money.Money
	Description string
	ExpenseDate time.Time
	UserID      uuid.UUID
}

// NewExpense validates and returns an expense ready to persist.
func NewExpense(in NewExpenseInput) (Expense, error) {
	if in.StoreID == uuid.Nil {
		return Expense{}, invalid("store is required")
	}
	if in.CategoryID == uuid.Nil {
		return Expense{}, invalid("expense category is required")
	}
	if in.UserID == uuid.Nil {
		return Expense{}, invalid("the user recording the expense is required")
	}
	// The column carries a CHECK for the negative half of this. Zero is
	// rejected here and not there, because an expense of nothing is a mistyped
	// form rather than something the schema should have to have an opinion on.
	if !in.Amount.IsPositive() {
		return Expense{}, invalid("expense amount must be greater than zero, got %s", in.Amount)
	}
	if in.ExpenseDate.IsZero() {
		return Expense{}, invalid("expense date is required")
	}
	return Expense{
		ID:          uuid.New(),
		StoreID:     in.StoreID,
		CategoryID:  in.CategoryID,
		Amount:      in.Amount,
		Description: strings.TrimSpace(in.Description),
		// The column is a DATE, so the time of day is not storable and must not
		// be pretended at: two expenses on the same day are the same day.
		ExpenseDate: Day(in.ExpenseDate),
		UserID:      in.UserID,
	}, nil
}

// Day truncates an instant to the start of its calendar day, in its own
// location.
func Day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// Filter narrows an expense listing. StoreID is mandatory: an expense listing
// that spans stores is somebody else's ledger.
type Filter struct {
	StoreID    uuid.UUID
	CategoryID *uuid.UUID
	From       *time.Time
	To         *time.Time
	// Limit is bounded by the repository. It reaches SQL as a bound parameter
	// and never as text: the handler this replaces concatenated it.
	Limit int
}

// Validate rejects a window that cannot describe a period.
func (f Filter) Validate() error {
	if f.From != nil && f.To != nil && f.To.Before(*f.From) {
		return invalid("end date %s is before start date %s",
			f.To.Format(DateLayout), f.From.Format(DateLayout))
	}
	return nil
}

// CategoryTotal is one line of the summary: what a store spent under one
// heading over the period asked for.
type CategoryTotal struct {
	CategoryID uuid.UUID   `json:"category_id"`
	Category   string      `json:"category"`
	Total      money.Money `json:"total"`
}

// Summary is the per-category breakdown together with its total.
//
// The JSON names are the shape the expenses screen already reads. GrandTotal
// is derived from the lines rather than counted separately, so the figure at
// the bottom is the sum of the figures above it by construction.
type Summary struct {
	Categories []CategoryTotal `json:"summary"`
	GrandTotal money.Money     `json:"grand_total"`
}

// NewSummary totals the lines it is given.
func NewSummary(lines []CategoryTotal) Summary {
	if lines == nil {
		lines = []CategoryTotal{}
	}
	total := money.Zero()
	for _, l := range lines {
		total = total.Add(l.Total)
	}
	return Summary{Categories: lines, GrandTotal: total}
}
