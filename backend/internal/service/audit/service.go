// Package audit is the service layer for the change trail: it owns the store
// boundary on reads and validates entries before they are written.
//
// The Repository interface is declared here, by the consumer, so this package
// states what it needs rather than accepting whatever a database package
// happens to offer.
package audit

import (
	"context"
	"errors"
	"fmt"

	domain "gbos/internal/domain/audit"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// ErrStoreRequired is returned when the trail is read without a store.
var ErrStoreRequired = errors.New("store is required")

// Repository is the persistence this service needs.
type Repository interface {
	List(ctx context.Context, q platform.Querier, f domain.Filter) ([]domain.Entry, error)
	DistinctTables(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]string, error)
	Record(ctx context.Context, q platform.Querier, e domain.Entry) error
}

// DB is the transaction capability the service needs from a pool.
type DB interface {
	platform.Querier
	InTx(ctx context.Context, fn func(ctx context.Context, tx platform.Tx) error) error
}

// Service reads and writes the trail.
type Service struct {
	db   DB
	repo Repository
}

// New returns an audit service.
func New(db DB, repo Repository) *Service {
	return &Service{db: db, repo: repo}
}

// List returns one store's trail.
func (s *Service) List(ctx context.Context, f domain.Filter) ([]domain.Entry, error) {
	if f.StoreID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.List(ctx, s.db, f)
}

// Tables returns the resources this store's trail mentions, for the screen's
// filter to offer.
func (s *Service) Tables(ctx context.Context, storeID uuid.UUID) ([]string, error) {
	if storeID == uuid.Nil {
		return nil, ErrStoreRequired
	}
	return s.repo.DistinctTables(ctx, s.db, storeID)
}

// Record validates and stores one entry.
func (s *Service) Record(ctx context.Context, in domain.NewEntryInput) (domain.Entry, error) {
	e, err := domain.NewEntry(in)
	if err != nil {
		return domain.Entry{}, err
	}
	if err := s.repo.Record(ctx, s.db, e); err != nil {
		return domain.Entry{}, fmt.Errorf("record audit entry: %w", err)
	}
	return e, nil
}

// RecordRequest writes the trail entry for one completed mutating request. It
// is the narrow capability the HTTP middleware needs, kept free of the audit
// domain types so that package does not have to import this one.
//
// The body is already redacted by the caller; this method does not inspect it.
func (s *Service) RecordRequest(ctx context.Context, userID, storeID uuid.UUID, action, table, recordID, body string) error {
	_, err := s.Record(ctx, domain.NewEntryInput{
		UserID:  userID,
		StoreID: storeID,
		Action:  action,
		// The resource and record the caller derived from the route. Together
		// they are what makes the trail answerable to "who touched this
		// invoice", which is the question it is kept for.
		TableName: table,
		RecordID:  recordID,
		NewValue:  body,
	})
	return err
}
