package server

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"murmur/internal/irc"
)

// maxHistoryDisplay is the maximum number of messages that can be requested
// via the !history command to prevent excessive DB reads and IRC flooding.
const maxHistoryDisplay = 100

// ModelSwitcher is the interface that the CommandHandler needs from the Agent
// for model-related commands. This is defined here (where it's consumed) per
// Go convention.
type ModelSwitcher interface {
	// SetProvider switches the LLM provider for a specific channel.
	// Use "default" or "" to clear the per-channel override.
	SetProvider(channel, name string) error
	// GetProvider returns the name of the global default LLM provider.
	GetProvider() string
	// GetProviderForChannel returns the effective provider for a channel
	// (per-channel override if set, otherwise global default).
	GetProviderForChannel(channel string) string
	// GetProviderNames returns all available provider names.
	GetProviderNames() []string
}

// FloodFlusher is the interface the CommandHandler needs to drain the flood
// guard's per-channel message queue. Defined here (where consumed) per Go
// convention.
type FloodFlusher interface {
	// flush drains all pending messages from a channel's queue and returns
	// the number of messages dropped.
	flush(channel string) int
}

// DebugToggler is the interface the CommandHandler needs to toggle the debug
// IRC log handler on/off. Defined here (where consumed) per Go convention.
type DebugToggler interface {
	// SetEnabled toggles the debug log handler on or off.
	SetEnabled(on bool)
	// IsEnabled returns whether the debug log handler is currently active.
	IsEnabled() bool
	// SetLevel changes the minimum log level for the debug handler.
	SetLevel(level slog.Level)
	// Level returns the current minimum log level.
	Level() slog.Level
}

// Reloader is the interface the CommandHandler needs to trigger a hot config
// reload. Defined here (where consumed) per Go convention.
type Reloader interface {
	// Reload re-reads the config file and applies safe changes.
	Reload() error
}

// CommandHandler dispatches built-in `!` commands. Commands are handled
// without involving the LLM — they provide quick status and management.
type CommandHandler struct {
	registry     *Registry
	memory       *Memory
	notes        *NotesStore
	scheduler    *Scheduler
	approvals    *ApprovalManager
	conn         *irc.Connection
	model        ModelSwitcher
	flood        FloodFlusher
	debug        DebugToggler
	reloader     Reloader
	permissions  atomic.Pointer[PermissionManager] // nil when permissions are not configured
	permWriter   atomic.Pointer[PermissionsWriter] // nil when permissions file is not configured
	allowedUsers atomic.Pointer[[]string]
	startTime    time.Time
	logger       *slog.Logger

	// sendFunc overrides the default send behavior for testing.
	// When nil, messages are sent via conn.Send.
	sendFunc func(channel, message string)
}

// NewCommandHandler creates a new command handler. The model parameter may be
// nil if no LLM providers are configured (model commands will return an error).
// The notes parameter may be nil if the notes store is not available.
// The scheduler parameter may be nil if the scheduler is not enabled.
// The approvals parameter may be nil if the approval flow is not configured.
// The flood parameter may be nil if flood protection is not configured.
// The debug parameter may be nil if the debug channel is not configured.
// The reloader parameter may be nil if hot reload is not supported.
func NewCommandHandler(
	registry *Registry,
	memory *Memory,
	notes *NotesStore,
	scheduler *Scheduler,
	approvals *ApprovalManager,
	conn *irc.Connection,
	model ModelSwitcher,
	flood FloodFlusher,
	debug DebugToggler,
	reloader Reloader,
	allowedUsers []string,
	startTime time.Time,
	logger *slog.Logger,
) *CommandHandler {
	h := &CommandHandler{
		registry:  registry,
		memory:    memory,
		notes:     notes,
		scheduler: scheduler,
		approvals: approvals,
		conn:      conn,
		model:     model,
		flood:     flood,
		debug:     debug,
		reloader:  reloader,
		startTime: startTime,
		logger:    logger,
	}
	h.allowedUsers.Store(&allowedUsers)
	return h
}

