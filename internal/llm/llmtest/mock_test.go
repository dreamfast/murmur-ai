package llmtest

import (
	"context"
	"errors"
	"testing"

	"murmur/internal/llm"
)

func TestMockProvider_Name(t *testing.T) {
	t.Parallel()
	m := &MockProvider{NameVal: "mock"}
	if m.Name() != "mock" {
		t.Errorf("Name() = %q, want %q", m.Name(), "mock")
	}
}

func TestMockProvider_ReturnsResponses(t *testing.T) {
	t.Parallel()

	m := &MockProvider{
		NameVal: "mock",
		Responses: []*llm.ChatResponse{
			{Content: "first"},
			{Content: "second"},
		},
	}

	resp1, err := m.ChatCompletion(context.Background(), &llm.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp1.Content != "first" {
		t.Errorf("first response = %q, want %q", resp1.Content, "first")
	}

	resp2, err := m.ChatCompletion(context.Background(), &llm.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp2.Content != "second" {
		t.Errorf("second response = %q, want %q", resp2.Content, "second")
	}
}

func TestMockProvider_ReturnsErrors(t *testing.T) {
	t.Parallel()

	expectedErr := errors.New("mock error")
	m := &MockProvider{
		NameVal: "mock",
		Errors:  []error{expectedErr},
	}

	_, err := m.ChatCompletion(context.Background(), &llm.ChatRequest{})
	if !errors.Is(err, expectedErr) {
		t.Errorf("error = %v, want %v", err, expectedErr)
	}
}

func TestMockProvider_RecordsCalls(t *testing.T) {
	t.Parallel()

	m := &MockProvider{NameVal: "mock"}
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}}}

	_, _ = m.ChatCompletion(context.Background(), req)
	_, _ = m.ChatCompletion(context.Background(), req)

	if len(m.Calls) != 2 {
		t.Errorf("expected 2 recorded calls, got %d", len(m.Calls))
	}
}

func TestMockProvider_DefaultResponse(t *testing.T) {
	t.Parallel()

	m := &MockProvider{NameVal: "mock"}
	resp, err := m.ChatCompletion(context.Background(), &llm.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "mock response" {
		t.Errorf("Content = %q, want %q", resp.Content, "mock response")
	}
}
