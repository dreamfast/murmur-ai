package irc

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"murmur/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewConnection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     config.IRCConfig
		wantErr bool
	}{
		{
			name: "missing server returns error",
			cfg: config.IRCConfig{
				Nick: "bot",
				Port: 6667,
			},
			wantErr: true,
		},
		{
			name: "missing nick returns error",
			cfg: config.IRCConfig{
				Server: "irc.example.com",
				Port:   6667,
			},
			wantErr: true,
		},
		{
			name: "oper user with spaces returns error",
			cfg: config.IRCConfig{
				Server:   "irc.example.com",
				Port:     6667,
				Nick:     "bot",
				OperUser: "bad user",
			},
			wantErr: true,
		},
		{
			name: "oper user with CR returns error",
			cfg: config.IRCConfig{
				Server:   "irc.example.com",
				Port:     6667,
				Nick:     "bot",
				OperUser: "bad\ruser",
			},
			wantErr: true,
		},
		{
			name: "oper password with LF returns error",
			cfg: config.IRCConfig{
				Server:       "irc.example.com",
				Port:         6667,
				Nick:         "bot",
				OperPassword: "bad\npass",
			},
			wantErr: true,
		},
		{
			name: "valid config returns non-nil Connection",
			cfg: config.IRCConfig{
				Server: "irc.example.com",
				Port:   6667,
				Nick:   "bot",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn, err := NewConnection(tt.cfg, []string{"#test"}, discardLogger())
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewConnection() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewConnection() unexpected error: %v", err)
			}
			if conn == nil {
				t.Fatal("NewConnection() returned nil Connection without error")
			}
		})
	}
}

func TestNewConnection_DefaultsUserAndRealname(t *testing.T) {
	t.Parallel()

	cfg := config.IRCConfig{
		Server: "irc.example.com",
		Port:   6667,
		Nick:   "mybot",
	}

	conn, err := NewConnection(cfg, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewConnection() unexpected error: %v", err)
	}

	// The girc client config should have User and Name set to Nick.
	gircCfg := conn.client.Config
	if gircCfg.User != "mybot" {
		t.Errorf("User = %q, want %q", gircCfg.User, "mybot")
	}
	if gircCfg.Name != "mybot" {
		t.Errorf("Name = %q, want %q", gircCfg.Name, "mybot")
	}
}

func TestNewConnection_TLSMinVersion(t *testing.T) {
	t.Parallel()

	cfg := config.IRCConfig{
		Server: "irc.example.com",
		Port:   6697,
		Nick:   "bot",
		TLS:    true,
	}

	conn, err := NewConnection(cfg, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewConnection() unexpected error: %v", err)
	}

	tlsCfg := conn.client.Config.TLSConfig
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLSConfig when TLS=true")
	}
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLSConfig.MinVersion = %d, want %d (TLS 1.2)", tlsCfg.MinVersion, tls.VersionTLS12)
	}
}

func TestConnection_IsChannel(t *testing.T) {
	t.Parallel()

	cfg := config.IRCConfig{
		Server: "irc.example.com",
		Port:   6667,
		Nick:   "bot",
	}
	conn, err := NewConnection(cfg, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewConnection() unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "#channel returns true", target: "#channel", want: true},
		{name: "user returns false", target: "user", want: false},
		{name: "empty string returns false", target: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := conn.IsChannel(tt.target)
			if got != tt.want {
				t.Errorf("IsChannel(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestNick(t *testing.T) {
	t.Parallel()

	cfg := config.IRCConfig{
		Server: "irc.example.com",
		Port:   6667,
		Nick:   "testbot",
	}
	conn, err := NewConnection(cfg, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewConnection() unexpected error: %v", err)
	}

	if got := conn.Nick(); got != "testbot" {
		t.Errorf("Nick() = %q, want %q", got, "testbot")
	}
}

func TestIsClosedConnError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "net.ErrClosed returns true",
			err:  net.ErrClosed,
			want: true,
		},
		{
			name: "wrapped net.OpError with ErrClosed returns true",
			err: &net.OpError{
				Op:  "read",
				Net: "tcp",
				Err: net.ErrClosed,
			},
			want: true,
		},
		{
			name: "other error returns false",
			err:  errors.New("some other error"),
			want: false,
		},
		{
			name: "nil error returns false",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := isClosedConnError(tt.err)
			if got != tt.want {
				t.Errorf("isClosedConnError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
