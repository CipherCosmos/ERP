// Package expense is the service layer for the shop's running costs: it owns
// the store boundary, validates before writing, and is the only place that
// decides what a caller may record.
//
// The Repository interface is declared here, by the consumer, so this package
// states what it needs rather than accepting whatever a database package
// happens to offer.
package expense

import (
	"context"
	"errors"
	"fmt"

	domain "gbos/internal/domain/expense"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when an operation that is inherently
// store-scoped is called without one.
var ErrStoreRequired = errors.New("store is required")

// ErrUnknownCategory reports a category that is not on file or no longer in
// use. It is separate from the foreign key violation the database would raise
// so the caller can be told which of the two references was wrong.
var ErrUnknownCategory = errors.New("no such expense category")

// Repository is the persistence this service needs. Every method takes a
// Querier so the service decides whether it runs in a transaction.
type Repository interface {
	ListCategories(ctx context.Context, q platform.Querier) ([]domain.Category, error)
	CreateCategory(ctx context.Context, q platform.Querier, c domain.Category) error
	CategoryExists(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error)

	List(ctx context.Context, q platform.Querier, f domain.Filter) ([]domain.Expense, error)
	Create(ctx context.Context, q platform.Querier, e domain.Expense) (domain.Expense, error)
	Summarise(ctx context.Context, q platform.Querier, f domain.Filter) ([]domain.CategoryTotal, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service applies the expense rules.
type Service struct {
	db   DB
	repo Repository
}

// New returns an expense service.
func New(db DB, repo Repository) *Service {
	return &Service{db: db, repo: repo}
}

// ListCategories returns the headings a shop spends under.
//
// Categories are not store-scoped: the table has no store column, so the list
// is shared by every shop on the installation. That matches the schema as it
// stands and is why a shop with its own vocabulary adds to the list rather
// than replacing it.
func (s *Service) ListCategories(ctx context.Context) ([]domain.Category, error) {
	return s.repo.ListCategories(ctx, s.db)
}

// CreateCategory validates and stores a new heading.
func (s *Service) CreateCategory(ctx context.Context, name string) (domain.Category, error) {
	c, err := domain.NewCategory(name)
	if err != nil {
		return domain.Category{}, err
	}
	if err := s.repo.CreateCategory(ctx, s.db, c); err != nil {
		return domain.Category{}, fmt.Errorf("create expense category: %w", err)
	}
	return c, nil
}

// List returns one store's expenses.
func (s *Service) List(ctx context.Context, f domain.Filter) ([]domain.Expense, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, s.db, f)
}

// Record validates and stores an expense.
//
// The category is checked before the insert so an unknown one comes back as
// "no such expense category" rather than as a foreign key violation that could
// equally have meant the store or the user.
func (s *Service) Record(ctx context.Context, in domain.NewExpenseInput) (domain.Expense, error) {
	e, err := domain.NewExpense(in)
	if err != nil {
		return domain.Expense{}, err
	}

	exists, err := s.repo.CategoryExists(ctx, s.db, e.CategoryID)
	if err != nil {
		return domain.Expense{}, fmt.Errorf("check expense category: %w", err)
	}
	if !exists {
		return domain.Expense{}, ErrUnknownCategory
	}

	stored, err := s.repo.Create(ctx, s.db, e)
	if err != nil {
		return domain.Expense{}, fmt.Errorf("record expense: %w", err)
	}
	return stored, nil
}

// Summarise returns what a store spent under each heading over a window,
// together with the total those headings add up to.
func (s *Service) Summarise(ctx context.Context, f domain.Filter) (domain.Summary, error) {
	if f.StoreID == uuid.Nil {
		return domain.Summary{}, ErrStoreRequired
	}
	if err := f.Validate(); err != nil {
		return domain.Summary{}, err
	}
	lines, err := s.repo.Summarise(ctx, s.db, f)
	if err != nil {
		return domain.Summary{}, fmt.Errorf("summarise expenses: %w", err)
	}
	return domain.NewSummary(lines), nil
}
