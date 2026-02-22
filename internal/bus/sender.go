package bus

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"unicode/utf8"

	mcrypto "murmur/internal/crypto"
	"murmur/internal/irc"
)

// partOverhead is the number of bytes consumed by the PartMessage envelope
// fields (type, pi, pt, mid, d key, JSON punctuation). Calculated conservatively
// to leave room for the data chunk.
// {"type":"_part","pi":999,"pt":999,"mid":"xxxxxxxxxxxxxxxx","d":""}
// ≈ 70 bytes of overhead.
const partOverhead = 70

// Sender sends bus protocol messages over an IRC connection.
type Sender struct {
	conn             *irc.Connection
	busChannel       string
	busKey           []byte // HMAC-SHA256 key; nil means no signing
	logger           *slog.Logger
	maxBusMessageLen int // max payload bytes for a single IRC PRIVMSG
	chunkSize        int // max data bytes per multi-part chunk
}

// NewSender creates a new bus message sender. maxLineLen is the IRC server's
// maximum line length in bytes (e.g. 512 for standard IRC, 8192 for Ergo).
// If busKey is non-empty, each message is signed with HMAC-SHA256 and the
// hex-encoded signature is set in the Envelope.Signature field.
func NewSender(conn *irc.Connection, busChannel, busKey string, maxLineLen int, logger *slog.Logger) *Sender {
	var key []byte
	if busKey != "" {
		key = []byte(busKey)
	}
	maxMsg := MaxBusMessageLen(maxLineLen)
	// chunkSize: json.Marshal output can contain multi-byte UTF-8 characters
	// and JSON-escaped sequences. When placed in PartMessage.Data (a JSON
	// string field), characters are re-escaped: '\' → '\\', '"' → '\"'.
	// In the worst case the data field doubles in size, so we use 1/2 of
	// the available space. The actual marshaled part size is verified in
	// sendMultiPart before sending.
	cs := (maxMsg - partOverhead) / 2
	if cs < 50 {
		cs = 50 // absolute floor
	}
	return &Sender{
		conn:             conn,
		busChannel:       busChannel,
		busKey:           key,
		logger:           logger,
		maxBusMessageLen: maxMsg,
		chunkSize:        cs,
	}
}

// Send marshals a message to JSON and sends it on the bus channel.
// If the serialized (and optionally signed) message fits within the
// configured max bus message length it is sent as a single IRC PRIVMSG. Otherwise it is split
// into multiple PartMessage chunks that the receiver reassembles before
// dispatching. If a busKey is configured, each outgoing JSON string is signed
// with HMAC-SHA256 before sending.
func (s *Sender) Send(msg any) error {
	if s.conn == nil {
		return fmt.Errorf("bus send: no IRC connection")
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		return fmt.Errorf("Sender.Send: %w", err)
	}

	// Sign before checking size — the signature adds ~90 bytes
	// (,"signature":"<64-hex-chars>") and may push the message over the limit.
	out, err := s.maybeSign(data)
	if err != nil {
		return fmt.Errorf("Sender.Send: %w", err)
	}

	if len(out) <= s.maxBusMessageLen {
		s.conn.SendRaw(s.busChannel, out)
		return nil
	}

	// Message (possibly after signing) is too large — split the original
	// unsigned payload into parts. Each part is signed individually in
	// sendMultiPart.
	return s.sendMultiPart(data)
}

// sendMultiPart splits a JSON payload into PartMessage chunks and sends each
// as a separate IRC PRIVMSG. The MessageID is a 16-hex-char random string.
// Returns an error if the payload requires more than MaxPartTotal parts.
// Each part is individually signed when a busKey is configured.
func (s *Sender) sendMultiPart(payload string) error {
	mid, err := randomHex(8)
	if err != nil {
		return fmt.Errorf("Sender.sendMultiPart: generate message ID: %w", err)
	}

	// Split payload into chunks.
	chunks := splitString(payload, s.chunkSize)
	total := len(chunks)

	if total > MaxPartTotal {
		return fmt.Errorf("Sender.sendMultiPart: payload requires %d parts, exceeds maximum of %d", total, MaxPartTotal)
	}

	s.logger.Debug("sending multi-part bus message",
		"mid", mid,
		"parts", total,
		"total_bytes", len(payload),
	)

	for i, chunk := range chunks {
		part := &PartMessage{
			Type:      TypePart,
			PartIndex: i,
			PartTotal: total,
			MessageID: mid,
			Data:      chunk,
		}
		partJSON, err := MarshalMessage(part)
		if err != nil {
			return fmt.Errorf("Sender.sendMultiPart: marshal part %d: %w", i, err)
		}
		// Sign the part before checking size (signature adds ~64 hex chars).
		partJSON, err = s.maybeSign(partJSON)
		if err != nil {
			return fmt.Errorf("Sender.sendMultiPart: sign part %d: %w", i, err)
		}
		// Verify the marshaled part fits within the IRC message limit.
		if len(partJSON) > s.maxBusMessageLen {
			return fmt.Errorf("Sender.sendMultiPart: part %d/%d is %d bytes, exceeds maxBusMessageLen %d", i, total, len(partJSON), s.maxBusMessageLen)
		}
		s.conn.SendRaw(s.busChannel, partJSON)
	}

	return nil
}

