package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lrstanley/girc"
)

func TestNewBridgeSASLConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		password string
		wantSASL bool
	}{
		{
			name:     "password set enables SASL",
			password: "secret123",
			wantSASL: true,
		},
		{
			name:     "empty password disables SASL",
			password: "",
			wantSASL: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			cfg := BridgeConfig{
				Nick:      "testuser",
				Password:  tt.password,
				IRCServer: "localhost",
				IRCPort:   6667,
				IRCTLS:    true,
				Channels:  []string{"#test"},
			}

			b, err := NewBridge(context.Background(), nil, cfg, logger)
			if err != nil {
				t.Fatalf("NewBridge() error: %v", err)
			}

			// Inspect the girc client config via the bridge's irc field.
			hasSASL := b.irc.Config.SASL != nil
			if hasSASL != tt.wantSASL {
				t.Errorf("SASL configured = %v, want %v", hasSASL, tt.wantSASL)
			}

			if tt.wantSASL {
				sasl, ok := b.irc.Config.SASL.(*girc.SASLPlain)
				if !ok {
					t.Fatal("SASL is not *girc.SASLPlain")
				}
				if sasl.User != cfg.Nick {
					t.Errorf("SASL User = %q, want %q", sasl.User, cfg.Nick)
				}
				if sasl.Pass != tt.password {
					t.Errorf("SASL Pass = %q, want %q", sasl.Pass, tt.password)
				}
			}
		})
	}
}

func TestWSMessageTimestampJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		msg           WSMessage
		wantTimestamp bool
	}{
		{
			name: "timestamp present in JSON",
			msg: WSMessage{
				Type:      "message",
				Channel:   "#test",
				Nick:      "user1",
				Text:      "hello",
				Timestamp: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).UnixMilli(),
			},
			wantTimestamp: true,
		},
		{
			name: "zero timestamp omitted from JSON",
			msg: WSMessage{
				Type:    "message",
				Channel: "#test",
				Nick:    "user1",
				Text:    "hello",
			},
			wantTimestamp: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("Marshal error: %v", err)
			}

			var raw map[string]interface{}
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}

			_, hasTS := raw["timestamp"]
			if hasTS != tt.wantTimestamp {
				t.Errorf("timestamp in JSON = %v, want %v (json: %s)", hasTS, tt.wantTimestamp, data)
			}

			if tt.wantTimestamp {
				// Verify round-trip: unmarshal back to WSMessage.
				var decoded WSMessage
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatalf("round-trip Unmarshal error: %v", err)
				}
				if decoded.Timestamp != tt.msg.Timestamp {
					t.Errorf("round-trip Timestamp = %d, want %d", decoded.Timestamp, tt.msg.Timestamp)
				}
			}
		})
	}
}

func TestWSMessageNewTypes(t *testing.T) {
	t.Parallel()

	now := time.Now().UnixMilli()

	tests := []struct {
		name string
		msg  WSMessage
		// Fields expected to be present in the JSON output.
		wantFields []string
	}{
		{
			name: "quit message",
			msg: WSMessage{
				Type:      "quit",
				Nick:      "departing_user",
				Text:      "Goodbye!",
				Timestamp: now,
			},
			wantFields: []string{"type", "nick", "text", "timestamp"},
		},
		{
			name: "kick message",
			msg: WSMessage{
				Type:      "kick",
				Channel:   "#test",
				Nick:      "kicked_user",
				Text:      "misbehaving",
				Mode:      "op_user",
				Timestamp: now,
			},
			wantFields: []string{"type", "channel", "nick", "text", "mode", "timestamp"},
		},
		{
			name: "nick change message",
			msg: WSMessage{
				Type:      "nick",
				Nick:      "old_nick",
				Text:      "new_nick",
				Timestamp: now,
			},
			wantFields: []string{"type", "nick", "text", "timestamp"},
		},
		{
			name: "notice message",
			msg: WSMessage{
				Type:      "notice",
				Channel:   "#test",
				Nick:      "NickServ",
				Text:      "You are now identified",
				Timestamp: now,
			},
			wantFields: []string{"type", "channel", "nick", "text", "timestamp"},
		},
		{
			name: "RPL_TOPIC (no nick)",
			msg: WSMessage{
				Type:    "topic",
				Channel: "#test",
				Topic:   "Welcome to #test",
			},
			wantFields: []string{"type", "channel", "topic"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("Marshal error: %v", err)
			}

			var raw map[string]interface{}
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}

			for _, field := range tt.wantFields {
				if _, ok := raw[field]; !ok {
					t.Errorf("missing field %q in JSON: %s", field, data)
				}
			}

			// Verify round-trip.
			var decoded WSMessage
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("round-trip Unmarshal error: %v", err)
			}
			if decoded.Type != tt.msg.Type {
				t.Errorf("Type = %q, want %q", decoded.Type, tt.msg.Type)
			}
			if decoded.Nick != tt.msg.Nick {
				t.Errorf("Nick = %q, want %q", decoded.Nick, tt.msg.Nick)
			}
		})
	}
}

func TestEventTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timestamp time.Time
		wantPos   bool
	}{
		{
			name:      "normal timestamp",
			timestamp: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			wantPos:   true,
		},
		{
			name:      "zero timestamp gets current time",
			timestamp: time.Time{},
			wantPos:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := girc.Event{Timestamp: tt.timestamp}
			ts := eventTimestamp(e)

			if tt.wantPos && ts <= 0 {
				t.Errorf("eventTimestamp() = %d, want > 0", ts)
			}

			if tt.name == "normal timestamp" {
				want := tt.timestamp.UnixMilli()
				if ts != want {
					t.Errorf("eventTimestamp() = %d, want %d", ts, want)
				}
			}

			if tt.name == "zero timestamp gets current time" {
				// Should be close to now (within 1 second).
				now := time.Now().UnixMilli()
				if ts < now-1000 || ts > now+1000 {
					t.Errorf("eventTimestamp() = %d, want near %d", ts, now)
				}
			}
		})
	}
}

func TestNewBridgeSASLTLSWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tls         bool
		password    string
		wantWarning bool
	}{
		{
			name:        "SASL without TLS warns",
			tls:         false,
			password:    "secret",
			wantWarning: true,
		},
		{
			name:        "SASL with TLS no warning",
			tls:         true,
			password:    "secret",
			wantWarning: false,
		},
		{
			name:        "no password no warning",
			tls:         false,
			password:    "",
			wantWarning: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

			cfg := BridgeConfig{
				Nick:      "testuser",
				Password:  tt.password,
				IRCServer: "localhost",
				IRCPort:   6667,
				IRCTLS:    tt.tls,
				Channels:  []string{"#test"},
			}

			_, err := NewBridge(context.Background(), nil, cfg, logger)
			if err != nil {
				t.Fatalf("NewBridge() error: %v", err)
			}

			hasWarning := strings.Contains(buf.String(), "SASL PLAIN enabled without TLS")
			if hasWarning != tt.wantWarning {
				t.Errorf("TLS warning present = %v, want %v (log: %q)", hasWarning, tt.wantWarning, buf.String())
			}
		})
	}
}
