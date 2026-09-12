// Command migrate applies and reverts the PostgreSQL schema.
//
//	go run ./cmd/migrate -database "$DATABASE_URL" up
//	go run ./cmd/migrate -database "$DATABASE_URL" version
//	go run ./cmd/migrate -database "$DATABASE_URL" down            # one step
//	go run ./cmd/migrate -database "$DATABASE_URL" down-all -yes   # everything
//
// The migrations are compiled into the binary, so this works from any
// directory and cannot disagree with the schema the server expects.
//
// It replaces a home-grown runner that split SQL files on ";" — shredding any
// statement containing a semicolon in a string literal or a function body —
// ran no migration inside a transaction, and treated a missing migrations
// directory as success.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"gbos/internal/app"
)

func main() {
	log.SetFlags(0)

	var (
		database = flag.String("database", os.Getenv("DATABASE_URL"), "PostgreSQL connection URL (defaults to $DATABASE_URL)")
		confirm  = flag.Bool("yes", false, "confirm a destructive operation")
		steps    = flag.Int("steps", 1, "number of migrations for up-n / down")
	)
	flag.Usage = usage
	flag.Parse()

	command := flag.Arg(0)
	if command == "" {
		usage()
		os.Exit(2)
	}
	// The stdlib flag package stops parsing at the first non-flag argument, so
	// `migrate down-all -yes` would otherwise silently drop the confirmation.
	// Accept it on either side of the command.
	confirmed := *confirm || hasFlag(flag.Args()[1:], "yes")
	if *database == "" {
		log.Fatal("no database URL: pass -database or set DATABASE_URL")
	}

	switch command {
	case "up":
		version, err := app.MigrateUp(*database)
		if err != nil {
			log.Fatalf("up failed: %v", err)
		}
		fmt.Printf("up complete — now at version %d\n", version)

	case "up-n":
		if err := app.MigrateSteps(*database, *steps); err != nil {
			log.Fatalf("up-n failed: %v", err)
		}
		reportVersion(*database)

	case "down":
		// One step by default. Reverting everything needs down-all, so a
		// mistyped command cannot destroy a database.
		if err := app.MigrateSteps(*database, -*steps); err != nil {
			log.Fatalf("down failed: %v", err)
		}
		reportVersion(*database)

	case "down-all":
		if !confirmed {
			log.Fatal("down-all reverts every migration and destroys all data — re-run with -yes if that is what you want")
		}
		if err := app.MigrateDownAll(*database); err != nil {
			log.Fatalf("down-all failed: %v", err)
		}
		fmt.Println("down-all complete — the schema is empty")

	case "version":
		reportVersion(*database)

	case "force":
		if flag.NArg() < 2 {
			log.Fatal("force needs a version: migrate -database ... force 1")
		}
		var v int
		if _, err := fmt.Sscanf(flag.Arg(1), "%d", &v); err != nil {
			log.Fatalf("invalid version %q", flag.Arg(1))
		}
		if !confirmed {
			log.Fatal("force overrides the recorded migration state without running anything — re-run with -yes")
		}
		if err := app.ForceVersion(*database, v); err != nil {
			log.Fatalf("force failed: %v", err)
		}
		reportVersion(*database)

	default:
		usage()
		os.Exit(2)
	}
}

func reportVersion(dsn string) {
	version, dirty, err := app.SchemaVersion(dsn)
	if err != nil {
		log.Fatalf("read version: %v", err)
	}
	if version == 0 {
		fmt.Println("no migrations applied")
		return
	}
	state := "clean"
	if dirty {
		state = "DIRTY — a migration failed part way; repair the schema and use `force`"
	}
	fmt.Printf("version %d (%s)\n", version, state)
}

// hasFlag reports whether args contain -name or --name.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "-"+name || a == "--"+name {
			return true
		}
	}
	return false
}

func usage() {
	fmt.Fprintf(os.Stderr, `migrate — apply and revert the GBOS PostgreSQL schema

The migrations are compiled into this binary; there is no directory to point at.

usage: migrate [flags] <command>

commands:
  up            apply all pending migrations
  up-n          apply -steps migrations (default 1)
  down          revert -steps migrations (default 1)
  down-all      revert every migration; requires -yes. DESTROYS ALL DATA
  version       print the current schema version
  force <v>     set the recorded version without running anything; requires -yes

flags:
`)
	flag.PrintDefaults()
}
