package llm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
)

// writeJSON encodes v as JSON to w, failing the test on error.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("writeJSON: %v", err)
	}
}

// makeTextResponse builds an openAIResponse with a single text choice.
func makeTextResponse(content string, promptTokens, completionTokens, totalTokens int) openAIResponse {
	return openAIResponse{
		Choices: []struct {
			Message struct {
				Role             string     `json:"role"`
				Content          string     `json:"content"`
				ReasoningContent string     `json:"reasoning_content"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Role             string     `json:"role"`
					Content          string     `json:"content"`
					ReasoningContent string     `json:"reasoning_content"`
					ToolCalls        []ToolCall `json:"tool_calls"`
				}{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
		},
	}
}

// makeToolCallResponse builds an openAIResponse with tool calls.
func makeToolCallResponse(toolCalls []ToolCall) openAIResponse {
	return openAIResponse{
		Choices: []struct {
			Message struct {
				Role             string     `json:"role"`
				Content          string     `json:"content"`
				ReasoningContent string     `json:"reasoning_content"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Role             string     `json:"role"`
					Content          string     `json:"content"`
					ReasoningContent string     `json:"reasoning_content"`
					ToolCalls        []ToolCall `json:"tool_calls"`
				}{
					Role:      "assistant",
					ToolCalls: toolCalls,
				},
				FinishReason: "tool_calls",
			},
		},
	}
}

// ---- ConvertBusTools tests ----

func TestConvertBusTools_Empty(t *testing.T) {
	t.Parallel()
	result := ConvertBusTools(nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestConvertBusTools_Single(t *testing.T) {
	t.Parallel()

	params := json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)
	busTools := []bus.ToolDef{
		{Name: "shell", Description: "Run a shell command", Parameters: params},
	}

	result := ConvertBusTools(busTools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].Type != "function" {
		t.Errorf("Type = %q, want %q", result[0].Type, "function")
	}
	if result[0].Function.Name != "shell" {
		t.Errorf("Function.Name = %q, want %q", result[0].Function.Name, "shell")
	}
	if result[0].Function.Description != "Run a shell command" {
		t.Errorf("Function.Description = %q", result[0].Function.Description)
	}
	if string(result[0].Function.Parameters) != string(params) {
		t.Errorf("Function.Parameters = %s, want %s", result[0].Function.Parameters, params)
	}
}

func TestConvertBusTools_Multiple(t *testing.T) {
	t.Parallel()

	busTools := []bus.ToolDef{
		{Name: "shell", Description: "Shell", Parameters: json.RawMessage(`{}`)},
		{Name: "mail_read", Description: "Read mail", Parameters: json.RawMessage(`{}`)},
		{Name: "web_search", Description: "Search web", Parameters: json.RawMessage(`{}`)},
	}

	result := ConvertBusTools(busTools)
	if len(result) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(result))
	}
	for i, bt := range busTools {
		if result[i].Function.Name != bt.Name {
			t.Errorf("tool[%d].Name = %q, want %q", i, result[i].Function.Name, bt.Name)
		}
	}
}

// ---- OpenAICompatProvider tests ----

func newTestProvider(t *testing.T, serverURL string) *OpenAICompatProvider {
	t.Helper()
	cfg := config.LLMProviderConfig{
		APIBase:     serverURL,
		APIKey:      "test-key",
		Model:       "test-model",
		MaxTokens:   1024,
		Temperature: 0.7,
	}
	return NewOpenAICompatProvider("test", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestOpenAICompatProvider_Name(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost")
	if p.Name() != "test" {
		t.Errorf("Name() = %q, want %q", p.Name(), "test")
	}
}

func TestOpenAICompatProvider_TextResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type header = %q", r.Header.Get("Content-Type"))
		}

		writeJSON(t, w, makeTextResponse("Hello, world!", 10, 5, 15))
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	req := &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "Hello"}},
	}

	resp, err := p.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello, world!")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("Usage.PromptTokens = %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("Usage.TotalTokens = %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestOpenAICompatProvider_ToolCallResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify tools were sent in the request.
		var body openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if len(body.Tools) != 1 {
			t.Errorf("expected 1 tool in request, got %d", len(body.Tools))
		}

		writeJSON(t, w, makeToolCallResponse([]ToolCall{
			{
				ID:   "call-123",
				Type: "function",
				Function: FunctionCall{
					Name:      "shell",
					Arguments: `{"command":"ls -la"}`,
				},
			},
		}))
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	req := &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "List files"}},
		Tools: []ToolDef{
			{Type: "function", Function: FunctionDef{Name: "shell", Description: "Run shell", Parameters: json.RawMessage(`{}`)}},
		},
	}

	resp, err := p.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Function.Name != "shell" {
		t.Errorf("ToolCall.Function.Name = %q, want %q", resp.ToolCalls[0].Function.Name, "shell")
	}
	if resp.ToolCalls[0].Function.Arguments != `{"command":"ls -la"}` {
		t.Errorf("ToolCall.Function.Arguments = %q", resp.ToolCalls[0].Function.Arguments)
	}
}

