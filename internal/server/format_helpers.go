package server

import (
	"fmt"
	"sort"
	"strings"

	"murmur/internal/config"
)

// IsNickAllowed checks if a nick is in the allowed users list
// (case-insensitive). Returns false for an empty list — callers should
// treat an empty list as "no restriction" and skip the check entirely.
func IsNickAllowed(nick string, users []string) bool {
	for _, u := range users {
		if strings.EqualFold(u, nick) {
			return true
		}
	}
	return false
}

// FormatList formats a string slice for display. Returns "(all)" for empty
// lists (which means "no restriction") and joins non-empty lists with ", ".
func FormatList(items []string) string {
	if len(items) == 0 {
		return "(all)"
	}
	return strings.Join(items, ", ")
}

// SplitCSV splits a comma-separated string into a list of trimmed, non-empty
// values. Returns nil for empty input.
func SplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

// ParseCSV parses a list of values that may contain comma-separated items.
// Each element in the input slice is split on commas, so
// []string{"a,b", "c"} produces []string{"a", "b", "c"}.
// This handles both "shell,mail_read" (single arg) and "shell mail_read"
// (multiple args already split by the caller) formats.
func ParseCSV(values []string) []string {
	var result []string
	for _, v := range values {
		result = append(result, SplitCSV(v)...)
	}
	return result
}

// FormatTaskList formats a slice of ScheduledTask into a human-readable
// multi-line string. Each task is displayed with its ID, type, name,
// schedule, channel, next run time, status, and creator.
func FormatTaskList(tasks []ScheduledTask) string {
	var lines []string
	for _, t := range tasks {
		status := "enabled"
		if !t.Enabled {
			status = "disabled"
		}
		nextRun := "N/A"
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
		creator := ""
		if t.CreatedBy != "" {
			creator = ", by: " + t.CreatedBy
		}
		lines = append(lines, fmt.Sprintf("  #%d [%s] %q [%s] %s — next: %s — %s%s",
			t.ID, typeLabel, t.Name, schedInfo, t.Channel, nextRun, status, creator))
	}
	return strings.Join(lines, "\n")
}

// FormatUserList formats a map of user permissions into a human-readable
// multi-line string listing each user with their role.
func FormatUserList(users map[string]config.UserPermissions) string {
	nicks := make([]string, 0, len(users))
	for nick := range users {
		nicks = append(nicks, nick)
	}
	sort.Strings(nicks)

	var lines []string
	for _, nick := range nicks {
		u := users[nick]
		role := u.Role
		if role == "" {
			role = "user"
		}
		lines = append(lines, fmt.Sprintf("  %s [%s]", nick, role))
	}
	return strings.Join(lines, "\n")
}

// FormatUserPermissions formats a single user's permissions into a
// human-readable multi-line string.
func FormatUserPermissions(target string, user config.UserPermissions) string {
	role := user.Role
	if role == "" {
		role = "user"
	}
	autonomy := user.Autonomy
	if autonomy == "" {
		autonomy = "(default)"
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("User: %s", target))
	parts = append(parts, fmt.Sprintf("  Role: %s", role))
	parts = append(parts, fmt.Sprintf("  Tools: %s", FormatList(user.Tools)))
	if len(user.DenyTools) > 0 {
		parts = append(parts, fmt.Sprintf("  Deny Tools: %s", FormatList(user.DenyTools)))
	}
	parts = append(parts, fmt.Sprintf("  Autonomy: %s", autonomy))
	parts = append(parts, fmt.Sprintf("  Models: %s", FormatList(user.AllowedModels)))
	if len(user.DenyModels) > 0 {
		parts = append(parts, fmt.Sprintf("  Deny Models: %s", FormatList(user.DenyModels)))
	}
	if user.MaxMessagesPerHour != 0 {
		parts = append(parts, fmt.Sprintf("  Rate Limit: %d/hr", user.MaxMessagesPerHour))
	}
	if user.APIKey != "" {
		parts = append(parts, "  API Key: (set)")
	}
	return strings.Join(parts, "\n")
}

// FormatChannelList formats a map of channel permissions into a human-readable
// multi-line string listing each channel with its autonomy level.
func FormatChannelList(channels map[string]config.ChannelPermissions) string {
	names := make([]string, 0, len(channels))
	for ch := range channels {
		names = append(names, ch)
	}
	sort.Strings(names)

	var lines []string
	for _, ch := range names {
		cp := channels[ch]
		autonomy := cp.Autonomy
		if autonomy == "" {
			autonomy = "(default)"
		}
		lines = append(lines, fmt.Sprintf("  %s [autonomy: %s]", ch, autonomy))
	}
	return strings.Join(lines, "\n")
}

// FormatChannelPermissions formats a single channel's permissions into a
// human-readable multi-line string.
func FormatChannelPermissions(target string, ch config.ChannelPermissions) string {
	autonomy := ch.Autonomy
	if autonomy == "" {
		autonomy = "(default)"
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("Channel: %s", target))
	parts = append(parts, fmt.Sprintf("  Tools: %s", FormatList(ch.Tools)))
	if len(ch.DenyTools) > 0 {
		parts = append(parts, fmt.Sprintf("  Deny Tools: %s", FormatList(ch.DenyTools)))
	}
	parts = append(parts, fmt.Sprintf("  Autonomy: %s", autonomy))
	parts = append(parts, fmt.Sprintf("  Models: %s", FormatList(ch.AllowedModels)))
	return strings.Join(parts, "\n")
}
