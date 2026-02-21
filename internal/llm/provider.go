// Package llm provides a multi-provider LLM integration layer using the
// OpenAI-compatible chat completions API with function calling support.
package llm

import (
	"context"
	"encoding/json"
	"sync"
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

// MockProvider is a test helper that returns predetermined responses.
// It records all calls for assertions.
type MockProvider struct {
	NameVal   string
	Responses []*ChatResponse
	Errors    []error
	Calls     []*ChatRequest

	mu      sync.Mutex
	callIdx int
}

// Name returns the mock provider's name.
func (m *MockProvider) Name() string {
	return m.NameVal
}

// ChatCompletion returns the next predetermined response or error.
// It records the request for later assertions.
func (m *MockProvider) ChatCompletion(_ context.Context, req *ChatRequest) (*ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Calls = append(m.Calls, req)
	idx := m.callIdx
	m.callIdx++

	// Return error if configured for this index.
	if idx < len(m.Errors) && m.Errors[idx] != nil {
		return nil, m.Errors[idx]
	}

	// Return response, cycling if we run out.
	if len(m.Responses) == 0 {
		return &ChatResponse{Content: "mock response"}, nil
	}
	respIdx := idx % len(m.Responses)
	return m.Responses[respIdx], nil
}
