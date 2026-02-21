package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"murmur/internal/tools"
)

// RegisterSchedulerTools registers server-side tools for managing scheduled
// tasks. These allow the LLM agent to create, list, and remove recurring
// tasks that fire on a cron schedule.
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
					}
				},
				"required": ["schedule", "description"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				schedule, err := tools.RequireStringArg(args, "schedule")
				if err != nil {
					return "", err
				}
				description, err := tools.RequireStringArg(args, "description")
				if err != nil {
					return "", err
				}
				channel := tools.OptionalStringArg(args, "channel", defaultChannel)

				id, err := scheduler.AddTask(description, schedule, description, channel)
				if err != nil {
					return "", fmt.Errorf("task_add: %w", err)
				}
				return fmt.Sprintf("Task #%d created: %q (schedule: %s, channel: %s)", id, description, schedule, channel), nil
			},
		},
		{
			Name:        "task_list",
			Description: "List all scheduled tasks with their ID, schedule, description, status, and next run time.",
			Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				taskList, err := scheduler.ListTasks()
				if err != nil {
					return "", fmt.Errorf("task_list: %w", err)
				}
				if len(taskList) == 0 {
					return "No scheduled tasks.", nil
				}
				var lines []string
				for _, t := range taskList {
					status := "enabled"
					if !t.Enabled {
						status = "disabled"
					}
					nextRun := "N/A"
					if t.NextRun.Valid {
						nextRun = t.NextRun.Time.Format("2006-01-02 15:04 UTC")
					}
					lines = append(lines, fmt.Sprintf("  #%d [%s] %s — %q (next: %s)", t.ID, status, t.Schedule, t.Name, nextRun))
				}
				return "Scheduled tasks:\n" + strings.Join(lines, "\n"), nil
			},
		},
		{
			Name:        "task_remove",
			Description: "Remove a scheduled task by its ID.",
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
			Description: "Enable a disabled scheduled task by its ID.",
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
			Description: "Disable a scheduled task by its ID without removing it.",
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
