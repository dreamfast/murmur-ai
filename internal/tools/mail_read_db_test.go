package tools

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestMailDB creates a shared-cache in-memory SQLite database with the
// Thunderbird global-messages-db schema and seeds it with test data. Returns
// a *mailDB whose per-query connections see the same shared data.
func setupTestMailDB(t *testing.T) *mailDB {
	t.Helper()

	// Use a unique shared-cache name per test so parallel tests don't collide.
	// file::memory:?cache=shared would share across all, so we use a unique name.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	// Keep at least one connection open so the shared-cache database
	// persists for the lifetime of the test.
	db.SetMaxIdleConns(1)

	// Create the Thunderbird schema.
	schema := `
		CREATE TABLE folderLocations (
			id INTEGER PRIMARY KEY,
			folderURI TEXT NOT NULL,
			dirtyStatus INTEGER NOT NULL DEFAULT 0,
			name TEXT NOT NULL,
			indexingPriority INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE messages (
			id INTEGER PRIMARY KEY,
			folderID INTEGER,
			messageKey INTEGER,
			conversationID INTEGER NOT NULL DEFAULT 0,
			date INTEGER,
			headerMessageID TEXT,
			deleted INTEGER NOT NULL DEFAULT 0,
			jsonAttributes TEXT,
			notability INTEGER NOT NULL DEFAULT 0
		);

		CREATE INDEX headerMessageID ON messages(headerMessageID);
		CREATE INDEX date ON messages(date);

		CREATE TABLE 'messagesText_content' (
			docid INTEGER PRIMARY KEY,
			'c0body',
			'c1subject',
			'c2attachmentNames',
			'c3author',
			'c4recipients'
		);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}

	// Seed folder locations.
	folders := []struct {
		id   int
		uri  string
		name string
	}{
		{1, "mailbox://nathan%40riverlab.com@pop.gmail.com/Inbox", "Inbox"},
		{2, "mailbox://nathan%40riverlab.com@pop.gmail.com/Sent", "Sent"},
		{3, "mailbox://nsapwell%40gmail.com@pop.gmail.com/Inbox", "Inbox"},
		{4, "mailbox://nobody@Local%20Folders/Archive", "Archive"},
	}
	for _, f := range folders {
		if _, err := db.Exec("INSERT INTO folderLocations (id, folderURI, dirtyStatus, name, indexingPriority) VALUES (?, ?, 0, ?, 0)",
			f.id, f.uri, f.name); err != nil {
			db.Close()
			t.Fatalf("insert folder %d: %v", f.id, err)
		}
	}

	// Seed messages. jsonAttributes key 61 = read status (non-zero=read, 0/null=unread).
	// Dates are in microseconds since Unix epoch.
	messages := []struct {
		id       int
		folderID int
		date     int64
		msgID    string
		json     string
		deleted  int
	}{
		// Riverlab Inbox — 3 messages, 1 unread.
		{1, 1, 1700000000000000, "msg-001@example.com", `{"61":1}`, 0},
		{2, 1, 1700100000000000, "msg-002@example.com", `{"61":0}`, 0},
		{3, 1, 1700200000000000, "msg-003@example.com", `{"61":1}`, 0},
		// Riverlab Sent — 1 message, read.
		{4, 2, 1700050000000000, "msg-004@example.com", `{"61":1}`, 0},
		// Personal Inbox — 2 messages, 1 unread.
		{5, 3, 1700300000000000, "msg-005@example.com", `{"61":0}`, 0},
		{6, 3, 1700400000000000, "msg-006@example.com", `{"61":1}`, 0},
		// Archive — 1 message, read.
		{7, 4, 1700500000000000, "msg-007@example.com", `{"61":1}`, 0},
		// Deleted message — should never appear.
		{8, 1, 1700600000000000, "msg-008@example.com", `{"61":0}`, 1},
	}
	for _, m := range messages {
		if _, err := db.Exec("INSERT INTO messages (id, folderID, date, headerMessageID, jsonAttributes, deleted, conversationID) VALUES (?, ?, ?, ?, ?, ?, 0)",
			m.id, m.folderID, m.date, m.msgID, m.json, m.deleted); err != nil {
			db.Close()
			t.Fatalf("insert message %d: %v", m.id, err)
		}
	}

	// Seed messagesText_content (FTS content table).
	texts := []struct {
		docid      int
		body       string
		subject    string
		author     string
		recipients string
	}{
		{1, "Hello from Riverlab. Project update attached.", "Project Update Q4", "Alice Smith <alice@riverlab.com> undefined", "nathan@riverlab.com"},
		{2, "Please review the invoice for November.", "Invoice for November 2024", "Bob Jones <bob@vendor.com> undefined", "nathan@riverlab.com"},
		{3, "Meeting notes from the standup.", "Standup Notes Dec 1", "Charlie Brown <charlie@riverlab.com> undefined", "nathan@riverlab.com"},
		{4, "Sent: follow up on the proposal.", "Re: Proposal Follow-up", "Nathan <nathan@riverlab.com>", "client@example.com"},
		{5, "Your order has shipped.", "Order Shipped - #12345", "Shop <noreply@shop.com> undefined", "nsapwell@gmail.com"},
		{6, "Weekly newsletter content here.", "Weekly Newsletter", "Newsletter <news@example.com> undefined", "nsapwell@gmail.com"},
		{7, "Archived old conversation.", "Old Thread", "Archive Bot <bot@example.com> undefined", "nathan@riverlab.com"},
		{8, "This is deleted and should not appear.", "Deleted Message", "Ghost <ghost@example.com> undefined", "nathan@riverlab.com"},
	}
	for _, tx := range texts {
		if _, err := db.Exec("INSERT INTO messagesText_content (docid, c0body, c1subject, c2attachmentNames, c3author, c4recipients) VALUES (?, ?, ?, '', ?, ?)",
			tx.docid, tx.body, tx.subject, tx.author, tx.recipients); err != nil {
			db.Close()
			t.Fatalf("insert text %d: %v", tx.docid, err)
		}
	}

	t.Cleanup(func() { db.Close() })
	return &mailDB{dsn: dsn}
}

func TestMailDB_Unread(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	// All accounts, no folder filter.
	msgs, err := mdb.unread("", "", 100)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}

	// Should find 2 unread (msg-002 in Riverlab Inbox, msg-005 in Personal Inbox).
	// msg-008 is deleted and should not appear.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 unread, got %d", len(msgs))
	}

	// Most recent first.
	if msgs[0].MessageID != "msg-005@example.com" {
		t.Errorf("first unread = %q, want msg-005", msgs[0].MessageID)
	}
	if msgs[1].MessageID != "msg-002@example.com" {
		t.Errorf("second unread = %q, want msg-002", msgs[1].MessageID)
	}
}

func TestMailDB_UnreadFilterByAccount(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	// Filter to Riverlab account only.
	msgs, err := mdb.unread("mailbox://nathan%40riverlab.com@%", "", 100)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 unread for Riverlab, got %d", len(msgs))
	}
	if msgs[0].MessageID != "msg-002@example.com" {
		t.Errorf("unread = %q, want msg-002", msgs[0].MessageID)
	}
}

func TestMailDB_UnreadFilterByFolder(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	// Filter to Sent folder — should have 0 unread.
	msgs, err := mdb.unread("", "Sent", 100)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}

	if len(msgs) != 0 {
		t.Fatalf("expected 0 unread in Sent, got %d", len(msgs))
	}
}

func TestMailDB_UnreadLimit(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	msgs, err := mdb.unread("", "", 1)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message with limit=1, got %d", len(msgs))
	}
}

func TestMailDB_Search(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		query   string
		wantIDs []string
	}{
		{
			name:    "search by subject keyword",
			query:   "invoice",
			wantIDs: []string{"msg-002@example.com"},
		},
		{
			name:    "search by author",
			query:   "Alice Smith",
			wantIDs: []string{"msg-001@example.com"},
		},
		{
			name:    "search by body content",
			query:   "order has shipped",
			wantIDs: []string{"msg-005@example.com"},
		},
		{
			name:    "search no results",
			query:   "xyznonexistent",
			wantIDs: nil,
		},
		{
			name:    "deleted messages excluded",
			query:   "deleted and should not appear",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each subtest gets its own DB to avoid cleanup races.
			mdb := setupTestMailDB(t)
			msgs, err := mdb.search(tt.query, "", "", 100)
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			if len(msgs) != len(tt.wantIDs) {
				ids := make([]string, len(msgs))
				for i, m := range msgs {
					ids[i] = m.MessageID
				}
				t.Fatalf("expected %d results, got %d: %v", len(tt.wantIDs), len(msgs), ids)
			}

			for i, wantID := range tt.wantIDs {
				if msgs[i].MessageID != wantID {
					t.Errorf("result[%d] = %q, want %q", i, msgs[i].MessageID, wantID)
				}
			}
		})
	}
}

func TestMailDB_SearchFilterByAccount(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	// "newsletter" is in Personal Inbox (msg-006). Filter to Riverlab only.
	msgs, err := mdb.search("newsletter", "mailbox://nathan%40riverlab.com@%", "", 100)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(msgs) != 0 {
		t.Errorf("expected 0 results for newsletter in Riverlab, got %d", len(msgs))
	}
}

func TestMailDB_ReadByID(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	msg, err := mdb.readByID("msg-002@example.com")
	if err != nil {
		t.Fatalf("readByID: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message, got nil")
	}

	if msg.MessageID != "msg-002@example.com" {
		t.Errorf("MessageID = %q", msg.MessageID)
	}
	if msg.Subject != "Invoice for November 2024" {
		t.Errorf("Subject = %q", msg.Subject)
	}
	if msg.From != "Bob Jones <bob@vendor.com> undefined" {
		t.Errorf("From = %q", msg.From)
	}
	if msg.Body != "Please review the invoice for November." {
		t.Errorf("Body = %q", msg.Body)
	}
	if msg.Date == "" {
		t.Error("Date is empty")
	}
}

func TestMailDB_ReadByID_NotFound(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	msg, err := mdb.readByID("nonexistent@example.com")
	if err != nil {
		t.Fatalf("readByID: %v", err)
	}
	if msg != nil {
		t.Errorf("expected nil for nonexistent message, got %+v", msg)
	}
}

func TestMailDB_ReadByID_DeletedExcluded(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	msg, err := mdb.readByID("msg-008@example.com")
	if err != nil {
		t.Fatalf("readByID: %v", err)
	}
	if msg != nil {
		t.Error("expected nil for deleted message")
	}
}

func TestMailDB_Folders(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	folders, err := mdb.folders("")
	if err != nil {
		t.Fatalf("folders: %v", err)
	}

	// Should have 4 folders (Riverlab Inbox=3, Personal Inbox=2, Sent=1, Archive=1).
	// Deleted messages don't count, so Riverlab Inbox has 3 not 4.
	if len(folders) != 4 {
		t.Fatalf("expected 4 folders, got %d: %+v", len(folders), folders)
	}

	// Results ordered by count DESC.
	if folders[0].Name != "Inbox" || folders[0].Count != 3 {
		t.Errorf("first folder = %q (%d), want Inbox (3)", folders[0].Name, folders[0].Count)
	}
}

func TestMailDB_FoldersFilterByAccount(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	folders, err := mdb.folders("mailbox://nsapwell%40gmail.com@%")
	if err != nil {
		t.Fatalf("folders: %v", err)
	}

	// Personal account has 1 folder (Inbox with 2 messages).
	if len(folders) != 1 {
		t.Fatalf("expected 1 folder for Personal, got %d", len(folders))
	}
	if folders[0].Count != 2 {
		t.Errorf("Personal Inbox count = %d, want 2", folders[0].Count)
	}
}

func TestFolderURIPattern(t *testing.T) {
	t.Parallel()

	accounts := []thunderbirdAccount{
		{Name: "Riverlab", Email: "nathan@riverlab.com", DirRel: "Mail/pop.gmail-1.com", Type: "pop3"},
		{Name: "Personal", Email: "nsapwell@gmail.com", DirRel: "Mail/pop.gmail-2.com", Type: "pop3"},
	}

	tests := []struct {
		name     string
		selector string
		want     string
	}{
		{"empty selector", "", ""},
		{"exact name", "Riverlab", "mailbox://nathan%40riverlab.com@%"},
		{"exact email", "nsapwell@gmail.com", "mailbox://nsapwell%40gmail.com@%"},
		{"case insensitive", "riverlab", "mailbox://nathan%40riverlab.com@%"},
		{"partial name", "river", "mailbox://nathan%40riverlab.com@%"},
		{"partial email", "nsapwell", "mailbox://nsapwell%40gmail.com@%"},
		{"no match", "nonexistent", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := folderURIPattern(accounts, tt.selector)
			if got != tt.want {
				t.Errorf("folderURIPattern(%q) = %q, want %q", tt.selector, got, tt.want)
			}
		})
	}
}

func TestFolderURIPattern_NoAccounts(t *testing.T) {
	t.Parallel()

	got := folderURIPattern(nil, "anything")
	if got != "" {
		t.Errorf("expected empty pattern for nil accounts, got %q", got)
	}
}

func TestFormatDBDate(t *testing.T) {
	t.Parallel()

	// 1700000000 seconds = 2023-11-14 22:13:20 UTC
	result := formatDBDate(1700000000000000)
	if result != "Tue, 14 Nov 2023 22:13:20 +0000" {
		t.Errorf("formatDBDate = %q", result)
	}
}

func TestFormatFolderList(t *testing.T) {
	t.Parallel()

	folders := []folderInfo{
		{Name: "Inbox", FolderURI: "mailbox://test@example.com/Inbox", Count: 42},
		{Name: "Sent", FolderURI: "mailbox://test@example.com/Sent", Count: 10},
	}

	result := formatFolderList(folders)
	if result != "1. Inbox (42 messages)\n2. Sent (10 messages)" {
		t.Errorf("formatFolderList = %q", result)
	}
}

// --- Integration tests: full handler flow through SQLite path ---

func TestHandleMailReadDB_Unread(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	accounts := []thunderbirdAccount{
		{Name: "Riverlab", Email: "nathan@riverlab.com", DirRel: "Mail/pop.gmail-1.com", Type: "pop3"},
		{Name: "Personal", Email: "nsapwell@gmail.com", DirRel: "Mail/pop.gmail-2.com", Type: "pop3"},
	}

	result, err := handleMailReadDB(mdb, "unread", "", "", 10, nil, accounts)
	if err != nil {
		t.Fatalf("handleMailReadDB: %v", err)
	}

	if !strings.Contains(result, "Invoice for November 2024") {
		t.Error("expected unread invoice message in results")
	}
	if !strings.Contains(result, "Order Shipped") {
		t.Error("expected unread order message in results")
	}
}

func TestHandleMailReadDB_UnreadByAccount(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	accounts := []thunderbirdAccount{
		{Name: "Riverlab", Email: "nathan@riverlab.com", DirRel: "Mail/pop.gmail-1.com", Type: "pop3"},
		{Name: "Personal", Email: "nsapwell@gmail.com", DirRel: "Mail/pop.gmail-2.com", Type: "pop3"},
	}

	result, err := handleMailReadDB(mdb, "unread", "Riverlab", "", 10, nil, accounts)
	if err != nil {
		t.Fatalf("handleMailReadDB: %v", err)
	}

	if !strings.Contains(result, "Invoice for November 2024") {
		t.Error("expected Riverlab unread message")
	}
	if strings.Contains(result, "Order Shipped") {
		t.Error("Personal account message should not appear when filtering by Riverlab")
	}
}

func TestHandleMailReadDB_Search(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	args := map[string]any{"query": "invoice"}
	result, err := handleMailReadDB(mdb, "search", "", "", 10, args, nil)
	if err != nil {
		t.Fatalf("handleMailReadDB: %v", err)
	}

	if !strings.Contains(result, "Invoice for November 2024") {
		t.Error("expected invoice in search results")
	}
}

func TestHandleMailReadDB_SearchNoQuery(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	args := map[string]any{}
	_, err := handleMailReadDB(mdb, "search", "", "", 10, args, nil)
	if err == nil {
		t.Fatal("expected error for search without query")
	}
}

func TestHandleMailReadDB_Read(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	args := map[string]any{"message_id": "msg-002@example.com"}
	result, err := handleMailReadDB(mdb, "read", "", "", 10, args, nil)
	if err != nil {
		t.Fatalf("handleMailReadDB: %v", err)
	}

	if !strings.Contains(result, "Invoice for November 2024") {
		t.Error("expected subject in read result")
	}
	if !strings.Contains(result, "Please review the invoice") {
		t.Error("expected body in read result")
	}
}

func TestHandleMailReadDB_ReadNotFound(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	args := map[string]any{"message_id": "nonexistent@example.com"}
	result, err := handleMailReadDB(mdb, "read", "", "", 10, args, nil)
	if err != nil {
		t.Fatalf("handleMailReadDB: %v", err)
	}

	if !strings.Contains(result, "No message found") {
		t.Errorf("expected 'No message found', got %q", result)
	}
}

func TestHandleMailReadDB_ReadNoMessageID(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	args := map[string]any{}
	_, err := handleMailReadDB(mdb, "read", "", "", 10, args, nil)
	if err == nil {
		t.Fatal("expected error for read without message_id")
	}
}

func TestHandleMailReadDB_Folders(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	result, err := handleMailReadDB(mdb, "folders", "", "", 10, nil, nil)
	if err != nil {
		t.Fatalf("handleMailReadDB: %v", err)
	}

	if !strings.Contains(result, "Inbox") {
		t.Error("expected Inbox in folders list")
	}
	if !strings.Contains(result, "messages") {
		t.Error("expected message count in folders list")
	}
}

func TestHandleMailReadDB_UnknownAction(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	_, err := handleMailReadDB(mdb, "bogus", "", "", 10, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestHandleMailReadDB_NoUnread(t *testing.T) {
	t.Parallel()
	mdb := setupTestMailDB(t)

	// Filter to Archive folder — all messages are read.
	result, err := handleMailReadDB(mdb, "unread", "", "Archive", 10, nil, nil)
	if err != nil {
		t.Fatalf("handleMailReadDB: %v", err)
	}

	if result != "No unread messages." {
		t.Errorf("expected 'No unread messages.', got %q", result)
	}
}

func TestHandleMailReadMbox_FoldersUnavailable(t *testing.T) {
	t.Parallel()

	_, err := handleMailReadMbox("folders", "", "", 10, nil, "/tmp", "Mail/test", nil)
	if err == nil {
		t.Fatal("expected error for folders action in mbox mode")
	}
	if !strings.Contains(err.Error(), "global-messages-db.sqlite") {
		t.Errorf("expected error mentioning sqlite, got %q", err.Error())
	}
}
