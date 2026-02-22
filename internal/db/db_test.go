package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestOpen_InMemory(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	defer database.Close()

	// Verify the connection works.
	if err := database.Ping(); err != nil {
		t.Fatalf("Ping error: %v", err)
	}
}

func TestOpen_WALMode(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	var journalMode string
	err = database.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatalf("PRAGMA journal_mode error: %v", err)
	}

	// In-memory databases may report "memory" instead of "wal".
	if journalMode != "wal" && journalMode != "memory" {
		t.Errorf("expected journal_mode 'wal' or 'memory', got %q", journalMode)
	}
}

func TestOpen_ForeignKeys(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	var fkEnabled int
	err = database.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatalf("PRAGMA foreign_keys error: %v", err)
	}

	if fkEnabled != 1 {
		t.Errorf("expected foreign_keys=1, got %d", fkEnabled)
	}
}

func TestMigrate_CreatesAllTables(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	// Verify all expected tables exist.
	expectedTables := []string{
		"schema_version",
		"conversations",
		"clients",
		"scheduled_tasks",
		"summaries",
		"notes",
		"custom_tools",
		"events",
		"channel_settings",
		"users",
		"channel_permissions",
		"metadata",
	}

	for _, table := range expectedTables {
		var name string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestMigrate_CreatesIndexes(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	expectedIndexes := []string{
		"idx_conversations_channel",
		"idx_conversations_channel_ts",
		"idx_scheduled_tasks_next_run",
		"idx_scheduled_tasks_enabled_next",
		"idx_summaries_channel",
		"idx_custom_tools_enabled",
		"idx_events_event_id",
		"idx_events_timestamp",
		"idx_events_source",
		"idx_channel_settings_auto_join",
		"idx_users_api_key",
		"idx_users_role",
	}

	for _, idx := range expectedIndexes {
		var name string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?",
			idx,
		).Scan(&name)
		if err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	// Run migrate twice — should not error.
	if err := database.Migrate(); err != nil {
		t.Fatalf("first Migrate error: %v", err)
	}

	if err := database.Migrate(); err != nil {
		t.Fatalf("second Migrate error: %v", err)
	}

	// Verify schema version matches the number of migrations.
	version, err := database.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion error: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("expected schema version %d, got %d", len(migrations), version)
	}
}

func TestSchemaVersion_BeforeMigrate(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	// Create schema_version table manually (Migrate does this, but we test the raw path).
	_, err = database.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)")
	if err != nil {
		t.Fatalf("create schema_version error: %v", err)
	}

	version, err := database.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion error: %v", err)
	}
	if version != 0 {
		t.Errorf("expected schema version 0 before migrate, got %d", version)
	}
}

func TestSchemaVersion_AfterMigrate(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	version, err := database.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion error: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("expected schema version %d, got %d", len(migrations), version)
	}
}

func TestMigrate_TablesAreUsable(t *testing.T) {
	t.Parallel()

	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	// Insert into conversations.
	_, err = database.Exec(
		"INSERT INTO conversations (channel, role, content) VALUES (?, ?, ?)",
		"#test", "user", "hello",
	)
	if err != nil {
		t.Errorf("insert into conversations error: %v", err)
	}

	// Insert into clients.
	_, err = database.Exec(
		"INSERT INTO clients (client_id, hostname, status) VALUES (?, ?, ?)",
		"test-client", "testhost", "online",
	)
	if err != nil {
		t.Errorf("insert into clients error: %v", err)
	}

	// Insert into scheduled_tasks.
	_, err = database.Exec(
		"INSERT INTO scheduled_tasks (name, schedule, action, channel) VALUES (?, ?, ?, ?)",
		"test-task", "0 * * * *", "do something", "#test",
	)
	if err != nil {
		t.Errorf("insert into scheduled_tasks error: %v", err)
	}

	// Insert into summaries.
	_, err = database.Exec(
		"INSERT INTO summaries (channel, summary, messages_start, messages_end) VALUES (?, ?, ?, ?)",
		"#test", "test summary", 1, 10,
	)
	if err != nil {
		t.Errorf("insert into summaries error: %v", err)
	}

	// Insert into notes.
	_, err = database.Exec(
		"INSERT INTO notes (key, value) VALUES (?, ?)",
		"test-key", "test-value",
	)
	if err != nil {
		t.Errorf("insert into notes error: %v", err)
	}

	// Verify we can read back.
	var content string
	err = database.QueryRow("SELECT content FROM conversations WHERE channel=?", "#test").Scan(&content)
	if err != nil {
		t.Errorf("select from conversations error: %v", err)
	}
	if content != "hello" {
		t.Errorf("expected content 'hello', got %q", content)
	}
}

