// Package irc provides a thin wrapper around the girc IRC client library,
// adding message routing, NickServ authentication, and IRC-safe formatting.
package irc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/lrstanley/girc"

	"murmur/internal/config"
)

// IRC numeric reply constants used for tracking operator status.
const (
	rplYoureOper      = "381" // RPL_YOUREOPER — OPER command succeeded
	errPasswdMismatch = "464" // ERR_PASSWDMISMATCH — bad OPER password
	errNoOperHost     = "491" // ERR_NOOPERHOST — OPER not allowed from this host
)

// Connection wraps a girc IRC client with convenience methods for
// sending messages, registering callbacks, and managing the connection lifecycle.
type Connection struct {
	client   *girc.Client
	cfg      config.IRCConfig
	channels []string
	logger   *slog.Logger

	mu        sync.Mutex
	onConnect []func()
	onOper    []func()
	onMessage []func(channel, nick, message string)

	// joinedMu protects joinedChannels for concurrent access.
	joinedMu sync.RWMutex
	// joinedChannels tracks which channels the bot is currently in.
	// Updated by girc JOIN/PART/KICK handlers for accurate state.
	joinedChannels map[string]struct{}

	// isOper tracks whether the bot has IRC operator status.
	operMu sync.RWMutex
	isOper bool
}

