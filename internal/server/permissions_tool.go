package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"murmur/internal/config"
	"murmur/internal/tools"
)

// RegisterPermissionsTool registers the permissions_manage server-side tool on
// the given ToolRegistry. The tool provides natural language admin access to
// user and channel permission management via the LLM. It is only visible to
// admin users because PermissionManager.FilterTools removes it for non-admins.
// As defense-in-depth, the handler also verifies admin status via the context.
func RegisterPermissionsTool(registry *ToolRegistry, pw *PermissionsWriter, pm *PermissionManager, reloader Reloader, logger *slog.Logger) error {
	t := tools.Tool{
		Name:        "permissions_manage",
		Description: "Manage user and channel permissions. Admin only. Use this tool to list, inspect, add, remove, or modify user/channel permission settings.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"description": "The action to perform",
					"enum": [
						"list_users", "get_user", "add_user", "remove_user",
						"set_user_role", "set_user_tools", "set_user_deny",
						"set_user_autonomy", "set_user_model", "set_user_ratelimit",
						"list_channels", "get_channel",
						"set_channel_tools", "set_channel_deny",
						"set_channel_autonomy", "set_channel_model"
					]
				},
				"nick": {
					"type": "string",
					"description": "Target user nick (required for user actions except list_users)"
				},
				"channel": {
					"type": "string",
					"description": "Target channel name (required for channel actions except list_channels)"
				},
				"value": {
					"type": "string",
					"description": "Value to set (for set_* actions). For list fields (tools, deny, model), use comma-separated values."
				},
				"role": {
					"type": "string",
					"description": "Role for add_user action (admin or user, defaults to user)"
				}
			},
			"required": ["action"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handlePermissionsManage(ctx, args, pw, pm, reloader, logger)
		},
	}

	if err := registry.Register(t); err != nil {
		return fmt.Errorf("RegisterPermissionsTool: %w", err)
	}
	logger.Info("enabled server tool", "name", "permissions_manage")
	return nil
}

