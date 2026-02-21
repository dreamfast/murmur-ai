package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testMbox is a sample mbox file with 3 messages:
// - Message 1: unread (status 0000)
// - Message 2: read (status 0001)
// - Message 3: unread (status 0000)
const testMbox = `From sender@example.com Mon Jan  1 00:00:00 2024
From: alice@example.com
Subject: Hello World
Date: Mon, 1 Jan 2024 00:00:00 +0000
Message-ID: <msg-001@example.com>
X-Mozilla-Status: 0000

This is the first message body.
It has multiple lines.

From sender@example.com Tue Jan  2 00:00:00 2024
From: bob@example.com
Subject: Re: Hello World
Date: Tue, 2 Jan 2024 00:00:00 +0000
Message-ID: <msg-002@example.com>
X-Mozilla-Status: 0001

This is a read message from Bob.

From sender@example.com Wed Jan  3 00:00:00 2024
From: charlie@example.com
Subject: Meeting Tomorrow
Date: Wed, 3 Jan 2024 00:00:00 +0000
Message-ID: <msg-003@example.com>
X-Mozilla-Status: 0000

Don't forget the meeting at 3pm.
`

func setupTestMbox(t *testing.T, content string) (profilePath, mailDir string) {
	t.Helper()
	dir := t.TempDir()
	mailPath := filepath.Join(dir, "Mail", "TestAccount")
	if err := os.MkdirAll(mailPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mailPath, "Inbox"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return dir, "Mail/TestAccount"
}

func TestMailRead_Unread(t *testing.T) {
	t.Parallel()

	profilePath, mailDir := setupTestMbox(t, testMbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "unread",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Should find 2 unread messages (msg-001 and msg-003).
	if !strings.Contains(result, "alice@example.com") {
		t.Error("expected alice in unread results")
	}
	if !strings.Contains(result, "charlie@example.com") {
		t.Error("expected charlie in unread results")
	}
	if strings.Contains(result, "bob@example.com") {
		t.Error("bob's message is read, should not appear in unread")
	}

	// Most recent first — charlie should be first.
	aliceIdx := strings.Index(result, "alice@example.com")
	charlieIdx := strings.Index(result, "charlie@example.com")
	if charlieIdx > aliceIdx {
		t.Error("expected charlie (most recent) before alice in results")
	}
}

func TestMailRead_Search(t *testing.T) {
	t.Parallel()

	profilePath, mailDir := setupTestMbox(t, testMbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "search",
		"query":  "Meeting",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "charlie@example.com") {
		t.Error("expected charlie in search results for 'Meeting'")
	}
	if !strings.Contains(result, "Meeting Tomorrow") {
		t.Error("expected 'Meeting Tomorrow' subject in results")
	}
	if strings.Contains(result, "alice@example.com") {
		t.Error("alice should not appear in search for 'Meeting'")
	}
}

func TestMailRead_SearchByBody(t *testing.T) {
	t.Parallel()

	profilePath, mailDir := setupTestMbox(t, testMbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "search",
		"query":  "3pm",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "charlie@example.com") {
		t.Error("expected charlie in search results for body content '3pm'")
	}
}

func TestMailRead_Read(t *testing.T) {
	t.Parallel()

	profilePath, mailDir := setupTestMbox(t, testMbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":     "read",
		"message_id": "<msg-002@example.com>",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "bob@example.com") {
		t.Error("expected bob in read result")
	}
	if !strings.Contains(result, "Re: Hello World") {
		t.Error("expected subject in read result")
	}
	if !strings.Contains(result, "read message from Bob") {
		t.Error("expected body content in read result")
	}
}

func TestMailRead_ReadNotFound(t *testing.T) {
	t.Parallel()

	profilePath, mailDir := setupTestMbox(t, testMbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":     "read",
		"message_id": "<nonexistent@example.com>",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "No message found") {
		t.Errorf("expected 'No message found', got %q", result)
	}
}

func TestMailRead_MozillaStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   string
		wantRead bool
	}{
		{"unread", "0000", false},
		{"read", "0001", true},
		{"read+deleted", "0009", true},
		{"flagged", "0004", false},
		{"new", "1000", false},
		{"empty", "", false},
		{"invalid", "ZZZZ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status := parseMozillaStatus(tt.status)
			gotRead := !isUnread(status)
			if gotRead != tt.wantRead {
				t.Errorf("parseMozillaStatus(%q): isRead=%v, want %v", tt.status, gotRead, tt.wantRead)
			}
		})
	}
}

