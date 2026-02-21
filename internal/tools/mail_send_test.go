package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockSMTPSender records calls for testing.
type mockSMTPSender struct {
	lastAddr  string
	lastFrom  string
	lastTo    []string
	lastMsg   []byte
	sendErr   error
	callCount int
}

func (m *mockSMTPSender) SendMail(addr, from string, to []string, msg []byte) error {
	m.callCount++
	m.lastAddr = addr
	m.lastFrom = from
	m.lastTo = to
	m.lastMsg = msg
	return m.sendErr
}

func TestMailSend_RequiredArgs(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			name:    "missing_to",
			args:    map[string]any{"subject": "Test", "body": "Hello"},
			wantErr: "missing required argument",
		},
		{
			name:    "missing_subject",
			args:    map[string]any{"to": "user@example.com", "body": "Hello"},
			wantErr: "missing required argument",
		},
		{
			name:    "missing_body",
			args:    map[string]any{"to": "user@example.com", "subject": "Test"},
			wantErr: "missing required argument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), tt.args)
			if err == nil {
				t.Fatal("expected error for missing required argument")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestMailSend_EmailValidation(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	tests := []struct {
		name    string
		to      string
		wantErr bool
	}{
		{"valid_simple", "user@example.com", false},
		{"valid_with_name", "User Name <user@example.com>", false},
		{"invalid_no_at", "not-an-email", true},
		{"invalid_empty", "", true},
		{"invalid_spaces", "user @example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"to":      tt.to,
				"subject": "Test",
				"body":    "Hello",
			})
			if tt.wantErr && err == nil {
				t.Errorf("expected error for to=%q", tt.to)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for to=%q: %v", tt.to, err)
			}
		})
	}
}

func TestMailSend_CRLFSanitization(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	// Subject with CRLF injection attempt.
	_, err := tool.Handler(context.Background(), map[string]any{
		"to":      "user@example.com",
		"subject": "Test\r\nBcc: evil@attacker.com",
		"body":    "Hello",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	msg := string(sender.lastMsg)

	// The injected Bcc should NOT appear as a separate header line.
	// After sanitization, the subject becomes "TestBcc: evil@attacker.com"
	// (CRLF stripped), which is safe because it's part of the Subject value.
	for _, line := range strings.Split(msg, "\r\n") {
		if line == "" {
			break // end of headers
		}
		if strings.HasPrefix(line, "Bcc:") {
			t.Error("CRLF injection: Bcc should not appear as a standalone header")
		}
	}

	// Verify the subject is on a single line with CRLF removed.
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, "Subject:") {
			// The sanitized subject should contain the injected text as part
			// of the value (not as a separate header).
			if !strings.Contains(line, "TestBcc:") {
				t.Errorf("expected sanitized subject to contain 'TestBcc:', got %q", line)
			}
			break
		}
	}
}

func TestMailSend_MessageFormat(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	_, err := tool.Handler(context.Background(), map[string]any{
		"to":       "recipient@example.com",
		"subject":  "Test Subject",
		"body":     "Hello, World!",
		"cc":       "cc@example.com",
		"reply_to": "reply@example.com",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	msg := string(sender.lastMsg)

	// Verify RFC 2822 headers.
	requiredHeaders := []string{
		"From: sender@example.com",
		"To: recipient@example.com",
		"Subject: Test Subject",
		"Cc: cc@example.com",
		"Reply-To: reply@example.com",
		"Date: ",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
	}

	for _, h := range requiredHeaders {
		if !strings.Contains(msg, h) {
			t.Errorf("missing header %q in message:\n%s", h, msg)
		}
	}

	// Verify body is present after blank line.
	parts := strings.SplitN(msg, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("message should have headers and body separated by blank line")
	}
	if parts[1] != "Hello, World!" {
		t.Errorf("body = %q, want %q", parts[1], "Hello, World!")
	}

	// Verify SMTP envelope.
	if sender.lastAddr != "mail.example.com:587" {
		t.Errorf("addr = %q, want %q", sender.lastAddr, "mail.example.com:587")
	}
	if sender.lastFrom != "sender@example.com" {
		t.Errorf("from = %q, want %q", sender.lastFrom, "sender@example.com")
	}
	// Recipients should include both To and Cc.
	if len(sender.lastTo) != 2 {
		t.Errorf("recipients count = %d, want 2", len(sender.lastTo))
	}
}

func TestMailSend_SendError(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{sendErr: fmt.Errorf("connection refused")}
	tool := NewMailSendTool(cfg, sender)

	_, err := tool.Handler(context.Background(), map[string]any{
		"to":      "user@example.com",
		"subject": "Test",
		"body":    "Hello",
	})
	if err == nil {
		t.Fatal("expected error when SMTP send fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected 'connection refused' in error, got %q", err.Error())
	}
}

func TestMailSend_SuccessMessage(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	result, err := tool.Handler(context.Background(), map[string]any{
		"to":      "user@example.com",
		"subject": "Test",
		"body":    "Hello",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "Email sent successfully") {
		t.Errorf("expected success message, got %q", result)
	}
	if !strings.Contains(result, "user@example.com") {
		t.Errorf("expected recipient in success message, got %q", result)
	}
}

func TestMailSend_InvalidFromConfig(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "not-valid",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	_, err := tool.Handler(context.Background(), map[string]any{
		"to":      "user@example.com",
		"subject": "Test",
		"body":    "Hello",
	})
	if err == nil {
		t.Fatal("expected error for invalid from_address config")
	}
	if !strings.Contains(err.Error(), "from_address") {
		t.Errorf("expected 'from_address' in error, got %q", err.Error())
	}
}

func TestMailSend_InvalidCcAddress(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	_, err := tool.Handler(context.Background(), map[string]any{
		"to":      "user@example.com",
		"subject": "Test",
		"body":    "Hello",
		"cc":      "invalid-email",
	})
	if err == nil {
		t.Fatal("expected error for invalid cc address")
	}
}

func TestMailSend_InvalidReplyTo(t *testing.T) {
	t.Parallel()

	cfg := MailSendConfig{
		SMTPHost:    "mail.example.com",
		SMTPPort:    587,
		FromAddress: "sender@example.com",
		RequireTLS:  true,
	}
	sender := &mockSMTPSender{}
	tool := NewMailSendTool(cfg, sender)

	_, err := tool.Handler(context.Background(), map[string]any{
		"to":       "user@example.com",
		"subject":  "Test",
		"body":     "Hello",
		"reply_to": "invalid-email",
	})
	if err == nil {
		t.Fatal("expected error for invalid reply_to address")
	}
}

func TestSanitizeHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean", "Hello World", "Hello World"},
		{"cr", "Hello\rWorld", "HelloWorld"},
		{"lf", "Hello\nWorld", "HelloWorld"},
		{"crlf", "Hello\r\nWorld", "HelloWorld"},
		{"multiple", "A\r\nB\r\nC", "ABC"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeHeader(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeHeader(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateEmailAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    string
		want    string
		wantErr bool
	}{
		{"simple", "user@example.com", "user@example.com", false},
		{"with_name", "User <user@example.com>", "user@example.com", false},
		{"quoted_name", `"User Name" <user@example.com>`, "user@example.com", false},
		{"invalid_no_at", "not-an-email", "", true},
		{"invalid_empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateEmailAddress(tt.addr)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", tt.addr)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tt.addr, err)
				return
			}
			if got != tt.want {
				t.Errorf("validateEmailAddress(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}
