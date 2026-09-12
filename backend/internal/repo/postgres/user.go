package postgres

import (
	"context"
	"fmt"

	domain "gbos/internal/domain/user"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// User reads and writes application users.
type User struct{}

// NewUser returns a user repository.
func NewUser() *User { return &User{} }

const userColumns = `id, username, password_hash, role, store_id, is_active, sessions_invalidated_at`

// FindByUsername looks a user up for sign-in. It returns platform.ErrNotFound
// when there is no such user; the caller must not distinguish that from a bad
// password in what it tells the client.
func (r *User) FindByUsername(ctx context.Context, q platform.Querier, username string) (domain.User, error) {
	var u domain.User
	err := q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE lower(username) = lower($1)`, username).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.StoreID, &u.IsActive, &u.SessionsInvalidatedAt)
	if err != nil {
		return domain.User{}, platform.MapError(err)
	}
	return u, nil
}

// FindByID loads a user by id. This runs on every authenticated request, so
// it is a single primary-key lookup.
func (r *User) FindByID(ctx context.Context, q platform.Querier, id uuid.UUID) (domain.User, error) {
	var u domain.User
	err := q.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.StoreID, &u.IsActive, &u.SessionsInvalidatedAt)
	if err != nil {
		return domain.User{}, platform.MapError(err)
	}
	return u, nil
}

// ListByStore returns the users attached to one store.
func (r *User) ListByStore(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]domain.User, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE store_id = $1 ORDER BY username`, storeID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	users := make([]domain.User, 0)
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role,
			&u.StoreID, &u.IsActive, &u.SessionsInvalidatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", platform.MapError(err))
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return users, nil
}

// Create inserts a user.
func (r *User) Create(ctx context.Context, q platform.Querier, u domain.User) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, store_id, is_active)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		u.ID, u.Username, u.PasswordHash, u.Role, u.StoreID, u.IsActive)
	return platform.MapError(err)
}

// SetPassword replaces a user's password and invalidates every session issued
// before now, so a password change signs out the sessions an attacker may
// already hold.
func (r *User) SetPassword(ctx context.Context, q platform.Querier, id uuid.UUID, hash string) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE users SET password_hash = $2, sessions_invalidated_at = now(), updated_at = now()
		  WHERE id = $1`, id, hash)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// SetActive deactivates or reactivates a user. Deactivating also invalidates
// their sessions: the previous implementation left a deactivated user's token
// working for the rest of its 24-hour life.
func (r *User) SetActive(ctx context.Context, q platform.Querier, id uuid.UUID, active bool) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE users
		    SET is_active = $2,
		        sessions_invalidated_at = CASE WHEN $2 THEN sessions_invalidated_at ELSE now() END,
		        updated_at = now()
		  WHERE id = $1`, id, active)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// InvalidateSessions signs a user out everywhere.
func (r *User) InvalidateSessions(ctx context.Context, q platform.Querier, id uuid.UUID) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE users SET sessions_invalidated_at = now(), updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return false, platform.MapError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, platform.MapError(err)
	}
	return n > 0, nil
}

// CountOwners reports how many active owners exist, so that the last one
// cannot be deactivated and lock everybody out of the system.
func (r *User) CountOwners(ctx context.Context, q platform.Querier, excluding uuid.UUID) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE role = 'owner' AND is_active AND id <> $1`, excluding).Scan(&n)
	if err != nil {
		return 0, platform.MapError(err)
	}
	return n, nil
}
