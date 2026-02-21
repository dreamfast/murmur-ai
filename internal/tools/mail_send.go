package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// MailSendConfig holds the SMTP configuration for the mail_send tool.
// This is a tool-level config separate from the TOML config struct.
type MailSendConfig struct {
	SMTPHost    string
	SMTPPort    int
	SMTPUser    string
	SMTPPass    string
	FromAddress string
	RequireTLS  bool
}

// smtpSender abstracts SMTP operations for testing.
type smtpSender interface {
	SendMail(addr, from string, to []string, msg []byte) error
}

// realSMTPSender implements smtpSender using net/smtp with STARTTLS.
type realSMTPSender struct {
	user       string
	pass       string
	requireTLS bool
}

// SendMail connects to the SMTP server, negotiates STARTTLS if required,
// authenticates, and sends the message.
func (s *realSMTPSender) SendMail(addr, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("SendMail: invalid address %q: %w", addr, err)
	}

	// Connect to the SMTP server.
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SendMail: dial: %w", err)
	}
	defer c.Close()

	// Say hello with the local hostname.
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}
	if err := c.Hello(hostname); err != nil {
		return fmt.Errorf("SendMail: hello: %w", err)
	}

	// Negotiate STARTTLS.
	if ok, _ := c.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("SendMail: STARTTLS: %w", err)
		}
	} else if s.requireTLS {
		return fmt.Errorf("SendMail: server does not support STARTTLS but require_tls is enabled")
	}

	// Authenticate if credentials are provided.
	if s.user != "" && s.pass != "" {
		auth := smtp.PlainAuth("", s.user, s.pass, host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SendMail: auth: %w", err)
		}
	}

	// Set sender.
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("SendMail: mail from: %w", err)
	}

	// Set recipients.
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("SendMail: rcpt to %q: %w", rcpt, err)
		}
	}

	// Write message body.
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SendMail: data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SendMail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SendMail: close data: %w", err)
	}

	return c.Quit()
}

// headerSanitizer strips CRLF characters to prevent header injection.
var headerSanitizer = strings.NewReplacer("\r", "", "\n", "")

// sanitizeHeader removes CR and LF characters from a header value to prevent
// CRLF injection attacks.
func sanitizeHeader(s string) string {
	return headerSanitizer.Replace(s)
}

// validateEmailAddress validates an email address using net/mail.ParseAddress.
// Returns the cleaned address (without display name).
func validateEmailAddress(addr string) (string, error) {
	parsed, err := mail.ParseAddress(addr)
	if err != nil {
		return "", fmt.Errorf("invalid email address %q: %w", addr, err)
	}
	return parsed.Address, nil
}

// NewMailSendTool creates the mail_send tool for sending emails via SMTP.
// Security: CRLF injection prevention, email validation, STARTTLS required
// by default, plain text only.
func NewMailSendTool(cfg MailSendConfig, sender smtpSender) Tool {
	if sender == nil {
		sender = &realSMTPSender{
			user:       cfg.SMTPUser,
			pass:       cfg.SMTPPass,
			requireTLS: cfg.RequireTLS,
		}
	}

	return Tool{
		Name:        "mail_send",
		Description: "Send an email via SMTP. Plain text only. Supports to, cc, reply_to, subject, and body fields.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"to": {
					"type": "string",
					"description": "Recipient email address (required)"
				},
				"subject": {
					"type": "string",
					"description": "Email subject line (required)"
				},
				"body": {
					"type": "string",
					"description": "Plain text email body (required)"
				},
				"cc": {
					"type": "string",
					"description": "CC recipient email address (optional)"
				},
				"reply_to": {
					"type": "string",
					"description": "Reply-To email address (optional)"
				}
			},
			"required": ["to", "subject", "body"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleMailSend(ctx, args, cfg, sender)
		},
	}
}

// handleMailSend validates inputs, builds the RFC 2822 message, and sends it.
func handleMailSend(_ context.Context, args map[string]any, cfg MailSendConfig, sender smtpSender) (string, error) {
	// Extract required arguments.
	to, err := RequireStringArg(args, "to")
	if err != nil {
		return "", err
	}
	subject, err := RequireStringArg(args, "subject")
	if err != nil {
		return "", err
	}
	body, err := RequireStringArg(args, "body")
	if err != nil {
		return "", err
	}

	// Extract optional arguments.
	cc := OptionalStringArg(args, "cc", "")
	replyTo := OptionalStringArg(args, "reply_to", "")

	// Validate and sanitize the To address.
	toAddr, err := validateEmailAddress(to)
	if err != nil {
		return "", fmt.Errorf("handleMailSend: to: %w", err)
	}

	// Validate From address.
	fromAddr, err := validateEmailAddress(cfg.FromAddress)
	if err != nil {
		return "", fmt.Errorf("handleMailSend: from_address config: %w", err)
	}

	// Collect all recipients for the SMTP envelope.
	recipients := []string{toAddr}

	// Sanitize header fields to prevent CRLF injection.
	safeSubject := sanitizeHeader(subject)
	safeTo := sanitizeHeader(toAddr)
	safeFrom := sanitizeHeader(fromAddr)

	// Build the RFC 2822 message.
	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\n", safeFrom)
	fmt.Fprintf(&msg, "To: %s\r\n", safeTo)

	if cc != "" {
		ccAddr, err := validateEmailAddress(cc)
		if err != nil {
			return "", fmt.Errorf("handleMailSend: cc: %w", err)
		}
		safeCc := sanitizeHeader(ccAddr)
		fmt.Fprintf(&msg, "Cc: %s\r\n", safeCc)
		recipients = append(recipients, ccAddr)
	}

	if replyTo != "" {
		replyAddr, err := validateEmailAddress(replyTo)
		if err != nil {
			return "", fmt.Errorf("handleMailSend: reply_to: %w", err)
		}
		safeReplyTo := sanitizeHeader(replyAddr)
		fmt.Fprintf(&msg, "Reply-To: %s\r\n", safeReplyTo)
	}

	fmt.Fprintf(&msg, "Subject: %s\r\n", safeSubject)
	fmt.Fprintf(&msg, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	// Send the email.
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	if err := sender.SendMail(addr, fromAddr, recipients, []byte(msg.String())); err != nil {
		return "", fmt.Errorf("handleMailSend: %w", err)
	}

	result := fmt.Sprintf("Email sent successfully to %s", toAddr)
	if cc != "" {
		result += fmt.Sprintf(" (cc: %s)", cc)
	}
	return result, nil
}
