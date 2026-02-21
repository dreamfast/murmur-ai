package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockOpenCodeServer creates a test server that simulates the OpenCode API.
// It supports session creation, async message sending, SSE events, and message retrieval.
type mockOpenCodeServer struct {
	mu       sync.Mutex
	sessions map[string]*mockSession
	// sseReady is closed when the SSE handler is ready to send events.
	sseReady chan struct{}
	// sseSend is used to trigger sending SSE events.
	sseSend chan string
	// authUser and authPass for Basic Auth verification.
	authUser string
	authPass string
}

type mockSession struct {
	id       string
	title    string
	messages []openCodeMessageWithParts
}

func newMockOpenCodeServer() *mockOpenCodeServer {
	return &mockOpenCodeServer{
		sessions: make(map[string]*mockSession),
		sseReady: make(chan struct{}),
		sseSend:  make(chan string, 10),
	}
}

func (m *mockOpenCodeServer) handler() http.Handler {
	mux := http.NewServeMux()

	// GET /global/health — health check
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"healthy": true, "version": "test"}`)
	})

	// POST /session — create session
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkAuth(w, r) {
			return
		}
		m.mu.Lock()
		id := fmt.Sprintf("sess-%d", len(m.sessions)+1)
		m.sessions[id] = &mockSession{
			id:    id,
			title: "New Session",
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id": %q, "title": "New Session", "createdAt": "2026-01-01T00:00:00Z"}`, id)
	})

	// GET /session — list sessions
	mux.HandleFunc("GET /session", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkAuth(w, r) {
			return
		}
		m.mu.Lock()
		var parts []string
		for _, s := range m.sessions {
			parts = append(parts, fmt.Sprintf(`{"id": %q, "title": %q, "createdAt": "2026-01-01T00:00:00Z"}`,
				s.id, s.title))
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%s]", strings.Join(parts, ","))
	})

	// GET /session/{id} — get session detail
	mux.HandleFunc("GET /session/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkAuth(w, r) {
			return
		}
		id := r.PathValue("id")

		// Don't match sub-paths like /session/{id}/message
		if strings.Contains(r.URL.Path, "/message") || strings.Contains(r.URL.Path, "/prompt_async") {
			http.NotFound(w, r)
			return
		}

		m.mu.Lock()
		session, ok := m.sessions[id]
		m.mu.Unlock()

		if !ok {
			http.Error(w, `{"error": "session not found"}`, http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id": %q, "title": %q, "createdAt": "2026-01-01T00:00:00Z"}`,
			session.id, session.title)
	})

	// GET /session/{id}/message — get messages
	mux.HandleFunc("GET /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkAuth(w, r) {
			return
		}
		id := r.PathValue("id")

		m.mu.Lock()
		session, ok := m.sessions[id]
		m.mu.Unlock()

		if !ok {
			http.Error(w, `{"error": "session not found"}`, http.StatusNotFound)
			return
		}

		m.mu.Lock()
		var msgs []string
		for _, msg := range session.messages {
			var partStrs []string
			for _, p := range msg.Parts {
				partStrs = append(partStrs, fmt.Sprintf(`{"type": "text", "text": %q}`, p.Text))
			}
			msgs = append(msgs, fmt.Sprintf(`{"info": {"id": %q, "sessionId": %q, "role": %q, "createdAt": "2026-01-01T00:00:00Z"}, "parts": [%s]}`,
				msg.Info.ID, msg.Info.SessionID, msg.Info.Role, strings.Join(partStrs, ",")))
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%s]", strings.Join(msgs, ","))
	})

	// GET /event — global SSE stream
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkAuth(w, r) {
			return
		}
		m.handleSSE(w, r)
	})

	// POST /session/{id}/prompt_async — send message async
	mux.HandleFunc("POST /session/{id}/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkAuth(w, r) {
			return
		}
		id := r.PathValue("id")

		m.mu.Lock()
		session, ok := m.sessions[id]
		if ok {
			session.messages = append(session.messages,
				openCodeMessageWithParts{
					Info:  openCodeMessageInfo{ID: "msg-1", SessionID: id, Role: "user"},
					Parts: []openCodePart{{Type: "text", Text: "test message"}},
				},
				openCodeMessageWithParts{
					Info:  openCodeMessageInfo{ID: "msg-2", SessionID: id, Role: "assistant"},
					Parts: []openCodePart{{Type: "text", Text: "I've completed the task."}},
				},
			)
		}
		m.mu.Unlock()

		if !ok {
			http.Error(w, `{"error": "session not found"}`, http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

func (m *mockOpenCodeServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Send initial server.connected event.
	fmt.Fprint(w, "event: server.connected\ndata: {}\n\n")
	flusher.Flush()

	// Signal that SSE is ready.
	select {
	case <-m.sseReady:
	default:
		close(m.sseReady)
	}

	// Send events from the channel.
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-m.sseSend:
			if !ok {
				return
			}
			fmt.Fprint(w, event)
			flusher.Flush()
		}
	}
}

