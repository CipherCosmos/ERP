// Package testsupport provides real PostgreSQL databases for tests.
//
// Tests here run against a real database rather than a mock. The defects this
// suite exists to catch — constraint violations, lost updates, transaction
// rollback behaviour, unique-index collisions — are precisely the ones a
// mocked SQL layer cannot see.
//
// Each test gets its own database, cloned from a migrated template, so tests
// are isolated and can run in parallel. Cloning from a template is far cheaper
// than re-running migrations per test.
//
// Set TEST_DATABASE_URL to point at a running Postgres:
//
//	make db-up     # starts one on localhost:5434
//	make test
//
// Without it, database-backed tests skip with an explanatory message.
package testsupport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	platform "gbos/internal/platform/postgres"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// templateLockID namespaces the advisory lock that serialises template
// creation. Go runs each package's tests as its own binary and `go test ./...`
// runs those binaries concurrently, so a sync.Once is not enough: the
// coordination has to happen in the database that all of them share.
const templateLockID int64 = 0x67626f735f746d70 // "gbos_tmp"

var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

// SkipIfNoDatabase skips the test unless TEST_DATABASE_URL is set.
func SkipIfNoDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set — run `make db-up` and re-run, or use `make test`")
	}
	return dsn
}

// DSN returns a connection string to an isolated, freshly migrated database
// that is dropped when the test finishes.
func DSN(t *testing.T) string {
	t.Helper()
	baseDSN := SkipIfNoDatabase(t)

	templateOnce.Do(func() { templateName, templateErr = buildTemplate(baseDSN) })
	if templateErr != nil {
		t.Fatalf("prepare template database: %v", templateErr)
	}

	name := "gbos_test_" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect for database creation: %v", err)
	}
	defer admin.Close()

	// TEMPLATE copies the already-migrated schema, so a test pays for the
	// migrations once rather than once per test. Postgres refuses to clone a
	// template while another session is connected to it, which can happen
	// transiently while a sibling package is finishing its own setup, so
	// retry briefly rather than failing the test for a scheduling accident.
	create := fmt.Sprintf(`CREATE DATABASE %q TEMPLATE %q`, name, templateName)
	var createErr error
	for attempt := 0; attempt < 20; attempt++ {
		if _, createErr = admin.Exec(ctx, create); createErr == nil {
			break
		}
		if !strings.Contains(createErr.Error(), "being accessed by other users") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if createErr != nil {
		t.Fatalf("create test database %s: %v", name, createErr)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin, err := pgxpool.New(ctx, baseDSN)
		if err != nil {
			return
		}
		defer admin.Close()
		// Terminate stragglers first; DROP DATABASE fails while a session is
		// still attached.
		_, _ = admin.Exec(ctx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
		_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, name))
	})

	return replaceDatabase(baseDSN, name)
}

// DBConfig returns a platform.Config for an isolated test database.
//
// The pool is deliberately small. Every parallel test in every concurrently
// running package opens its own pool, and the production default of twenty
// connections each exhausts the server's max_connections long before the
// tests are the bottleneck.
func DBConfig(t *testing.T) platform.Config {
	t.Helper()
	return platform.Config{
		DSN:          DSN(t),
		MaxOpenConns: 3,
		MaxIdleConns: 1,
	}
}

// OpenDB returns a platform pool on an isolated, freshly migrated database,
// closed when the test finishes.
func OpenDB(t *testing.T) *platform.DB {
	t.Helper()
	db, err := platform.Open(context.Background(), DBConfig(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Pool returns a connection pool to an isolated, freshly migrated database.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, DSN(t))
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}
	return pool
}

// buildTemplate ensures a migrated template database exists and returns its
// name.
//
// The name carries a fingerprint of the migration files, so editing a
// migration produces a different template rather than silently reusing a stale
// one — and no template is ever dropped, which would race with a sibling test
// binary cloning from it.
func buildTemplate(baseDSN string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	dir, err := MigrationsDir()
	if err != nil {
		return "", err
	}
	fingerprint, err := fingerprintMigrations(dir)
	if err != nil {
		return "", err
	}
	name := "gbos_test_tmpl_" + fingerprint

	pool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	// One connection holds the advisory lock for the whole check-and-create,
	// so concurrent test binaries queue rather than colliding on CREATE
	// DATABASE.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, templateLockID); err != nil {
		return "", fmt.Errorf("take template lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, templateLockID)
	}()

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", fmt.Errorf("look for template: %w", err)
	}
	if exists {
		return name, nil
	}

	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		return "", fmt.Errorf("create template: %w", err)
	}

	m, err := migrate.New("file://"+dir, replaceDatabase(baseDSN, name))
	if err != nil {
		return "", fmt.Errorf("open migrations at %s: %w", dir, err)
	}
	// migrate holds a connection to the template and a clone cannot start
	// while it does. Deferred calls run last-registered-first, so this closes
	// before the advisory lock is released above.
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		// Leave nothing half-migrated behind for the next run to reuse.
		_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, name))
		return "", fmt.Errorf("apply migrations: %w", err)
	}
	return name, nil
}

// fingerprintMigrations hashes the migration file names and contents, so a
// changed schema yields a different template database.
func fingerprintMigrations(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read migrations at %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	h := sha256.New()
	for _, n := range names {
		content, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", n, err)
		}
		h.Write([]byte(n))
		h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// MigrationsDir locates backend/migrations/postgres by walking up from the
// working directory until it finds go.mod, so tests work from any package.
func MigrationsDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			candidate := filepath.Join(dir, "migrations", "postgres")
			if _, err := os.Stat(candidate); err != nil {
				return "", fmt.Errorf("migrations directory not found at %s: %w", candidate, err)
			}
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate go.mod above %s", dir)
		}
		dir = parent
	}
}

// replaceDatabase swaps the database name in a Postgres connection string.
func replaceDatabase(dsn, name string) string {
	scheme := dsn
	query := ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		scheme, query = dsn[:i], dsn[i:]
	}
	if i := strings.LastIndex(scheme, "/"); i >= 0 {
		scheme = scheme[:i+1] + name
	}
	return scheme + query
}
