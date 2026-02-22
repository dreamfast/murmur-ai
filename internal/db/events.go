package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrEventNotFound is returned when an event ID does not exist.
var ErrEventNotFound = errors.New("event not found")

// Event represents a row in the events table.
type Event struct {
	ID          int64      `json:"id"`
	EventID     string     `json:"event_id,omitempty"`
	Source      string     `json:"source"`
	EventType   string     `json:"event_type"`
	Summary     string     `json:"summary"`
	Data        string     `json:"data,omitempty"`
	Channel     string     `json:"channel"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
	Timestamp   time.Time  `json:"timestamp"`
}

// InsertEvent stores a new event in the database. If the event has an EventID
// that already exists (unique index conflict), it returns the existing event's
// ID and false (not inserted). On successful insert it returns the new ID and
// true. Uses INSERT OR IGNORE to handle concurrent inserts atomically.
func (db *DB) InsertEvent(ctx context.Context, e *Event) (int64, bool, error) {
	var eventID *string
	if e.EventID != "" {
		eventID = &e.EventID
	}

	var data *string
	if e.Data != "" {
		data = &e.Data
	}

	// Use the DB default channel when caller provides empty string.
	channel := e.Channel
	if channel == "" {
		channel = "#murmur"
	}

	// INSERT OR IGNORE is atomic: if event_id conflicts with the unique index,
	// the row is silently skipped (RowsAffected == 0) instead of returning an error.
	// This avoids the check-then-insert race condition.
	result, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO events (event_id, source, event_type, summary, data, channel)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		eventID, e.Source, e.EventType, e.Summary, data, channel,
	)
	if err != nil {
		return 0, false, fmt.Errorf("InsertEvent: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("InsertEvent: rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// Conflict on event_id — fetch the existing row.
		existing, err := db.EventByEventID(ctx, e.EventID)
		if err != nil {
			return 0, false, fmt.Errorf("InsertEvent: fetch existing: %w", err)
		}
		if existing == nil {
			// Should not happen: INSERT OR IGNORE only skips on unique constraint violation.
			return 0, false, fmt.Errorf("InsertEvent: conflict detected but existing event not found")
		}
		return existing.ID, false, nil
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("InsertEvent: last insert id: %w", err)
	}

	return id, true, nil
}

// MarkEventProcessed sets the processed_at timestamp for an event.
// Returns ErrEventNotFound if no event with the given ID exists.
func (db *DB) MarkEventProcessed(ctx context.Context, id int64) error {
	result, err := db.ExecContext(ctx,
		`UPDATE events SET processed_at = CURRENT_TIMESTAMP WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("MarkEventProcessed: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("MarkEventProcessed: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("MarkEventProcessed: %w", ErrEventNotFound)
	}

	return nil
}

// EventByEventID looks up an event by its idempotency key.
// Returns (nil, nil) if no event with that event_id exists.
func (db *DB) EventByEventID(ctx context.Context, eventID string) (*Event, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, event_id, source, event_type, summary, data, channel, processed_at, timestamp
		 FROM events WHERE event_id = ?`, eventID,
	)
	return scanEvent(row)
}

// EventsQuery specifies filters for listing events.
// Source filters by event source (empty means all sources).
// AfterID returns only events with id > AfterID for cursor-based pagination.
// Limit caps the number of results (default 50, max 200).
type EventsQuery struct {
	Source  string
	AfterID int64
	Limit   int
}

// ListEvents returns events matching the query, ordered by id ascending.
func (db *DB) ListEvents(ctx context.Context, q EventsQuery) ([]Event, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}

	query := `SELECT id, event_id, source, event_type, summary, data, channel, processed_at, timestamp
	          FROM events WHERE id > ?`
	args := []any{q.AfterID}

	if q.Source != "" {
		query += ` AND source = ?`
		args = append(args, q.Source)
	}

	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, q.Limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListEvents: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var eventID, data sql.NullString
		var processedAt sql.NullTime

		if err := rows.Scan(&e.ID, &eventID, &e.Source, &e.EventType, &e.Summary,
			&data, &e.Channel, &processedAt, &e.Timestamp); err != nil {
			return nil, fmt.Errorf("ListEvents: scan: %w", err)
		}

		e.EventID = eventID.String
		e.Data = data.String
		if processedAt.Valid {
			e.ProcessedAt = &processedAt.Time
		}
		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListEvents: rows: %w", err)
	}

	return events, nil
}

// CleanupEvents deletes events older than the given number of days.
// retentionDays must be positive. Returns the number of deleted rows.
func (db *DB) CleanupEvents(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, fmt.Errorf("CleanupEvents: retentionDays must be positive, got %d", retentionDays)
	}

	result, err := db.ExecContext(ctx,
		`DELETE FROM events WHERE timestamp < datetime('now', ?)`,
		fmt.Sprintf("-%d days", retentionDays),
	)
	if err != nil {
		return 0, fmt.Errorf("CleanupEvents: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("CleanupEvents: rows affected: %w", err)
	}

	return n, nil
}

// scanEvent scans a single event row. Returns (nil, nil) if no row found.
func scanEvent(row *sql.Row) (*Event, error) {
	var e Event
	var eventID, data sql.NullString
	var processedAt sql.NullTime

	err := row.Scan(&e.ID, &eventID, &e.Source, &e.EventType, &e.Summary,
		&data, &e.Channel, &processedAt, &e.Timestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanEvent: %w", err)
	}

	e.EventID = eventID.String
	e.Data = data.String
	if processedAt.Valid {
		e.ProcessedAt = &processedAt.Time
	}

	return &e, nil
}