// newTestDB creates an in-memory database with all migrations applied.
func TestInsertEvent(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	e := &Event{
		EventID:   "test-123",
		Source:    "backup-script",
		EventType: "backup.completed",
		Summary:   "Backup completed successfully",
		Data:      `{"size": "1.2GB"}`,
		Channel:   "#murmur",
	}

	id, inserted, err := db.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("InsertEvent error: %v", err)
	}
	if !inserted {
		t.Error("expected inserted=true for new event")
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}
}

func TestInsertEvent_NoEventID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	e := &Event{
		Source:    "cron",
		EventType: "job.done",
		Summary:   "Cron job finished",
		Channel:   "#murmur",
	}

	id, inserted, err := db.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("InsertEvent error: %v", err)
	}
	if !inserted {
		t.Error("expected inserted=true")
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}
}

func TestInsertEvent_Idempotency(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	e := &Event{
		EventID:   "dedup-key-1",
		Source:    "webhook",
		EventType: "deploy.started",
		Summary:   "Deployment started",
		Channel:   "#murmur",
	}

	id1, inserted1, err := db.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("first InsertEvent error: %v", err)
	}
	if !inserted1 {
		t.Error("expected first insert to succeed")
	}

	// Second insert with same event_id should return existing id.
	id2, inserted2, err := db.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("second InsertEvent error: %v", err)
	}
	if inserted2 {
		t.Error("expected second insert to be deduplicated")
	}
	if id2 != id1 {
		t.Errorf("expected same id %d, got %d", id1, id2)
	}
}

func TestEventByEventID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Non-existent event_id returns nil.
	got, err := db.EventByEventID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("EventByEventID error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent event_id")
	}

	// Insert and retrieve.
	e := &Event{
		EventID:   "lookup-1",
		Source:    "test",
		EventType: "test.event",
		Summary:   "Test event",
		Data:      "some data",
		Channel:   "#test",
	}
	if _, _, err = db.InsertEvent(ctx, e); err != nil {
		t.Fatalf("InsertEvent error: %v", err)
	}

	got, err = db.EventByEventID(ctx, "lookup-1")
	if err != nil {
		t.Fatalf("EventByEventID error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil event")
	}
	if got.Source != "test" {
		t.Errorf("expected source 'test', got %q", got.Source)
	}
	if got.Summary != "Test event" {
		t.Errorf("expected summary 'Test event', got %q", got.Summary)
	}
	if got.Data != "some data" {
		t.Errorf("expected data 'some data', got %q", got.Data)
	}
	if got.Channel != "#test" {
		t.Errorf("expected channel '#test', got %q", got.Channel)
	}
}

func TestMarkEventProcessed(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	e := &Event{
		EventID:   "proc-1",
		Source:    "test",
		EventType: "test.event",
		Summary:   "Test",
		Channel:   "#murmur",
	}
	id, _, err := db.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("InsertEvent error: %v", err)
	}

	// Before marking, processed_at should be nil.
	got, err := db.EventByEventID(ctx, "proc-1")
	if err != nil {
		t.Fatalf("EventByEventID error: %v", err)
	}
	if got.ProcessedAt != nil {
		t.Error("expected nil ProcessedAt before marking")
	}

	if err := db.MarkEventProcessed(ctx, id); err != nil {
		t.Fatalf("MarkEventProcessed error: %v", err)
	}

	got, err = db.EventByEventID(ctx, "proc-1")
	if err != nil {
		t.Fatalf("EventByEventID error: %v", err)
	}
	if got.ProcessedAt == nil {
		t.Error("expected non-nil ProcessedAt after marking")
	}
}