// NewConnection creates a new IRC connection from the given config and channel list.
// It does not connect immediately — call Connect to establish the connection.
func NewConnection(cfg config.IRCConfig, channels []string, logger *slog.Logger) (*Connection, error) {
	if cfg.Server == "" {
		return nil, fmt.Errorf("NewConnection: irc server is required")
	}
	if cfg.Nick == "" {
		return nil, fmt.Errorf("NewConnection: irc nick is required")
	}

	// Validate OPER credentials: reject control characters and spaces that
	// could allow IRC command injection via the raw OPER command.
	if cfg.OperUser != "" && strings.ContainsAny(cfg.OperUser, "\r\n ") {
		return nil, fmt.Errorf("NewConnection: oper_user must not contain spaces, CR, or LF")
	}
	if cfg.OperPassword != "" && strings.ContainsAny(cfg.OperPassword, "\r\n") {
		return nil, fmt.Errorf("NewConnection: oper_password must not contain CR or LF")
	}

	gircCfg := girc.Config{
		Server:     cfg.Server,
		Port:       cfg.Port,
		Nick:       cfg.Nick,
		User:       cfg.User,
		Name:       cfg.Realname,
		AllowFlood: true, // disable girc's client-side rate limiter (private bot network)
	}

	if cfg.Password != "" {
		gircCfg.ServerPass = cfg.Password
	}

	if cfg.TLS {
		gircCfg.SSL = true
		gircCfg.TLSConfig = &tls.Config{
			ServerName: cfg.Server,
			MinVersion: tls.VersionTLS12,
		}
	}

	// Set defaults for user/realname if not provided.
	if gircCfg.User == "" {
		gircCfg.User = cfg.Nick
	}
	if gircCfg.Name == "" {
		gircCfg.Name = cfg.Nick
	}

	gircCfg.RecoverFunc = func(_ *girc.Client, e *girc.HandlerError) {
		logger.Error("irc handler panic recovered", "error", e.Error())
	}

	client := girc.New(gircCfg)

	conn := &Connection{
		client:         client,
		cfg:            cfg,
		channels:       channels,
		logger:         logger,
		joinedChannels: make(map[string]struct{}),
	}

	// Join channels on connect.
	client.Handlers.Add(girc.CONNECTED, func(c *girc.Client, _ girc.Event) {
		// Reset state on reconnect — we're starting fresh.
		conn.joinedMu.Lock()
		conn.joinedChannels = make(map[string]struct{})
		conn.joinedMu.Unlock()

		conn.operMu.Lock()
		conn.isOper = false
		conn.operMu.Unlock()

		for _, ch := range conn.channels {
			c.Cmd.Join(ch)
			logger.Info("joined channel", "channel", ch)
		}

		// NickServ identification.
		if cfg.NickServPassword != "" {
			c.Cmd.Message("NickServ", "IDENTIFY "+cfg.NickServPassword)
			logger.Info("sent NickServ IDENTIFY")
		}

		// IRC operator authentication.
		if cfg.OperUser != "" && cfg.OperPassword != "" {
			if err := c.Cmd.SendRaw("OPER " + cfg.OperUser + " " + cfg.OperPassword); err != nil {
				logger.Warn("failed to send OPER command", "error", err)
			} else {
				logger.Info("sent OPER command", "user", cfg.OperUser)
			}
		}

		// Fire registered on-connect callbacks.
		conn.mu.Lock()
		callbacks := make([]func(), len(conn.onConnect))
		copy(callbacks, conn.onConnect)
		conn.mu.Unlock()

		for _, cb := range callbacks {
			cb()
		}
	})

	// Track channel membership via JOIN/PART/KICK events.
	// Nick comparisons use EqualFold because IRC nicks are case-insensitive.
	client.Handlers.Add(girc.JOIN, func(c *girc.Client, e girc.Event) {
		if e.Source == nil || !strings.EqualFold(e.Source.Name, c.GetNick()) {
			return // not us
		}
		if len(e.Params) == 0 {
			return
		}
		ch := e.Params[0]
		conn.joinedMu.Lock()
		conn.joinedChannels[ch] = struct{}{}
		conn.joinedMu.Unlock()
		logger.Debug("tracked join", "channel", ch)
	})

	client.Handlers.Add(girc.PART, func(c *girc.Client, e girc.Event) {
		if e.Source == nil || !strings.EqualFold(e.Source.Name, c.GetNick()) {
			return
		}
		if len(e.Params) == 0 {
			return
		}
		ch := e.Params[0]
		conn.joinedMu.Lock()
		delete(conn.joinedChannels, ch)
		conn.joinedMu.Unlock()
		logger.Debug("tracked part", "channel", ch)
	})

	client.Handlers.Add(girc.KICK, func(c *girc.Client, e girc.Event) {
		// KICK params: [channel, kicked_nick, reason]
		if len(e.Params) < 2 || !strings.EqualFold(e.Params[1], c.GetNick()) {
			return
		}
		ch := e.Params[0]
		conn.joinedMu.Lock()
		delete(conn.joinedChannels, ch)
		conn.joinedMu.Unlock()
		logger.Warn("kicked from channel", "channel", ch)
	})

	// Track IRC operator status via RPL_YOUREOPER (381).
	client.Handlers.Add(rplYoureOper, func(_ *girc.Client, _ girc.Event) {
		conn.operMu.Lock()
		conn.isOper = true
		conn.operMu.Unlock()
		logger.Info("IRC operator status granted")

		// Fire registered on-oper callbacks.
		conn.mu.Lock()
		operCallbacks := make([]func(), len(conn.onOper))
		copy(operCallbacks, conn.onOper)
		conn.mu.Unlock()

		for _, cb := range operCallbacks {
			cb()
		}
	})

	// Log OPER failures so authentication issues are diagnosable.
	client.Handlers.Add(errPasswdMismatch, func(_ *girc.Client, _ girc.Event) {
		logger.Error("OPER authentication failed: password mismatch")
	})
	client.Handlers.Add(errNoOperHost, func(_ *girc.Client, _ girc.Event) {
		logger.Error("OPER authentication failed: no oper host match")
	})

	// Route incoming PRIVMSG to registered handlers.
	client.Handlers.Add(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
		if len(e.Params) == 0 || e.Source == nil {
			return
		}
		channel := e.Params[0]
		nick := e.Source.Name
		message := e.Last()

		conn.mu.Lock()
		handlers := make([]func(string, string, string), len(conn.onMessage))
		copy(handlers, conn.onMessage)
		conn.mu.Unlock()

		for _, h := range handlers {
			h(channel, nick, message)
		}
	})

	return conn, nil
}