func TestMailRead_EmptyMbox(t *testing.T) {
	t.Parallel()

	profilePath, mailDir := setupTestMbox(t, "")
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "unread",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if result != "No unread messages." {
		t.Errorf("expected 'No unread messages.', got %q", result)
	}
}

func TestMailRead_FolderNotFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tool := NewMailReadTool(dir, "Mail/TestAccount")

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "unread",
		"folder": "NonexistentFolder",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent folder")
	}
	if !strings.Contains(err.Error(), "folder not found") {
		t.Errorf("expected 'folder not found' error, got %q", err.Error())
	}
}

func TestMailRead_LimitRespected(t *testing.T) {
	t.Parallel()

	// Create mbox with 5 unread messages.
	var mbox strings.Builder
	for i := 1; i <= 5; i++ {
		mbox.WriteString("From sender@example.com Mon Jan  1 00:00:00 2024\n")
		mbox.WriteString("From: user" + strings.Repeat("x", i) + "@example.com\n")
		mbox.WriteString("Subject: Message " + string(rune('0'+i)) + "\n")
		mbox.WriteString("Date: Mon, 1 Jan 2024 00:00:00 +0000\n")
		mbox.WriteString("Message-ID: <msg-00" + string(rune('0'+i)) + "@example.com>\n")
		mbox.WriteString("X-Mozilla-Status: 0000\n")
		mbox.WriteString("\nBody of message.\n\n")
	}

	profilePath, mailDir := setupTestMbox(t, mbox.String())
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "unread",
		"limit":  float64(2), // JSON numbers are float64
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Count numbered entries (lines starting with "N. From:")
	count := strings.Count(result, ". From:")
	if count != 2 {
		t.Errorf("expected 2 messages with limit=2, got %d in:\n%s", count, result)
	}
}

func TestMailRead_PathTraversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tool := NewMailReadTool(dir, "Mail/TestAccount")

	tests := []struct {
		name   string
		folder string
	}{
		{"dotdot", "../../../etc/passwd"},
		{"dotdot_middle", "Inbox/../../secret"},
		{"absolute", "/etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"action": "unread",
				"folder": tt.folder,
			})
			if err == nil {
				t.Errorf("expected error for folder %q", tt.folder)
			}
		})
	}
}

func TestMailRead_ResolvedPathEscapesProfile(t *testing.T) {
	t.Parallel()

	// Even without ".." in the folder name, a crafted mail_dir could escape.
	// validateFolderPath checks the resolved path stays under profilePath.
	_, err := validateFolderPath("Inbox", "/home/user/.thunderbird/profile", "../../etc")
	if err == nil {
		t.Error("expected error when resolved path escapes profile directory")
	}
	if !strings.Contains(err.Error(), "escapes profile directory") {
		t.Errorf("expected 'escapes profile directory' error, got %q", err.Error())
	}
}

func TestMailRead_SymlinkEscape(t *testing.T) {
	t.Parallel()

	// Create a profile directory and a target outside it.
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "profile")
	outsideDir := filepath.Join(dir, "outside")
	mailDir := filepath.Join(profileDir, "Mail")

	if err := os.MkdirAll(mailDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Write a secret file outside the profile.
	if err := os.WriteFile(filepath.Join(outsideDir, "secret"), []byte("secret data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create a symlink inside the mail dir pointing outside.
	symlinkPath := filepath.Join(mailDir, "EvilLink")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	// validateFolderPath should reject the symlink that escapes.
	_, err := validateFolderPath("EvilLink", profileDir, "Mail")
	if err == nil {
		t.Error("expected error for symlink escaping profile directory")
	}
}

func TestMailRead_CRLFMbox(t *testing.T) {
	t.Parallel()

	// Mbox with CRLF line endings (Windows-style).
	mbox := "From sender@example.com Mon Jan  1 00:00:00 2024\r\n" +
		"From: alice@example.com\r\n" +
		"Subject: CRLF Test\r\n" +
		"Date: Mon, 1 Jan 2024 00:00:00 +0000\r\n" +
		"Message-ID: <crlf@example.com>\r\n" +
		"X-Mozilla-Status: 0000\r\n" +
		"\r\n" +
		"Body with CRLF line endings.\r\n" +
		"Second line of body.\r\n"

	profilePath, mailDir := setupTestMbox(t, mbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":     "read",
		"message_id": "<crlf@example.com>",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "alice@example.com") {
		t.Error("expected alice in CRLF mbox read result")
	}
	if !strings.Contains(result, "CRLF Test") {
		t.Error("expected subject in CRLF mbox read result")
	}
	if !strings.Contains(result, "Body with CRLF line endings") {
		t.Error("expected body content in CRLF mbox read result")
	}
}

func TestMailRead_SearchNoQuery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tool := NewMailReadTool(dir, "Mail/TestAccount")

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "search",
	})
	if err == nil {
		t.Fatal("expected error for search without query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("expected 'query is required' error, got %q", err.Error())
	}
}

