// Package tools defines the tool interface used by Murmur clients to expose
// capabilities to the server's agent loop.
package tools

import (
	"context"
	"encoding/json"

	"murmur/internal/bus"
)

// Tool represents a capability that a client exposes to the server.
// Each tool has a name, description, JSON Schema parameters, and a handler
// function that executes the tool's logic.
type Tool struct {
	// Name is the unique identifier for this tool (e.g., "shell", "mail_read").
	Name string

	// Description is a human-readable description of what this tool does.
	// This is sent to the LLM to help it decide when to use the tool.
	Description string

	// Parameters is the JSON Schema defining the tool's input parameters.
	// This is sent to the LLM for function calling.
	Parameters json.RawMessage

	// Handler executes the tool with the given arguments and returns the result.
	// The context should be used for cancellation and timeouts.
	Handler func(ctx context.Context, args map[string]any) (string, error)
}

// ToBusToolDefs converts a slice of Tools to bus.ToolDef for client
// registration with the server.
func ToBusToolDefs(tools []Tool) []bus.ToolDef {
	defs := make([]bus.ToolDef, len(tools))
	for i, t := range tools {
		defs[i] = bus.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		}
	}
	return defs
}