// Connect establishes the IRC connection and blocks until the context is
// cancelled or a fatal error occurs. It uses girc's built-in reconnection
// with exponential backoff.
func (c *Connection) Connect(ctx context.Context) error {
	c.logger.Info("connecting to IRC", "server", c.cfg.Server, "port", c.cfg.Port, "tls", c.cfg.TLS)

	// Run in a goroutine so we can select on context cancellation.
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.client.Connect()
	}()

	select {
	case <-ctx.Done():
		c.client.Close()
		return ctx.Err()
	case err := <-errCh:
		if err != nil && !isClosedConnError(err) {
			return fmt.Errorf("Connect: %w", err)
		}
		return nil
	}
}

// Send sends a message to the given channel, splitting long messages at word
// boundaries to respect IRC line length limits. This is for user-facing
// messages only — do NOT use for bus protocol messages.
func (c *Connection) Send(channel, message string) {
	lines := SplitMessage(message, MaxMessageLen)
	for _, line := range lines {
		c.client.Cmd.Message(channel, line)
	}
}

// SendRaw sends a single message to the given channel without any splitting.
// This is intended for bus protocol messages that must be delivered as a single
// IRC PRIVMSG. It bypasses girc's built-in event.split() logic (which would
// silently split messages exceeding MaxEventLength into multiple IRC lines,
// corrupting JSON payloads). The caller is responsible for ensuring the message
// fits within the IRC server's max-line-len.
func (c *Connection) SendRaw(channel, message string) {
	raw := "PRIVMSG " + channel + " :" + message
	if err := c.client.Cmd.SendRawNoSplit(raw); err != nil {
		c.logger.Warn("SendRaw failed", "channel", channel, "error", err, "len", len(raw))
	}
}

// OnConnect registers a callback that fires each time the IRC connection is
// established (including reconnections). This is useful for re-registering
// with the bus after a reconnect.
func (c *Connection) OnConnect(handler func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onConnect = append(c.onConnect, handler)
}

// OnOper registers a callback that fires each time the bot receives IRC
// operator status (RPL_YOUREOPER 381). This is useful for performing
// actions that require operator privileges, such as setting channel topics.
func (c *Connection) OnOper(handler func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onOper = append(c.onOper, handler)
}

// OnMessage registers a callback for incoming PRIVMSG events.
// The callback receives the channel name, sender nick, and message text.
func (c *Connection) OnMessage(handler func(channel, nick, message string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMessage = append(c.onMessage, handler)
}

// Close gracefully disconnects from the IRC server.
func (c *Connection) Close() {
	c.logger.Info("disconnecting from IRC")
	c.client.Close()
}

// Nick returns the configured IRC nickname.
func (c *Connection) Nick() string {
	return c.cfg.Nick
}

// IsChannel returns true if the given target looks like an IRC channel name
// (starts with '#'). This is used to distinguish channel messages from private
// messages (DMs), where the target is a user nick.
func (c *Connection) IsChannel(target string) bool {
	return len(target) > 0 && target[0] == '#'
}

// Channels returns a sorted list of channels the bot is currently joined to.
func (c *Connection) Channels() []string {
	c.joinedMu.RLock()
	defer c.joinedMu.RUnlock()

	result := make([]string, 0, len(c.joinedChannels))
	for ch := range c.joinedChannels {
		result = append(result, ch)
	}
	sort.Strings(result)
	return result
}

// IsConnected returns true if the underlying IRC client is connected to the server.
func (c *Connection) IsConnected() bool {
	return c.client.IsConnected()
}

// IsOper returns true if the bot currently has IRC operator status.
func (c *Connection) IsOper() bool {
	c.operMu.RLock()
	defer c.operMu.RUnlock()
	return c.isOper
}

// isClosedConnError checks whether an error indicates a closed network
// connection, which is expected during graceful shutdown.
func isClosedConnError(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	// Some Go versions and OS combinations wrap the error differently.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, net.ErrClosed)
	}
	return false
}
