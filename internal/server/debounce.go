package server

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// debounceMaxLines is the maximum number of lines that can be buffered for a
// single nick+channel before the buffer is flushed immediately. This prevents
// memory abuse from someone pasting hundreds of lines.
const debounceMaxLines = 10

// messageDebouncer collects consecutive messages from the same nick on the
// same channel and concatenates them into a single message after a quiet
// period. This allows multi-line IRC pastes to be processed as one LLM call
// instead of triggering separate calls per line.
//
// When the debounce window is zero, messages are passed through immediately
// without buffering (disabled mode).
//
// The flush callback must be safe for concurrent use — it may be called from
// timer goroutines, the max-lines path, or Close().
type messageDebouncer struct {
	mu      sync.Mutex
	window  time.Duration
	pending map[string]*pendingBatch // key: "channel\x00nick"
	flush   func(channel, nick, message string)
	logger  *slog.Logger
	closed  bool

	// afterFunc is injectable for testing. Defaults to time.AfterFunc.
	// The returned *time.Timer can be stopped to cancel the callback.
	afterFunc func(d time.Duration, f func()) *time.Timer
}

// pendingBatch holds accumulated lines from a single nick on a single channel.
type pendingBatch struct {
	channel string
	nick    string
	lines   []string
	timer   *time.Timer
}

// newMessageDebouncer creates a debouncer with the given quiet window and
// flush callback. The callback is invoked with the concatenated message when
// the quiet period expires or the line limit is hit. A zero window disables
// debouncing — messages pass through immediately.
func newMessageDebouncer(window time.Duration, flushFn func(channel, nick, message string), logger *slog.Logger) *messageDebouncer {
	return &messageDebouncer{
		window:    window,
		pending:   make(map[string]*pendingBatch),
		flush:     flushFn,
		logger:    logger,
		afterFunc: time.AfterFunc,
	}
}

// SetFlush replaces the flush callback under the mutex. This is used to
// wire the real callback in Run() after the server context is available.
func (d *messageDebouncer) SetFlush(fn func(channel, nick, message string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flush = fn
}

// debounceKey builds the map key for a channel+nick pair. The null byte
// separator is safe because IRC channel names and nicks cannot contain NUL.
func debounceKey(channel, nick string) string {
	return channel + "\x00" + nick
}

// Add buffers a message line for the given channel and nick. If the debounce
// window is zero (disabled), the flush callback is invoked immediately.
// Otherwise the line is appended to the pending batch and the timer is reset.
// When the batch reaches debounceMaxLines, it is flushed immediately.
func (d *messageDebouncer) Add(channel, nick, message string) {
	// Disabled mode: pass through immediately, but still respect closed state.
	if d.window == 0 {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return
		}
		fn := d.flush
		d.mu.Unlock()
		fn(channel, nick, message)
		return
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}

	key := debounceKey(channel, nick)
	batch, ok := d.pending[key]
	if !ok {
		batch = &pendingBatch{
			channel: channel,
			nick:    nick,
			lines:   make([]string, 0, 4),
		}
		d.pending[key] = batch
	}

	batch.lines = append(batch.lines, message)

	// If we hit the max lines cap, flush immediately.
	if len(batch.lines) >= debounceMaxLines {
		// Stop existing timer if any.
		if batch.timer != nil {
			batch.timer.Stop()
		}
		lines := batch.lines
		fn := d.flush
		delete(d.pending, key)
		d.mu.Unlock()

		d.logger.Debug("debounce: max lines reached, flushing",
			"channel", channel,
			"nick", nick,
			"lines", len(lines),
		)
		fn(channel, nick, strings.Join(lines, "\n"))
		return
	}

	// Reset or create the timer. time.AfterFunc runs the callback in its
	// own goroutine when the timer fires — no leaked goroutines when the
	// timer is stopped before firing.
	if batch.timer != nil {
		batch.timer.Stop()
	}
	fn := d.flush
	batch.timer = d.afterFunc(d.window, func() {
		d.mu.Lock()
		// Check that this batch is still the current one for this key.
		// It may have been flushed by Close() or a max-lines flush.
		current, exists := d.pending[key]
		if !exists || current != batch {
			d.mu.Unlock()
			return
		}
		lines := current.lines
		delete(d.pending, key)
		d.mu.Unlock()

		d.logger.Debug("debounce: quiet period elapsed, flushing",
			"channel", channel,
			"nick", nick,
			"lines", len(lines),
		)
		fn(channel, nick, strings.Join(lines, "\n"))
	})
	d.mu.Unlock()
}

// Close stops all pending timers and flushes any buffered messages. It is
// safe to call concurrently and is idempotent. After Close, further Add
// calls are silently dropped.
func (d *messageDebouncer) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	fn := d.flush

	// Collect all pending batches and clear the map.
	batches := make([]*pendingBatch, 0, len(d.pending))
	for key, batch := range d.pending {
		if batch.timer != nil {
			batch.timer.Stop()
		}
		batches = append(batches, batch)
		delete(d.pending, key)
	}
	d.mu.Unlock()

	// Flush all pending batches outside the lock.
	for _, batch := range batches {
		fn(batch.channel, batch.nick, strings.Join(batch.lines, "\n"))
	}
}
