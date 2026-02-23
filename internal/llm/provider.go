// Package llm provides a multi-provider LLM integration layer using the
// OpenAI-compatible chat completions API with function calling support.
package llm

import (
	"context"
	"encoding/json"
)

// Role constants for message roles in the chat completions API.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Provider defines the interface for LLM providers.
type Provider interface {
	// ChatCompletion sends a chat completion request and returns the response.
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// Name returns the provider's configured name.
	Name() string
}

// ChatRequest holds the parameters for a chat completion request.
type ChatRequest struct {
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
}

// Message represents a single message in the conversation.
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and arguments for a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef defines a tool available to the LLM (OpenAI function calling format).
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a function that the LLM can call.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatResponse holds the result of a chat completion request.
type ChatResponse struct {
	Content          string
	ReasoningContent string
	ToolCalls        []ToolCall
	Usage            Usage
}

// Usage tracks token consumption for a chat completion request.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// SanitizeHistory removes orphaned tool-call sequences from the start of a
// message history. When history is truncated by a LIMIT query, the window may
// begin with tool-result messages whose corresponding assistant tool_calls
// message was outside the window. Providers like Kimi reject these with
// "toolcallid not found". This function strips leading orphaned tool results
// and any assistant tool_calls message whose results are not all present.
func SanitizeHistory(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}

	// Skip leading tool-result messages that have no preceding assistant
	// tool_calls message in the window.
	start := 0
	for start < len(msgs) && msgs[start].Role == RoleTool {
		start++
	}

	// Also skip a leading assistant message with tool_calls if not all of
	// its tool results are present in the remaining messages.
	for start < len(msgs) && msgs[start].Role == RoleAssistant && len(msgs[start].ToolCalls) > 0 {
		// Collect the tool_call IDs from this assistant message.
		needed := make(map[string]struct{}, len(msgs[start].ToolCalls))
		for _, tc := range msgs[start].ToolCalls {
			needed[tc.ID] = struct{}{}
		}

		// Scan forward for matching tool results.
		for j := start + 1; j < len(msgs); j++ {
			if msgs[j].Role == RoleTool && msgs[j].ToolCallID != "" {
				delete(needed, msgs[j].ToolCallID)
			}
			// Stop scanning at the next non-tool message.
			if msgs[j].Role != RoleTool {
				break
			}
		}

		if len(needed) > 0 {
			// Not all tool results are present — skip this assistant message
			// and any following tool results that belong to it.
			start++
			for start < len(msgs) && msgs[start].Role == RoleTool {
				start++
			}
		} else {
			break // All tool results present — this is a valid start.
		}
	}

	if start == 0 {
		return msgs
	}
	return msgs[start:]
}
