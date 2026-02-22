// Package llmtest provides test helpers for the llm package.
package llmtest

import (
	"context"
	"sync"

	"murmur/internal/llm"
)

// MockProvider is a test helper that returns predetermined responses.
// It records all calls for assertions.
type MockProvider struct {
	NameVal   string
	Responses []*llm.ChatResponse
	Errors    []error
	Calls     []*llm.ChatRequest

	mu      sync.Mutex
	callIdx int
}

// Name returns the mock provider's name.
func (m *MockProvider) Name() string {
	return m.NameVal
}

// ChatCompletion returns the next predetermined response or error.
// It records the request for later assertions.
func (m *MockProvider) ChatCompletion(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
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
		return &llm.ChatResponse{Content: "mock response"}, nil
	}
	respIdx := idx % len(m.Responses)
	return m.Responses[respIdx], nil
}