func TestMailRead_ReadNoMessageID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tool := NewMailReadTool(dir, "Mail/TestAccount")

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
	})
	if err == nil {
		t.Fatal("expected error for read without message_id")
	}
	if !strings.Contains(err.Error(), "message_id is required") {
		t.Errorf("expected 'message_id is required' error, got %q", err.Error())
	}
}

func TestMailRead_FoldedHeaders(t *testing.T) {
	t.Parallel()

	mbox := `From sender@example.com Mon Jan  1 00:00:00 2024
From: alice@example.com
Subject: This is a very long subject
 that continues on the next line
Date: Mon, 1 Jan 2024 00:00:00 +0000
Message-ID: <folded@example.com>
X-Mozilla-Status: 0000

Body text.
`

	profilePath, mailDir := setupTestMbox(t, mbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":     "read",
		"message_id": "<folded@example.com>",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "very long subject that continues on the next line") {
		t.Errorf("expected folded header to be joined, got %q", result)
	}
}

func TestMailRead_DashedLinesNotMIMEBoundary(t *testing.T) {
	t.Parallel()

	// Messages with lines starting with "--" should NOT be truncated
	// when there is no Content-Type boundary header.
	mbox := `From sender@example.com Mon Jan  1 00:00:00 2024
From: alice@example.com
Subject: Markdown Email
Date: Mon, 1 Jan 2024 00:00:00 +0000
Message-ID: <dashes@example.com>
X-Mozilla-Status: 0000

Here is some text.
---
This is a horizontal rule in markdown.
-- 
This is a signature separator.
More text after dashes.
`

	profilePath, mailDir := setupTestMbox(t, mbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":     "read",
		"message_id": "<dashes@example.com>",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// All content should be present since there's no MIME boundary.
	if !strings.Contains(result, "horizontal rule") {
		t.Error("expected 'horizontal rule' text after --- line")
	}
	if !strings.Contains(result, "signature separator") {
		t.Error("expected 'signature separator' text after -- line")
	}
	if !strings.Contains(result, "More text after dashes") {
		t.Error("expected text after dashed lines")
	}
}

func TestMailRead_MIMEBoundaryRespected(t *testing.T) {
	t.Parallel()

	// When Content-Type has a boundary, text after the boundary should be excluded.
	mbox := `From sender@example.com Mon Jan  1 00:00:00 2024
From: alice@example.com
Subject: MIME Email
Date: Mon, 1 Jan 2024 00:00:00 +0000
Message-ID: <mime@example.com>
X-Mozilla-Status: 0000
Content-Type: multipart/mixed; boundary="----=_Part_123"

This is the preamble text.
------=_Part_123
Content-Type: text/plain

This is the plain text part.
------=_Part_123
Content-Type: text/html

<p>This is HTML</p>
------=_Part_123--
`

	profilePath, mailDir := setupTestMbox(t, mbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":     "read",
		"message_id": "<mime@example.com>",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Preamble text should be present.
	if !strings.Contains(result, "preamble text") {
		t.Error("expected preamble text before MIME boundary")
	}
	// Content after the boundary should NOT be present.
	if strings.Contains(result, "plain text part") {
		t.Error("MIME part content should not appear in plain body extraction")
	}
	if strings.Contains(result, "<p>This is HTML</p>") {
		t.Error("HTML MIME part should not appear in plain body extraction")
	}
}

func TestExtractBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		want        string
	}{
		{"empty", "", ""},
		{"no_boundary", "text/plain; charset=utf-8", ""},
		{"unquoted", "multipart/mixed; boundary=----=_Part_123", "----=_Part_123"},
		{"quoted", `multipart/mixed; boundary="----=_Part_456"`, "----=_Part_456"},
		{"case_insensitive", "multipart/mixed; Boundary=abc123", "abc123"},
		{"with_extra_params", "multipart/mixed; boundary=abc; charset=utf-8", "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractBoundary(tt.contentType)
			if got != tt.want {
				t.Errorf("extractBoundary(%q) = %q, want %q", tt.contentType, got, tt.want)
			}
		})
	}
}

