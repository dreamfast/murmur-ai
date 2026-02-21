package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// mailMessage represents a parsed email message from an mbox file.
type mailMessage struct {
	From      string
	Subject   string
	Date      string
	MessageID string
	Status    uint16 // X-Mozilla-Status bitmask
	Body      string // plain text only
}

// mozStatusRead is the "Read" bit in X-Mozilla-Status.
const mozStatusRead = 0x0001

// isUnread returns true if the message has not been read.
func isUnread(status uint16) bool {
	return status&mozStatusRead == 0
}

// parseMozillaStatus parses a hex X-Mozilla-Status value (e.g., "0001")
// into a uint16 bitmask.
func parseMozillaStatus(s string) uint16 {
	s = strings.TrimSpace(s)
	v, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0
	}
	return uint16(v)
}

// thunderbirdAccount represents a mail account discovered from Thunderbird's
// prefs.js configuration file.
type thunderbirdAccount struct {
	// Name is the display name (e.g., "Riverlab", "Personal").
	Name string
	// Email is the login username / email address (e.g., "nathan@riverlab.com").
	Email string
	// DirRel is the directory relative to the profile (e.g., "Mail/pop.gmail-1.com").
	DirRel string
	// Type is the account type (e.g., "pop3", "imap").
	Type string
}

// rePref matches Thunderbird prefs.js lines like:
//
//	user_pref("mail.server.server1.userName", "nathan@riverlab.com");
var rePref = regexp.MustCompile(`^user_pref\("mail\.server\.(server\d+)\.(\w[\w-]*)"\s*,\s*"(.*)"\);`)

// parseThunderbirdPrefs reads a Thunderbird prefs.js file and returns all
// discovered mail accounts with their name, email, directory, and type.
// Accounts of type "none" (Local Folders) and "nntp" are excluded.
func parseThunderbirdPrefs(prefsPath string) ([]thunderbirdAccount, error) {
	f, err := os.Open(prefsPath)
	if err != nil {
		return nil, fmt.Errorf("parseThunderbirdPrefs: %w", err)
	}
	defer f.Close()

	// Collect per-server properties.
	type serverProps struct {
		name, userName, dirRel, serverType string
	}
	servers := make(map[string]*serverProps)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := rePref.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		serverID, key, value := m[1], m[2], m[3]

		sp, ok := servers[serverID]
		if !ok {
			sp = &serverProps{}
			servers[serverID] = sp
		}

		switch key {
		case "name":
			sp.name = value
		case "userName":
			sp.userName = value
		case "directory-rel":
			// Convert "[ProfD]Mail/pop.gmail.com" → "Mail/pop.gmail.com"
			sp.dirRel = strings.TrimPrefix(value, "[ProfD]")
		case "type":
			sp.serverType = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parseThunderbirdPrefs: %w", err)
	}

	// Build account list, excluding non-mail types.
	var accounts []thunderbirdAccount
	for _, sp := range servers {
		if sp.serverType == "none" || sp.serverType == "nntp" || sp.serverType == "" {
			continue
		}
		if sp.dirRel == "" {
			continue
		}
		accounts = append(accounts, thunderbirdAccount{
			Name:   sp.name,
			Email:  sp.userName,
			DirRel: sp.dirRel,
			Type:   sp.serverType,
		})
	}

	// Sort by name for stable ordering.
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].Name < accounts[j].Name
	})

	return accounts, nil
}

// formatAccountList formats discovered accounts into a human-readable string.
func formatAccountList(accounts []thunderbirdAccount) string {
	var sb strings.Builder
	for i, a := range accounts {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%d. %s <%s> [%s]", i+1, a.Name, a.Email, a.Type)
	}
	return sb.String()
}

