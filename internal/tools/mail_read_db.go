package tools

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// mailDB wraps a read-only connection to Thunderbird's global-messages-db.sqlite.
// All queries use immutable mode to avoid locking a running Thunderbird instance.
type mailDB struct {
	db *sql.DB
}

// globalMessagesDB is the filename of Thunderbird's global search index.
const globalMessagesDB = "global-messages-db.sqlite"

// openMailDB opens the Thunderbird global messages database in read-only
// immutable mode. Returns an error if the database cannot be opened or
// does not contain the expected tables.
func openMailDB(profilePath string) (*mailDB, error) {
	dbPath := filepath.Join(profilePath, globalMessagesDB)
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("openMailDB: %w", err)
	}

	// Verify the database has the expected tables.
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='messages'").Scan(&name)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("openMailDB: missing messages table: %w", err)
	}

	return &mailDB{db: db}, nil
}

// Close closes the database connection.
func (m *mailDB) Close() error {
	return m.db.Close()
}

// folderInfo represents a mail folder with its message count.
type folderInfo struct {
	Name      string
	FolderURI string
	Count     int
}

// folders returns all folders with their non-deleted message counts.
func (m *mailDB) folders(accountPattern string) ([]folderInfo, error) {
	query := `
		SELECT fl.name, fl.folderURI, COUNT(msg.id) as cnt
		FROM folderLocations fl
		JOIN messages msg ON msg.folderID = fl.id
		WHERE msg.deleted = 0`

	var args []any
	if accountPattern != "" {
		query += " AND fl.folderURI LIKE ?"
		args = append(args, accountPattern)
	}

	query += " GROUP BY fl.id ORDER BY cnt DESC"

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("mailDB.folders: %w", err)
	}
	defer rows.Close()

	var result []folderInfo
	for rows.Next() {
		var fi folderInfo
		if err := rows.Scan(&fi.Name, &fi.FolderURI, &fi.Count); err != nil {
			return nil, fmt.Errorf("mailDB.folders: scan: %w", err)
		}
		result = append(result, fi)
	}
	return result, rows.Err()
}

// unread returns unread messages, optionally filtered by account and folder name.
// Messages are returned most-recent-first, limited to the given count.
func (m *mailDB) unread(accountPattern string, folderName string, limit int) ([]mailMessage, error) {
	query := `
		SELECT m.headerMessageID, mt.c3author, mt.c1subject,
		       m.date, fl.name
		FROM messages m
		JOIN messagesText_content mt ON mt.docid = m.id
		JOIN folderLocations fl ON m.folderID = fl.id
		WHERE m.deleted = 0
		  AND json_extract(m.jsonAttributes, '$.59') = 0`

	var args []any
	if accountPattern != "" {
		query += " AND fl.folderURI LIKE ?"
		args = append(args, accountPattern)
	}
	if folderName != "" {
		query += " AND fl.name = ?"
		args = append(args, folderName)
	}

	query += " ORDER BY m.date DESC LIMIT ?"
	args = append(args, limit)

	return m.queryMessages(query, args)
}

// search returns messages matching the query string in subject, author, or body.
// Results are optionally filtered by account and folder, returned most-recent-first.
func (m *mailDB) search(searchQuery string, accountPattern string, folderName string, limit int) ([]mailMessage, error) {
	pattern := "%" + searchQuery + "%"

	query := `
		SELECT m.headerMessageID, mt.c3author, mt.c1subject,
		       m.date, fl.name
		FROM messages m
		JOIN messagesText_content mt ON mt.docid = m.id
		JOIN folderLocations fl ON m.folderID = fl.id
		WHERE m.deleted = 0
		  AND (mt.c1subject LIKE ? COLLATE NOCASE
		    OR mt.c3author LIKE ? COLLATE NOCASE
		    OR mt.c0body LIKE ? COLLATE NOCASE)`

	args := []any{pattern, pattern, pattern}
	if accountPattern != "" {
		query += " AND fl.folderURI LIKE ?"
		args = append(args, accountPattern)
	}
	if folderName != "" {
		query += " AND fl.name = ?"
		args = append(args, folderName)
	}

	query += " ORDER BY m.date DESC LIMIT ?"
	args = append(args, limit)

	return m.queryMessages(query, args)
}

