package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ircManageParams defines the JSON schema for the irc_manage tool parameters.
var ircManageParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {
			"type": "string",
			"enum": ["join", "part", "send", "topic", "kick", "ban", "unban", "op", "deop", "voice", "devoice", "list_channels", "read_history", "summarize_channel"],
			"description": "The IRC management action to perform"
		},
		"channel": {
			"type": "string",
			"description": "The IRC channel name (must start with #). Required for all actions except list_channels."
		},
		"message": {
			"type": "string",
			"description": "The message text (for send action), topic text (for topic action), or kick reason (for kick action)"
		},
		"nick": {
			"type": "string",
			"description": "The target nickname (for kick, ban, unban, op, deop, voice, devoice actions). For ban/unban, used to generate a nick!*@* mask if mask is not provided."
		},
		"mask": {
			"type": "string",
			"description": "The hostmask (for ban/unban actions, e.g. nick!*@*). If not provided, a nick!*@* mask is generated from the nick parameter."
		},
		"limit": {
			"type": "integer",
			"description": "Number of history messages to retrieve (for read_history). Defaults to 20, max 100."
		}
	},
	"required": ["action"]
}`)

// joinRateLimit is the minimum interval between join requests to prevent
// flooding the IRC server.
const joinRateLimit = 2 * time.Second

// maxHistoryLimit is the maximum number of messages that can be retrieved
// via read_history.
const maxHistoryLimit = 100

// defaultHistoryLimit is the default number of messages retrieved when no
// limit is specified.
const defaultHistoryLimit = 20

// ircManageTool holds the state for the irc_manage tool.
type ircManageTool struct {
	irc        IRCManager
	memory     MemoryReader
	busChannel string
	persister  ChannelPersister // may be nil; persists join/part for auto-rejoin

	// Rate limiting for joins.
	joinMu   sync.Mutex
	lastJoin time.Time
}

// NewIRCManageTool creates a new irc_manage tool for IRC channel management
// and cross-channel operations. The busChannel parameter identifies the bus
// channel that must not be parted. Pass nil for memory to disable history
// operations. Pass nil for persister to disable join/part persistence.
func NewIRCManageTool(irc IRCManager, memory MemoryReader, busChannel string, persister ChannelPersister) Tool {
	mgr := &ircManageTool{
		irc:        irc,
		memory:     memory,
		busChannel: busChannel,
		persister:  persister,
	}

	return Tool{
		Name:        "irc_manage",
		Description: "Manage IRC channels: join/part channels, send messages, set topics, kick/ban/unban users, grant/revoke op and voice, list channels, read cross-channel history, and summarize channel conversations.",
		Parameters:  ircManageParams,
		Handler:     mgr.handle,
	}
}

func (m *ircManageTool) handle(_ context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return "", fmt.Errorf("irc_manage: action is required")
	}

	switch action {
	case "join":
		return m.handleJoin(args)
	case "part":
		return m.handlePart(args)
	case "send":
		return m.handleSend(args)
	case "topic":
		return m.handleTopic(args)
	case "kick":
		return m.handleKick(args)
	case "ban":
		return m.handleBan(args)
	case "unban":
		return m.handleUnban(args)
	case "op":
		return m.handleMode(args, "+o", "op")
	case "deop":
		return m.handleMode(args, "-o", "deop")
	case "voice":
		return m.handleMode(args, "+v", "voice")
	case "devoice":
		return m.handleMode(args, "-v", "devoice")
	case "list_channels":
		return m.handleListChannels()
	case "read_history":
		return m.handleReadHistory(args)
	case "summarize_channel":
		return m.handleSummarizeChannel(args)
	default:
		return "", fmt.Errorf("irc_manage: unknown action %q", action)
	}
}

func (m *ircManageTool) handleJoin(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	// Rate limit joins. The timestamp is updated only after a successful
	// join so that failed attempts don't consume the rate limit window.
	m.joinMu.Lock()
	elapsed := time.Since(m.lastJoin)
	if elapsed < joinRateLimit {
		m.joinMu.Unlock()
		return "", fmt.Errorf("irc_manage: join rate limited, wait %s", joinRateLimit-elapsed)
	}
	m.joinMu.Unlock()

	if err := m.irc.Join(channel); err != nil {
		return "", fmt.Errorf("irc_manage: join %s: %w", channel, err)
	}

	m.joinMu.Lock()
	m.lastJoin = time.Now()
	m.joinMu.Unlock()

	// Persist auto-join so the channel survives reconnects. Errors are
	// non-fatal — the join already succeeded on the IRC side.
	if m.persister != nil {
		_ = m.persister.SetAutoJoin(channel, true)
	}

	return fmt.Sprintf("Requested join to %s", channel), nil
}

func (m *ircManageTool) handlePart(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	// Prevent parting the bus channel.
	if strings.EqualFold(channel, m.busChannel) {
		return "", fmt.Errorf("irc_manage: cannot part the bus channel %s", m.busChannel)
	}

	if err := m.irc.Part(channel); err != nil {
		return "", fmt.Errorf("irc_manage: part %s: %w", channel, err)
	}

	// Clear auto-join so the channel is not rejoined on reconnect. Errors
	// are non-fatal — the part already succeeded on the IRC side.
	if m.persister != nil {
		_ = m.persister.SetAutoJoin(channel, false)
	}

	return fmt.Sprintf("Left %s", channel), nil
}

func (m *ircManageTool) handleSend(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	message, _ := args["message"].(string)
	if message == "" {
		return "", fmt.Errorf("irc_manage: message is required for send action")
	}

	// Only send to joined channels.
	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	m.irc.Send(channel, message)
	return fmt.Sprintf("Message sent to %s", channel), nil
}

func (m *ircManageTool) handleTopic(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	topic, _ := args["message"].(string)
	if topic == "" {
		return "", fmt.Errorf("irc_manage: message (topic text) is required for topic action")
	}

	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	if err := m.irc.SetTopic(channel, topic); err != nil {
		return "", fmt.Errorf("irc_manage: set topic on %s: %w", channel, err)
	}
	return fmt.Sprintf("Topic set on %s", channel), nil
}

func (m *ircManageTool) handleKick(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	nick, _ := args["nick"].(string)
	if nick == "" {
		return "", fmt.Errorf("irc_manage: nick is required for kick action")
	}

	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	reason, _ := args["message"].(string)
	if err := m.irc.Kick(channel, nick, reason); err != nil {
		return "", fmt.Errorf("irc_manage: kick %s from %s: %w", nick, channel, err)
	}
	if reason != "" {
		return fmt.Sprintf("Kicked %s from %s (%s)", nick, channel, reason), nil
	}
	return fmt.Sprintf("Kicked %s from %s", nick, channel), nil
}

func (m *ircManageTool) handleBan(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	// Prefer explicit mask; fall back to nick!*@* from the nick parameter.
	mask, _ := args["mask"].(string)
	if mask == "" {
		nick, _ := args["nick"].(string)
		if nick == "" {
			return "", fmt.Errorf("irc_manage: mask or nick is required for ban action")
		}
		mask = nick + "!*@*"
	}

	if err := m.irc.Ban(channel, mask); err != nil {
		return "", fmt.Errorf("irc_manage: ban %s on %s: %w", mask, channel, err)
	}
	return fmt.Sprintf("Banned %s on %s", mask, channel), nil
}

func (m *ircManageTool) handleUnban(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	mask, _ := args["mask"].(string)
	if mask == "" {
		nick, _ := args["nick"].(string)
		if nick == "" {
			return "", fmt.Errorf("irc_manage: mask or nick is required for unban action")
		}
		mask = nick + "!*@*"
	}

	if err := m.irc.Unban(channel, mask); err != nil {
		return "", fmt.Errorf("irc_manage: unban %s on %s: %w", mask, channel, err)
	}
	return fmt.Sprintf("Unbanned %s on %s", mask, channel), nil
}

// handleMode handles op/deop/voice/devoice actions. The modeStr is the IRC
// mode change (e.g., "+o", "-v") and label is a human-readable name for the
// response message.
func (m *ircManageTool) handleMode(args map[string]any, modeStr, label string) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	nick, _ := args["nick"].(string)
	if nick == "" {
		return "", fmt.Errorf("irc_manage: nick is required for %s action", label)
	}

	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	if err := m.irc.SetMode(channel, modeStr, nick); err != nil {
		return "", fmt.Errorf("irc_manage: %s %s on %s: %w", label, nick, channel, err)
	}
	return fmt.Sprintf("Set %s on %s in %s", label, nick, channel), nil
}

func (m *ircManageTool) handleListChannels() (string, error) {
	channels := m.irc.Channels()
	if len(channels) == 0 {
		return "Not joined to any channels", nil
	}
	return fmt.Sprintf("Joined channels: %s", strings.Join(channels, ", ")), nil
}

func (m *ircManageTool) handleReadHistory(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	if m.memory == nil {
		return "", fmt.Errorf("irc_manage: memory reader not available")
	}

	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	limit := defaultHistoryLimit
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}

	msgs, err := m.memory.GetHistory(channel, limit)
	if err != nil {
		return "", fmt.Errorf("irc_manage: read history for %s: %w", channel, err)
	}

	if len(msgs) == 0 {
		return fmt.Sprintf("No history for %s", channel), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "History for %s (%d messages):\n", channel, len(msgs))
	for _, msg := range msgs {
		fmt.Fprintf(&sb, "[%s] %s\n", msg.Role, msg.Content)
	}
	return sb.String(), nil
}

func (m *ircManageTool) handleSummarizeChannel(args map[string]any) (string, error) {
	channel, err := m.requireChannel(args)
	if err != nil {
		return "", err
	}

	if m.memory == nil {
		return "", fmt.Errorf("irc_manage: memory reader not available")
	}

	if !m.isJoined(channel) {
		return "", fmt.Errorf("irc_manage: not joined to %s, join first", channel)
	}

	// Read a larger chunk of history for summarization.
	msgs, err := m.memory.GetHistory(channel, maxHistoryLimit)
	if err != nil {
		return "", fmt.Errorf("irc_manage: read history for %s: %w", channel, err)
	}

	if len(msgs) == 0 {
		return fmt.Sprintf("No history to summarize for %s", channel), nil
	}

	count, err := m.memory.GetHistoryCount(channel)
	if err != nil {
		return "", fmt.Errorf("irc_manage: get history count for %s: %w", channel, err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Channel %s context (%d messages total, showing last %d):\n", channel, count, len(msgs))
	for _, msg := range msgs {
		fmt.Fprintf(&sb, "[%s] %s\n", msg.Role, msg.Content)
	}
	return sb.String(), nil
}

// requireChannel extracts and validates the channel parameter.
func (m *ircManageTool) requireChannel(args map[string]any) (string, error) {
	channel, _ := args["channel"].(string)
	if channel == "" {
		return "", fmt.Errorf("irc_manage: channel is required")
	}
	if channel[0] != '#' {
		return "", fmt.Errorf("irc_manage: channel must start with '#': %q", channel)
	}
	return channel, nil
}

// isJoined checks if the bot is currently in the given channel.
func (m *ircManageTool) isJoined(channel string) bool {
	for _, ch := range m.irc.Channels() {
		if strings.EqualFold(ch, channel) {
			return true
		}
	}
	return false
}
