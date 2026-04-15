package db

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// migrationDir is set by the caller (main.go) before Initialize() is called.
var MigrationDir string

func runMigrations() error {
	// Create migrations tracking table
	_, err := DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return err
	}

	if MigrationDir == "" {
		// Try to locate migrations relative to the executable
		exe, _ := os.Executable()
		MigrationDir = filepath.Join(filepath.Dir(exe), "migrations")
		if _, err := os.Stat(MigrationDir); os.IsNotExist(err) {
			// Fallback: current working directory
			MigrationDir = "migrations"
		}
	}

	entries, err := os.ReadDir(MigrationDir)
	if err != nil {
		// No migrations directory — not an error
		log.Printf("No migrations directory at %s, skipping", MigrationDir)
		return nil
	}

	// Sort by filename (0001_xxx.sql, 0002_xxx.sql, ...)
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		// Check if already applied
		var count int
		err := DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name).Scan(&count)
		if err != nil {
			return err
		}
		if count > 0 {
			continue
		}

		// Read and execute
		data, err := os.ReadFile(filepath.Join(MigrationDir, name))
		if err != nil {
			return err
		}

		sqlContent := string(data)
		// Execute each statement separated by semicolons
		statements := strings.Split(sqlContent, ";")
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := DB.Exec(stmt); err != nil {
				log.Printf("Migration %s failed: %v (statement: %s)", name, err, stmt)
				// For ALTER TABLE ADD COLUMN, duplicate column is not fatal
				if strings.Contains(err.Error(), "duplicate column") {
					continue
				}
				return err
			}
		}

		// Mark as applied
		_, err = DB.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name)
		if err != nil {
			return err
		}
		log.Printf("Applied migration: %s", name)
	}

	return nil
}
