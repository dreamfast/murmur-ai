// Package bus implements the JSON-based bus protocol used for server-client
// communication over IRC. Messages are serialized as JSON and sent as single
// IRC PRIVMSGs on the bus channel.
package bus

import (
	"encoding/json"
	"fmt"
)

// Message type constants for the bus protocol.
const (
	TypeRegister         = "register"
	TypeDeregister       = "deregister"
	TypeHeartbeat        = "heartbeat"
	TypeToolRequest      = "tool_request"
	TypeToolResponse     = "tool_response"
	TypeCronResult       = "cron_result"
	TypeCronAdd          = "cron_add"
	TypeCronRemove       = "cron_remove"
	TypeCronList         = "cron_list"
	TypeCronListResponse = "cron_list_response"
	TypeEvent            = "event"

	// TypePart is the internal message type used to carry chunks of a
	// multi-part message. Receivers reassemble parts by MessageID before
	// dispatching the reconstructed payload.
	TypePart = "_part"
)

// ircPrefixOverhead is the approximate byte overhead of the IRC PRIVMSG
// envelope: ":nick!user@host PRIVMSG #channel :" plus trailing CRLF.
// Conservative estimate; actual overhead depends on nick/user/host lengths.
const ircPrefixOverhead = 200

// DefaultMaxLineLen is the standard IRC line length limit (RFC 2812).
const DefaultMaxLineLen = 512

// MaxBusMessageLen computes the usable payload size for a given IRC
// max-line-len. Exported for use in tests.
func MaxBusMessageLen(maxLineLen int) int {
	if maxLineLen <= 0 {
		maxLineLen = DefaultMaxLineLen
	}
	v := maxLineLen - ircPrefixOverhead
	if v < 100 {
		v = 100 // absolute floor to avoid degenerate chunking
	}
	return v
}

// MaxPartTotal is the maximum number of parts allowed in a multi-part message.
// This prevents memory exhaustion from malformed or malicious messages.
const MaxPartTotal = 200

// Envelope is used for initial type detection when parsing bus messages.
// For multi-part messages, PartIndex, PartTotal, and MessageID are set.
type Envelope struct {
	Type      string `json:"type"`
	Signature string `json:"signature,omitempty"` // Phase 2: HMAC signature
	PartIndex int    `json:"pi,omitempty"`        // 0-based part index (multi-part only)
	PartTotal int    `json:"pt,omitempty"`        // total number of parts (multi-part only)
	MessageID string `json:"mid,omitempty"`       // groups parts together (multi-part only)
}

// PartMessage carries one chunk of a multi-part message. The Data field
// contains a slice of the original JSON payload.
type PartMessage struct {
	Type      string `json:"type"`                // always TypePart ("_part")
	PartIndex int    `json:"pi"`                  // 0-based index of this chunk
	PartTotal int    `json:"pt"`                  // total number of chunks
	MessageID string `json:"mid"`                 // random ID grouping all chunks
	Data      string `json:"d"`                   // chunk of the original JSON payload
	Signature string `json:"signature,omitempty"` // Phase 2: HMAC signature
}

// ToolDef describes a tool that a client provides. This is sent during
// registration and used by the server to build the LLM's tool list.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// RegisterMessage is sent by a client when it connects to announce its
// capabilities.
type RegisterMessage struct {
	Type     string    `json:"type"`
	ClientID string    `json:"client_id"`
	Hostname string    `json:"hostname"`
	Tools    []ToolDef `json:"tools"`
	Autonomy string    `json:"autonomy,omitempty"` // "report", "approve", "auto"; default "auto"
}

// DeregisterMessage is sent by a client during graceful disconnect.
type DeregisterMessage struct {
	Type     string `json:"type"`
	ClientID string `json:"client_id"`
}

// HeartbeatMessage is sent periodically by clients to indicate they are alive.
type HeartbeatMessage struct {
	Type     string   `json:"type"`
	ClientID string   `json:"client_id"`
	Uptime   int64    `json:"uptime"`
	Load     LoadInfo `json:"load"`
}

// LoadInfo contains system load metrics sent with heartbeats.
type LoadInfo struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
}

