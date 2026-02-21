package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"murmur/internal/config"
)

// cmdUser handles the !user command for managing user permissions.
// Subcommands: list, info, add, remove, and field setters (role, tools, deny,
// autonomy, model, ratelimit).
func (h *CommandHandler) cmdUser(channel, nick string, args []string) {
	if !h.requireAdmin(channel, nick) {
		return
	}

	if len(args) == 0 {
		h.send(channel, "usage: !user list | !user info <nick> | !user add <nick> [role] | !user remove <nick> | !user <nick> <field> <value>")
		return
	}

	switch args[0] {
	case "list":
		h.cmdUserList(channel)
	case "info":
		if len(args) < 2 {
			h.send(channel, "usage: !user info <nick>")
			return
		}
		h.cmdUserInfo(channel, args[1])
	case "add":
		if len(args) < 2 {
			h.send(channel, "usage: !user add <nick> [admin|user]")
			return
		}
		role := "user"
		if len(args) >= 3 {
			role = args[2]
		}
		h.cmdUserAdd(channel, args[1], role)
	case "remove":
		if len(args) < 2 {
			h.send(channel, "usage: !user remove <nick>")
			return
		}
		h.cmdUserRemove(channel, args[1])
	default:
		// !user <nick> <field> <value...>
		if len(args) < 3 {
			h.send(channel, "usage: !user <nick> role|tools|deny|autonomy|model|ratelimit <value>")
			return
		}
		h.cmdUserSet(channel, args[0], args[1], args[2:])
	}
}

// cmdChannel handles the !channel command for managing channel permissions.
// Subcommands: list, info, and field setters (tools, deny, autonomy, model).
func (h *CommandHandler) cmdChannel(channel, nick string, args []string) {
	if !h.requireAdmin(channel, nick) {
		return
	}

	if len(args) == 0 {
		h.send(channel, "usage: !channel list | !channel info <channel> | !channel <channel> <field> <value>")
		return
	}

	switch args[0] {
	case "list":
		h.cmdChannelList(channel)
	case "info":
		if len(args) < 2 {
			h.send(channel, "usage: !channel info <channel>")
			return
		}
		h.cmdChannelInfo(channel, args[1])
	default:
		// !channel <ch> <field> <value...>
		if len(args) < 3 {
			h.send(channel, "usage: !channel <channel> tools|deny|autonomy|model <value>")
			return
		}
		h.cmdChannelSet(channel, args[0], args[1], args[2:])
	}
}

// requireAdmin checks that the caller is an admin. Returns false (and sends
// a rejection message) if not.
func (h *CommandHandler) requireAdmin(channel, nick string) bool {
	pm := h.permissions.Load()
	if pm == nil || !pm.IsAdmin(nick) {
		h.send(channel, "permission denied: admin role required")
		return false
	}
	return true
}

// loadPermissions returns the current PermissionManager, or nil.
func (h *CommandHandler) loadPermissions() *PermissionManager {
	return h.permissions.Load()
}

// loadPermWriter returns the current PermissionsWriter, or nil.
func (h *CommandHandler) loadPermWriter() *PermissionsWriter {
	return h.permWriter.Load()
}

// cmdUserList lists all configured users.
func (h *CommandHandler) cmdUserList(channel string) {
	cfg := h.loadPermissions().Config()
	if len(cfg.Users) == 0 {
		h.send(channel, "no users configured")
		return
	}

	// Sort nicks for deterministic output.
	nicks := make([]string, 0, len(cfg.Users))
	for nick := range cfg.Users {
		nicks = append(nicks, nick)
	}
	sort.Strings(nicks)

	var lines []string
	for _, nick := range nicks {
		u := cfg.Users[nick]
		role := u.Role
		if role == "" {
			role = "user"
		}
		lines = append(lines, fmt.Sprintf("  %s [%s]", nick, role))
	}
	h.send(channel, "users:\n"+strings.Join(lines, "\n"))
}