func TestParseHeaders(t *testing.T) {
	t.Parallel()

	lines := []string{
		"From: alice@example.com",
		"Subject: Test",
		"X-Mozilla-Status: 0001",
		"Content-Type: text/plain;",
		" charset=utf-8",
	}

	headers := parseHeaders(lines)

	if headers["from"] != "alice@example.com" {
		t.Errorf("from = %q", headers["from"])
	}
	if headers["subject"] != "Test" {
		t.Errorf("subject = %q", headers["subject"])
	}
	if headers["x-mozilla-status"] != "0001" {
		t.Errorf("x-mozilla-status = %q", headers["x-mozilla-status"])
	}
	if !strings.Contains(headers["content-type"], "charset=utf-8") {
		t.Errorf("content-type = %q, expected folded value", headers["content-type"])
	}
}

// --- Multi-account tests ---

// samplePrefsJS is a minimal Thunderbird prefs.js with three mail accounts
// and one "none" (Local Folders) account that should be excluded.
const samplePrefsJS = `// Mozilla User Preferences
user_pref("mail.server.server1.directory-rel", "[ProfD]Mail/pop.gmail-1.com");
user_pref("mail.server.server1.name", "Riverlab");
user_pref("mail.server.server1.type", "pop3");
user_pref("mail.server.server1.userName", "nathan@riverlab.com");
user_pref("mail.server.server2.directory-rel", "[ProfD]Mail/pop.gmail-2.com");
user_pref("mail.server.server2.name", "Personal");
user_pref("mail.server.server2.type", "pop3");
user_pref("mail.server.server2.userName", "nsapwell@gmail.com");
user_pref("mail.server.server3.directory-rel", "[ProfD]Mail/Local Folders");
user_pref("mail.server.server3.name", "Local Folders");
user_pref("mail.server.server3.type", "none");
user_pref("mail.server.server3.userName", "nobody");
user_pref("mail.server.server4.directory-rel", "[ProfD]Mail/imap.example.com");
user_pref("mail.server.server4.name", "Work");
user_pref("mail.server.server4.type", "imap");
user_pref("mail.server.server4.userName", "nathan@work.com");
user_pref("some.other.pref", "ignored");
`

func writePrefsJS(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "prefs.js")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile prefs.js: %v", err)
	}
	return path
}

func TestParseThunderbirdPrefs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writePrefsJS(t, dir, samplePrefsJS)

	accounts, err := parseThunderbirdPrefs(filepath.Join(dir, "prefs.js"))
	if err != nil {
		t.Fatalf("parseThunderbirdPrefs: %v", err)
	}

	// Should have 3 accounts (Local Folders excluded).
	if len(accounts) != 3 {
		t.Fatalf("expected 3 accounts, got %d: %+v", len(accounts), accounts)
	}

	// Accounts should be sorted by name.
	names := make([]string, len(accounts))
	for i, a := range accounts {
		names[i] = a.Name
	}
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("accounts not sorted: %v", names)
			break
		}
	}

	// Verify specific account data.
	found := false
	for _, a := range accounts {
		if a.Name == "Riverlab" {
			found = true
			if a.Email != "nathan@riverlab.com" {
				t.Errorf("Riverlab email = %q, want nathan@riverlab.com", a.Email)
			}
			if a.DirRel != "Mail/pop.gmail-1.com" {
				t.Errorf("Riverlab DirRel = %q, want Mail/pop.gmail-1.com", a.DirRel)
			}
			if a.Type != "pop3" {
				t.Errorf("Riverlab Type = %q, want pop3", a.Type)
			}
		}
	}
	if !found {
		t.Error("Riverlab account not found in parsed results")
	}

	// Verify IMAP account is included.
	found = false
	for _, a := range accounts {
		if a.Name == "Work" {
			found = true
			if a.Type != "imap" {
				t.Errorf("Work Type = %q, want imap", a.Type)
			}
		}
	}
	if !found {
		t.Error("Work (imap) account not found in parsed results")
	}
}

