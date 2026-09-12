// Package migrations carries the PostgreSQL schema inside the binary.
//
// Embedding rather than reading from disk removes a whole failure mode: the
// previous runner looked for a migrations directory relative to the executable
// and treated a missing one as success, so an image built without it would
// start cleanly and run against whatever schema happened to be there.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed postgres/*.sql
var files embed.FS

// Postgres returns the PostgreSQL migrations as a filesystem rooted at the
// migration files themselves, ready for golang-migrate's iofs source.
func Postgres() (fs.FS, error) {
	return fs.Sub(files, "postgres")
}
