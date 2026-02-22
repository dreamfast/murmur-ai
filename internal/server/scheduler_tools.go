package server

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"murmur/internal/tools"
)

// relativeTimeRe matches relative time strings like "+2h", "+30m", "+1d".
var relativeTimeRe = regexp.MustCompile(`^\+(\d+)([hmd])$`)

// parseReminderTime parses a time string that is either an ISO 8601 timestamp
// or a relative duration (e.g., "+2h", "+30m", "+1d"). Returns the absolute
// time in UTC.
func parseReminderTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	// Try relative time first.
	if m := relativeTimeRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return time.Time{}, fmt.Errorf("parseReminderTime: invalid number %q: %w", m[1], err)
		}
		if n <= 0 {
			return time.Time{}, fmt.Errorf("parseReminderTime: duration must be positive")
		}
		now := time.Now().UTC()
		switch m[2] {
		case "h":
			return now.Add(time.Duration(n) * time.Hour), nil
		case "m":
			return now.Add(time.Duration(n) * time.Minute), nil
		case "d":
			return now.AddDate(0, 0, n), nil
		}
	}

	// Try ISO 8601 with timezone.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}

	// Try ISO 8601 without timezone (assume UTC).
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return t.UTC(), nil
	}

	// Try date-only.
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}

	return time.Time{}, fmt.Errorf("parseReminderTime: unrecognized time format %q (use ISO 8601 like 2026-02-22T15:00:00Z or relative like +2h, +30m, +1d)", s)
}