// HandleCommand checks if the message is a `!` command and handles it.
// Returns true if the message was a command (handled), false if not (should
// be passed to the agent loop). If allowedUsers is non-empty, unauthorized
// users receive a rejection message.
func (h *CommandHandler) HandleCommand(channel, nick, message string) bool {
	if !strings.HasPrefix(message, "!") {
		return false
	}

	// Check authorization.
	if users := h.loadAllowedUsers(); len(users) > 0 && !h.isAllowed(nick) {
		h.send(channel, "unauthorized: you are not in the allowed users list")
		return true
	}

	// Parse command and arguments.
	parts := strings.Fields(message)
	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "!status":
		h.cmdStatus(channel)
	case "!clients":
		h.cmdClients(channel)
	case "!tools":
		h.cmdTools(channel)
	case "!model":
		h.cmdModel(channel, args)
	case "!history":
		h.cmdHistory(channel, args)
	case "!forget":
		h.cmdForget(channel)
	case "!flush":
		h.cmdFlush(channel)
	case "!notes":
		h.cmdNotes(channel, args)
	case "!approve":
		h.cmdApprove(channel)
	case "!deny":
		h.cmdDeny(channel)
	case "!pending":
		h.cmdPending(channel)
	case "!tasks":
		h.cmdTasks(channel)
	case "!task":
		h.cmdTask(channel, args)
	case "!user":
		h.cmdUser(channel, nick, args)
	case "!channel":
		h.cmdChannel(channel, nick, args)
	case "!debug":
		h.cmdDebug(channel, args)
	case "!reload":
		h.cmdReload(channel)
	case "!help":
		h.cmdHelp(channel)
	default:
		h.send(channel, fmt.Sprintf("unknown command: %s (try !help)", cmd))
	}

	return true
}

func (h *CommandHandler) cmdStatus(channel string) {
	uptime := time.Since(h.startTime).Truncate(time.Second)
	clients := h.registry.GetOnlineClients()

	model := "none"
	if h.model != nil {
		model = h.model.GetProviderForChannel(channel)
	}

	count, err := h.memory.GetHistoryCount(channel)
	if err != nil {
		h.logger.Error("failed to get history count", "error", err)
		count = -1
	}

	h.send(channel, fmt.Sprintf("uptime: %s | clients: %d | model: %s | messages: %d",
		uptime, len(clients), model, count))
}

func (h *CommandHandler) cmdClients(channel string) {
	clients := h.registry.GetOnlineClients()
	if len(clients) == 0 {
		h.send(channel, "no clients connected")
		return
	}

	var lines []string
	for _, c := range clients {
		ago := time.Since(c.LastHeartbeat).Truncate(time.Second)
		lines = append(lines, fmt.Sprintf("  %s (%s) — %d tools, %s, last heartbeat %s ago",
			c.ClientID, c.Hostname, len(c.Tools), c.Status, ago))
	}
	h.send(channel, "connected clients:\n"+strings.Join(lines, "\n"))
}

func (h *CommandHandler) cmdTools(channel string) {
	tools := h.registry.AllTools()
	if len(tools) == 0 {
		h.send(channel, "no tools available")
		return
	}

	var lines []string
	for _, t := range tools {
		lines = append(lines, fmt.Sprintf("  %s — %s", t.Name, t.Description))
	}
	h.send(channel, "available tools:\n"+strings.Join(lines, "\n"))
}

func (h *CommandHandler) cmdModel(channel string, args []string) {
	if h.model == nil {
		h.send(channel, "no LLM providers configured")
		return
	}

	if len(args) == 0 {
		// Show channel-specific model, global default, and available providers.
		channelModel := h.model.GetProviderForChannel(channel)
		globalDefault := h.model.GetProvider()
		names := h.model.GetProviderNames()

		scope := "global default"
		if channelModel != globalDefault {
			scope = "channel-specific"
		}

		h.send(channel, fmt.Sprintf("channel model: %s (%s) | global default: %s | available: %s",
			channelModel, scope, globalDefault, strings.Join(names, ", ")))
		return
	}

	// Switch to named provider (per-channel) or reset to global default.
	name := args[0]
	if err := h.model.SetProvider(channel, name); err != nil {
		h.send(channel, fmt.Sprintf("failed to switch model: %v", err))
		return
	}
	if name == "default" {
		h.send(channel, fmt.Sprintf("reset to global default model: %s", h.model.GetProvider()))
	} else {
		h.send(channel, fmt.Sprintf("switched channel model to: %s", name))
	}
}

