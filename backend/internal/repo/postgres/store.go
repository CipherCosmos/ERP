package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gbos/internal/domain/document"
	domain "gbos/internal/domain/store"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// Store reads and writes shops and their billing terminals.
type Store struct{}

// NewStore returns a store repository.
func NewStore() *Store { return &Store{} }

const storeColumns = `id, name, COALESCE(address, ''), COALESCE(phone, ''),
	COALESCE(gstin, ''), location_type, is_active`

// IsActive reports whether a store exists and is open for business. The
// request-scoping middleware calls this on every owner request, so it is
// deliberately a single indexed lookup.
func (r *Store) IsActive(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error) {
	var active bool
	err := q.QueryRowContext(ctx,
		`SELECT is_active FROM stores WHERE id = $1`, id).Scan(&active)
	if err != nil {
		mapped := platform.MapError(err)
		// A store that does not exist is not an error for this question: the
		// answer is simply "no".
		if errors.Is(mapped, platform.ErrNotFound) {
			return false, nil
		}
		return false, mapped
	}
	return active, nil
}

// List returns the active stores.
func (r *Store) List(ctx context.Context, q platform.Querier) ([]domain.Store, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+storeColumns+` FROM stores WHERE is_active ORDER BY name`)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	stores := make([]domain.Store, 0)
	for rows.Next() {
		var s domain.Store
		if err := rows.Scan(&s.ID, &s.Name, &s.Address, &s.Phone,
			&s.GSTIN, &s.LocationType, &s.IsActive); err != nil {
			return nil, fmt.Errorf("scan store: %w", platform.MapError(err))
		}
		stores = append(stores, s)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return stores, nil
}

// Get returns one store.
func (r *Store) Get(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.Store, error) {
	var s domain.Store
	err := q.QueryRowContext(ctx,
		`SELECT `+storeColumns+` FROM stores WHERE id = $1`, id).Scan(
		&s.ID, &s.Name, &s.Address, &s.Phone, &s.GSTIN, &s.LocationType, &s.IsActive)
	if err != nil {
		return domain.Store{}, platform.MapError(err)
	}
	return s, nil
}

// Create inserts a store.
func (r *Store) Create(ctx context.Context, q platform.Querier, s domain.Store) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO stores (id, name, address, phone, gstin, location_type, is_active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.Name, s.Address, s.Phone, nullIfBlank(s.GSTIN), s.LocationType, s.IsActive)
	return platform.MapError(err)
}

// Update replaces a store's editable fields and reports whether it was found.
func (r *Store) Update(ctx context.Context, q platform.Querier, s domain.Store) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE stores SET name = $2, address = $3, phone = $4, gstin = $5,
			location_type = $6, updated_at = now()
		  WHERE id = $1`,
		s.ID, s.Name, s.Address, s.Phone, nullIfBlank(s.GSTIN), s.LocationType)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// SetActive opens or closes a store.
func (r *Store) SetActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE stores SET is_active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// ListTerminals returns the terminals registered to a store.
func (r *Store) ListTerminals(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]domain.Terminal, error) {
	finYear := document.FinancialYear(time.Now())
	// The counter is LEFT JOINed and defaults to 1: a terminal that has issued
	// nothing this year has no row yet, and the next number it should use is the
	// first one.
	rows, err := q.QueryContext(ctx,
		`SELECT t.id, t.store_id, t.code, t.name, t.is_active,
		        COALESCE(s.next_value, 1)
		   FROM terminals t
		   LEFT JOIN document_sequences s
		          ON s.store_id = t.store_id AND s.terminal_id = t.id
		         AND s.doc_type = 'invoice' AND s.fin_year = $2
		  WHERE t.store_id = $1 ORDER BY t.code`, storeID, finYear)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	terminals := make([]domain.Terminal, 0)
	for rows.Next() {
		var t domain.Terminal
		if err := rows.Scan(&t.ID, &t.StoreID, &t.Code, &t.Name, &t.IsActive, &t.NextInvoiceSeq); err != nil {
			return nil, fmt.Errorf("scan terminal: %w", platform.MapError(err))
		}
		terminals = append(terminals, t)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return terminals, nil
}

// GetTerminal returns one terminal.
func (r *Store) GetTerminal(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.Terminal, error) {
	var t domain.Terminal
	err := q.QueryRowContext(ctx,
		`SELECT id, store_id, code, name, is_active FROM terminals WHERE id = $1`, id).Scan(
		&t.ID, &t.StoreID, &t.Code, &t.Name, &t.IsActive)
	if err != nil {
		return domain.Terminal{}, platform.MapError(err)
	}
	return t, nil
}

// CreateTerminal registers a billing counter.
func (r *Store) CreateTerminal(ctx context.Context, q platform.Querier, t domain.Terminal) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO terminals (id, store_id, code, name, is_active)
		 VALUES ($1, $2, $3, $4, $5)`,
		t.ID, t.StoreID, t.Code, t.Name, t.IsActive)
	return platform.MapError(err)
}

// SetTerminalActive retires or restores a terminal. A retired terminal keeps
// its invoice series: the bills it issued remain, and the series must stay
// intact for a tax audit.
func (r *Store) SetTerminalActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE terminals SET is_active = $2, updated_at = now() WHERE id = $1`, id, active)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}