// NewMailReadTool creates the mail_read tool for reading Thunderbird mail.
// It attempts to open Thunderbird's global-messages-db.sqlite for fast indexed
// queries. If the database is unavailable, it falls back to mbox file parsing.
// It also parses prefs.js at creation time to discover all configured mail
// accounts and includes them in the tool description so the LLM knows which
// accounts are available.
func NewMailReadTool(profilePath, fallbackMailDir string) Tool {
	// Discover accounts from prefs.js.
	prefsPath := filepath.Join(profilePath, "prefs.js")
	accounts, _ := parseThunderbirdPrefs(prefsPath) // best-effort

	// Try to open the SQLite global search index.
	var mdb *mailDB
	if db, err := openMailDB(profilePath); err == nil {
		mdb = db
	}

	description := "Read emails from local Thunderbird mail storage. Actions: accounts (list mail accounts), folders (list mail folders), unread (list unread), search (find by query), read (get full message by Message-ID). Use the 'account' parameter to select which mail account to query."
	if len(accounts) > 0 {
		description += " Available accounts: "
		for i, a := range accounts {
			if i > 0 {
				description += ", "
			}
			description += fmt.Sprintf("%s <%s>", a.Name, a.Email)
		}
	}

	return Tool{
		Name:        "mail_read",
		Description: description,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["accounts", "folders", "unread", "search", "read"],
					"description": "accounts: list all mail accounts. folders: list mail folders with message counts. unread: list unread messages. search: find messages matching query. read: get full message by Message-ID."
				},
				"account": {
					"type": "string",
					"description": "Account name or email to query (e.g., 'Riverlab' or 'nathan@riverlab.com'). If omitted, searches all accounts."
				},
				"query": {
					"type": "string",
					"description": "Search term for sender, subject, or content (used with search action)"
				},
				"message_id": {
					"type": "string",
					"description": "Message-ID to read (used with read action)"
				},
				"folder": {
					"type": "string",
					"description": "Mail folder name to filter by (e.g., 'Inbox', 'Sent')"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of results (default: 10)"
				}
			},
			"required": ["action"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleMailRead(ctx, args, profilePath, fallbackMailDir, accounts, mdb)
		},
	}
}

// resolveMailDir finds the mail directory for the given account selector.
// The selector can be an account name or email address (case-insensitive).
// If empty, returns the first account's directory or the fallback.
func resolveMailDir(selector string, accounts []thunderbirdAccount, fallbackMailDir string) (string, error) {
	if len(accounts) == 0 {
		if fallbackMailDir != "" {
			return fallbackMailDir, nil
		}
		return "", fmt.Errorf("no mail accounts discovered and no fallback configured")
	}

	if selector == "" {
		return accounts[0].DirRel, nil
	}

	selectorLower := strings.ToLower(selector)
	for _, a := range accounts {
		if strings.ToLower(a.Name) == selectorLower || strings.ToLower(a.Email) == selectorLower {
			return a.DirRel, nil
		}
	}

	// Partial match on name or email.
	for _, a := range accounts {
		if strings.Contains(strings.ToLower(a.Name), selectorLower) || strings.Contains(strings.ToLower(a.Email), selectorLower) {
			return a.DirRel, nil
		}
	}

	return "", fmt.Errorf("no account matching %q found", selector)
}

// handleMailRead dispatches to the appropriate mail read action.
// When mdb is non-nil, SQLite indexed queries are used for speed.
// Otherwise, falls back to mbox file parsing.
func handleMailRead(_ context.Context, args map[string]any, profilePath, fallbackMailDir string, accounts []thunderbirdAccount, mdb *mailDB) (string, error) {
	action, err := RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	// Handle accounts action first — no mail_dir or DB needed.
	if action == "accounts" {
		if len(accounts) == 0 {
			return "No mail accounts discovered. Check that the Thunderbird profile path is correct.", nil
		}
		return formatAccountList(accounts), nil
	}

	accountSelector := OptionalStringArg(args, "account", "")
	folder := OptionalStringArg(args, "folder", "")
	limit := optionalIntArg(args, "limit", 10)
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	// SQLite path — fast indexed queries.
	if mdb != nil {
		return handleMailReadDB(mdb, action, accountSelector, folder, limit, args, accounts)
	}

	// Mbox fallback path.
	return handleMailReadMbox(action, accountSelector, folder, limit, args, profilePath, fallbackMailDir, accounts)
}