func (h *CommandHandler) cmdHistory(channel string, args []string) {
	limit := 10
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			h.send(channel, "usage: !history [n] (n must be a positive number)")
			return
		}
		limit = n
	}
	if limit > maxHistoryDisplay {
		limit = maxHistoryDisplay
	}

	msgs, err := h.memory.GetHistory(channel, limit)
	if err != nil {
		h.send(channel, fmt.Sprintf("error retrieving history: %v", err))
		return
	}

	if len(msgs) == 0 {
		h.send(channel, "no conversation history")
		return
	}

	var lines []string
	for _, m := range msgs {
		content := m.Content
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", m.Role, content))
	}
	h.send(channel, strings.Join(lines, "\n"))
}

func (h *CommandHandler) cmdForget(channel string) {
	if err := h.memory.ClearHistory(channel); err != nil {
		h.send(channel, fmt.Sprintf("error clearing history: %v", err))
		return
	}
	h.send(channel, "conversation history cleared")
}

func (h *CommandHandler) cmdFlush(channel string) {
	dropped := 0
	if h.flood != nil {
		dropped = h.flood.flush(channel)
	}
	if err := h.memory.ClearHistory(channel); err != nil {
		h.send(channel, fmt.Sprintf("error clearing history: %v", err))
		return
	}
	h.send(channel, fmt.Sprintf("flushed: %d queued messages dropped, history cleared", dropped))
}

func (h *CommandHandler) cmdNotes(channel string, args []string) {
	if h.notes == nil {
		h.send(channel, "notes store not configured")
		return
	}

	if len(args) == 0 {
		// List all note keys.
		entries, err := h.notes.List()
		if err != nil {
			h.send(channel, fmt.Sprintf("error listing notes: %v", err))
			return
		}
		if len(entries) == 0 {
			h.send(channel, "no notes stored")
			return
		}
		var lines []string
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf("  %s (updated: %s)", e.Key, e.Updated))
		}
		h.send(channel, "notes:\n"+strings.Join(lines, "\n"))
		return
	}

	subcmd := args[0]
	switch subcmd {
	case "get":
		if len(args) < 2 {
			h.send(channel, "usage: !notes get <key>")
			return
		}
		key := args[1]
		value, err := h.notes.Get(key)
		if errors.Is(err, ErrNoteNotFound) {
			h.send(channel, fmt.Sprintf("note %q not found", key))
			return
		}
		if err != nil {
			h.send(channel, fmt.Sprintf("error: %v", err))
			return
		}
		h.send(channel, fmt.Sprintf("%s: %s", key, value))

	case "set":
		if len(args) < 3 {
			h.send(channel, "usage: !notes set <key> <value>")
			return
		}
		key := args[1]
		value := strings.Join(args[2:], " ")
		if err := h.notes.Set(key, value); err != nil {
			h.send(channel, fmt.Sprintf("error: %v", err))
			return
		}
		h.send(channel, fmt.Sprintf("note %q saved", key))

	case "delete":
		if len(args) < 2 {
			h.send(channel, "usage: !notes delete <key>")
			return
		}
		key := args[1]
		if err := h.notes.Delete(key); err != nil {
			h.send(channel, fmt.Sprintf("error: %v", err))
			return
		}
		h.send(channel, fmt.Sprintf("note %q deleted", key))

	case "search":
		if len(args) < 2 {
			h.send(channel, "usage: !notes search <query>")
			return
		}
		query := strings.Join(args[1:], " ")
		entries, err := h.notes.Search(query)
		if err != nil {
			h.send(channel, fmt.Sprintf("error searching notes: %v", err))
			return
		}
		if len(entries) == 0 {
			h.send(channel, fmt.Sprintf("no notes matching %q", query))
			return
		}
		var lines []string
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf("  %s (updated: %s)", e.Key, e.Updated))
		}
		h.send(channel, fmt.Sprintf("found %d note(s):\n%s", len(entries), strings.Join(lines, "\n")))

	default:
		h.send(channel, "usage: !notes [get <key> | set <key> <value> | delete <key> | search <query>]")
	}
}