func (m *mockOpenCodeServer) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if m.authUser == "" && m.authPass == "" {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != m.authUser || pass != m.authPass {
		w.Header().Set("WWW-Authenticate", `Basic realm="opencode"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func TestOpenCode_ChatSuccess(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{
		URL:            server.URL,
		SessionTimeout: 10 * time.Second,
	}

	// Send the idle event after a short delay (with session ID).
	go func() {
		<-mock.sseReady
		time.Sleep(100 * time.Millisecond)
		mock.sseSend <- "event: session.updated\ndata: {\"properties\": {\"sessionId\": \"sess-1\", \"status\": \"idle\"}}\n\n"
	}()

	tool := NewOpenCodeTool(cfg, server.Client())
	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "chat",
		"message": "Write a hello world program",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "I've completed the task") {
		t.Errorf("expected assistant response in result, got %q", result)
	}
	if !strings.Contains(result, "sess-1") {
		t.Errorf("expected session ID in result, got %q", result)
	}
}

func TestOpenCode_ChatSuccessFlatStatus(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{
		URL:            server.URL,
		SessionTimeout: 10 * time.Second,
	}

	// Send idle event with flat structure (no properties wrapper).
	go func() {
		<-mock.sseReady
		time.Sleep(100 * time.Millisecond)
		mock.sseSend <- "event: session.updated\ndata: {\"sessionId\": \"sess-1\", \"status\": \"idle\"}\n\n"
	}()

	tool := NewOpenCodeTool(cfg, server.Client())
	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "chat",
		"message": "Hello",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "I've completed the task") {
		t.Errorf("expected assistant response in result, got %q", result)
	}
}

func TestOpenCode_ListSessions(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	// Pre-populate sessions.
	mock.sessions["sess-1"] = &mockSession{id: "sess-1", title: "Project A"}
	mock.sessions["sess-2"] = &mockSession{id: "sess-2", title: "Project B"}

	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{URL: server.URL, SessionTimeout: 10 * time.Second}
	tool := NewOpenCodeTool(cfg, server.Client())

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "list_sessions",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "2 session(s)") {
		t.Errorf("expected '2 session(s)' in result, got %q", result)
	}
}

func TestOpenCode_ListSessionsEmpty(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{URL: server.URL, SessionTimeout: 10 * time.Second}
	tool := NewOpenCodeTool(cfg, server.Client())

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "list_sessions",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "No sessions found") {
		t.Errorf("expected 'No sessions found', got %q", result)
	}
}

func TestOpenCode_GetSession(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	mock.sessions["sess-1"] = &mockSession{
		id:    "sess-1",
		title: "Test Session",
		messages: []openCodeMessageWithParts{
			{
				Info:  openCodeMessageInfo{ID: "msg-1", SessionID: "sess-1", Role: "user"},
				Parts: []openCodePart{{Type: "text", Text: "Hello"}},
			},
			{
				Info:  openCodeMessageInfo{ID: "msg-2", SessionID: "sess-1", Role: "assistant"},
				Parts: []openCodePart{{Type: "text", Text: "Hi there!"}},
			},
		},
	}

	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{URL: server.URL, SessionTimeout: 10 * time.Second}
	tool := NewOpenCodeTool(cfg, server.Client())

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":     "get_session",
		"session_id": "sess-1",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "Hi there!") {
		t.Errorf("expected assistant message in result, got %q", result)
	}
	if !strings.Contains(result, "sess-1") {
		t.Errorf("expected session ID in result, got %q", result)
	}
}

func TestOpenCode_GetSessionMissing(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{URL: server.URL, SessionTimeout: 10 * time.Second}
	_, err := handleOpenCode(context.Background(), map[string]any{
		"action":     "get_session",
		"session_id": "nonexistent",
	}, cfg, 10*time.Second, server.Client())
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 in error, got %q", err.Error())
	}
}

func TestOpenCode_Timeout(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{
		URL:            server.URL,
		SessionTimeout: 500 * time.Millisecond,
	}

	// Don't send any SSE events — should timeout.
	tool := NewOpenCodeTool(cfg, server.Client())
	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "chat",
		"message": "Do something slow",
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") && !strings.Contains(err.Error(), "context") {
		t.Errorf("expected timeout/context error, got %q", err.Error())
	}
}

func TestOpenCode_BasicAuth(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	mock.authUser = "admin"
	mock.authPass = "secret"

	server := httptest.NewServer(mock.handler())
	defer server.Close()

	// Without auth — should fail.
	cfg := OpenCodeToolConfig{URL: server.URL, SessionTimeout: 10 * time.Second}
	_, err := handleOpenCode(context.Background(), map[string]any{
		"action": "list_sessions",
	}, cfg, 10*time.Second, server.Client())
	if err == nil {
		t.Fatal("expected auth error without credentials")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got %q", err.Error())
	}

	// With auth — should succeed.
	cfgAuth := OpenCodeToolConfig{
		URL:            server.URL,
		Username:       "admin",
		Password:       "secret",
		SessionTimeout: 10 * time.Second,
	}
	result, err := handleOpenCode(context.Background(), map[string]any{
		"action": "list_sessions",
	}, cfgAuth, 10*time.Second, server.Client())
	if err != nil {
		t.Fatalf("Handler with auth: %v", err)
	}
	if !strings.Contains(result, "No sessions found") {
		t.Errorf("expected 'No sessions found', got %q", result)
	}
}

func TestOpenCode_Unreachable(t *testing.T) {
	t.Parallel()

	cfg := OpenCodeToolConfig{
		URL:            "http://127.0.0.1:1", // Unreachable port.
		SessionTimeout: 2 * time.Second,
	}
	client := &http.Client{Timeout: 1 * time.Second}

	_, err := handleOpenCode(context.Background(), map[string]any{
		"action": "list_sessions",
	}, cfg, 2*time.Second, client)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestOpenCode_UnknownAction(t *testing.T) {
	t.Parallel()

	cfg := OpenCodeToolConfig{URL: "http://localhost:3000", SessionTimeout: 10 * time.Second}
	_, err := handleOpenCode(context.Background(), map[string]any{
		"action": "invalid",
	}, cfg, 10*time.Second, &http.Client{})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' in error, got %q", err.Error())
	}
}

func TestOpenCode_MissingAction(t *testing.T) {
	t.Parallel()

	cfg := OpenCodeToolConfig{URL: "http://localhost:3000", SessionTimeout: 10 * time.Second}
	tool := NewOpenCodeTool(cfg, nil)

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing action")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected 'missing required argument' error, got %q", err.Error())
	}
}

func TestOpenCode_ChatMissingMessage(t *testing.T) {
	t.Parallel()

	cfg := OpenCodeToolConfig{URL: "http://localhost:3000", SessionTimeout: 10 * time.Second}
	_, err := handleOpenCode(context.Background(), map[string]any{
		"action": "chat",
	}, cfg, 10*time.Second, &http.Client{})
	if err == nil {
		t.Fatal("expected error for missing message")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected 'missing required argument' error, got %q", err.Error())
	}
}

func TestOpenCode_GetSessionMissingID(t *testing.T) {
	t.Parallel()

	cfg := OpenCodeToolConfig{URL: "http://localhost:3000", SessionTimeout: 10 * time.Second}
	_, err := handleOpenCode(context.Background(), map[string]any{
		"action": "get_session",
	}, cfg, 10*time.Second, &http.Client{})
	if err == nil {
		t.Fatal("expected error for missing session_id")
	}
}

func TestOpenCode_SSEMultiLineData(t *testing.T) {
	t.Parallel()

	// Test that multi-line SSE data fields are handled correctly.
	// Per SSE spec, multiple data: lines are joined with newlines.
	eventCh := make(chan openCodeSSEEvent, 10)
	errCh := make(chan error, 1)

	sseData := "event: session.idle\ndata: {\"status\":\ndata: \"idle\"}\n\n"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go parseSSEStream(ctx, strings.NewReader(sseData), eventCh, errCh)

	select {
	case event := <-eventCh:
		if event.Event != "session.idle" {
			t.Errorf("expected event 'session.idle', got %q", event.Event)
		}
		// Multi-line data should be joined with newlines.
		expected := "{\"status\":\n\"idle\"}"
		if event.Data != expected {
			t.Errorf("expected data %q, got %q", expected, event.Data)
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-ctx.Done():
		t.Fatal("timeout waiting for SSE event")
	}
}

func TestOpenCode_SSEErrorStatus(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{
		URL:            server.URL,
		SessionTimeout: 10 * time.Second,
	}

	// Send an error status event.
	go func() {
		<-mock.sseReady
		time.Sleep(100 * time.Millisecond)
		mock.sseSend <- "event: session.updated\ndata: {\"properties\": {\"sessionId\": \"sess-1\", \"status\": \"error\"}}\n\n"
	}()

	tool := NewOpenCodeTool(cfg, server.Client())
	_, err := tool.Handler(context.Background(), map[string]any{
		"action":  "chat",
		"message": "Do something that fails",
	})
	if err == nil {
		t.Fatal("expected error for error status")
	}
	if !strings.Contains(err.Error(), "error") {
		t.Errorf("expected 'error' in error message, got %q", err.Error())
	}
}

func TestOpenCode_FormatMessages_NoMessages(t *testing.T) {
	t.Parallel()

	result := formatOpenCodeMessages("sess-1", nil)
	if !strings.Contains(result, "No messages") {
		t.Errorf("expected 'No messages' in result, got %q", result)
	}
}

func TestOpenCode_FormatMessages_NoAssistant(t *testing.T) {
	t.Parallel()

	messages := []openCodeMessageWithParts{
		{
			Info:  openCodeMessageInfo{ID: "msg-1", Role: "user"},
			Parts: []openCodePart{{Type: "text", Text: "Hello"}},
		},
		{
			Info:  openCodeMessageInfo{ID: "msg-2", Role: "system"},
			Parts: []openCodePart{{Type: "text", Text: "System message"}},
		},
	}
	result := formatOpenCodeMessages("sess-1", messages)
	// Should fall back to showing all messages.
	if !strings.Contains(result, "[user]") {
		t.Errorf("expected '[user]' in fallback format, got %q", result)
	}
	if !strings.Contains(result, "[system]") {
		t.Errorf("expected '[system]' in fallback format, got %q", result)
	}
}

func TestOpenCode_ExtractTextFromParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		parts []openCodePart
		want  string
	}{
		{
			name:  "empty",
			parts: nil,
			want:  "",
		},
		{
			name: "single text",
			parts: []openCodePart{
				{Type: "text", Text: "hello"},
			},
			want: "hello",
		},
		{
			name: "multiple text parts",
			parts: []openCodePart{
				{Type: "text", Text: "hello"},
				{Type: "text", Text: "world"},
			},
			want: "hello\nworld",
		},
		{
			name: "mixed types",
			parts: []openCodePart{
				{Type: "text", Text: "hello"},
				{Type: "tool-invocation", ToolName: "read_file"},
				{Type: "text", Text: "done"},
			},
			want: "hello\ndone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractTextFromParts(tt.parts)
			if got != tt.want {
				t.Errorf("extractTextFromParts() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenCode_SSEFiltersBySessionID(t *testing.T) {
	t.Parallel()

	mock := newMockOpenCodeServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()

	cfg := OpenCodeToolConfig{
		URL:            server.URL,
		SessionTimeout: 5 * time.Second,
	}

	// Send an idle event for a DIFFERENT session first, then for ours.
	go func() {
		<-mock.sseReady
		time.Sleep(100 * time.Millisecond)
		// This should be ignored (wrong session ID).
		mock.sseSend <- "event: session.updated\ndata: {\"properties\": {\"sessionId\": \"other-session\", \"status\": \"idle\"}}\n\n"
		time.Sleep(50 * time.Millisecond)
		// This should match.
		mock.sseSend <- "event: session.updated\ndata: {\"properties\": {\"sessionId\": \"sess-1\", \"status\": \"idle\"}}\n\n"
	}()

	tool := NewOpenCodeTool(cfg, server.Client())
	result, err := tool.Handler(context.Background(), map[string]any{
		"action":  "chat",
		"message": "Test filtering",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "sess-1") {
		t.Errorf("expected session ID in result, got %q", result)
	}
}