func TestListEvents(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert several events.
	sources := []string{"src-a", "src-b", "src-a", "src-b", "src-a"}
	for i, src := range sources {
		e := &Event{
			Source:    src,
			EventType: "test.event",
			Summary:   fmt.Sprintf("Event %d", i),
			Channel:   "#murmur",
		}
		if _, _, err := db.InsertEvent(ctx, e); err != nil {
			t.Fatalf("InsertEvent %d error: %v", i, err)
		}
	}

	tests := []struct {
		name      string
		query     EventsQuery
		wantLen   int
		wantFirst string
	}{
		{
			name:      "all events",
			query:     EventsQuery{},
			wantLen:   5,
			wantFirst: "Event 0",
		},
		{
			name:      "filter by source",
			query:     EventsQuery{Source: "src-a"},
			wantLen:   3,
			wantFirst: "Event 0",
		},
		{
			name:      "cursor pagination",
			query:     EventsQuery{AfterID: 2},
			wantLen:   3,
			wantFirst: "Event 2",
		},
		{
			name:    "limit",
			query:   EventsQuery{Limit: 2},
			wantLen: 2,
		},
		{
			name:    "combined filter and cursor",
			query:   EventsQuery{Source: "src-b", AfterID: 2},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := db.ListEvents(ctx, tt.query)
			if err != nil {
				t.Fatalf("ListEvents error: %v", err)
			}
			if len(events) != tt.wantLen {
				t.Errorf("expected %d events, got %d", tt.wantLen, len(events))
			}
			if tt.wantFirst != "" && len(events) > 0 && events[0].Summary != tt.wantFirst {
				t.Errorf("expected first summary %q, got %q", tt.wantFirst, events[0].Summary)
			}
		})
	}
}

func TestListEvents_LimitCap(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Request limit > 200 should be capped.
	events, err := db.ListEvents(ctx, EventsQuery{Limit: 999})
	if err != nil {
		t.Fatalf("ListEvents error: %v", err)
	}
	// No events inserted, just verify it doesn't error.
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestCleanupEvents(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert an event with an old timestamp.
	_, err := db.ExecContext(ctx,
		`INSERT INTO events (source, event_type, summary, channel, timestamp)
		 VALUES (?, ?, ?, ?, ?)`,
		"old-source", "old.event", "Old event", "#murmur",
		time.Now().Add(-60*24*time.Hour).UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert old event error: %v", err)
	}

	// Insert a recent event.
	e := &Event{
		Source:    "new-source",
		EventType: "new.event",
		Summary:   "New event",
		Channel:   "#murmur",
	}
	if _, _, err := db.InsertEvent(ctx, e); err != nil {
		t.Fatalf("InsertEvent error: %v", err)
	}

	// Cleanup events older than 30 days.
	deleted, err := db.CleanupEvents(ctx, 30)
	if err != nil {
		t.Fatalf("CleanupEvents error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Verify only the new event remains.
	events, err := db.ListEvents(ctx, EventsQuery{})
	if err != nil {
		t.Fatalf("ListEvents error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 remaining event, got %d", len(events))
	}
	if len(events) > 0 && events[0].Source != "new-source" {
		t.Errorf("expected remaining event source 'new-source', got %q", events[0].Source)
	}
}

func TestInsertEvent_DefaultChannel(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert via raw SQL without channel to test DB DEFAULT.
	_, err := db.ExecContext(ctx,
		`INSERT INTO events (source, event_type, summary) VALUES (?, ?, ?)`,
		"raw-test", "test.event", "Test",
	)
	if err != nil {
		t.Fatalf("insert error: %v", err)
	}

	var channel string
	err = db.QueryRowContext(ctx,
		"SELECT channel FROM events WHERE source = ?", "raw-test",
	).Scan(&channel)
	if err != nil {
		t.Fatalf("select error: %v", err)
	}
	if channel != "#murmur" {
		t.Errorf("expected default channel '#murmur', got %q", channel)
	}
}

func TestInsertEvent_EmptyChannelFallback(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// InsertEvent with empty Channel should use the default '#murmur'.
	e := &Event{
		Source:    "test",
		EventType: "test.event",
		Summary:   "Test empty channel",
		Channel:   "",
	}
	id, inserted, err := db.InsertEvent(ctx, e)
	if err != nil {
		t.Fatalf("InsertEvent error: %v", err)
	}
	if !inserted {
		t.Error("expected inserted=true")
	}

	var channel string
	err = db.QueryRowContext(ctx,
		"SELECT channel FROM events WHERE id = ?", id,
	).Scan(&channel)
	if err != nil {
		t.Fatalf("select error: %v", err)
	}
	if channel != "#murmur" {
		t.Errorf("expected channel '#murmur', got %q", channel)
	}
}

func TestMarkEventProcessed_NotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	err := db.MarkEventProcessed(ctx, 99999)
	if err == nil {
		t.Fatal("expected error for nonexistent event ID")
	}
	if !errors.Is(err, ErrEventNotFound) {
		t.Errorf("expected ErrEventNotFound, got: %v", err)
	}
}

func TestCleanupEvents_InvalidRetention(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name string
		days int
	}{
		{"zero", 0},
		{"negative", -5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.CleanupEvents(ctx, tt.days)
			if err == nil {
				t.Error("expected error for invalid retentionDays")
			}
		})
	}
}
