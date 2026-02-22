package bus

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mcrypto "murmur/internal/crypto"
)

// partBuffer accumulates chunks of a multi-part message.
// The buffer key already encodes the sender nick (as "nick:messageID"), so
// cross-sender injection is prevented at the map-key level.
type partBuffer struct {
	parts    []string  // indexed by PartIndex; payload data for each chunk
	received []bool    // tracks which indices have been received (avoids empty-string sentinel)
	count    int       // number of distinct parts received so far
	total    int       // expected total number of parts
	deadline time.Time // when to discard this buffer
}

// Receiver dispatches incoming bus messages to registered handlers based on
// message type. It is safe for concurrent use. Handler panics are recovered
// and logged.
//
// Multi-part messages (TypePart / "_part") are buffered internally and
// dispatched only after all chunks have arrived and been reassembled.
// Incomplete multi-part messages are discarded after 30 seconds.
//
// When a busKey is configured, each incoming message's HMAC-SHA256 signature
// is verified before dispatching. Messages with invalid or missing signatures
// are logged and dropped.
type Receiver struct {
	mu       sync.RWMutex
	handlers map[string]func(nick string, msg any)
	busKey   []byte // HMAC-SHA256 key; nil means no verification
	logger   *slog.Logger

	partMu  sync.Mutex
	partBuf map[string]*partBuffer // keyed by "nick:messageID"
}

// partTTL is how long an incomplete multi-part message is kept before being
// discarded.
const partTTL = 30 * time.Second

// NewReceiver creates a new bus message receiver. If busKey is non-empty,
// each incoming message's HMAC-SHA256 signature is verified before dispatching.
// Messages with invalid or missing signatures are logged and dropped.
func NewReceiver(busKey string, logger *slog.Logger) *Receiver {
	var key []byte
	if busKey != "" {
		key = []byte(busKey)
	}
	return &Receiver{
		handlers: make(map[string]func(nick string, msg any)),
		busKey:   key,
		logger:   logger,
		partBuf:  make(map[string]*partBuffer),
	}
}

// On registers a handler for a specific message type. The handler receives
// the sender's IRC nick and the parsed message struct (e.g., *RegisterMessage).
// The nick can be used for source verification (e.g., ensuring tool requests
// come from the expected server nick).
// Only one handler per message type is supported; registering a new handler
// for the same type replaces the previous one.
func (r *Receiver) On(msgType string, handler func(nick string, msg any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[msgType] = handler
}

// HandleRaw parses a raw JSON bus message and dispatches it to the
// appropriate handler. Invalid messages are logged and dropped. Handler
// panics are recovered and logged — this method never panics.
//
// If a busKey is configured, the message signature is verified before any
// further processing. Messages with invalid signatures are dropped.
//
// If the message is a TypePart chunk, it is buffered. When all chunks for a
// MessageID have arrived, the reassembled payload is dispatched normally.
func (r *Receiver) HandleRaw(nick, raw string) {
	// Verify signature if busKey is configured.
	if len(r.busKey) > 0 {
		if err := verifyMessage(raw, r.busKey); err != nil {
			r.logger.Warn("bus message signature verification failed",
				"nick", nick,
				"error", err,
			)
			return
		}
	}

	// Peek at the envelope to detect multi-part messages.
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		r.logger.Warn("failed to parse bus message envelope",
			"nick", nick,
			"error", err,
		)
		return
	}

	if env.Type == TypePart {
		r.handlePart(nick, raw)
		return
	}

	r.dispatchRaw(nick, raw)
}

// verifyMessage checks the HMAC-SHA256 signature of a JSON message.
// The signature is expected in the "signature" field. The HMAC is verified
// against the canonical form: the JSON object with "signature":"" (empty).
// This mirrors the signing process in signMessage.
func verifyMessage(jsonMsg string, key []byte) error {
	// Parse the object and extract the signature.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonMsg), &obj); err != nil {
		return fmt.Errorf("verifyMessage: unmarshal: %w", err)
	}

	sigRaw, ok := obj["signature"]
	if !ok {
		return fmt.Errorf("%w: missing signature field", ErrInvalidSignature)
	}
	var sig string
	if err := json.Unmarshal(sigRaw, &sig); err != nil || sig == "" {
		return fmt.Errorf("%w: invalid or empty signature", ErrInvalidSignature)
	}

	// Reconstruct the canonical form (signature field set to "").
	canonical, _, err := canonicalForm(jsonMsg)
	if err != nil {
		return fmt.Errorf("verifyMessage: %w", err)
	}

	if !mcrypto.VerifyHMAC(string(key), sig, canonical) {
		return ErrInvalidSignature
	}
	return nil
}

