package db

import (
	"database/sql"
	"fmt"
)

// migrations is an ordered list of SQL migration scripts.
// Each entry corresponds to a schema version (1-indexed).
// Migrations are applied in order and tracked via the schema_version table.
var migrations = []string{
	// Migration 1: Core tables for conversations, clients, tasks, summaries, and notes.
	`
	CREATE TABLE conversations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		tool_name TEXT,
		tool_call_id TEXT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_conversations_channel ON conversations(channel);
	CREATE INDEX idx_conversations_channel_ts ON conversations(channel, timestamp);

	CREATE TABLE clients (
		client_id TEXT PRIMARY KEY,
		hostname TEXT,
		tools_json TEXT,
		last_heartbeat DATETIME,
		status TEXT DEFAULT 'online'
	);

	CREATE TABLE scheduled_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		schedule TEXT NOT NULL,
		action TEXT NOT NULL,
		channel TEXT NOT NULL,
		enabled BOOLEAN DEFAULT 1,
		last_run DATETIME,
		next_run DATETIME
	);
	CREATE INDEX idx_scheduled_tasks_next_run ON scheduled_tasks(next_run);

	CREATE TABLE summaries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel TEXT NOT NULL,
		summary TEXT NOT NULL,
		messages_start INTEGER,
		messages_end INTEGER,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_summaries_channel ON summaries(channel);

	CREATE TABLE notes (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		created DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`,

	// Migration 2: Composite index for efficient scheduled task lookups.
	`CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_enabled_next ON scheduled_tasks(enabled, next_run);`,

	// Migration 3: Custom tools table for LLM-created runtime tools.
	`CREATE TABLE custom_tools (
		name TEXT PRIMARY KEY,
		description TEXT NOT NULL,
		parameters TEXT NOT NULL DEFAULT '{}',
		backend TEXT NOT NULL CHECK(backend IN ('shell', 'http', 'code_exec')),
		backend_config TEXT NOT NULL DEFAULT '{}',
		enabled BOOLEAN NOT NULL DEFAULT 1,
		created DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_custom_tools_enabled ON custom_tools(enabled);`,

	// Migration 4: Events table for external event ingestion via REST API.
	`CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT,
		source TEXT NOT NULL,
		event_type TEXT NOT NULL,
		summary TEXT NOT NULL,
		data TEXT,
		channel TEXT NOT NULL DEFAULT '#murmur',
		processed_at DATETIME,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE UNIQUE INDEX idx_events_event_id ON events(event_id) WHERE event_id IS NOT NULL;
	CREATE INDEX idx_events_timestamp ON events(timestamp);
	CREATE INDEX idx_events_source ON events(source);`,

	// Migration 5: Per-channel settings for auto-join, model selection, and topic sync.
	`CREATE TABLE channel_settings (
		channel TEXT PRIMARY KEY,
		provider TEXT NOT NULL DEFAULT '',
		auto_join BOOLEAN NOT NULL DEFAULT 0,
		topic_prefix TEXT NOT NULL DEFAULT '',
		created DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_channel_settings_auto_join ON channel_settings(auto_join) WHERE auto_join = 1;`,

	// Migration 6: Add 'pipeline' to the custom_tools backend CHECK constraint.
	// SQLite does not support ALTER TABLE ... ALTER CONSTRAINT, so we recreate
	// the table in a transaction: backup → drop → create with new constraint → copy → drop backup.
	`CREATE TABLE custom_tools_backup AS SELECT * FROM custom_tools;
	DROP TABLE custom_tools;
	CREATE TABLE custom_tools (
		name TEXT PRIMARY KEY,
		description TEXT NOT NULL,
		parameters TEXT NOT NULL DEFAULT '{}',
		backend TEXT NOT NULL CHECK(backend IN ('shell', 'http', 'code_exec', 'pipeline')),
		backend_config TEXT NOT NULL DEFAULT '{}',
		enabled BOOLEAN NOT NULL DEFAULT 1,
		created DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_custom_tools_enabled ON custom_tools(enabled);
	INSERT INTO custom_tools (name, description, parameters, backend, backend_config, enabled, created, updated)
		SELECT name, description, parameters, backend, backend_config, enabled, created, updated FROM custom_tools_backup;
	DROP TABLE custom_tools_backup;`,

	// Migration 7: Add type and run_at columns to scheduled_tasks for one-shot reminders.
	// 'type' is 'cron' (existing recurring tasks) or 'once' (fire once then auto-disable).
	// 'run_at' is the absolute fire time for one-shot tasks (NULL for cron tasks).
	`ALTER TABLE scheduled_tasks ADD COLUMN type TEXT NOT NULL DEFAULT 'cron';
	 ALTER TABLE scheduled_tasks ADD COLUMN run_at DATETIME;`,
}

// Migrate runs all pending schema migrations. It creates the schema_version
// table if it doesn't exist and applies migrations in order. Each migration
// and its version update run in the same transaction for atomicity.
func (db *DB) Migrate() error {
	// Create the schema_version table if it doesn't exist.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`)
	if err != nil {
		return fmt.Errorf("Migrate: create schema_version table: %w", err)
	}

	// Get current schema version.
	version, err := db.SchemaVersion()
	if err != nil {
		return fmt.Errorf("Migrate: get schema version: %w", err)
	}

	// Apply pending migrations.
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("Migrate: begin transaction for migration %d: %w", i+1, err)
		}

		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("Migrate: apply migration %d: %w", i+1, err)
		}

		// Update schema version inside the same transaction for atomicity.
		// If the process crashes, either both the migration and version update
		// are applied, or neither is.
		if _, err := tx.Exec("DELETE FROM schema_version"); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("Migrate: update version for migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", i+1); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("Migrate: insert version for migration %d: %w", i+1, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("Migrate: commit migration %d: %w", i+1, err)
		}
	}

	return nil
}

// SchemaVersion returns the current schema version of the database.
// Returns 0 if no version has been set yet (before any migrations).
func (db *DB) SchemaVersion() (int, error) {
	var version int
	err := db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("SchemaVersion: %w", err)
	}
	return version, nil
}
