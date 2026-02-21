package dashboard

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

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