// handlePart buffers a single chunk of a multi-part message. When all chunks
// for a MessageID have been received from the same sender, the reassembled
// payload is dispatched.
func (r *Receiver) handlePart(nick, raw string) {
	var part PartMessage
	if err := json.Unmarshal([]byte(raw), &part); err != nil {
		r.logger.Warn("failed to parse part message",
			"nick", nick,
			"error", err,
		)
		return
	}

	if part.MessageID == "" || part.PartTotal <= 0 || part.PartIndex < 0 || part.PartIndex >= part.PartTotal {
		r.logger.Warn("invalid part message fields",
			"nick", nick,
			"mid", part.MessageID,
			"pi", part.PartIndex,
			"pt", part.PartTotal,
		)
		return
	}

	if part.PartTotal > MaxPartTotal {
		r.logger.Warn("part message total exceeds maximum, dropping",
			"nick", nick,
			"mid", part.MessageID,
			"pt", part.PartTotal,
			"max", MaxPartTotal,
		)
		return
	}

	// Key by sender nick + message ID to prevent cross-sender injection.
	bufKey := nick + ":" + part.MessageID

	// Collect the reassembled payload (if complete) outside the lock.
	reassembled := r.bufferPart(bufKey, nick, &part)
	if reassembled != "" {
		r.dispatchRaw(nick, reassembled)
	}
}

// bufferPart stores a chunk in the part buffer and returns the reassembled
// payload when all parts have arrived, or "" if still waiting.
func (r *Receiver) bufferPart(bufKey, nick string, part *PartMessage) string {
	r.partMu.Lock()
	defer r.partMu.Unlock()

	// Evict stale buffers lazily.
	r.evictStaleLocked()

	buf, ok := r.partBuf[bufKey]
	if !ok {
		buf = &partBuffer{
			parts:    make([]string, part.PartTotal),
			received: make([]bool, part.PartTotal),
			total:    part.PartTotal,
			deadline: time.Now().Add(partTTL),
		}
		r.partBuf[bufKey] = buf
	}

	// Guard against mismatched totals (shouldn't happen in practice).
	if part.PartTotal != buf.total {
		r.logger.Warn("part message total mismatch, ignoring part",
			"mid", part.MessageID,
			"expected", buf.total,
			"got", part.PartTotal,
		)
		return ""
	}

	// Only count a part once (idempotent, using bool tracker).
	if !buf.received[part.PartIndex] {
		buf.parts[part.PartIndex] = part.Data
		buf.received[part.PartIndex] = true
		buf.count++
	}

	if buf.count < buf.total {
		// Still waiting for more parts.
		return ""
	}

	// All parts received — reassemble and clean up.
	reassembled := strings.Join(buf.parts, "")
	delete(r.partBuf, bufKey)

	r.logger.Debug("reassembled multi-part bus message",
		"mid", part.MessageID,
		"parts", part.PartTotal,
		"bytes", len(reassembled),
	)

	return reassembled
}

// evictStaleLocked removes expired part buffers. Must be called with partMu held.
func (r *Receiver) evictStaleLocked() {
	now := time.Now()
	for key, buf := range r.partBuf {
		if now.After(buf.deadline) {
			r.logger.Warn("discarding incomplete multi-part message (timeout)",
				"key", key,
				"received", buf.count,
				"total", buf.total,
			)
			delete(r.partBuf, key)
		}
	}
}

// dispatchRaw parses a complete JSON payload and dispatches it to the
// registered handler for its message type.
func (r *Receiver) dispatchRaw(nick, raw string) {
	msgType, msg, err := ParseMessage(raw)
	if err != nil {
		r.logger.Warn("failed to parse bus message",
			"nick", nick,
			"error", err,
		)
		return
	}

	r.mu.RLock()
	handler, ok := r.handlers[msgType]
	r.mu.RUnlock()

	if !ok {
		r.logger.Debug("no handler for bus message type",
			"type", msgType,
			"nick", nick,
		)
		return
	}

	r.safeCall(handler, nick, msg, msgType)
}

// safeCall invokes a handler with panic recovery.
func (r *Receiver) safeCall(handler func(string, any), nick string, msg any, msgType string) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("panic in bus message handler",
				"type", msgType,
				"nick", nick,
				"panic", rec,
			)
		}
	}()

	handler(nick, msg)
}

// partBufferCount returns the number of in-progress multi-part buffers.
// Exposed for testing only.
func (r *Receiver) partBufferCount() int {
	r.partMu.Lock()
	defer r.partMu.Unlock()
	return len(r.partBuf)
}
