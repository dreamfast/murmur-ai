// Package irc provides a thin wrapper around the girc IRC client library,
// adding message routing, NickServ authentication, and IRC-safe formatting.

package irc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// IRCLogHandler is a custom slog.Handler that forwards log records to an IRC
// channel. It uses lazy binding — created without a connection, then bound
// later via SetConnection. Until bound, Enabled returns false and all records
// are silently dropped.
//
// Messages are buffered in a channel with drop-newest semantics to avoid
// blocking the caller. A background goroutine drains the buffer periodically,
// batching up to maxBatchSize lines per send to respect IRC flood limits.
type IRCLogHandler struct {
	channel string
	level   atomic.Int64 // slog.Level stored as int64 for lock-free access
	enabled atomic.Bool  // true when connection is set and handler is active

	// userDisabled tracks whether the user explicitly disabled the handler
	// via SetEnabled(false). This prevents SetConnection from re-enabling
	// the handler after a reconnect when the user turned it off.
	userDisabled atomic.Bool

	conn *Connection
	mu   sync.Mutex // protects conn

	msgCh  chan string
	stopCh chan struct{}
	wg     sync.WaitGroup
}

const (
	// ircLogBufSize is the capacity of the message buffer channel.
	ircLogBufSize = 100
	// ircLogDrainInterval is how often the background goroutine drains the buffer.
	ircLogDrainInterval = 500 * time.Millisecond
	// ircLogMaxBatch is the maximum number of lines sent per drain cycle.
	ircLogMaxBatch = 5
)

// NewIRCLogHandler creates a new IRC log handler for the given channel.
// The handler starts disabled and must be activated via SetConnection.
func NewIRCLogHandler(channel string, level slog.Level) *IRCLogHandler {
	h := &IRCLogHandler{
		channel: channel,
		msgCh:   make(chan string, ircLogBufSize),
		stopCh:  make(chan struct{}),
	}
	h.level.Store(int64(level))
	h.enabled.Store(false)

	// Start the background drain goroutine.
	h.wg.Add(1)
	go h.drainLoop()

	return h
}

// SetConnection binds the handler to an IRC connection. If the user has not
// explicitly disabled the handler (via SetEnabled(false)), it is automatically
// enabled. This is safe across reconnects — a user's "!debug off" is preserved.
func (h *IRCLogHandler) SetConnection(conn *Connection) {
	h.mu.Lock()
	h.conn = conn
	h.mu.Unlock()
	if !h.userDisabled.Load() {
		h.enabled.Store(true)
	}
}

// SetEnabled toggles the handler on or off. When disabled, Enabled returns
// false and all records are silently dropped. The user's preference is
// tracked so that SetConnection (called on reconnect) does not override it.
func (h *IRCLogHandler) SetEnabled(on bool) {
	h.userDisabled.Store(!on)
	h.enabled.Store(on)
}

// IsEnabled returns whether the handler is currently active.
func (h *IRCLogHandler) IsEnabled() bool {
	return h.enabled.Load()
}

// SetLevel changes the minimum log level for the handler.
func (h *IRCLogHandler) SetLevel(level slog.Level) {
	h.level.Store(int64(level))
}

// Level returns the current minimum log level.
func (h *IRCLogHandler) Level() slog.Level {
	return slog.Level(h.level.Load())
}

// Close shuts down the background goroutine and drains remaining messages.
func (h *IRCLogHandler) Close() {
	close(h.stopCh)
	h.wg.Wait()
}

// Enabled implements slog.Handler. Returns false until SetConnection is called
// and the handler is enabled.
func (h *IRCLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.enabled.Load() && level >= slog.Level(h.level.Load())
}

// Handle implements slog.Handler. Formats the record and enqueues it for
// delivery to IRC. Uses drop-newest semantics — if the buffer is full, the
// message is silently dropped to avoid blocking the caller.
func (h *IRCLogHandler) Handle(_ context.Context, r slog.Record) error {
	if !h.enabled.Load() {
		return nil
	}

	// Format: [LEVEL] message key=value key=value ...
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(r.Level.String())
	b.WriteString("] ")
	b.WriteString(r.Message)

	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(fmt.Sprintf("%v", a.Value.Any()))
		return true
	})

	msg := b.String()

	// Drop-newest: if the buffer is full, silently discard.
	select {
	case h.msgCh <- msg:
	default:
	}

	return nil
}

// WithAttrs implements slog.Handler. Returns the handler unchanged since
// IRC log output is flat (no attribute groups).
func (h *IRCLogHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

// WithGroup implements slog.Handler. Returns the handler unchanged since
// IRC log output is flat (no attribute groups).
func (h *IRCLogHandler) WithGroup(_ string) slog.Handler {
	return h
}

// drainLoop runs in a background goroutine, periodically draining the message
// buffer and sending batches to IRC.
func (h *IRCLogHandler) drainLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(ircLogDrainInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopCh:
			// Drain all remaining messages before exiting.
			h.drainAll()
			return
		case <-ticker.C:
			h.drainBatch()
		}
	}
}

// drainAll sends all remaining messages from the buffer to IRC. Used during
// shutdown to ensure no messages are lost.
func (h *IRCLogHandler) drainAll() {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	if conn == nil {
		return
	}

	for {
		select {
		case msg := <-h.msgCh:
			conn.Send(h.channel, msg)
		default:
			return
		}
	}
}

// drainBatch sends up to ircLogMaxBatch messages from the buffer to IRC.
func (h *IRCLogHandler) drainBatch() {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	if conn == nil {
		return
	}

	for i := 0; i < ircLogMaxBatch; i++ {
		select {
		case msg := <-h.msgCh:
			conn.Send(h.channel, msg)
		default:
			return
		}
	}
}

// MultiHandler fans out slog records to multiple handlers. Go's slog package
// does not provide a built-in multi-handler, so this is a minimal implementation.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler creates a handler that forwards records to all given handlers.
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

// Enabled implements slog.Handler. Returns true if any child handler is enabled.
func (m *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle implements slog.Handler. Forwards the record to all enabled handlers.
func (m *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

// WithGroup implements slog.Handler.
func (m *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}
