package app_test

import (
	"io/fs"
	"strings"
	"testing"

	"gbos/internal/app"
	"gbos/internal/testsupport"
	"gbos/migrations"
)

func TestTheSchemaIsCompiledIntoTheBinary(t *testing.T) {
	t.Parallel()

	// If this is ever empty the server will start against whatever schema
	// happens to be in the database, which is the failure the embedding
	// exists to prevent. A missing directory used to be treated as success.
	src, err := migrations.Postgres()
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}

	var up, down int
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".up.sql"):
			up++
		case strings.HasSuffix(e.Name(), ".down.sql"):
			down++
		}
	}
	if up == 0 {
		t.Fatal("no up migrations are embedded in the binary")
	}
	// Every migration needs a way back, or a bad deploy has no exit.
	if up != down {
		t.Errorf("%d up migrations but %d down migrations", up, down)
	}
}

func TestMigrateUpIsIdempotent(t *testing.T) {
	t.Parallel()
	dsn := testsupport.DSN(t)

	// The test database is already migrated, so this must be a clean no-op
	// that still reports the version rather than an error.
	version, err := app.MigrateUp(dsn)
	if err != nil {
		t.Fatalf("migrate up on an already-migrated database: %v", err)
	}
	if version == 0 {
		t.Error("reported version 0 for a migrated database")
	}

	again, err := app.MigrateUp(dsn)
	if err != nil {
		t.Fatalf("second migrate up: %v", err)
	}
	if again != version {
		t.Errorf("version moved from %d to %d without a new migration", version, again)
	}
}

func TestSchemaVersionReportsWithoutChangingAnything(t *testing.T) {
	t.Parallel()
	dsn := testsupport.DSN(t)

	version, dirty, err := app.SchemaVersion(dsn)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version == 0 {
		t.Error("reported version 0 for a migrated database")
	}
	// A dirty schema means a migration failed part way. Starting a server
	// against one would run application code against a half-built database.
	if dirty {
		t.Error("a freshly migrated database reported dirty")
	}
}