// handlePermissionsManage dispatches the permissions_manage tool call to the
// appropriate action handler. It performs defense-in-depth admin verification
// by extracting the requesting nick from the context.
func handlePermissionsManage(ctx context.Context, args map[string]any, pw *PermissionsWriter, pm *PermissionManager, reloader Reloader, logger *slog.Logger) (string, error) {
	// Defense-in-depth: verify the requesting user is an admin.
	// The tool should only be visible to admins (FilterTools removes it for
	// non-admins), but we check again here in case of a bug in filtering.
	nick, _ := ctx.Value(requestNickKey{}).(string)
	if nick == "" || !pm.IsAdmin(nick) {
		return "", fmt.Errorf("permission denied: admin role required")
	}

	action, err := tools.RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	switch action {
	// User actions.
	case "list_users":
		return permListUsers(pm)
	case "get_user":
		return permGetUser(args, pm)
	case "add_user":
		return permAddUser(args, pw, pm, reloader, logger)
	case "remove_user":
		return permRemoveUser(args, pw, reloader, logger)
	case "set_user_role":
		return permSetUserField(args, pw, pm, reloader, logger, "role")
	case "set_user_tools":
		return permSetUserField(args, pw, pm, reloader, logger, "tools")
	case "set_user_deny":
		return permSetUserField(args, pw, pm, reloader, logger, "deny")
	case "set_user_autonomy":
		return permSetUserField(args, pw, pm, reloader, logger, "autonomy")
	case "set_user_model":
		return permSetUserField(args, pw, pm, reloader, logger, "model")
	case "set_user_ratelimit":
		return permSetUserField(args, pw, pm, reloader, logger, "ratelimit")

	// Channel actions.
	case "list_channels":
		return permListChannels(pm)
	case "get_channel":
		return permGetChannel(args, pm)
	case "set_channel_tools":
		return permSetChannelField(args, pw, pm, reloader, logger, "tools")
	case "set_channel_deny":
		return permSetChannelField(args, pw, pm, reloader, logger, "deny")
	case "set_channel_autonomy":
		return permSetChannelField(args, pw, pm, reloader, logger, "autonomy")
	case "set_channel_model":
		return permSetChannelField(args, pw, pm, reloader, logger, "model")

	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// permListUsers returns a formatted list of all configured users.
func permListUsers(pm *PermissionManager) (string, error) {
	cfg := pm.Config()
	if len(cfg.Users) == 0 {
		return "No users configured.", nil
	}
	return "Users:\n" + FormatUserList(cfg.Users) + "\n", nil
}

// permGetUser returns detailed information about a user.
func permGetUser(args map[string]any, pm *PermissionManager) (string, error) {
	target, err := tools.RequireStringArg(args, "nick")
	if err != nil {
		return "", err
	}

	cfg := pm.Config()
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
		return fmt.Sprintf("User %q not found (using default permissions).", target), nil
	}

	return FormatUserPermissions(target, user) + "\n", nil
}

// permAddUser adds a new user with the given role.
func permAddUser(args map[string]any, pw *PermissionsWriter, pm *PermissionManager, reloader Reloader, logger *slog.Logger) (string, error) {
	target, err := tools.RequireStringArg(args, "nick")
	if err != nil {
		return "", err
	}

	role := tools.OptionalStringArg(args, "role", "user")

	// Check if user already exists.
	cfg := pm.Config()
	for k := range cfg.Users {
		if strings.EqualFold(k, target) {
			return fmt.Sprintf("User %q already exists.", k), nil
		}
	}

	user := config.UserPermissions{Role: role}
	if err := pw.WriteUser(target, user); err != nil {
		return "", fmt.Errorf("permAddUser: %w", err)
	}

	if err := reloadPermissions(reloader, logger); err != nil {
		return fmt.Sprintf("User %q added with role %q, but reload failed: %v", target, role, err), nil
	}
	return fmt.Sprintf("User %q added with role %q.", target, role), nil
}

// permRemoveUser removes a user.
func permRemoveUser(args map[string]any, pw *PermissionsWriter, reloader Reloader, logger *slog.Logger) (string, error) {
	target, err := tools.RequireStringArg(args, "nick")
	if err != nil {
		return "", err
	}

	if err := pw.RemoveUser(target); err != nil {
		return "", fmt.Errorf("permRemoveUser: %w", err)
	}

	if err := reloadPermissions(reloader, logger); err != nil {
		return fmt.Sprintf("User %q removed, but reload failed: %v", target, err), nil
	}
	return fmt.Sprintf("User %q removed.", target), nil
}

// permSetUserField modifies a single field on a user.
func permSetUserField(args map[string]any, pw *PermissionsWriter, pm *PermissionManager, reloader Reloader, logger *slog.Logger, field string) (string, error) {
	target, err := tools.RequireStringArg(args, "nick")
	if err != nil {
		return "", err
	}

	value := tools.OptionalStringArg(args, "value", "")

	cfg := pm.Config()
	user := cfg.GetUser(target)

	// Ensure the user exists — we don't create users implicitly.
	canonicalTarget := ""
	for k := range cfg.Users {
		if strings.EqualFold(k, target) {
			canonicalTarget = k
			break
		}
	}
	if canonicalTarget == "" {
		return fmt.Sprintf("User %q not found. Use add_user first.", target), nil
	}

	switch field {
	case "role":
		if value == "" {
			return "Value is required for role.", nil
		}
		user.Role = value
	case "tools":
		user.Tools = SplitCSV(value)
	case "deny":
		user.DenyTools = SplitCSV(value)
	case "autonomy":
		if value == "" {
			return "Value is required for autonomy.", nil
		}
		user.Autonomy = value
	case "model":
		user.AllowedModels = SplitCSV(value)
	case "ratelimit":
		if value == "" {
			return "Value is required for ratelimit.", nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return "Rate limit must be a number (-1 for unlimited).", nil
		}
		user.MaxMessagesPerHour = n
	}

	if err := pw.WriteUser(canonicalTarget, user); err != nil {
		return "", fmt.Errorf("permSetUserField: %w", err)
	}

	if err := reloadPermissions(reloader, logger); err != nil {
		return fmt.Sprintf("User %q: %s updated, but reload failed: %v", canonicalTarget, field, err), nil
	}
	return fmt.Sprintf("User %q: %s updated.", canonicalTarget, field), nil
}

// permListChannels returns a formatted list of all configured channels.
func permListChannels(pm *PermissionManager) (string, error) {
	cfg := pm.Config()
	if len(cfg.Channels) == 0 {
		return "No channels configured.", nil
	}
	return "Channels:\n" + FormatChannelList(cfg.Channels) + "\n", nil
}

// permGetChannel returns detailed information about a channel.
func permGetChannel(args map[string]any, pm *PermissionManager) (string, error) {
	target, err := tools.RequireStringArg(args, "channel")
	if err != nil {
		return "", err
	}

	cfg := pm.Config()

	// Check if the channel actually exists.
	found := false
	canonicalTarget := target
	for k := range cfg.Channels {
		if strings.EqualFold(k, target) {
			found = true
			canonicalTarget = k
			break
		}
	}
	if !found {
		return fmt.Sprintf("Channel %q not configured.", target), nil
	}

	return FormatChannelPermissions(canonicalTarget, cfg.Channels[canonicalTarget]) + "\n", nil
}

// permSetChannelField modifies a single field on a channel.
func permSetChannelField(args map[string]any, pw *PermissionsWriter, pm *PermissionManager, reloader Reloader, logger *slog.Logger, field string) (string, error) {
	target, err := tools.RequireStringArg(args, "channel")
	if err != nil {
		return "", err
	}

	value := tools.OptionalStringArg(args, "value", "")

	cfg := pm.Config()
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
		ch.Tools = SplitCSV(value)
	case "deny":
		ch.DenyTools = SplitCSV(value)
	case "autonomy":
		if value == "" {
			return "Value is required for autonomy.", nil
		}
		ch.Autonomy = value
	case "model":
		ch.AllowedModels = SplitCSV(value)
	}

	if err := pw.WriteChannel(canonicalTarget, ch); err != nil {
		return "", fmt.Errorf("permSetChannelField: %w", err)
	}

	if err := reloadPermissions(reloader, logger); err != nil {
		return fmt.Sprintf("Channel %q: %s updated, but reload failed: %v", canonicalTarget, field, err), nil
	}
	return fmt.Sprintf("Channel %q: %s updated.", canonicalTarget, field), nil
}

// reloadPermissions triggers a config reload after a permissions change.
// Returns an error if the reload fails; the caller decides how to report it.
func reloadPermissions(reloader Reloader, logger *slog.Logger) error {
	if reloader == nil {
		return nil
	}
	if err := reloader.Reload(); err != nil {
		logger.Error("failed to reload after permissions change", "error", err)
		return err
	}
	return nil
}
