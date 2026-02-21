// Package db provides SQLite database connectivity and schema migrations
// for the Murmur server. It uses modernc.org/sqlite (pure Go, no CGO).
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// DB wraps a *sql.DB connection to a SQLite database.
type DB struct {
	*sql.DB
}

// Open opens or creates a SQLite database at the given path.
// It enables WAL mode and foreign key enforcement.
// Use ":memory:" for an in-memory database (useful for testing).
func Open(path string) (*DB, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("Open: create directory %s: %w", dir, err)
		}
	}

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("Open: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("Open: enable WAL mode: %w", err)
	}

	// Enable foreign key enforcement.
	if _, err := sqlDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("Open: enable foreign keys: %w", err)
	}

	// Verify the connection works.
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("Open: ping: %w", err)
	}

	return &DB{sqlDB}, nil
}
