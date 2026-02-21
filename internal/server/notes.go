package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"murmur/internal/db"
	"murmur/internal/tools"
)

// ErrNoteNotFound is returned when a note key does not exist.
var ErrNoteNotFound = errors.New("note not found")

// maxNoteResults is the maximum number of notes returned by List and Search.
const maxNoteResults = 50

// maxNotePreviewLen is the maximum length of a note value preview in search results.
const maxNotePreviewLen = 100

// NoteEntry represents a single note with its key, value, and timestamps.
type NoteEntry struct {
	Key     string
	Value   string
	Created string
	Updated string
}

// NotesStore provides CRUD operations on the notes table in the database.
type NotesStore struct {
	db     *db.DB
	logger *slog.Logger
}

// NewNotesStore creates a new NotesStore backed by the given database.
func NewNotesStore(database *db.DB, logger *slog.Logger) *NotesStore {
	return &NotesStore{
		db:     database,
		logger: logger,
	}
}

// Set stores a note. If the key already exists, its value and updated
// timestamp are replaced. Returns an error if the key is empty.
func (n *NotesStore) Set(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("notes.Set: key must not be empty")
	}
	_, err := n.db.Exec(
		`INSERT INTO notes (key, value, created, updated)
		 VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET
		   value = excluded.value,
		   updated = CURRENT_TIMESTAMP`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("notes.Set: %w", err)
	}
	return nil
}

// Get retrieves a note by key. Returns ErrNoteNotFound if the key does not
// exist.
func (n *NotesStore) Get(key string) (string, error) {
	var value string
	err := n.db.QueryRow(`SELECT value FROM notes WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoteNotFound
	}
	if err != nil {
		return "", fmt.Errorf("notes.Get: %w", err)
	}
	return value, nil
}

// List returns up to maxNoteResults notes ordered by key.
func (n *NotesStore) List() ([]NoteEntry, error) {
	rows, err := n.db.Query(`SELECT key, value, created, updated FROM notes ORDER BY key ASC LIMIT ?`, maxNoteResults)
	if err != nil {
		return nil, fmt.Errorf("notes.List: %w", err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var e NoteEntry
		if err := rows.Scan(&e.Key, &e.Value, &e.Created, &e.Updated); err != nil {
			return nil, fmt.Errorf("notes.List: scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes.List: %w", err)
	}
	return entries, nil
}

// Delete removes a note by key. It is not an error if the key does not exist.
func (n *NotesStore) Delete(key string) error {
	_, err := n.db.Exec(`DELETE FROM notes WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("notes.Delete: %w", err)
	}
	return nil
}

// Search finds notes where the key or value contains the query string
// (case-insensitive for ASCII via SQLite LIKE). Results are ordered by key,
// limited to maxNoteResults.
func (n *NotesStore) Search(query string) ([]NoteEntry, error) {
	pattern := "%" + query + "%"
	rows, err := n.db.Query(
		`SELECT key, value, created, updated FROM notes
		 WHERE key LIKE ? OR value LIKE ?
		 ORDER BY key ASC
		 LIMIT ?`,
		pattern, pattern, maxNoteResults,
	)
	if err != nil {
		return nil, fmt.Errorf("notes.Search: %w", err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var e NoteEntry
		if err := rows.Scan(&e.Key, &e.Value, &e.Created, &e.Updated); err != nil {
			return nil, fmt.Errorf("notes.Search: scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes.Search: %w", err)
	}
	return entries, nil
}

// RegisterNoteTools registers the 5 note tools on the given ToolRegistry.
func RegisterNoteTools(registry *ToolRegistry, store *NotesStore) error {
	noteTools := []tools.Tool{
		{
			Name:        "note_set",
			Description: "Store a note with a key and value. Overwrites existing notes with the same key.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {"type": "string", "description": "Note key (unique identifier)"},
					"value": {"type": "string", "description": "Note value (text content)"}
				},
				"required": ["key", "value"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				key, err := tools.RequireStringArg(args, "key")
				if err != nil {
					return "", err
				}
				value, err := tools.RequireStringArg(args, "value")
				if err != nil {
					return "", err
				}
				if err := store.Set(key, value); err != nil {
					return "", err
				}
				return fmt.Sprintf("Note %q saved.", key), nil
			},
		},
		{
			Name:        "note_get",
			Description: "Retrieve a note by its key.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {"type": "string", "description": "Note key to retrieve"}
				},
				"required": ["key"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				key, err := tools.RequireStringArg(args, "key")
				if err != nil {
					return "", err
				}
				value, err := store.Get(key)
				if errors.Is(err, ErrNoteNotFound) {
					return fmt.Sprintf("No note found with key %q.", key), nil
				}
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%s: %s", key, value), nil
			},
		},
		{
			Name:        "note_list",
			Description: "List all stored note keys with their last updated timestamps.",
			Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				entries, err := store.List()
				if err != nil {
					return "", err
				}
				if len(entries) == 0 {
					return "No notes stored.", nil
				}
				var lines []string
				for _, e := range entries {
					lines = append(lines, fmt.Sprintf("  %s (updated: %s)", e.Key, e.Updated))
				}
				return "Notes:\n" + strings.Join(lines, "\n"), nil
			},
		},
		{
			Name:        "note_delete",
			Description: "Delete a note by its key.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"key": {"type": "string", "description": "Note key to delete"}
				},
				"required": ["key"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				key, err := tools.RequireStringArg(args, "key")
				if err != nil {
					return "", err
				}
				if err := store.Delete(key); err != nil {
					return "", err
				}
				return fmt.Sprintf("Note %q deleted.", key), nil
			},
		},
		{
			Name:        "note_search",
			Description: "Search notes by key or value content (case-insensitive substring match).",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Search term to match against note keys and values"}
				},
				"required": ["query"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				query, err := tools.RequireStringArg(args, "query")
				if err != nil {
					return "", err
				}
				entries, err := store.Search(query)
				if err != nil {
					return "", err
				}
				if len(entries) == 0 {
					return fmt.Sprintf("No notes matching %q.", query), nil
				}
				var lines []string
				for _, e := range entries {
					preview := e.Value
					if len(preview) > maxNotePreviewLen {
						preview = preview[:maxNotePreviewLen] + "..."
					}
					lines = append(lines, fmt.Sprintf("  %s: %s", e.Key, preview))
				}
				return fmt.Sprintf("Found %d note(s):\n%s", len(entries), strings.Join(lines, "\n")), nil
			},
		},
	}

	for _, t := range noteTools {
		if err := registry.Register(t); err != nil {
			return fmt.Errorf("RegisterNoteTools: %w", err)
		}
	}
	return nil
}