func TestOpenAICompatProvider_MultipleToolCalls(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, makeToolCallResponse([]ToolCall{
			{ID: "call-1", Type: "function", Function: FunctionCall{Name: "tool_a", Arguments: `{}`}},
			{ID: "call-2", Type: "function", Function: FunctionCall{Name: "tool_b", Arguments: `{}`}},
		}))
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	resp, err := p.ChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "test"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Errorf("expected 2 tool calls, got %d", len(resp.ToolCalls))
	}
}

func TestOpenAICompatProvider_4xxError_NoRetry(t *testing.T) {
	t.Parallel()

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		if err := json.NewEncoder(w).Encode(openAIResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    any    `json:"code"`
			}{Message: "invalid api key"},
		}); err != nil {
			t.Errorf("writeJSON: %v", err)
		}
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	_, err := p.ChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", callCount)
	}
}

func TestOpenAICompatProvider_5xxError_Retries(t *testing.T) {
	t.Parallel()

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"server error"}`))
			return
		}
		// Third attempt succeeds.
		writeJSON(t, w, makeTextResponse("success after retry", 0, 0, 0))
	}))
	defer srv.Close()

	// Use a provider with very short backoff for testing.
	cfg := config.LLMProviderConfig{
		APIBase: srv.URL, APIKey: "test", Model: "test", MaxTokens: 100, Temperature: 0.7,
	}
	p := NewOpenAICompatProvider("test", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Override backoff to be instant for tests.
	p.httpClient.Timeout = 5 * time.Second

	resp, err := p.ChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "test"}},
	})
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if resp.Content != "success after retry" {
		t.Errorf("Content = %q", resp.Content)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", callCount)
	}
}

func TestOpenAICompatProvider_EmptyChoices(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, openAIResponse{Choices: nil})
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	_, err := p.ChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
}

func TestOpenAICompatProvider_ContextCancelled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled.
		<-r.Context().Done()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := p.ChatCompletion(ctx, &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// ---- marshalMessages / reasoning mode tests ----

func TestMarshalMessages_ReasoningMode_IncludesReasoningContent(t *testing.T) {
	t.Parallel()

	cfg := config.LLMProviderConfig{
		APIBase: "http://localhost", APIKey: "k", Model: "m",
		MaxTokens: 100, Temperature: 0.7, Reasoning: true,
	}
	p := NewOpenAICompatProvider("kimi", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Type: "function", Function: FunctionCall{Name: "shell", Arguments: "{}"}},
		}, ReasoningContent: "thinking about it"},
		{Role: RoleTool, Content: "ok", ToolCallID: "c1", Name: "shell"},
	}

	raw, err := p.marshalMessages(msgs)
	if err != nil {
		t.Fatalf("marshalMessages: %v", err)
	}

	// The assistant message (index 1) should have reasoning_content.
	var assistantMsg map[string]any
	if err := json.Unmarshal(raw[1], &assistantMsg); err != nil {
		t.Fatalf("unmarshal assistant msg: %v", err)
	}
	rc, ok := assistantMsg["reasoning_content"]
	if !ok {
		t.Fatal("reasoning_content missing from assistant message in reasoning mode")
	}
	if rc != "thinking about it" {
		t.Errorf("reasoning_content = %q, want %q", rc, "thinking about it")
	}

	// The user message (index 0) should NOT have reasoning_content.
	var userMsg map[string]any
	if err := json.Unmarshal(raw[0], &userMsg); err != nil {
		t.Fatalf("unmarshal user msg: %v", err)
	}
	if _, ok := userMsg["reasoning_content"]; ok {
		t.Error("user message should not have reasoning_content")
	}
}

func TestMarshalMessages_ReasoningMode_EmptyReasoningStillPresent(t *testing.T) {
	t.Parallel()

	cfg := config.LLMProviderConfig{
		APIBase: "http://localhost", APIKey: "k", Model: "m",
		MaxTokens: 100, Temperature: 0.7, Reasoning: true,
	}
	p := NewOpenAICompatProvider("kimi", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Assistant message with tool_calls but no reasoning_content (e.g., from GLM history).
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Type: "function", Function: FunctionCall{Name: "dns_check", Arguments: "{}"}},
		}},
	}

	raw, err := p.marshalMessages(msgs)
	if err != nil {
		t.Fatalf("marshalMessages: %v", err)
	}

	var msg map[string]any
	if err := json.Unmarshal(raw[0], &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Even with empty reasoning, the field must be present for Kimi.
	rc, ok := msg["reasoning_content"]
	if !ok {
		t.Fatal("reasoning_content must be present (even empty) for reasoning provider")
	}
	if rc != "" {
		t.Errorf("reasoning_content = %q, want empty string", rc)
	}
}

func TestMarshalMessages_NonReasoningMode_StripsReasoningContent(t *testing.T) {
	t.Parallel()

	cfg := config.LLMProviderConfig{
		APIBase: "http://localhost", APIKey: "k", Model: "m",
		MaxTokens: 100, Temperature: 0.7, Reasoning: false,
	}
	p := NewOpenAICompatProvider("glm", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Message has reasoning_content from Kimi history, but we're sending to GLM.
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "c1", Type: "function", Function: FunctionCall{Name: "shell", Arguments: "{}"}},
		}, ReasoningContent: "kimi was thinking"},
	}

	raw, err := p.marshalMessages(msgs)
	if err != nil {
		t.Fatalf("marshalMessages: %v", err)
	}

	var msg map[string]any
	if err := json.Unmarshal(raw[0], &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Non-reasoning provider should NOT have reasoning_content.
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("reasoning_content should be stripped for non-reasoning provider")
	}
}

func TestMarshalMessages_ReasoningMode_TextOnlyAssistant_NoReasoning(t *testing.T) {
	t.Parallel()

	cfg := config.LLMProviderConfig{
		APIBase: "http://localhost", APIKey: "k", Model: "m",
		MaxTokens: 100, Temperature: 0.7, Reasoning: true,
	}
	p := NewOpenAICompatProvider("kimi", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Text-only assistant message — no tool_calls, so no reasoning_content needed.
	msgs := []Message{
		{Role: RoleAssistant, Content: "Here is your answer"},
	}

	raw, err := p.marshalMessages(msgs)
	if err != nil {
		t.Fatalf("marshalMessages: %v", err)
	}

	var msg map[string]any
	if err := json.Unmarshal(raw[0], &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Text-only assistant messages don't need reasoning_content.
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("text-only assistant message should not have reasoning_content")
	}
}

func TestOpenAICompatProvider_UserAgent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		if ua != "claude-code/1.0" {
			t.Errorf("User-Agent = %q, want %q", ua, "claude-code/1.0")
		}
		writeJSON(t, w, makeTextResponse("ok", 1, 1, 2))
	}))
	defer srv.Close()

	cfg := config.LLMProviderConfig{
		APIBase: srv.URL, APIKey: "k", Model: "m",
		MaxTokens: 100, Temperature: 0.7, UserAgent: "claude-code/1.0",
	}
	p := NewOpenAICompatProvider("kimi", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := p.ChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "test"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAICompatProvider_NoUserAgent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		// Should be Go's default User-Agent, not empty.
		if ua == "claude-code/1.0" {
			t.Error("User-Agent should not be claude-code/1.0 when not configured")
		}
		writeJSON(t, w, makeTextResponse("ok", 1, 1, 2))
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	_, err := p.ChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "test"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAICompatProvider_ReasoningContentPassthrough(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a response with reasoning_content.
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":              "assistant",
						"content":           "Hello!",
						"reasoning_content": "Let me think about this greeting",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8,
			},
		}
		writeJSON(t, w, resp)
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)
	resp, err := p.ChatCompletion(context.Background(), &ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ReasoningContent != "Let me think about this greeting" {
		t.Errorf("ReasoningContent = %q, want %q", resp.ReasoningContent, "Let me think about this greeting")
	}
}
