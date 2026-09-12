// Package postgres holds the database access primitives the repository layer is
// built on: pool construction, a transaction runner, and driver-error
// translation.
//
// # Why database/sql + pgx/v5/stdlib rather than pgxpool
//
// pgxpool is the better pool in isolation: it speaks the native protocol with no
// database/sql layer in between, and it exposes richer Postgres types. It was
// not chosen here, for two reasons specific to this codebase.
//
//  1. The migration has to be incremental. The application has ~219 query sites
//     written against *sql.DB. Keeping database/sql lets one table at a time
//     move behind a repository while every untouched site keeps compiling and
//     keeps working. A pgxpool cutover is all-or-nothing across all 219, which
//     is not a change anyone can review.
//
//  2. The rest of the toolchain already speaks database/sql. golang-migrate
//     drives Postgres through it, so sharing the driver means one DSN, one
//     driver and one set of pool semantics between migrations and the
//     application rather than two stacks to reason about during an incident.
//
// The cost is real and worth naming: every query crosses the database/sql
// abstraction, and Postgres-native types (arrays, ranges, composites) need
// explicit handling that pgxpool would give for free. For this workload — a few
// dozen concurrent users across 1-3 stores, short OLTP writes — that overhead is
// not the constraint.
//
// The escape hatch is deliberately cheap. Nothing outside this file and tx.go
// touches the pool type, and error mapping already reads *pgconn.PgError, which
// is what pgx surfaces either way. Swapping the innards for pgxpool later leaves
// Querier, Tx, InTx and every sentinel error unchanged.
//
// # Registering the driver
//
// Importing this package registers the pgx driver with database/sql as a side
// effect (see driver.go), so Open needs no wiring at the call site.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"
)

// DefaultDriverName is the driver database/sql looks up in Open. It matches the
// name github.com/jackc/pgx/v5/stdlib registers itself under.
const DefaultDriverName = "pgx"

// Pool defaults sized for a single VPS serving 1-3 retail stores.
//
// This is light traffic: a few dozen concurrent users, short OLTP writes, and
// the occasional report. The numbers below are a starting point, not a ceiling —
// raise MaxOpenConns only alongside Postgres's own max_connections.
//
// For reference, the SQLite configuration this replaces caps at 2 connections,
// which serialises every request behind two file handles.
const (
	// DefaultMaxOpenConns bounds total connections. Postgres allocates
	// per-connection memory, so an unbounded pool is a slow memory leak. 20
	// leaves headroom under a stock max_connections of 100 for psql, backups
	// and migrations.
	DefaultMaxOpenConns = 20

	// DefaultMaxIdleConns keeps connections warm between requests. Setting this
	// well below MaxOpenConns causes connection churn under bursty load, so it
	// tracks at half.
	DefaultMaxIdleConns = 10

	// DefaultConnMaxLifetime recycles connections so that a failover, a DNS
	// change or a server-side memory leak cannot pin a stale connection
	// indefinitely.
	DefaultConnMaxLifetime = 30 * time.Minute

	// DefaultConnMaxIdleTime returns connections that outlived their usefulness
	// during a quiet night, rather than holding 10 sockets open until morning.
	DefaultConnMaxIdleTime = 5 * time.Minute

	// DefaultConnectTimeout bounds the initial Open + Ping.
	DefaultConnectTimeout = 10 * time.Second
)

// Config describes a connection pool.
type Config struct {
	// DSN is the libpq connection string or postgres:// URL. Required.
	DSN string

	// DriverName is the database/sql driver to use. Empty means
	// DefaultDriverName.
	DriverName string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration

	// ConnectTimeout bounds the health check Open performs before returning.
	ConnectTimeout time.Duration
}

// ConfigFromEnv reads a Config from the environment. DATABASE_URL supplies the
// DSN; the pool knobs fall back to the defaults above when unset or unparseable.
//
// Recognised variables:
//
//	DATABASE_URL              connection string (required)
//	DATABASE_MAX_OPEN_CONNS   integer
//	DATABASE_MAX_IDLE_CONNS   integer
//	DATABASE_CONN_MAX_LIFETIME  duration, e.g. "30m"
//	DATABASE_CONN_MAX_IDLE_TIME duration, e.g. "5m"
//	DATABASE_CONNECT_TIMEOUT    duration, e.g. "10s"
func ConfigFromEnv() Config {
	return Config{
		DSN:             os.Getenv("DATABASE_URL"),
		DriverName:      os.Getenv("DATABASE_DRIVER"),
		MaxOpenConns:    envInt("DATABASE_MAX_OPEN_CONNS", DefaultMaxOpenConns),
		MaxIdleConns:    envInt("DATABASE_MAX_IDLE_CONNS", DefaultMaxIdleConns),
		ConnMaxLifetime: envDuration("DATABASE_CONN_MAX_LIFETIME", DefaultConnMaxLifetime),
		ConnMaxIdleTime: envDuration("DATABASE_CONN_MAX_IDLE_TIME", DefaultConnMaxIdleTime),
		ConnectTimeout:  envDuration("DATABASE_CONNECT_TIMEOUT", DefaultConnectTimeout),
	}
}

