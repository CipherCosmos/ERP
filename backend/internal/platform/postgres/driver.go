package postgres

// Registering the pgx driver with database/sql is a side effect of importing
// this package, so Open works with no wiring at the call site and a repository
// test cannot silently run against a driver nobody registered.
//
// stdlib registers itself under the names "pgx" and "pgx/v5"; DefaultDriverName
// is the former.
import _ "github.com/jackc/pgx/v5/stdlib"
