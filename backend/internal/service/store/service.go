// Package store is the service layer for shops and billing terminals.
package store

import (
	"context"
	"fmt"

	domain "gbos/internal/domain/store"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Repository is the persistence this service needs.
type Repository interface {
	IsActive(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error)
	List(ctx context.Context, q platform.Querier) ([]domain.Store, error)
	Get(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.Store, error)
	Create(ctx context.Context, q platform.Querier, s domain.Store) error
	Update(ctx context.Context, q platform.Querier, s domain.Store) (bool, error)
	SetActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)

	ListTerminals(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]domain.Terminal, error)
	GetTerminal(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.Terminal, error)
	CreateTerminal(ctx context.Context, q platform.Querier, t domain.Terminal) error
	SetTerminalActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error)
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service applies store and terminal rules.
type Service struct {
	db   DB
	repo Repository
}

// New returns a store service.
func New(db DB, repo Repository) *Service {
	return &Service{db: db, repo: repo}
}

// StoreIsActive satisfies the request-scoping middleware's StoreChecker. It is
// the authority for whether an owner may act on a given store.
func (s *Service) StoreIsActive(ctx context.Context, storeID uuid.UUID) (bool, error) {
	return s.repo.IsActive(ctx, s.db, storeID)
}

// List returns the active stores.
func (s *Service) List(ctx context.Context) ([]domain.Store, error) {
	return s.repo.List(ctx, s.db)
}

// Get returns one store.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Store, error) {
	return s.repo.Get(ctx, s.db, id)
}

// Create validates and stores a new shop or godown.
func (s *Service) Create(ctx context.Context, name, address, phone, gstin string, locationType domain.LocationType) (domain.Store, error) {
	st, err := domain.NewStore(name, address, phone, gstin, locationType)
	if err != nil {
		return domain.Store{}, err
	}
	if err := s.repo.Create(ctx, s.db, st); err != nil {
		return domain.Store{}, fmt.Errorf("create store: %w", err)
	}
	return st, nil
}

// Update replaces a store's editable fields.
func (s *Service) Update(ctx context.Context, id uuid.UUID, name, address, phone, gstin string, locationType domain.LocationType) (domain.Store, error) {
	st, err := domain.NewStore(name, address, phone, gstin, locationType)
	if err != nil {
		return domain.Store{}, err
	}
	st.ID = id

	// Turning a shop into a godown would strand its counters: they would still
	// exist, still carry an invoice series, and no longer be allowed to bill.
	// Retiring them is a deliberate act, so it is asked for rather than done
	// silently here.
	if locationType != domain.LocationStore {
		terminals, err := s.repo.ListTerminals(ctx, s.db, id)
		if err != nil {
			return domain.Store{}, fmt.Errorf("load terminals: %w", err)
		}
		if len(terminals) > 0 {
			return domain.Store{}, fmt.Errorf(
				"%w: %s still has %d billing counter(s); retire them before making it a %s",
				domain.ErrValidation, name, len(terminals), locationType)
		}
	}

	found, err := s.repo.Update(ctx, s.db, st)
	if err != nil {
		return domain.Store{}, fmt.Errorf("update store: %w", err)
	}
	if !found {
		return domain.Store{}, platform.ErrNotFound
	}
	return st, nil
}

// SetActive opens or closes a store.
func (s *Service) SetActive(ctx context.Context, id uuid.UUID, active bool) error {
	found, err := s.repo.SetActive(ctx, s.db, id, active)
	if err != nil {
		return fmt.Errorf("set store active: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}

// ListTerminals returns a store's billing counters.
func (s *Service) ListTerminals(ctx context.Context, storeID uuid.UUID) ([]domain.Terminal, error) {
	return s.repo.ListTerminals(ctx, s.db, storeID)
}

// RegisterTerminal adds a billing counter to a store. The code becomes part of
// every invoice number the counter issues, so it cannot be changed afterwards
// without breaking the series.
//
// A godown cannot have one. Goods are sold from shops only, so a counter in a
// godown is a counter that can never legitimately bill; refusing it here stops
// the bad state being created rather than catching it at the till.
func (s *Service) RegisterTerminal(ctx context.Context, storeID uuid.UUID, code, name string) (domain.Terminal, error) {
	location, err := s.repo.Get(ctx, s.db, storeID)
	if err != nil {
		return domain.Terminal{}, fmt.Errorf("load store: %w", err)
	}
	if location.LocationType != domain.LocationStore {
		return domain.Terminal{}, fmt.Errorf(
			"%w: %s is a %s, and only a shop can have a billing counter",
			domain.ErrValidation, location.Name, location.LocationType)
	}

	t, err := domain.NewTerminal(storeID, code, name)
	if err != nil {
		return domain.Terminal{}, err
	}
	if err := s.repo.CreateTerminal(ctx, s.db, t); err != nil {
		return domain.Terminal{}, fmt.Errorf("register terminal: %w", err)
	}
	return t, nil
}

// SetTerminalActive retires or restores a terminal.
func (s *Service) SetTerminalActive(ctx context.Context, id uuid.UUID, active bool) error {
	found, err := s.repo.SetTerminalActive(ctx, s.db, id, active)
	if err != nil {
		return fmt.Errorf("set terminal active: %w", err)
	}
	if !found {
		return platform.ErrNotFound
	}
	return nil
}