// cmdUserInfo shows detailed permissions for a user.
func (h *CommandHandler) cmdUserInfo(channel, target string) {
	cfg := h.loadPermissions().Config()
	user := cfg.GetUser(target)

	// Check if the user actually exists (vs falling back to default).
	found := false
	for k := range cfg.Users {
		if strings.EqualFold(k, target) {
			found = true
			break
		}
	}
	if !found {
		h.send(channel, fmt.Sprintf("user %q not found (using default permissions)", target))
		return
	}

	role := user.Role
	if role == "" {
		role = "user"
	}
	autonomy := user.Autonomy
	if autonomy == "" {
		autonomy = "(default)"
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("user: %s", target))
	parts = append(parts, fmt.Sprintf("  role: %s", role))
	parts = append(parts, fmt.Sprintf("  tools: %s", formatList(user.Tools)))
	if len(user.DenyTools) > 0 {
		parts = append(parts, fmt.Sprintf("  deny_tools: %s", formatList(user.DenyTools)))
	}
	parts = append(parts, fmt.Sprintf("  autonomy: %s", autonomy))
	parts = append(parts, fmt.Sprintf("  models: %s", formatList(user.AllowedModels)))
	if len(user.DenyModels) > 0 {
		parts = append(parts, fmt.Sprintf("  deny_models: %s", formatList(user.DenyModels)))
	}
	if user.MaxMessagesPerHour != 0 {
		parts = append(parts, fmt.Sprintf("  ratelimit: %d/hr", user.MaxMessagesPerHour))
	}
	if user.APIKey != "" {
		parts = append(parts, "  api_key: (set)")
	}

	h.send(channel, strings.Join(parts, "\n"))
}

// cmdUserAdd adds a new user with the given role.
func (h *CommandHandler) cmdUserAdd(channel, target, role string) {
	pw := h.loadPermWriter()
	if pw == nil {
		h.send(channel, "permissions file not configured")
		return
	}

	// Check if user already exists.
	cfg := h.loadPermissions().Config()
	for k := range cfg.Users {
		if strings.EqualFold(k, target) {
			h.send(channel, fmt.Sprintf("user %q already exists", k))
			return
		}
	}

	user := config.UserPermissions{Role: role}
	if err := pw.WriteUser(target, user); err != nil {
		h.send(channel, fmt.Sprintf("error: %v", err))
		return
	}

	h.reloadPermissions(channel)
	h.send(channel, fmt.Sprintf("user %q added with role %q", target, role))
}

// cmdUserRemove removes a user.
func (h *CommandHandler) cmdUserRemove(channel, target string) {
	pw := h.loadPermWriter()
	if pw == nil {
		h.send(channel, "permissions file not configured")
		return
	}

	if err := pw.RemoveUser(target); err != nil {
		h.send(channel, fmt.Sprintf("error: %v", err))
		return
	}

	h.reloadPermissions(channel)
	h.send(channel, fmt.Sprintf("user %q removed", target))
}

// cmdUserSet modifies a single field on a user.
func (h *CommandHandler) cmdUserSet(channel, target, field string, values []string) {
	pw := h.loadPermWriter()
	if pw == nil {
		h.send(channel, "permissions file not configured")
		return
	}

	// Read current user (or default).
	cfg := h.loadPermissions().Config()
	user := cfg.GetUser(target)

	// Ensure the user exists — we don't create users implicitly.
	found := false
	for k := range cfg.Users {
		if strings.EqualFold(k, target) {
			found = true
			target = k // use the canonical key
			break
		}
	}
	if !found {
		h.send(channel, fmt.Sprintf("user %q not found (use !user add first)", target))
		return
	}

	switch field {
	case "role":
		user.Role = values[0]
	case "tools":
		user.Tools = parseCSV(values)
	case "deny":
		user.DenyTools = parseCSV(values)
	case "autonomy":
		user.Autonomy = values[0]
	case "model":
		user.AllowedModels = parseCSV(values)
	case "ratelimit":
		n, err := strconv.Atoi(values[0])
		if err != nil {
			h.send(channel, "ratelimit must be a number (-1 for unlimited)")
			return
		}
		user.MaxMessagesPerHour = n
	default:
		h.send(channel, fmt.Sprintf("unknown field %q (use role, tools, deny, autonomy, model, ratelimit)", field))
		return
	}

	if err := pw.WriteUser(target, user); err != nil {
		h.send(channel, fmt.Sprintf("error: %v", err))
		return
	}

	h.reloadPermissions(channel)
	h.send(channel, fmt.Sprintf("user %q: %s updated", target, field))
}

