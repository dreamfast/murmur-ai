package tools

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// mailDB provides read-only access to Thunderbird's global-messages-db.sqlite.
// Each query opens a fresh immutable connection so that it always sees the
// latest data that Thunderbird has flushed to the main database file, without
// holding a lock that would conflict with a running Thunderbird/Betterbird.
type mailDB struct {
	dsn string
}

// globalMessagesDB is the filename of Thunderbird's global search index.
const globalMessagesDB = "global-messages-db.sqlite"

// openMailDB verifies that the Thunderbird global messages database exists and
// contains the expected tables. It does not keep a persistent connection;
// individual queries open short-lived immutable connections for freshness.
func openMailDB(profilePath string) (*mailDB, error) {
	dbPath := filepath.Join(profilePath, globalMessagesDB)
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("openMailDB: %w", err)
	}
	defer db.Close()

	// Verify the database has the expected tables.
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='messages'").Scan(&name)
	if err != nil {
		return nil, fmt.Errorf("openMailDB: missing messages table: %w", err)
	}

	return &mailDB{dsn: dsn}, nil
}

// open returns a short-lived database connection. Each call opens a fresh
// immutable snapshot of the database file, ensuring recent data is visible.
// Callers must close the returned *sql.DB when done.
func (m *mailDB) open() (*sql.DB, error) {
	db, err := sql.Open("sqlite", m.dsn)
	if err != nil {
		return nil, fmt.Errorf("mailDB.open: %w", err)
	}
	// Immutable mode means one connection is sufficient and we don't want
	// the pool keeping stale connections around.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}

// Close is a no-op since connections are opened per-query.
func (m *mailDB) Close() error {
	return nil
}

// folderInfo represents a mail folder with its message count.
type folderInfo struct {
	Name      string
	FolderURI string
	Count     int
}

// folders returns all folders with their non-deleted message counts.
func (m *mailDB) folders(accountPattern string) ([]folderInfo, error) {
	db, err := m.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

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

	rows, err := db.Query(query, args...)
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
		  AND (m.jsonAttributes IS NULL
		    OR json_extract(m.jsonAttributes, '$.61') IS NULL
		    OR json_extract(m.jsonAttributes, '$.61') = 0)`

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
	db, err := m.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

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

	err = db.QueryRow(query, headerMessageID).Scan(
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
	db, err := m.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(query, args...)
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