// handleMailReadDB handles mail_read actions using the SQLite global search index.
func handleMailReadDB(mdb *mailDB, action, accountSelector, folder string, limit int, args map[string]any, accounts []thunderbirdAccount) (string, error) {
	accountPattern := folderURIPattern(accounts, accountSelector)

	switch action {
	case "folders":
		folders, err := mdb.folders(accountPattern)
		if err != nil {
			return "", err
		}
		if len(folders) == 0 {
			return "No folders found.", nil
		}
		return formatFolderList(folders), nil

	case "unread":
		msgs, err := mdb.unread(accountPattern, folder, limit)
		if err != nil {
			return "", err
		}
		if len(msgs) == 0 {
			return "No unread messages.", nil
		}
		return formatMessageList(msgs), nil

	case "search":
		query := OptionalStringArg(args, "query", "")
		if query == "" {
			return "", fmt.Errorf("query is required for search action")
		}
		msgs, err := mdb.search(query, accountPattern, folder, limit)
		if err != nil {
			return "", err
		}
		if len(msgs) == 0 {
			return fmt.Sprintf("No messages matching %q.", query), nil
		}
		return formatMessageList(msgs), nil

	case "read":
		messageID := OptionalStringArg(args, "message_id", "")
		if messageID == "" {
			return "", fmt.Errorf("message_id is required for read action")
		}
		msg, err := mdb.readByID(messageID)
		if err != nil {
			return "", err
		}
		if msg == nil {
			return fmt.Sprintf("No message found with Message-ID %q.", messageID), nil
		}
		return formatFullMessage(*msg), nil

	default:
		return "", fmt.Errorf("unknown action %q, expected: accounts, folders, unread, search, read", action)
	}
}

// handleMailReadMbox handles mail_read actions using mbox file parsing (fallback).
func handleMailReadMbox(action, accountSelector, folder string, limit int, args map[string]any, profilePath, fallbackMailDir string, accounts []thunderbirdAccount) (string, error) {
	if action == "folders" {
		return "", fmt.Errorf("folders action requires the Thunderbird search index (global-messages-db.sqlite)")
	}

	mailDir, err := resolveMailDir(accountSelector, accounts, fallbackMailDir)
	if err != nil {
		return "", err
	}

	if folder == "" {
		folder = "Inbox"
	}

	// Security: prevent path traversal and validate resolved path.
	mboxPath, err := validateFolderPath(folder, profilePath, mailDir)
	if err != nil {
		return "", err
	}

	switch action {
	case "unread":
		return mailUnread(mboxPath, limit)
	case "search":
		query := OptionalStringArg(args, "query", "")
		if query == "" {
			return "", fmt.Errorf("query is required for search action")
		}
		return mailSearch(mboxPath, query, limit)
	case "read":
		messageID := OptionalStringArg(args, "message_id", "")
		if messageID == "" {
			return "", fmt.Errorf("message_id is required for read action")
		}
		return mailReadByID(mboxPath, messageID)
	default:
		return "", fmt.Errorf("unknown action %q, expected: accounts, unread, search, read", action)
	}
}

// formatFolderList formats a list of folders with message counts.
func formatFolderList(folders []folderInfo) string {
	var b strings.Builder
	for i, f := range folders {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s (%d messages)", i+1, f.Name, f.Count)
	}
	return b.String()
}

// validateFolderPath rejects folder names that could cause path traversal and
// verifies the resolved path stays within the profile directory. Symlinks are
// resolved to prevent escaping via symlinked directories.
func validateFolderPath(folder, profilePath, mailDir string) (string, error) {
	if strings.Contains(folder, "..") {
		return "", fmt.Errorf("invalid folder name: must not contain '..'")
	}
	if filepath.IsAbs(folder) {
		return "", fmt.Errorf("invalid folder name: must not be an absolute path")
	}

	resolved := filepath.Clean(filepath.Join(profilePath, mailDir, folder))

	// Resolve the canonical base path (follow symlinks in profilePath).
	canonBase, err := filepath.EvalSymlinks(profilePath)
	if err != nil {
		// If the profile path doesn't exist, fall back to lexical check.
		canonBase = filepath.Clean(profilePath)
	}

	// Resolve the canonical target path if it exists (follow symlinks).
	canonResolved, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// File may not exist yet — fall back to lexical check on the
		// non-symlink-resolved path.
		canonResolved = resolved
	}

	if !strings.HasPrefix(canonResolved, canonBase+string(filepath.Separator)) && canonResolved != canonBase {
		return "", fmt.Errorf("invalid folder path: resolved path escapes profile directory")
	}

	return resolved, nil
}