// maybeSign adds an HMAC-SHA256 signature to a JSON message string if a
// busKey is configured. The signature is computed over the message body
// (with the signature field absent/empty) and set as a hex string in the
// "signature" field.
func (s *Sender) maybeSign(jsonMsg string) (string, error) {
	if len(s.busKey) == 0 {
		return jsonMsg, nil
	}
	return signMessage(jsonMsg, s.busKey)
}

// signMessage computes HMAC-SHA256(key, body) and injects the hex-encoded
// signature into the JSON object's "signature" field. The HMAC is computed
// over the canonical form: the JSON object with "signature":"" (empty string).
// This allows the receiver to verify by zeroing the signature field.
func signMessage(jsonMsg string, key []byte) (string, error) {
	canonical, obj, err := canonicalForm(jsonMsg)
	if err != nil {
		return "", fmt.Errorf("signMessage: %w", err)
	}

	sig := mcrypto.SignHMAC(string(key), canonical)

	// Replace the empty signature with the actual signature.
	obj["signature"] = json.RawMessage(`"` + sig + `"`)
	out, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("signMessage: marshal signed: %w", err)
	}
	return string(out), nil
}

// canonicalForm builds the canonical JSON representation for HMAC signing.
// It sets the "signature" field to "" and returns the marshaled bytes along
// with the parsed object for further manipulation.
func canonicalForm(jsonMsg string) ([]byte, map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonMsg), &obj); err != nil {
		return nil, nil, fmt.Errorf("unmarshal: %w", err)
	}
	obj["signature"] = json.RawMessage(`""`)
	canonical, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal canonical: %w", err)
	}
	return canonical, obj, nil
}

// SendRegister sends a client registration message. The autonomy parameter
// specifies the client's autonomy level ("auto", "approve", or "report").
// If empty, the server defaults to "auto" for backward compatibility.
func (s *Sender) SendRegister(clientID, hostname string, tools []ToolDef, autonomy string) error {
	return s.Send(&RegisterMessage{
		Type:     TypeRegister,
		ClientID: clientID,
		Hostname: hostname,
		Tools:    tools,
		Autonomy: autonomy,
	})
}

// SendDeregister sends a client deregistration message.
func (s *Sender) SendDeregister(clientID string) error {
	return s.Send(&DeregisterMessage{
		Type:     TypeDeregister,
		ClientID: clientID,
	})
}

// SendHeartbeat sends a client heartbeat message.
func (s *Sender) SendHeartbeat(clientID string, uptime int64, load LoadInfo) error {
	return s.Send(&HeartbeatMessage{
		Type:     TypeHeartbeat,
		ClientID: clientID,
		Uptime:   uptime,
		Load:     load,
	})
}

// SendToolRequest sends a tool execution request to a client.
func (s *Sender) SendToolRequest(requestID, tool string, arguments []byte) error {
	return s.Send(&ToolRequestMessage{
		Type:      TypeToolRequest,
		RequestID: requestID,
		Tool:      tool,
		Arguments: arguments,
	})
}

// SendToolResponse sends a tool execution result back to the server.
func (s *Sender) SendToolResponse(requestID, status, result string) error {
	return s.Send(&ToolResponseMessage{
		Type:      TypeToolResponse,
		RequestID: requestID,
		Status:    status,
		Result:    result,
	})
}

// SendEvent sends an event notification to the server via the bus.
func (s *Sender) SendEvent(clientID, source, eventType, summary, data, eventID, timestamp string) error {
	return s.Send(&EventMessage{
		Type:      TypeEvent,
		ClientID:  clientID,
		Source:    source,
		EventType: eventType,
		Summary:   summary,
		Data:      data,
		EventID:   eventID,
		Timestamp: timestamp,
	})
}

// randomHex returns n random bytes encoded as a hex string (2n characters).
func randomHex(n int) (string, error) {
	return mcrypto.RandomHex(n)
}

// splitString splits s into chunks of at most size bytes, always cutting at
// valid UTF-8 rune boundaries to avoid corrupting multi-byte characters.
func splitString(s string, size int) []string {
	if size <= 0 {
		size = 1
	}
	var chunks []string
	for len(s) > 0 {
		if len(s) <= size {
			chunks = append(chunks, s)
			break
		}
		// Find the largest prefix that fits within size bytes and ends on a
		// rune boundary. Walk backward from s[size] until we find a valid
		// rune start byte.
		cut := size
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			// Degenerate: single rune is wider than size. Include it whole to
			// avoid an infinite loop.
			_, runeLen := utf8.DecodeRuneInString(s)
			cut = runeLen
		}
		chunks = append(chunks, s[:cut])
		s = s[cut:]
	}
	return chunks
}