// ToolRequestMessage is sent by the server to a client to execute a tool.
type ToolRequestMessage struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResponseMessage is sent by a client back to the server with tool results.
type ToolResponseMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"` // "success" or "error"
	Result    string `json:"result"`
}

// CronResultMessage is sent by a client when a cron job completes (Phase 4).
type CronResultMessage struct {
	Type      string `json:"type"`
	ClientID  string `json:"client_id"`
	JobName   string `json:"job_name"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	Changed   bool   `json:"changed"`
	Timestamp string `json:"timestamp"`
}

// CronAddMessage is sent by the server to add a cron job on a client (Phase 4).
type CronAddMessage struct {
	Type     string  `json:"type"`
	ClientID string  `json:"client_id"`
	Job      CronJob `json:"job"`
}

// CronJob defines a scheduled job for client-side cron.
type CronJob struct {
	Name               string `json:"name"`
	Schedule           string `json:"schedule"`
	Command            string `json:"command"`
	Tool               string `json:"tool"`
	Notify             bool   `json:"notify"`
	NotifyOnlyOnChange bool   `json:"notify_only_on_change,omitempty"`
	NotifyOnlyOnError  bool   `json:"notify_only_on_error,omitempty"`
}

// CronRemoveMessage is sent by the server to remove a cron job (Phase 4).
type CronRemoveMessage struct {
	Type     string `json:"type"`
	ClientID string `json:"client_id"`
	JobName  string `json:"job_name"`
}

// CronListMessage is sent by the server to request a list of cron jobs (Phase 4).
type CronListMessage struct {
	Type     string `json:"type"`
	ClientID string `json:"client_id"`
}

// CronListResponseMessage is sent by a client with its cron job list (Phase 4).
type CronListResponseMessage struct {
	Type     string        `json:"type"`
	ClientID string        `json:"client_id"`
	Jobs     []CronJobInfo `json:"jobs"`
}

// CronJobInfo describes the status of a cron job on a client.
type CronJobInfo struct {
	Name        string `json:"name"`
	Schedule    string `json:"schedule"`
	LastRun     string `json:"last_run"`
	NextRun     string `json:"next_run"`
	LastStatus  string `json:"last_status"`
	LastChanged bool   `json:"last_changed"`
}

// EventMessage is sent by a client (or directly via the server API) to notify
// the server of an external event (e.g., backup completed, deploy finished).
// The server stores the event and injects it into the agent loop.
type EventMessage struct {
	Type      string `json:"type"`
	ClientID  string `json:"client_id"`
	Source    string `json:"source"`              // originating system (e.g., "backup", "ci")
	EventType string `json:"event_type"`          // event category (e.g., "completed", "failed")
	Summary   string `json:"summary"`             // human-readable summary
	Data      string `json:"data,omitempty"`      // optional extra data (JSON, text, etc.)
	EventID   string `json:"event_id,omitempty"`  // optional idempotency key
	Timestamp string `json:"timestamp,omitempty"` // ISO 8601 timestamp
}

// ParseMessage parses a raw JSON bus message string. It returns the message
// type and the parsed message struct. Returns ErrInvalidJSON if the input
// is not valid JSON, and ErrUnknownMessageType if the type is not recognized.
func ParseMessage(raw string) (string, any, error) {
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	var msg any
	var err error

	switch env.Type {
	case TypeRegister:
		var m RegisterMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeDeregister:
		var m DeregisterMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeHeartbeat:
		var m HeartbeatMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeToolRequest:
		var m ToolRequestMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeToolResponse:
		var m ToolResponseMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeCronResult:
		var m CronResultMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeCronAdd:
		var m CronAddMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeCronRemove:
		var m CronRemoveMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeCronList:
		var m CronListMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeCronListResponse:
		var m CronListResponseMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	case TypeEvent:
		var m EventMessage
		err = json.Unmarshal([]byte(raw), &m)
		msg = &m
	default:
		return env.Type, nil, fmt.Errorf("%w: %q", ErrUnknownMessageType, env.Type)
	}

	if err != nil {
		return env.Type, nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	return env.Type, msg, nil
}

// MarshalMessage serializes a bus message struct to a JSON string.
func MarshalMessage(msg any) (string, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("MarshalMessage: %w", err)
	}
	return string(data), nil
}