// withDefaults fills zero fields and reports why the config is unusable.
func (c Config) withDefaults() (Config, error) {
	if c.DSN == "" {
		return c, fmt.Errorf("postgres: DSN is empty (set DATABASE_URL)")
	}
	if c.DriverName == "" {
		c.DriverName = DefaultDriverName
	}
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = DefaultMaxOpenConns
	}
	if c.MaxIdleConns <= 0 {
		c.MaxIdleConns = DefaultMaxIdleConns
	}
	// An idle cap above the open cap is silently clamped by database/sql; clamp
	// it here so Stats and the configuration agree.
	if c.MaxIdleConns > c.MaxOpenConns {
		c.MaxIdleConns = c.MaxOpenConns
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = DefaultConnMaxLifetime
	}
	if c.ConnMaxIdleTime <= 0 {
		c.ConnMaxIdleTime = DefaultConnMaxIdleTime
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = DefaultConnectTimeout
	}
	return c, nil
}

// Querier is the read/write surface shared by a pool and a transaction. A
// repository method takes a Querier so the same code runs inside or outside a
// transaction.
//
// Every method takes a context. That is the point: none of the 219 query sites
// this replaces can be cancelled, so a slow report holds its connection until it
// finishes no matter what the client does.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Compile-time proof that both concrete types satisfy Querier.
var (
	_ Querier = (*sql.DB)(nil)
	_ Querier = (*sql.Tx)(nil)
)

// DB is a connection pool.
type DB struct {
	db  *sql.DB
	cfg Config
}

// Open builds a pool and verifies it with a real round-trip. It returns an
// error rather than a half-usable handle: sql.Open alone is lazy and will
// happily return a pool pointed at a database that does not exist.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}

	sqlDB, err := sql.Open(cfg.DriverName, cfg.DSN)
	if err != nil {
		// Do not wrap with %w into the caller's face: a bad DSN string can
		// contain the password.
		return nil, fmt.Errorf("postgres: open driver %q: %w", cfg.DriverName, MapError(err))
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	db := &DB{db: sqlDB, cfg: cfg}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := db.Ping(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	return db, nil
}

// New wraps an already-open *sql.DB. It applies no pool settings and performs no
// health check; Open is the normal entrypoint. This exists so a test harness can
// hand over a pool it built itself.
func New(sqlDB *sql.DB) *DB {
	return &DB{db: sqlDB, cfg: Config{DriverName: DefaultDriverName}}
}

// SQL exposes the underlying pool. It is the migration escape hatch for code
// that still needs a raw *sql.DB; new code should take a Querier.
func (db *DB) SQL() *sql.DB { return db.db }

// Config returns the settings this pool was built with.
func (db *DB) Config() Config { return db.cfg }

// Stats reports pool utilisation. WaitCount and WaitDuration climbing means
// MaxOpenConns is too low.
func (db *DB) Stats() sql.DBStats { return db.db.Stats() }

// Close drains the pool.
func (db *DB) Close() error { return db.db.Close() }

// Ping round-trips a real query rather than relying on the driver's cached
// connection state, so a health endpoint built on it reports the database as
// down when the database is actually down.
func (db *DB) Ping(ctx context.Context) error {
	var one int
	if err := db.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return MapError(err)
	}
	if one != 1 {
		return fmt.Errorf("postgres: health check returned %d, want 1", one)
	}
	return nil
}

// ExecContext runs a statement against the pool, mapping the driver error.
func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := db.db.ExecContext(ctx, query, args...)
	return res, MapError(err)
}

// QueryContext runs a query against the pool, mapping the driver error.
//
// Errors deferred to rows.Err() are not mapped here; call MapError on the
// result of rows.Err(). Do not ignore it — a mid-stream failure surfaces there
// and nowhere else.
func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := db.db.QueryContext(ctx, query, args...)
	return rows, MapError(err)
}

// QueryRowContext runs a single-row query.
//
// The error surfaces from Row.Scan and is therefore raw. Pass it through
// MapError to turn sql.ErrNoRows into ErrNotFound:
//
//	err := postgres.MapError(db.QueryRowContext(ctx, q, id).Scan(&name))
func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.db.QueryRowContext(ctx, query, args...)
}

func envInt(key string, def int) int {
	if raw := os.Getenv(key); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if raw := os.Getenv(key); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return def
}