func TestParseThunderbirdPrefs_FileNotFound(t *testing.T) {
	t.Parallel()

	_, err := parseThunderbirdPrefs("/nonexistent/prefs.js")
	if err == nil {
		t.Fatal("expected error for nonexistent prefs.js")
	}
}

func TestParseThunderbirdPrefs_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writePrefsJS(t, dir, "// empty prefs\n")

	accounts, err := parseThunderbirdPrefs(filepath.Join(dir, "prefs.js"))
	if err != nil {
		t.Fatalf("parseThunderbirdPrefs: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("expected 0 accounts from empty prefs, got %d", len(accounts))
	}
}

func TestParseThunderbirdPrefs_NNTPExcluded(t *testing.T) {
	t.Parallel()

	prefs := `user_pref("mail.server.server1.directory-rel", "[ProfD]News/news.example.com");
user_pref("mail.server.server1.name", "Usenet");
user_pref("mail.server.server1.type", "nntp");
user_pref("mail.server.server1.userName", "user");
`
	dir := t.TempDir()
	writePrefsJS(t, dir, prefs)

	accounts, err := parseThunderbirdPrefs(filepath.Join(dir, "prefs.js"))
	if err != nil {
		t.Fatalf("parseThunderbirdPrefs: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("expected 0 accounts (nntp excluded), got %d", len(accounts))
	}
}

func TestResolveMailDir(t *testing.T) {
	t.Parallel()

	accounts := []thunderbirdAccount{
		{Name: "Riverlab", Email: "nathan@riverlab.com", DirRel: "Mail/pop.gmail-1.com", Type: "pop3"},
		{Name: "Personal", Email: "nsapwell@gmail.com", DirRel: "Mail/pop.gmail-2.com", Type: "pop3"},
		{Name: "Work", Email: "nathan@work.com", DirRel: "Mail/imap.example.com", Type: "imap"},
	}

	tests := []struct {
		name     string
		selector string
		accounts []thunderbirdAccount
		fallback string
		wantDir  string
		wantErr  bool
	}{
		{
			name:     "empty selector returns first account",
			selector: "",
			accounts: accounts,
			wantDir:  "Mail/pop.gmail-1.com",
		},
		{
			name:     "exact name match",
			selector: "Personal",
			accounts: accounts,
			wantDir:  "Mail/pop.gmail-2.com",
		},
		{
			name:     "exact name case insensitive",
			selector: "personal",
			accounts: accounts,
			wantDir:  "Mail/pop.gmail-2.com",
		},
		{
			name:     "exact email match",
			selector: "nathan@work.com",
			accounts: accounts,
			wantDir:  "Mail/imap.example.com",
		},
		{
			name:     "partial name match",
			selector: "river",
			accounts: accounts,
			wantDir:  "Mail/pop.gmail-1.com",
		},
		{
			name:     "partial email match",
			selector: "nsapwell",
			accounts: accounts,
			wantDir:  "Mail/pop.gmail-2.com",
		},
		{
			name:     "no match returns error",
			selector: "nonexistent",
			accounts: accounts,
			wantErr:  true,
		},
		{
			name:     "no accounts with fallback",
			selector: "",
			accounts: nil,
			fallback: "Mail/fallback",
			wantDir:  "Mail/fallback",
		},
		{
			name:     "no accounts no fallback",
			selector: "",
			accounts: nil,
			fallback: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, err := resolveMailDir(tt.selector, tt.accounts, tt.fallback)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got dir=%q", dir)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dir != tt.wantDir {
				t.Errorf("got %q, want %q", dir, tt.wantDir)
			}
		})
	}
}