// mailUnread returns unread messages from the mbox file.
func mailUnread(mboxPath string, limit int) (string, error) {
	msgs, err := parseMboxFile(mboxPath, limit, func(m mailMessage) bool {
		return isUnread(m.Status)
	})
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "No unread messages.", nil
	}
	return formatMessageList(msgs), nil
}

// mailSearch returns messages matching the query in From, Subject, or Body.
func mailSearch(mboxPath string, query string, limit int) (string, error) {
	queryLower := strings.ToLower(query)
	msgs, err := parseMboxFile(mboxPath, limit, func(m mailMessage) bool {
		return strings.Contains(strings.ToLower(m.From), queryLower) ||
			strings.Contains(strings.ToLower(m.Subject), queryLower) ||
			strings.Contains(strings.ToLower(m.Body), queryLower)
	})
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return fmt.Sprintf("No messages matching %q.", query), nil
	}
	return formatMessageList(msgs), nil
}

// mailReadByID returns the full message with the given Message-ID.
func mailReadByID(mboxPath string, messageID string) (string, error) {
	msgs, err := parseMboxFile(mboxPath, 1, func(m mailMessage) bool {
		return m.MessageID == messageID
	})
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return fmt.Sprintf("No message found with Message-ID %q.", messageID), nil
	}
	return formatFullMessage(msgs[0]), nil
}

// parseMboxFile reads an mbox file and returns messages matching the filter,
// up to the given limit. Messages are returned in reverse order (most recent
// first) since mbox files append new messages at the end. A sliding window of
// size limit is used so that only the most recent matching messages are kept
// in memory regardless of mailbox size.
func parseMboxFile(path string, limit int, filter func(mailMessage) bool) ([]mailMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("folder not found: %s", filepath.Base(path))
		}
		return nil, fmt.Errorf("open mbox: %w", err)
	}
	defer f.Close()

	msgs, err := parseMbox(f, limit, filter)
	if err != nil {
		return nil, err
	}

	// Reverse to get most recent first (mbox appends newest at end).
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return msgs, nil
}

// parseMbox reads an mbox stream and returns the last limit messages matching
// the filter. A ring buffer is used so that at most limit matching messages
// are kept in memory at any time. Messages in mbox format are separated by
// lines starting with "From ". If limit <= 0, all matching messages are
// returned.
func parseMbox(r io.Reader, limit int, filter func(mailMessage) bool) ([]mailMessage, error) {
	scanner := bufio.NewScanner(r)
	// Increase buffer size for large messages.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Ring buffer for bounded memory usage.
	var ring []mailMessage
	var ringIdx int
	ringFull := false
	if limit > 0 {
		ring = make([]mailMessage, limit)
	}

	var unbounded []mailMessage // used when limit <= 0
	var currentLines []string
	inMessage := false

	addMatch := func(msg mailMessage) {
		if limit <= 0 {
			unbounded = append(unbounded, msg)
			return
		}
		ring[ringIdx] = msg
		ringIdx++
		if ringIdx >= limit {
			ringIdx = 0
			ringFull = true
		}
	}

	for scanner.Scan() {
		// Normalize CRLF line endings to LF for consistent parsing.
		line := strings.TrimSuffix(scanner.Text(), "\r")

		if strings.HasPrefix(line, "From ") {
			// Start of a new message — process the previous one.
			if inMessage && len(currentLines) > 0 {
				msg := parseMessage(currentLines)
				if filter(msg) {
					addMatch(msg)
				}
			}
			currentLines = nil
			inMessage = true
			continue
		}

		if inMessage {
			currentLines = append(currentLines, line)
		}
	}

	// Process the last message.
	if inMessage && len(currentLines) > 0 {
		msg := parseMessage(currentLines)
		if filter(msg) {
			addMatch(msg)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parseMbox: %w", err)
	}

	if limit <= 0 {
		return unbounded, nil
	}

	// Extract ring buffer contents in chronological order.
	var result []mailMessage
	if ringFull {
		result = append(result, ring[ringIdx:]...)
		result = append(result, ring[:ringIdx]...)
	} else {
		result = ring[:ringIdx]
	}

	return result, nil
}

// parseMessage parses a single message from its lines (headers + body).
func parseMessage(lines []string) mailMessage {
	var msg mailMessage

	// Find the blank line separating headers from body.
	headerEnd := len(lines)
	for i, line := range lines {
		if line == "" {
			headerEnd = i
			break
		}
	}

	// Parse headers.
	headers := parseHeaders(lines[:headerEnd])
	msg.From = headers["from"]
	msg.Subject = headers["subject"]
	msg.Date = headers["date"]
	msg.MessageID = headers["message-id"]
	msg.Status = parseMozillaStatus(headers["x-mozilla-status"])

	// Extract MIME boundary from Content-Type header if present.
	boundary := extractBoundary(headers["content-type"])

	// Extract plain text body.
	if headerEnd < len(lines)-1 {
		bodyLines := lines[headerEnd+1:]
		msg.Body = extractPlainBody(bodyLines, boundary)
	}

	return msg
}

// parseHeaders parses RFC 2822 headers from lines, handling folded headers
// (continuation lines starting with whitespace). Returns a map with
// lowercase header names as keys.
func parseHeaders(lines []string) map[string]string {
	headers := make(map[string]string)
	var currentKey string
	var currentVal strings.Builder

	flush := func() {
		if currentKey != "" {
			headers[currentKey] = strings.TrimSpace(currentVal.String())
		}
	}

	for _, line := range lines {
		// Folded header: starts with space or tab.
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if currentKey != "" {
				currentVal.WriteString(" ")
				currentVal.WriteString(strings.TrimSpace(line))
			}
			continue
		}

		// New header line.
		flush()

		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			currentKey = ""
			continue
		}

		currentKey = strings.ToLower(strings.TrimSpace(line[:idx]))
		currentVal.Reset()
		currentVal.WriteString(line[idx+1:])
	}

	flush()
	return headers
}

