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
