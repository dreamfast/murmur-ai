package llm

import (
	"murmur/internal/bus"
)

// ConvertBusTools converts a slice of bus.ToolDef (from client registration)
// to the OpenAI function calling format used in LLM requests.
func ConvertBusTools(busTools []bus.ToolDef) []ToolDef {
	if len(busTools) == 0 {
		return nil
	}
	tools := make([]ToolDef, len(busTools))
	for i, bt := range busTools {
		tools[i] = ToolDef{
			Type: "function",
			Function: FunctionDef{
				Name:        bt.Name,
				Description: bt.Description,
				Parameters:  bt.Parameters,
			},
		}
	}
	return tools
}
