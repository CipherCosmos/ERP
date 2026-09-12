package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	storedomain "gbos/internal/domain/store"
	userdomain "gbos/internal/domain/user"
	platform "gbos/internal/platform/postgres"
)

// Bootstrap describes the account created on an empty database.
type Bootstrap struct {
	// Created is false when the database already had users, in which case
	// nothing was written and the other fields are empty.
	Created  bool
	Username string
	// Password is only set when one was generated. It is the single
	// opportunity to read it.
	Password string
	// Generated distinguishes a password this process invented from one the
	// operator supplied.
	Generated bool
}

// EnsureBootstrap creates the first store, owner and counter on an empty
// database, so that a fresh installation can be signed into.
//
// It does nothing at all once any user exists, so it is safe to call on every
// start. Unlike the seeding it replaces, it plants no demo products, no
// wholesale customer and no supplier: a shop's catalogue is its own, and the
// old seed's published cashier/helper credentials were a way in.
func EnsureBootstrap(ctx context.Context, db *platform.DB) (Bootstrap, error) {
	var users int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return Bootstrap{}, fmt.Errorf("count users: %w", platform.MapError(err))
	}
	if users > 0 {
		return Bootstrap{}, nil
	}

	password, generated, err := resolveAdminPassword()
	if err != nil {
		return Bootstrap{}, err
	}

	store, err := storedomain.NewStore("Main Store", "", "", "", storedomain.LocationStore)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("build store: %w", err)
	}
	admin, err := userdomain.New("admin", password, userdomain.RoleOwner, store.ID)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("build owner: %w", err)
	}
	terminal, err := storedomain.NewTerminal(store.ID, "T1", "Counter 1")
	if err != nil {
		return Bootstrap{}, fmt.Errorf("build terminal: %w", err)
	}

	err = db.InTx(ctx, func(ctx context.Context, tx platform.Tx) error {
		// Serialise on an advisory lock, then re-check. Two instances starting
		// together would otherwise both find the database empty and both seed
		// it. There is no row to lock — the table is empty, which is the whole
		// point — so a row lock cannot do this job.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, bootstrapLockID); err != nil {
			return fmt.Errorf("take bootstrap lock: %w", platform.MapError(err))
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return platform.MapError(err)
		}
		if n > 0 {
			return errAlreadyBootstrapped
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stores (id, name, location_type) VALUES ($1, $2, $3)`,
			store.ID, store.Name, store.LocationType); err != nil {
			return fmt.Errorf("create store: %w", platform.MapError(err))
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, role, store_id, is_active)
			 VALUES ($1, $2, $3, 'owner', $4, true)`,
			admin.ID, admin.Username, admin.PasswordHash, store.ID); err != nil {
			return fmt.Errorf("create owner: %w", platform.MapError(err))
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO terminals (id, store_id, code, name) VALUES ($1, $2, $3, $4)`,
			terminal.ID, terminal.StoreID, terminal.Code, terminal.Name); err != nil {
			return fmt.Errorf("create terminal: %w", platform.MapError(err))
		}
		return nil
	})
	if errors.Is(err, errAlreadyBootstrapped) || errors.Is(err, platform.ErrConflict) {
		// Another instance won the race. That is the intended outcome.
		return Bootstrap{}, nil
	}
	if err != nil {
		return Bootstrap{}, err
	}

	out := Bootstrap{Created: true, Username: admin.Username, Generated: generated}
	if generated {
		out.Password = password
	}
	return out, nil
}

var errAlreadyBootstrapped = errors.New("already bootstrapped")

// bootstrapLockID namespaces the advisory lock that serialises first-run
// seeding across instances. It is released when the transaction ends.
const bootstrapLockID int64 = 0x67626f735f696e74 // "gbos_int"

// resolveAdminPassword takes the bootstrap password from the environment, or
// generates one. Nothing is ever hardcoded: the implementation this replaces
// shipped admin/admin123 in the repository.
func resolveAdminPassword() (password string, generated bool, err error) {
	if fromEnv := os.Getenv("GBOS_ADMIN_PASSWORD"); fromEnv != "" {
		if err := userdomain.ValidatePassword(fromEnv); err != nil {
			return "", false, fmt.Errorf("GBOS_ADMIN_PASSWORD: %w", err)
		}
		return fromEnv, false, nil
	}
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		// A predictable bootstrap password is not an acceptable fallback.
		return "", false, fmt.Errorf("generate admin password: %w", err)
	}
	// Ensure the policy's "letters and something else" rule is met whatever
	// base64 produced.
	return base64.RawURLEncoding.EncodeToString(buf) + "-1", true, nil
}
