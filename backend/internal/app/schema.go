package app

import (
	"database/sql"
	"errors"
	"fmt"

	"gbos/migrations"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// MigrateUp applies every pending migration and reports the resulting schema
// version.
//
// It opens its own short-lived connection rather than borrowing the
// application pool: golang-migrate's Postgres driver closes the *sql.DB it is
// given, which would take the running pool down with it.
//
// The migrations are compiled into the binary, so the schema cannot drift from
// the code that expects it, and an image built without a migrations directory
// cannot quietly start against a stale database — which is exactly what the
// previous runner did, treating a missing directory as success.
//
// golang-migrate holds a Postgres advisory lock for the duration, so two
// instances starting together queue instead of racing.
func MigrateUp(dsn string) (version uint, err error) {
	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		return 0, err
	}
	defer closeFn()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, errors.New("no migrations were applied — the schema is empty")
	}
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return version, fmt.Errorf("schema version %d is dirty: a migration failed part way and the database is in an unknown state", version)
	}
	return version, nil
}

// MigrateSteps moves the schema by n migrations: forward when positive,
// backward when negative.
func MigrateSteps(dsn string, n int) error {
	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer closeFn()

	if err := m.Steps(n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("move schema by %d: %w", n, err)
	}
	return nil
}

// MigrateDownAll reverts every migration. It destroys all data and exists for
// the CI check that proves the schema can be rolled back.
func MigrateDownAll(dsn string) error {
	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer closeFn()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("revert schema: %w", err)
	}
	return nil
}

// ForceVersion overwrites the recorded version without running anything. It is
// the escape hatch for a dirty schema that has been repaired by hand.
func ForceVersion(dsn string, version int) error {
	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer closeFn()

	if err := m.Force(version); err != nil {
		return fmt.Errorf("force version %d: %w", version, err)
	}
	return nil
}

// SchemaVersion reports the applied version without changing anything, for a
// deployment that migrates as a separate step and wants the server to verify
// rather than act.
func SchemaVersion(dsn string) (version uint, dirty bool, err error) {
	m, closeFn, err := newMigrator(dsn)
	if err != nil {
		return 0, false, err
	}
	defer closeFn()

	version, dirty, err = m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}

func newMigrator(dsn string) (*migrate.Migrate, func(), error) {
	src, err := migrations.Postgres()
	if err != nil {
		return nil, nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	source, err := iofs.New(src, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("open embedded migrations: %w", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("connect for migration: %w", err)
	}
	driver, err := migratepg.WithInstance(db, &migratepg.Config{})
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("prepare migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("prepare migrator: %w", err)
	}
	// m.Close() closes the driver, which closes db. Nothing else holds it.
	return m, func() { _, _ = m.Close() }, nil
}