// readByID returns the full message (including body) for the given
// headerMessageID. Returns nil if not found.
func (m *mailDB) readByID(headerMessageID string) (*mailMessage, error) {
	query := `
		SELECT m.headerMessageID, mt.c3author, mt.c1subject,
		       m.date, fl.name, mt.c0body
		FROM messages m
		JOIN messagesText_content mt ON mt.docid = m.id
		JOIN folderLocations fl ON m.folderID = fl.id
		WHERE m.deleted = 0
		  AND m.headerMessageID = ?
		LIMIT 1`

	var msg mailMessage
	var dateUsec int64
	var folder string
	var body sql.NullString

	err := m.db.QueryRow(query, headerMessageID).Scan(
		&msg.MessageID, &msg.From, &msg.Subject,
		&dateUsec, &folder, &body,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mailDB.readByID: %w", err)
	}

	msg.Date = formatDBDate(dateUsec)
	if body.Valid {
		msg.Body = body.String
	}

	return &msg, nil
}

// queryMessages executes a query that returns the standard message columns
// (headerMessageID, c3author, c1subject, date, folder name) and converts
// them into mailMessage structs.
func (m *mailDB) queryMessages(query string, args []any) ([]mailMessage, error) {
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("mailDB.queryMessages: %w", err)
	}
	defer rows.Close()

	var result []mailMessage
	for rows.Next() {
		var msg mailMessage
		var dateUsec int64
		var folder string

		if err := rows.Scan(&msg.MessageID, &msg.From, &msg.Subject, &dateUsec, &folder); err != nil {
			return nil, fmt.Errorf("mailDB.queryMessages: scan: %w", err)
		}

		msg.Date = formatDBDate(dateUsec)
		result = append(result, msg)
	}
	return result, rows.Err()
}

// formatDBDate converts a Thunderbird date (microseconds since Unix epoch)
// to a human-readable string.
func formatDBDate(usec int64) string {
	t := time.Unix(usec/1_000_000, (usec%1_000_000)*1000)
	return t.UTC().Format("Mon, 2 Jan 2006 15:04:05 -0700")
}

// folderURIPattern builds a SQL LIKE pattern to match folder URIs belonging
// to a specific account. Thunderbird folder URIs look like:
//
//	mailbox://nathan%40riverlab.com@pop.gmail.com/Inbox
//	mailbox://nobody@Local%20Folders/Sent
//
// The account email is URL-encoded in the URI. This function finds the
// matching account and builds a pattern like "mailbox://nathan%40riverlab.com@%".
func folderURIPattern(accounts []thunderbirdAccount, selector string) string {
	if selector == "" || len(accounts) == 0 {
		return ""
	}

	// Find the matching account.
	var matchedEmail string
	selectorLower := strings.ToLower(selector)

	// Exact match first.
	for _, a := range accounts {
		if strings.ToLower(a.Name) == selectorLower || strings.ToLower(a.Email) == selectorLower {
			matchedEmail = a.Email
			break
		}
	}

	// Partial match if no exact match.
	if matchedEmail == "" {
		for _, a := range accounts {
			if strings.Contains(strings.ToLower(a.Name), selectorLower) || strings.Contains(strings.ToLower(a.Email), selectorLower) {
				matchedEmail = a.Email
				break
			}
		}
	}

	if matchedEmail == "" {
		return ""
	}

	// Thunderbird encodes the @ in the email as %40 in folder URIs.
	encoded := strings.ReplaceAll(matchedEmail, "@", "%40")
	return "mailbox://" + encoded + "@%"
}