// cmdChannelList lists all configured channels.
func (h *CommandHandler) cmdChannelList(channel string) {
	cfg := h.loadPermissions().Config()
	if len(cfg.Channels) == 0 {
		h.send(channel, "no channels configured")
		return
	}

	channels := make([]string, 0, len(cfg.Channels))
	for ch := range cfg.Channels {
		channels = append(channels, ch)
	}
	sort.Strings(channels)

	var lines []string
	for _, ch := range channels {
		cp := cfg.Channels[ch]
		autonomy := cp.Autonomy
		if autonomy == "" {
			autonomy = "(default)"
		}
		lines = append(lines, fmt.Sprintf("  %s [autonomy: %s]", ch, autonomy))
	}
	h.send(channel, "channels:\n"+strings.Join(lines, "\n"))
}

// cmdChannelInfo shows detailed permissions for a channel.
func (h *CommandHandler) cmdChannelInfo(channel, target string) {
	cfg := h.loadPermissions().Config()

	// Check if the channel actually exists.
	found := false
	for k := range cfg.Channels {
		if strings.EqualFold(k, target) {
			found = true
			target = k
			break
		}
	}
	if !found {
		h.send(channel, fmt.Sprintf("channel %q not configured", target))
		return
	}

	ch := cfg.Channels[target]
	autonomy := ch.Autonomy
	if autonomy == "" {
		autonomy = "(default)"
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("channel: %s", target))
	parts = append(parts, fmt.Sprintf("  tools: %s", formatList(ch.Tools)))
	if len(ch.DenyTools) > 0 {
		parts = append(parts, fmt.Sprintf("  deny_tools: %s", formatList(ch.DenyTools)))
	}
	parts = append(parts, fmt.Sprintf("  autonomy: %s", autonomy))
	parts = append(parts, fmt.Sprintf("  models: %s", formatList(ch.AllowedModels)))

	h.send(channel, strings.Join(parts, "\n"))
}

// cmdChannelSet modifies a single field on a channel.
func (h *CommandHandler) cmdChannelSet(channel, target, field string, values []string) {
	pw := h.loadPermWriter()
	if pw == nil {
		h.send(channel, "permissions file not configured")
		return
	}

	cfg := h.loadPermissions().Config()
	ch := cfg.GetChannel(target)

	// Find the canonical key (or use target as-is for new channels).
	canonicalTarget := target
	for k := range cfg.Channels {
		if strings.EqualFold(k, target) {
			canonicalTarget = k
			break
		}
	}

	switch field {
	case "tools":
		ch.Tools = parseCSV(values)
	case "deny":
		ch.DenyTools = parseCSV(values)
	case "autonomy":
		ch.Autonomy = values[0]
	case "model":
		ch.AllowedModels = parseCSV(values)
	default:
		h.send(channel, fmt.Sprintf("unknown field %q (use tools, deny, autonomy, model)", field))
		return
	}

	if err := pw.WriteChannel(canonicalTarget, ch); err != nil {
		h.send(channel, fmt.Sprintf("error: %v", err))
		return
	}

	h.reloadPermissions(channel)
	h.send(channel, fmt.Sprintf("channel %q: %s updated", canonicalTarget, field))
}

// reloadPermissions triggers a config reload after a permissions change.
// Errors are logged but not fatal — the change was already persisted.
func (h *CommandHandler) reloadPermissions(channel string) {
	if h.reloader == nil {
		return
	}
	if err := h.reloader.Reload(); err != nil {
		h.logger.Error("failed to reload after permissions change", "error", err)
		h.send(channel, fmt.Sprintf("warning: change saved but reload failed: %v", err))
	}
}

// formatList formats a string slice for display. Returns "(all)" for empty
// lists (which means "no restriction") and joins non-empty lists with ", ".
func formatList(items []string) string {
	if len(items) == 0 {
		return "(all)"
	}
	return strings.Join(items, ", ")
}

// parseCSV splits comma-separated or space-separated values into a list.
// Handles both "shell,mail_read" and "shell mail_read" formats.
func parseCSV(values []string) []string {
	var result []string
	for _, v := range values {
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				result = append(result, item)
			}
		}
	}
	return result
}
