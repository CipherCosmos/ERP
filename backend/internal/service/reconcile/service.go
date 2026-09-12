// Package reconcile serves the working list of things a shop has to look at
// after a till has been offline.
package reconcile

import (
	"context"
	"errors"
	"fmt"

	domain "gbos/internal/domain/reconcile"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired means the caller named no shop.
var ErrStoreRequired = errors.New("store is required")

// ErrAlreadyResolved means somebody else dealt with it first.
var ErrAlreadyResolved = errors.New("this exception has already been resolved")

// Repository is the persistence this service needs.
type Repository interface {
	List(ctx context.Context, q platform.Querier, f domain.Filter) ([]domain.Exception, error)
	Resolve(ctx context.Context, q platform.Querier, storeID, id, userID uuid.UUID, note string) (bool, error)
	CountOpen(ctx context.Context, q platform.Querier, storeID uuid.UUID) (int, error)
}

// Service reads and resolves reconciliation exceptions.
type Service struct {
	db   platform.Querier
	repo Repository
}

// New returns a reconciliation service.
func New(db platform.Querier, r Repository) *Service { return &Service{db: db, repo: r} }

// List returns a store's exceptions, open ones by default.
func (s *Service) List(ctx context.Context, f domain.Filter) ([]domain.Exception, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	if f.Kind != "" && !f.Kind.Valid() {
		return nil, fmt.Errorf("%w: %q is not a kind of exception this system records",
			domain.ErrValidation, f.Kind)
	}
	return s.repo.List(ctx, s.db, f)
}

// CountOpen is how many things are waiting for somebody.
func (s *Service) CountOpen(ctx context.Context, storeID uuid.UUID) (int, error) {
	if storeID == uuid.Nil {
		return 0, ErrStoreRequired
	}
	return s.repo.CountOpen(ctx, s.db, storeID)
}

// Resolve marks one dealt with.
//
// Resolving twice is an error rather than a no-op, because the second caller
// believes they are the one who dealt with it and the note they are attaching
// will not be stored. Telling them is the only way they find out.
func (s *Service) Resolve(ctx context.Context, storeID, id, userID uuid.UUID, note string) error {
	if storeID == uuid.Nil {
		return ErrStoreRequired
	}
	if id == uuid.Nil {
		return fmt.Errorf("%w: which exception?", domain.ErrValidation)
	}
	ok, err := s.repo.Resolve(ctx, s.db, storeID, id, userID, note)
	if err != nil {
		return fmt.Errorf("resolve exception: %w", err)
	}
	if !ok {
		// Either it is another store's, or it does not exist, or somebody else
		// got there first. The store-scoped read tells the three apart without
		// leaking whether another shop has an exception with this id.
		found, err := s.repo.List(ctx, s.db, domain.Filter{
			StoreID: storeID, IncludeResolved: true, Limit: 500,
		})
		if err != nil {
			return fmt.Errorf("resolve exception: %w", err)
		}
		for _, e := range found {
			if e.ID == id {
				return ErrAlreadyResolved
			}
		}
		return platform.ErrNotFound
	}
	return nil
}
