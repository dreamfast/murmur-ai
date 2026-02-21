package irc

import (
	"sync"
	"testing"

	"murmur/internal/config"
)

func TestMessageHandler_DMSwap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		botNick     string
		channel     string
		senderNick  string
		wantChannel string
	}{
		{
			name:        "channel message unchanged",
			botNick:     "murmur",
			channel:     "#general",
			senderNick:  "alice",
			wantChannel: "#general",
		},
		{
			name:        "DM swaps channel to sender nick",
			botNick:     "murmur",
			channel:     "murmur",
			senderNick:  "alice",
			wantChannel: "alice",
		},
		{
			name:        "DM case insensitive bot nick",
			botNick:     "Murmur",
			channel:     "murmur",
			senderNick:  "Bob",
			wantChannel: "Bob",
		},
		{
			name:        "DM case insensitive channel",
			botNick:     "murmur",
			channel:     "Murmur",
			senderNick:  "carol",
			wantChannel: "carol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotChannel, gotNick, gotMessage string
			var mu sync.Mutex

			conn := &Connection{cfg: config.IRCConfig{Nick: tt.botNick}}
			h := &MessageHandler{
				conn:       conn,
				busChannel: "#murmur-bus",
				userHandler: func(channel, nick, message string) {
					mu.Lock()
					defer mu.Unlock()
					gotChannel = channel
					gotNick = nick
					gotMessage = message
				},
			}

			h.route(tt.channel, tt.senderNick, "hello")

			mu.Lock()
			defer mu.Unlock()

			if gotChannel != tt.wantChannel {
				t.Errorf("channel = %q, want %q", gotChannel, tt.wantChannel)
			}
			if gotNick != tt.senderNick {
				t.Errorf("nick = %q, want %q", gotNick, tt.senderNick)
			}
			if gotMessage != "hello" {
				t.Errorf("message = %q, want %q", gotMessage, "hello")
			}
		})
	}
}

func TestMessageHandler_BusNotSwapped(t *testing.T) {
	t.Parallel()

	var gotNick, gotMessage string
	conn := &Connection{cfg: config.IRCConfig{Nick: "murmur"}}
	h := &MessageHandler{
		conn:       conn,
		busChannel: "#murmur-bus",
		busHandler: func(nick, raw string) {
			gotNick = nick
			gotMessage = raw
		},
	}

	h.route("#murmur-bus", "client1", `{"type":"heartbeat"}`)

	if gotNick != "client1" {
		t.Errorf("bus handler nick = %q, want %q", gotNick, "client1")
	}
	if gotMessage != `{"type":"heartbeat"}` {
		t.Errorf("bus handler message = %q", gotMessage)
	}
}

func TestMessageHandler_BusCaseInsensitive(t *testing.T) {
	t.Parallel()

	var gotNick, gotMessage string
	conn := &Connection{cfg: config.IRCConfig{Nick: "murmur"}}
	h := &MessageHandler{
		conn:       conn,
		busChannel: "#murmur-bus",
		busHandler: func(nick, raw string) {
			gotNick = nick
			gotMessage = raw
		},
		userHandler: func(_, _, _ string) {
			t.Error("user handler should not be called for bus messages")
		},
	}

	// Send with different case — should still route to bus handler.
	h.route("#MURMUR-BUS", "client1", `{"type":"heartbeat"}`)

	if gotNick != "client1" {
		t.Errorf("bus handler nick = %q, want %q", gotNick, "client1")
	}
	if gotMessage != `{"type":"heartbeat"}` {
		t.Errorf("bus handler message = %q", gotMessage)
	}
}

func TestMessageHandler_SelfMessageIgnored(t *testing.T) {
	t.Parallel()

	called := false
	conn := &Connection{cfg: config.IRCConfig{Nick: "murmur"}}
	h := &MessageHandler{
		conn:       conn,
		busChannel: "#murmur-bus",
		userHandler: func(_, _, _ string) {
			called = true
		},
	}

	h.route("#general", "murmur", "echo")

	if called {
		t.Error("expected self-message to be ignored")
	}
}

func TestIsChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		target string
		want   bool
	}{
		{"#general", true},
		{"#murmur-bus", true},
		{"alice", false},
		{"", false},
		{"#", true},
	}

	conn := &Connection{}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			t.Parallel()
			if got := conn.IsChannel(tt.target); got != tt.want {
				t.Errorf("IsChannel(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}