// extractBoundary extracts the MIME boundary string from a Content-Type
// header value. Returns empty string if no boundary is found.
func extractBoundary(contentType string) string {
	if contentType == "" {
		return ""
	}
	// Look for boundary= in the Content-Type value.
	lower := strings.ToLower(contentType)
	idx := strings.Index(lower, "boundary=")
	if idx < 0 {
		return ""
	}
	val := contentType[idx+len("boundary="):]
	// Remove surrounding quotes if present.
	val = strings.TrimSpace(val)
	if len(val) > 0 && val[0] == '"' {
		end := strings.IndexByte(val[1:], '"')
		if end >= 0 {
			return val[1 : end+1]
		}
	}
	// Unquoted: take until semicolon or whitespace.
	if idx := strings.IndexAny(val, "; \t"); idx >= 0 {
		val = val[:idx]
	}
	return val
}

// extractPlainBody extracts the plain text body from message body lines.
// If a MIME boundary is provided (non-empty), lines starting with "--"+boundary
// mark the end of the preamble text. Truncation is NOT applied here — callers
// use TruncateOutput on the final formatted output.
func extractPlainBody(lines []string, boundary string) string {
	var b strings.Builder
	boundaryPrefix := ""
	if boundary != "" {
		boundaryPrefix = "--" + boundary
	}
	for _, line := range lines {
		// Stop at MIME boundary only if we know the boundary string.
		if boundaryPrefix != "" && strings.HasPrefix(line, boundaryPrefix) {
			break
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
	}
	return b.String()
}

// formatMessageList formats a list of messages as a numbered summary.
func formatMessageList(msgs []mailMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&b, "%d. From: %s\n   Subject: %s\n   Date: %s\n   Message-ID: %s\n",
			i+1, m.From, m.Subject, m.Date, m.MessageID)
		if i < len(msgs)-1 {
			b.WriteString("\n")
		}
	}
	return TruncateOutput(b.String())
}

// formatFullMessage formats a single message with headers and body.
func formatFullMessage(m mailMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\nSubject: %s\nDate: %s\nMessage-ID: %s\n\n%s",
		m.From, m.Subject, m.Date, m.MessageID, m.Body)
	return TruncateOutput(b.String())
}

// optionalIntArg extracts an optional integer argument from the args map.
// JSON numbers are unmarshaled as float64, so this handles the conversion.
// Returns defaultVal if the key is missing or the value is not a number.
func optionalIntArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return defaultVal
		}
		return int(i)
	default:
		return defaultVal
	}
}