// RegisterSchedulerTools registers server-side tools for managing scheduled
// tasks and one-shot reminders. These allow the LLM agent to create, list,
// and manage recurring cron tasks and one-time reminders.
func RegisterSchedulerTools(registry *ToolRegistry, scheduler *Scheduler, defaultChannel string) error {
	if scheduler == nil {
		return nil
	}

	schedulerTools := []tools.Tool{
		{
			Name:        "task_add",
			Description: "Schedule a recurring task. The task description is sent to the AI agent on the cron schedule, which then decides what tools to use. Use 5-field cron syntax (minute hour day-of-month month day-of-week). All times are UTC.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"schedule": {
						"type": "string",
						"description": "Cron schedule in 5-field format (e.g. '*/5 * * * *' for every 5 minutes, '0 9 * * 1-5' for weekdays at 9am UTC)"
					},
					"description": {
						"type": "string",
						"description": "What the task should do when it fires (natural language instruction for the AI agent)"
					},
					"provider": {
						"type": "string",
						"description": "Optional LLM provider name to use for this task (e.g. 'openrouter-claude'). If omitted, uses the channel or global default."
					}
				},
				"required": ["schedule", "description"]
			}`),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				schedule, err := tools.RequireStringArg(args, "schedule")
				if err != nil {
					return "", err
				}
				description, err := tools.RequireStringArg(args, "description")
				if err != nil {
					return "", err
				}
				channel := tools.OptionalStringArg(args, "channel", defaultChannel)
				provider := strings.TrimSpace(tools.OptionalStringArg(args, "provider", ""))

				// Extract the requesting user's nick from context for permission tracking.
				// Fail closed: if the nick is missing, reject the request rather than
				// creating a task that bypasses permission filtering.
				createdBy, ok := ctx.Value(requestNickKey{}).(string)
				if !ok || createdBy == "" {
					return "", fmt.Errorf("task_add: unable to identify task creator from context")
				}

				id, err := scheduler.AddTask(description, schedule, description, channel, createdBy, provider)
				if err != nil {
					return "", fmt.Errorf("task_add: %w", err)
				}
				msg := fmt.Sprintf("Task #%d created: %q (schedule: %s, channel: %s", id, description, schedule, channel)
				if provider != "" {
					msg += fmt.Sprintf(", provider: %s", provider)
				}
				msg += ")"
				return msg, nil
			},
		},
		{
			Name:        "reminder_add",
			Description: "Set a one-time reminder that fires at a specific time. The message is sent to the AI agent at the specified time, which then delivers it. The reminder auto-disables after firing. All times are UTC.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"message": {
						"type": "string",
						"description": "The reminder message to deliver when the time arrives"
					},
					"time": {
						"type": "string",
						"description": "When to fire the reminder. ISO 8601 format (e.g. '2026-02-22T15:00:00Z') or relative (e.g. '+2h', '+30m', '+1d')"
					},
					"provider": {
						"type": "string",
						"description": "Optional LLM provider name to use for this reminder (e.g. 'openrouter-claude'). If omitted, uses the channel or global default."
					}
				},
				"required": ["message", "time"]
			}`),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				message, err := tools.RequireStringArg(args, "message")
				if err != nil {
					return "", err
				}
				timeStr, err := tools.RequireStringArg(args, "time")
				if err != nil {
					return "", err
				}
				channel := tools.OptionalStringArg(args, "channel", defaultChannel)
				provider := strings.TrimSpace(tools.OptionalStringArg(args, "provider", ""))

				runAt, err := parseReminderTime(timeStr)
				if err != nil {
					return "", fmt.Errorf("reminder_add: %w", err)
				}

				// Extract the requesting user's nick from context for permission tracking.
				// Fail closed: if the nick is missing, reject the request rather than
				// creating a task that bypasses permission filtering.
				createdBy, ok := ctx.Value(requestNickKey{}).(string)
				if !ok || createdBy == "" {
					return "", fmt.Errorf("reminder_add: unable to identify task creator from context")
				}

				action := "[Reminder] " + message
				id, err := scheduler.AddOneShotTask(message, runAt, action, channel, createdBy, provider)
				if err != nil {
					return "", fmt.Errorf("reminder_add: %w", err)
				}
				msg := fmt.Sprintf("Reminder #%d set: %q (fires at %s, channel: %s", id, message, runAt.Format("2006-01-02 15:04 UTC"), channel)
				if provider != "" {
					msg += fmt.Sprintf(", provider: %s", provider)
				}
				msg += ")"
				return msg, nil
			},
		},
		{
			Name:        "task_list",
			Description: "List all scheduled tasks and reminders with their ID, type, schedule, description, status, and next run time.",
			Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				taskList, err := scheduler.ListTasks()
				if err != nil {
					return "", fmt.Errorf("task_list: %w", err)
				}
				if len(taskList) == 0 {
					return "No scheduled tasks or reminders.", nil
				}
				return "Scheduled tasks:\n" + FormatTaskList(taskList), nil
			},
		},
		{
			Name:        "task_remove",
			Description: "Remove a scheduled task or reminder by its ID.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {
						"type": "integer",
						"description": "Task ID to remove"
					}
				},
				"required": ["id"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				idFloat, ok := args["id"].(float64)
				if !ok {
					return "", fmt.Errorf("task_remove: id is required and must be a number")
				}
				id := int64(idFloat)
				if err := scheduler.RemoveTask(id); err != nil {
					return "", fmt.Errorf("task_remove: %w", err)
				}
				return fmt.Sprintf("Task #%d removed.", id), nil
			},
		},
		{
			Name:        "task_enable",
			Description: "Enable a disabled scheduled task or reminder by its ID.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {
						"type": "integer",
						"description": "Task ID to enable"
					}
				},
				"required": ["id"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				idFloat, ok := args["id"].(float64)
				if !ok {
					return "", fmt.Errorf("task_enable: id is required and must be a number")
				}
				id := int64(idFloat)
				if err := scheduler.EnableTask(id); err != nil {
					return "", fmt.Errorf("task_enable: %w", err)
				}
				return fmt.Sprintf("Task #%d enabled.", id), nil
			},
		},
		{
			Name:        "task_disable",
			Description: "Disable a scheduled task or reminder by its ID without removing it.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {
						"type": "integer",
						"description": "Task ID to disable"
					}
				},
				"required": ["id"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				idFloat, ok := args["id"].(float64)
				if !ok {
					return "", fmt.Errorf("task_disable: id is required and must be a number")
				}
				id := int64(idFloat)
				if err := scheduler.DisableTask(id); err != nil {
					return "", fmt.Errorf("task_disable: %w", err)
				}
				return fmt.Sprintf("Task #%d disabled.", id), nil
			},
		},
	}

	for _, t := range schedulerTools {
		if err := registry.Register(t); err != nil {
			return fmt.Errorf("RegisterSchedulerTools: %w", err)
		}
	}
	return nil
}