func (h *CommandHandler) cmdTasks(channel string) {
	if h.scheduler == nil {
		h.send(channel, "scheduler not enabled")
		return
	}

	tasks, err := h.scheduler.ListTasks()
	if err != nil {
		h.send(channel, fmt.Sprintf("error listing tasks: %v", err))
		return
	}
	if len(tasks) == 0 {
		h.send(channel, "no scheduled tasks or reminders")
		return
	}

	var lines []string
	for _, t := range tasks {
		status := "enabled"
		if !t.Enabled {
			status = "disabled"
		}
		nextRun := "—"
		if t.NextRun.Valid {
			nextRun = t.NextRun.Time.UTC().Format("2006-01-02 15:04 UTC")
		}
		typeLabel := "cron"
		schedInfo := t.Schedule
		if t.Type == TaskTypeOnce {
			typeLabel = "once"
			if t.RunAt.Valid {
				schedInfo = "at " + t.RunAt.Time.UTC().Format("2006-01-02 15:04 UTC")
			}
		}
		lines = append(lines, fmt.Sprintf("  #%d [%s] %s [%s] %s — next: %s — %s",
			t.ID, typeLabel, t.Name, schedInfo, t.Channel, nextRun, status))
	}
	h.send(channel, "scheduled tasks:\n"+strings.Join(lines, "\n"))
}

func (h *CommandHandler) cmdTask(channel string, args []string) {
	if h.scheduler == nil {
		h.send(channel, "scheduler not enabled")
		return
	}

	if len(args) == 0 {
		h.send(channel, "usage: !task add <cron_expr> <description> | !task remove <id> | !task enable <id> | !task disable <id>")
		return
	}

	subcmd := args[0]
	switch subcmd {
	case "add":
		// !task add <cron_expr> <description>
		// cron_expr is 5 fields: min hour dom month dow
		if len(args) < 7 {
			h.send(channel, "usage: !task add <min> <hour> <dom> <month> <dow> <description>")
			return
		}
		cronExpr := strings.Join(args[1:6], " ")
		description := strings.Join(args[6:], " ")
		id, err := h.scheduler.AddTask(description, cronExpr, description, channel)
		if err != nil {
			h.send(channel, fmt.Sprintf("error adding task: %v", err))
			return
		}
		h.send(channel, fmt.Sprintf("task #%d added: %s [%s]", id, description, cronExpr))

	case "remove":
		if len(args) < 2 {
			h.send(channel, "usage: !task remove <id>")
			return
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			h.send(channel, "invalid task ID")
			return
		}
		if err := h.scheduler.RemoveTask(id); err != nil {
			h.send(channel, fmt.Sprintf("error removing task: %v", err))
			return
		}
		h.send(channel, fmt.Sprintf("task #%d removed", id))

	case "enable":
		if len(args) < 2 {
			h.send(channel, "usage: !task enable <id>")
			return
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			h.send(channel, "invalid task ID")
			return
		}
		if err := h.scheduler.EnableTask(id); err != nil {
			h.send(channel, fmt.Sprintf("error enabling task: %v", err))
			return
		}
		h.send(channel, fmt.Sprintf("task #%d enabled", id))

	case "disable":
		if len(args) < 2 {
			h.send(channel, "usage: !task disable <id>")
			return
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			h.send(channel, "invalid task ID")
			return
		}
		if err := h.scheduler.DisableTask(id); err != nil {
			h.send(channel, fmt.Sprintf("error disabling task: %v", err))
			return
		}
		h.send(channel, fmt.Sprintf("task #%d disabled", id))

	default:
		h.send(channel, "usage: !task add <cron_expr> <description> | !task remove <id> | !task enable <id> | !task disable <id>")
	}
}

func (h *CommandHandler) cmdApprove(channel string) {
	if h.approvals == nil {
		h.send(channel, "approval flow not configured")
		return
	}

	latest := h.approvals.GetLatestPending(channel)
	if latest == nil {
		h.send(channel, "no pending approvals")
		return
	}

	if err := h.approvals.Resolve(latest.ID, true); err != nil {
		h.send(channel, fmt.Sprintf("error approving: %v", err))
		return
	}
	h.send(channel, fmt.Sprintf("approved: %s", latest.ToolName))
}

func (h *CommandHandler) cmdDeny(channel string) {
	if h.approvals == nil {
		h.send(channel, "approval flow not configured")
		return
	}

	latest := h.approvals.GetLatestPending(channel)
	if latest == nil {
		h.send(channel, "no pending approvals")
		return
	}

	if err := h.approvals.Resolve(latest.ID, false); err != nil {
		h.send(channel, fmt.Sprintf("error denying: %v", err))
		return
	}
	h.send(channel, fmt.Sprintf("denied: %s", latest.ToolName))
}

