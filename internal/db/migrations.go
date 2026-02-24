package db

import (
	"database/sql"
	"errors"
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

	// Migration 8: Add created_by column to scheduled_tasks for permission tracking.
	// Stores the IRC nick of the user who created the task. When the scheduler
	// fires the task, the creator's current permissions are used for tool filtering.
	// Empty string (default) means legacy tasks with no creator — these bypass
	// permission filtering for backward compatibility.
	`ALTER TABLE scheduled_tasks ADD COLUMN created_by TEXT NOT NULL DEFAULT '';`,

	// Migration 9: Users, channel permissions, and metadata tables.
	// Users table replaces the TOML-based permissions.toml file. The 'default'
	// nick serves as the fallback row when no specific user entry exists.
	// Channel permissions store per-channel tool/model/autonomy overrides.
	// Metadata stores import markers and other key-value state.
	`CREATE TABLE users (
		nick TEXT PRIMARY KEY COLLATE NOCASE,
		role TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin', 'user')),
		tools TEXT NOT NULL DEFAULT '["*"]',
		deny_tools TEXT NOT NULL DEFAULT '[]',
		autonomy TEXT NOT NULL DEFAULT 'approve' CHECK(autonomy IN ('report', 'approve', 'auto', '')),
		allowed_models TEXT NOT NULL DEFAULT '[]',
		deny_models TEXT NOT NULL DEFAULT '[]',
		max_messages_per_hour INTEGER NOT NULL DEFAULT 0,
		api_key TEXT NOT NULL DEFAULT '',
		nickserv_account TEXT NOT NULL DEFAULT '',
		created DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE UNIQUE INDEX idx_users_api_key ON users(api_key) WHERE api_key != '';
	CREATE INDEX idx_users_role ON users(role);

	CREATE TABLE channel_permissions (
		channel TEXT PRIMARY KEY COLLATE NOCASE,
		tools TEXT NOT NULL DEFAULT '[]',
		deny_tools TEXT NOT NULL DEFAULT '[]',
		autonomy TEXT NOT NULL DEFAULT '' CHECK(autonomy IN ('report', 'approve', 'auto', '')),
		allowed_models TEXT NOT NULL DEFAULT '[]'
	);

	CREATE TABLE metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);`,

	// Migration 10: Add provider column to scheduled_tasks for per-task model assignment.
	// When set, the task uses this LLM provider instead of the channel/global default.
	// Empty string (default) means use the normal resolution chain.
	`ALTER TABLE scheduled_tasks ADD COLUMN provider TEXT NOT NULL DEFAULT '';`,

	// Migration 11: RAG memory documents with FTS5 full-text search.
	// memory_documents stores chunked text content for retrieval-augmented generation.
	// The FTS5 virtual table provides fast full-text search over content.
	// Triggers keep the FTS5 index in sync with the base table.
	`CREATE TABLE memory_documents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source TEXT NOT NULL,
		chunk_id TEXT NOT NULL UNIQUE,
		content TEXT NOT NULL,
		metadata TEXT NOT NULL DEFAULT '{}',
		embedding BLOB,
		created DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_memory_source ON memory_documents(source);

	CREATE VIRTUAL TABLE memory_documents_fts USING fts5(
		content, source, chunk_id,
		content=memory_documents,
		content_rowid=id
	);

	CREATE TRIGGER memory_documents_ai AFTER INSERT ON memory_documents BEGIN
		INSERT INTO memory_documents_fts(rowid, content, source, chunk_id)
		VALUES (new.id, new.content, new.source, new.chunk_id);
	END;

	CREATE TRIGGER memory_documents_ad AFTER DELETE ON memory_documents BEGIN
		INSERT INTO memory_documents_fts(memory_documents_fts, rowid, content, source, chunk_id)
		VALUES ('delete', old.id, old.content, old.source, old.chunk_id);
	END;

	CREATE TRIGGER memory_documents_au AFTER UPDATE ON memory_documents BEGIN
		INSERT INTO memory_documents_fts(memory_documents_fts, rowid, content, source, chunk_id)
		VALUES ('delete', old.id, old.content, old.source, old.chunk_id);
		INSERT INTO memory_documents_fts(rowid, content, source, chunk_id)
		VALUES (new.id, new.content, new.source, new.chunk_id);
	END;`,

	// Migration 12: Docker container tracking for the docker_manage tool.
	// Stores metadata for containers created and managed by the LLM agent.
	// container_id and name are unique to prevent duplicates. Status is
	// synced with Docker on startup via reconciliation.
	`CREATE TABLE docker_containers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		container_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL UNIQUE,
		image TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'created',
		channel TEXT NOT NULL,
		nick TEXT NOT NULL,
		ports TEXT NOT NULL DEFAULT '[]',
		created DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_docker_containers_status ON docker_containers(status);`,

	// Migration 13: Usage statistics table for tracking LLM token consumption,
	// tool invocations, and request metadata. Each row represents one LLM API
	// call. Tool-level detail is stored as a JSON array in tool_details.
	// The status column tracks whether the call succeeded or failed.
	`CREATE TABLE usage_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		channel TEXT NOT NULL COLLATE NOCASE,
		nick TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
		provider TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		tool_calls_count INTEGER NOT NULL DEFAULT 0,
		tool_details TEXT NOT NULL DEFAULT '[]',
		latency_ms INTEGER NOT NULL DEFAULT 0,
		iteration INTEGER NOT NULL DEFAULT 0,
		request_type TEXT NOT NULL DEFAULT 'chat' CHECK(request_type IN ('chat', 'task', 'event', 'summary')),
		status TEXT NOT NULL DEFAULT 'ok' CHECK(status IN ('ok', 'error')),
		error_message TEXT
	);
	CREATE INDEX idx_usage_stats_timestamp ON usage_stats(timestamp);
	CREATE INDEX idx_usage_stats_channel ON usage_stats(channel);
	CREATE INDEX idx_usage_stats_provider ON usage_stats(provider);
	CREATE INDEX idx_usage_stats_nick ON usage_stats(nick);
	CREATE INDEX idx_usage_stats_channel_ts ON usage_stats(channel, timestamp);
	CREATE INDEX idx_usage_stats_provider_ts ON usage_stats(provider, timestamp);`,
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
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("SchemaVersion: %w", err)
	}
	return version, nil
}