func TestFormatAccountList(t *testing.T) {
	t.Parallel()

	accounts := []thunderbirdAccount{
		{Name: "Riverlab", Email: "nathan@riverlab.com", Type: "pop3"},
		{Name: "Work", Email: "nathan@work.com", Type: "imap"},
	}

	result := formatAccountList(accounts)

	if !strings.Contains(result, "1. Riverlab <nathan@riverlab.com> [pop3]") {
		t.Errorf("expected formatted Riverlab entry, got:\n%s", result)
	}
	if !strings.Contains(result, "2. Work <nathan@work.com> [imap]") {
		t.Errorf("expected formatted Work entry, got:\n%s", result)
	}
}

func TestFormatAccountList_Empty(t *testing.T) {
	t.Parallel()

	result := formatAccountList(nil)
	if result != "" {
		t.Errorf("expected empty string for nil accounts, got %q", result)
	}
}

func TestMailRead_AccountsAction(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writePrefsJS(t, dir, samplePrefsJS)

	tool := NewMailReadTool(dir, "Mail/fallback")

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "accounts",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Should list all 3 non-excluded accounts.
	if !strings.Contains(result, "Riverlab") {
		t.Error("expected Riverlab in accounts list")
	}
	if !strings.Contains(result, "Personal") {
		t.Error("expected Personal in accounts list")
	}
	if !strings.Contains(result, "Work") {
		t.Error("expected Work in accounts list")
	}
	if strings.Contains(result, "Local Folders") {
		t.Error("Local Folders (type=none) should be excluded")
	}
}

func TestMailRead_AccountsAction_NoAccounts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// No prefs.js — accounts will be nil.
	tool := NewMailReadTool(dir, "Mail/fallback")

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "accounts",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "No mail accounts discovered") {
		t.Errorf("expected 'No mail accounts discovered', got %q", result)
	}
}

func TestMailRead_AccountSelector(t *testing.T) {
	t.Parallel()

	// Create a profile dir with prefs.js and two mail account directories.
	dir := t.TempDir()
	writePrefsJS(t, dir, samplePrefsJS)

	// Create Inbox files for two accounts.
	for _, sub := range []string{"Mail/pop.gmail-1.com", "Mail/pop.gmail-2.com"} {
		mailPath := filepath.Join(dir, sub)
		if err := os.MkdirAll(mailPath, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	// Write different mbox content to each account's Inbox.
	inbox1 := `From sender@example.com Mon Jan  1 00:00:00 2024
From: riverlab-sender@example.com
Subject: Riverlab Message
Date: Mon, 1 Jan 2024 00:00:00 +0000
Message-ID: <river-001@example.com>
X-Mozilla-Status: 0000

Riverlab inbox content.
`
	inbox2 := `From sender@example.com Mon Jan  1 00:00:00 2024
From: personal-sender@example.com
Subject: Personal Message
Date: Mon, 1 Jan 2024 00:00:00 +0000
Message-ID: <personal-001@example.com>
X-Mozilla-Status: 0000

Personal inbox content.
`
	if err := os.WriteFile(filepath.Join(dir, "Mail/pop.gmail-1.com/Inbox"), []byte(inbox1), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Mail/pop.gmail-2.com/Inbox"), []byte(inbox2), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tool := NewMailReadTool(dir, "")

	// Query Riverlab account by name.
	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "unread",
		"account": "Riverlab",
	})
	if err != nil {
		t.Fatalf("Handler (Riverlab): %v", err)
	}
	if !strings.Contains(result, "riverlab-sender@example.com") {
		t.Errorf("expected Riverlab inbox content, got:\n%s", result)
	}

	// Query Personal account by email.
	result, err = tool.Handler(context.Background(), map[string]any{
		"action":  "unread",
		"account": "nsapwell@gmail.com",
	})
	if err != nil {
		t.Fatalf("Handler (Personal): %v", err)
	}
	if !strings.Contains(result, "personal-sender@example.com") {
		t.Errorf("expected Personal inbox content, got:\n%s", result)
	}
}

func TestMailRead_FallbackWhenNoAccounts(t *testing.T) {
	t.Parallel()

	// No prefs.js, but fallbackMailDir is set — should use fallback.
	profilePath, mailDir := setupTestMbox(t, testMbox)
	tool := NewMailReadTool(profilePath, mailDir)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "unread",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Should still work via fallback.
	if !strings.Contains(result, "alice@example.com") {
		t.Error("expected fallback to work when no accounts discovered")
	}
}