func (h *CommandHandler) cmdPending(channel string) {
	if h.approvals == nil {
		h.send(channel, "approval flow not configured")
		return
	}

	pending := h.approvals.GetPending(channel)
	if len(pending) == 0 {
		h.send(channel, "no pending approvals")
		return
	}

	var lines []string
	for _, pa := range pending {
		age := time.Since(pa.RequestedAt).Truncate(time.Second)
		argsSummary := string(pa.Arguments)
		if len(argsSummary) > 100 {
			argsSummary = argsSummary[:100] + "..."
		}
		lines = append(lines, fmt.Sprintf("  %s(%s) from %s — %s ago",
			pa.ToolName, argsSummary, pa.ClientID, age))
	}
	h.send(channel, fmt.Sprintf("pending approvals (%d):\n%s", len(pending), strings.Join(lines, "\n")))
}

func (h *CommandHandler) cmdReload(channel string) {
	if h.reloader == nil {
		h.send(channel, "hot reload not available")
		return
	}
	if err := h.reloader.Reload(); err != nil {
		h.send(channel, fmt.Sprintf("reload failed: %v", err))
		return
	}
	h.send(channel, "config reloaded successfully")
}

func (h *CommandHandler) cmdDebug(channel string, args []string) {
	if h.debug == nil {
		h.send(channel, "debug channel not configured (set server.debug_channel in config)")
		return
	}

	if len(args) == 0 {
		// Show current state.
		state := "off"
		if h.debug.IsEnabled() {
			state = "on"
		}
		h.send(channel, fmt.Sprintf("debug: %s (level: %s)", state, h.debug.Level().String()))
		return
	}

	switch args[0] {
	case "on":
		h.debug.SetEnabled(true)
		h.send(channel, "debug logging enabled")
	case "off":
		h.debug.SetEnabled(false)
		h.send(channel, "debug logging disabled")
	case "level":
		if len(args) < 2 {
			h.send(channel, fmt.Sprintf("current level: %s (usage: !debug level debug|info|warn|error)", h.debug.Level().String()))
			return
		}
		switch strings.ToLower(args[1]) {
		case "debug":
			h.debug.SetLevel(slog.LevelDebug)
		case "info":
			h.debug.SetLevel(slog.LevelInfo)
		case "warn":
			h.debug.SetLevel(slog.LevelWarn)
		case "error":
			h.debug.SetLevel(slog.LevelError)
		default:
			h.send(channel, "unknown level: "+args[1]+" (use debug, info, warn, error)")
			return
		}
		h.send(channel, fmt.Sprintf("debug level set to %s", args[1]))
	default:
		h.send(channel, "usage: !debug [on|off|level debug|info|warn|error]")
	}
}

func (h *CommandHandler) cmdHelp(channel string) {
	help := `available commands:
  !status — server uptime, clients, model, message count
  !clients — list connected clients
  !tools — list available tools
  !model — show/switch LLM provider
  !history [n] — show last N messages (default 10)
  !forget — clear conversation history
  !flush — drop queued messages and clear history (use during floods)
  !notes — list/get/set/delete/search notes
  !approve — approve the most recent pending tool call
  !deny — deny the most recent pending tool call
  !pending — list pending tool call approvals
  !tasks — list scheduled tasks and reminders
  !task add/remove/enable/disable — manage scheduled tasks (30s granularity, UTC)
  !user list/info/add/remove/<nick> — manage user permissions (admin)
  !channel list/info/<channel> — manage channel permissions (admin)
  !debug [on|off|level <level>] — toggle debug IRC channel logging
  !reload — reload configuration from disk
  !help — show this help`
	h.send(channel, help)
}

func (h *CommandHandler) send(channel, message string) {
	if h.sendFunc != nil {
		h.sendFunc(channel, message)
		return
	}
	h.conn.Send(channel, message)
}

// loadAllowedUsers returns the current allowed users list from the atomic pointer.
// Returns nil if no users have been stored (zero-value atomic pointer).
func (h *CommandHandler) loadAllowedUsers() []string {
	p := h.allowedUsers.Load()
	if p == nil {
		return nil
	}
	return *p
}

// UpdateAllowedUsers atomically replaces the allowed users list. This is
// called during hot config reload.
func (h *CommandHandler) UpdateAllowedUsers(users []string) {
	h.allowedUsers.Store(&users)
}

func (h *CommandHandler) isAllowed(nick string) bool {
	for _, u := range h.loadAllowedUsers() {
		if strings.EqualFold(u, nick) {
			return true
		}
	}
	return false
}
