package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"murmur/internal/api"
	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/db"
	"murmur/internal/llm"
	"murmur/internal/llm/llmtest"
)

// testAPIEnv holds the components needed for API handler tests.
type testAPIEnv struct {
	server  *Server
	handler http.Handler
}

// newTestAPIEnv creates a minimal Server with an in-memory database and the
// API mux wired up. The agent uses a mock LLM provider so HandleEvent calls
// complete immediately.
func newTestAPIEnv(t *testing.T) *testAPIEnv {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	registry := NewRegistry(2*time.Minute, logger)
	memory := NewMemory(database, 100, 80, nil, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	serverTools := NewToolRegistry()

	mock := &llmtest.MockProvider{
		NameVal: "test-provider",
		Responses: []*llm.ChatResponse{
			{Content: "event processed"},
		},
	}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(
		providers,
		"test-provider",
		serverTools,
		registry,
		memory,
		router,
		nil, // no approval manager
		nil, // no IRC connection
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,   // cross-channel context disabled in tests
		nil, // no channel settings
		2*time.Second,
		2*time.Second,
		false,
		config.DebugConfig{},
		logger,
	)
	// Suppress IRC sends in tests.
	agent.sendFunc = func(_, _ string) {}

	cfg := &config.ServerConfig{}
	cfg.Server.Name = "test-murmur"
	cfg.IRC.Channels.Main = "#murmur"
	cfg.API.Enabled = true
	cfg.API.Listen = "127.0.0.1:0"
	cfg.API.APIKey = "test-secret"

	s := &Server{
		cfg:         cfg,
		registry:    registry,
		logger:      logger,
		database:    database,
		serverTools: serverTools,
		agent:       agent,
		startTime:   time.Now(),
	}

	handler := newServerAPIMux(s)

	return &testAPIEnv{
		server:  s,
		handler: handler,
	}
}

// doRequest is a helper that creates and executes an HTTP request against the
// test handler, returning the response recorder.
func (env *testAPIEnv) doRequest(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)
	return w
}

// parseResponse decodes the JSON response envelope.
func parseResponse(t *testing.T, w *httptest.ResponseRecorder) api.Response {
	t.Helper()
	var resp api.Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	return resp
}

func TestAPI_Health(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	w := env.doRequest(t, "GET", "/api/v1/health", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	if !resp.OK {
		t.Error("expected ok=true")
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want map", resp.Data)
	}
	if data["status"] != "ok" {
		t.Errorf("status = %v, want ok", data["status"])
	}
}

func TestAPI_Status(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	w := env.doRequest(t, "GET", "/api/v1/status", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	if !resp.OK {
		t.Error("expected ok=true")
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want map", resp.Data)
	}
	if data["server"] != "test-murmur" {
		t.Errorf("server = %v, want test-murmur", data["server"])
	}
	if data["provider"] != "test-provider" {
		t.Errorf("provider = %v, want test-provider", data["provider"])
	}
	if data["api_version"] != "v1" {
		t.Errorf("api_version = %v, want v1", data["api_version"])
	}
	// Uptime should be a non-empty string.
	uptime, ok := data["uptime"].(string)
	if !ok || uptime == "" {
		t.Errorf("uptime = %v, want non-empty string", data["uptime"])
	}
}

func TestAPI_Clients_Empty(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	w := env.doRequest(t, "GET", "/api/v1/clients", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	if !resp.OK {
		t.Error("expected ok=true")
	}

	// Data should be an empty array (not null).
	data, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("data type = %T, want []any", resp.Data)
	}
	if len(data) != 0 {
		t.Errorf("expected 0 clients, got %d", len(data))
	}
}

func TestAPI_Clients_WithRegistered(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	// Register a client.
	env.server.registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "test-desktop",
		Hostname: "testhost",
		Autonomy: "auto",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)},
			{Name: "mail_read", Description: "Read mail", Parameters: json.RawMessage(`{}`)},
		},
	})

	w := env.doRequest(t, "GET", "/api/v1/clients", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	data, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("data type = %T, want []any", resp.Data)
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 client, got %d", len(data))
	}

	client, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("client type = %T, want map", data[0])
	}
	if client["client_id"] != "test-desktop" {
		t.Errorf("client_id = %v, want test-desktop", client["client_id"])
	}
	if client["hostname"] != "testhost" {
		t.Errorf("hostname = %v, want testhost", client["hostname"])
	}
	if client["autonomy"] != "auto" {
		t.Errorf("autonomy = %v, want auto", client["autonomy"])
	}
	if client["status"] != "online" {
		t.Errorf("status = %v, want online", client["status"])
	}

	tools, ok := client["tools"].([]any)
	if !ok {
		t.Fatalf("tools type = %T, want []any", client["tools"])
	}
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
}

func TestAPI_PostEvent_Success(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	body := `{"source":"backup-script","event_type":"backup.completed","summary":"Backup finished","data":"{\"size\":\"1.2GB\"}"}`
	w := env.doRequest(t, "POST", "/api/v1/events", body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusAccepted, w.Body.String())
	}

	resp := parseResponse(t, w)
	if !resp.OK {
		t.Error("expected ok=true")
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want map", resp.Data)
	}
	if data["message"] != "event accepted" {
		t.Errorf("message = %v, want 'event accepted'", data["message"])
	}
	// ID should be a number > 0.
	id, ok := data["id"].(float64)
	if !ok || id <= 0 {
		t.Errorf("id = %v, want positive number", data["id"])
	}

	// Wait for the agent goroutine to finish.
	env.server.agentWg.Wait()
}

func TestAPI_PostEvent_MissingFields(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	tests := []struct {
		name string
		body string
	}{
		{"missing source", `{"event_type":"test","summary":"test"}`},
		{"missing event_type", `{"source":"test","summary":"test"}`},
		{"missing summary", `{"source":"test","event_type":"test"}`},
		{"empty body", `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := env.doRequest(t, "POST", "/api/v1/events", tt.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestAPI_PostEvent_InvalidJSON(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	w := env.doRequest(t, "POST", "/api/v1/events", "not json")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPI_PostEvent_Idempotency(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	body := `{"source":"test","event_type":"test","summary":"test event","event_id":"unique-123"}`

	// First request — should be accepted.
	w1 := env.doRequest(t, "POST", "/api/v1/events", body)
	if w1.Code != http.StatusAccepted {
		t.Fatalf("first request: status = %d, want %d", w1.Code, http.StatusAccepted)
	}
	env.server.agentWg.Wait()

	resp1 := parseResponse(t, w1)
	data1, ok := resp1.Data.(map[string]any)
	if !ok {
		t.Fatalf("data1 type = %T, want map", resp1.Data)
	}
	firstID := data1["id"].(float64)

	// Second request with same event_id — should be deduplicated.
	w2 := env.doRequest(t, "POST", "/api/v1/events", body)
	if w2.Code != http.StatusOK {
		t.Fatalf("second request: status = %d, want %d (body: %s)", w2.Code, http.StatusOK, w2.Body.String())
	}

	resp2 := parseResponse(t, w2)
	data2, ok := resp2.Data.(map[string]any)
	if !ok {
		t.Fatalf("data2 type = %T, want map", resp2.Data)
	}
	if data2["duplicate"] != true {
		t.Errorf("duplicate = %v, want true", data2["duplicate"])
	}
	secondID := data2["id"].(float64)
	if firstID != secondID {
		t.Errorf("IDs differ: first=%v, second=%v", firstID, secondID)
	}
}

func TestAPI_PostEvent_DefaultChannel(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	// Post without channel — should use the main channel from config.
	body := `{"source":"test","event_type":"test","summary":"test event","event_id":"chan-test-1"}`
	w := env.doRequest(t, "POST", "/api/v1/events", body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	env.server.agentWg.Wait()

	// Verify the event was stored with the default channel.
	events, err := env.server.database.ListEvents(t.Context(), db.EventsQuery{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least 1 event")
	}
	if events[0].Channel != "#murmur" {
		t.Errorf("channel = %q, want #murmur", events[0].Channel)
	}
}

func TestAPI_PostEvent_CustomChannel(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	body := `{"source":"test","event_type":"test","summary":"test event","channel":"#alerts","event_id":"chan-test-2"}`
	w := env.doRequest(t, "POST", "/api/v1/events", body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	env.server.agentWg.Wait()

	events, err := env.server.database.ListEvents(t.Context(), db.EventsQuery{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least 1 event")
	}
	if events[0].Channel != "#alerts" {
		t.Errorf("channel = %q, want #alerts", events[0].Channel)
	}
}

func TestAPI_GetEvents_Empty(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	w := env.doRequest(t, "GET", "/api/v1/events", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	if !resp.OK {
		t.Error("expected ok=true")
	}

	// Should return null or empty array for no events.
	if resp.Data != nil {
		data, ok := resp.Data.([]any)
		if ok && len(data) != 0 {
			t.Errorf("expected 0 events, got %d", len(data))
		}
	}
}

func TestAPI_GetEvents_WithData(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	// Insert some events directly.
	for i := 0; i < 3; i++ {
		_, _, err := env.server.database.InsertEvent(t.Context(), &db.Event{
			Source:    "test-source",
			EventType: "test.event",
			Summary:   "test summary",
			Channel:   "#murmur",
		})
		if err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	w := env.doRequest(t, "GET", "/api/v1/events", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	data, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("data type = %T, want []any", resp.Data)
	}
	if len(data) != 3 {
		t.Errorf("expected 3 events, got %d", len(data))
	}
}

func TestAPI_GetEvents_Pagination(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	// Insert 5 events.
	for i := 0; i < 5; i++ {
		_, _, err := env.server.database.InsertEvent(t.Context(), &db.Event{
			Source:    "test-source",
			EventType: "test.event",
			Summary:   "test summary",
			Channel:   "#murmur",
		})
		if err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	// Get first 2 events.
	w := env.doRequest(t, "GET", "/api/v1/events?limit=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	data, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("data type = %T, want []any", resp.Data)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 events, got %d", len(data))
	}

	// Get the ID of the last event in the first page.
	lastEvent, ok := data[1].(map[string]any)
	if !ok {
		t.Fatalf("event type = %T, want map", data[1])
	}
	lastID := lastEvent["id"].(float64)

	// Get next page using the computed cursor from the first page.
	afterIDStr := strconv.FormatInt(int64(lastID), 10)
	w2 := env.doRequest(t, "GET", "/api/v1/events?limit=2&after_id="+afterIDStr, "")
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w2.Code, http.StatusOK)
	}

	resp2 := parseResponse(t, w2)
	data2, ok := resp2.Data.([]any)
	if !ok {
		t.Fatalf("data2 type = %T, want []any", resp2.Data)
	}
	if len(data2) != 2 {
		t.Fatalf("expected 2 events in page 2, got %d", len(data2))
	}

	// Verify the first event in page 2 has ID > lastID.
	firstPage2, ok := data2[0].(map[string]any)
	if !ok {
		t.Fatalf("event type = %T, want map", data2[0])
	}
	firstPage2ID := firstPage2["id"].(float64)
	if firstPage2ID <= lastID {
		t.Errorf("first event in page 2 has id=%v, expected > %v", firstPage2ID, lastID)
	}
}

func TestAPI_GetEvents_SourceFilter(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	// Insert events from different sources.
	for _, source := range []string{"backup", "cron", "backup"} {
		_, _, err := env.server.database.InsertEvent(t.Context(), &db.Event{
			Source:    source,
			EventType: "test.event",
			Summary:   "test",
			Channel:   "#murmur",
		})
		if err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	w := env.doRequest(t, "GET", "/api/v1/events?source=backup", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	data, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("data type = %T, want []any", resp.Data)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 backup events, got %d", len(data))
	}
}

func TestAPI_GetEvents_InvalidParams(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	tests := []struct {
		name string
		path string
	}{
		{"invalid after_id", "/api/v1/events?after_id=abc"},
		{"invalid limit", "/api/v1/events?limit=xyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := env.doRequest(t, "GET", tt.path, "")
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestAPI_AuthMiddleware_ValidKey(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	// Request with valid key — should succeed.
	w := env.doRequest(t, "GET", "/api/v1/health", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAPI_AuthMiddleware_InvalidKey(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAPI_AuthMiddleware_MissingKey(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	// No Authorization header.
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAPI_AuthMiddleware_EmptyConfigKey(t *testing.T) {
	t.Parallel()

	// Create an env with empty API key — should bypass auth.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	registry := NewRegistry(2*time.Minute, logger)
	memory := NewMemory(database, 100, 80, nil, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	serverTools := NewToolRegistry()

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(providers, "test-provider", serverTools, registry, memory, router,
		nil, nil, "test", "test-server", "#test-bus", 100, 0, nil, 2*time.Second, 2*time.Second, false, config.DebugConfig{}, logger)
	agent.sendFunc = func(_, _ string) {}

	cfg := &config.ServerConfig{}
	cfg.Server.Name = "test"
	cfg.IRC.Channels.Main = "#murmur"
	cfg.API.APIKey = "" // Empty — no auth required.

	s := &Server{
		cfg:         cfg,
		registry:    registry,
		logger:      logger,
		database:    database,
		serverTools: serverTools,
		agent:       agent,
		startTime:   time.Now(),
	}

	handler := newServerAPIMux(s)

	// Request without any auth header — should succeed.
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (empty key should bypass auth)", w.Code, http.StatusOK)
	}
}

func TestAPI_StatusToolCount(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnv(t)

	// Register a client with 2 tools.
	env.server.registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "test-client",
		Hostname: "test-host",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)},
			{Name: "mail_read", Description: "Read mail", Parameters: json.RawMessage(`{}`)},
		},
	})

	w := env.doRequest(t, "GET", "/api/v1/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseResponse(t, w)
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want map", resp.Data)
	}

	// Tool count should include client tools (2) + server tools (0).
	toolCount := data["tools"].(float64)
	if toolCount != 2 {
		t.Errorf("tools = %v, want 2", toolCount)
	}

	// Client count should be 1.
	clientCount := data["clients"].(float64)
	if clientCount != 1 {
		t.Errorf("clients = %v, want 1", clientCount)
	}
}

// newTestAPIEnvWithPerms creates a test API environment with a PermissionManager
// that has per-user API keys configured.
func newTestAPIEnvWithPerms(t *testing.T) *testAPIEnv {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	registry := NewRegistry(2*time.Minute, logger)
	memory := NewMemory(database, 100, 80, nil, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	serverTools := NewToolRegistry()

	mock := &llmtest.MockProvider{
		NameVal: "test-provider",
		Responses: []*llm.ChatResponse{
			{Content: "event processed"},
		},
	}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(
		providers,
		"test-provider",
		serverTools,
		registry,
		memory,
		router,
		nil, nil,
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100, 0, nil,
		2*time.Second,
		2*time.Second,
		false,
		config.DebugConfig{},
		logger,
	)
	agent.sendFunc = func(_, _ string) {}

	// Create a PermissionManager with per-user API keys.
	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"alice": {Role: "user", APIKey: "alice-secret-key"},
			"admin": {Role: "admin", APIKey: "admin-secret-key"},
		},
	}
	pm := NewPermissionManager(permCfg, logger)

	cfg := &config.ServerConfig{}
	cfg.Server.Name = "test-murmur"
	cfg.IRC.Channels.Main = "#murmur"
	cfg.API.Enabled = true
	cfg.API.Listen = "127.0.0.1:0"
	cfg.API.APIKey = "global-secret"

	s := &Server{
		cfg:         cfg,
		registry:    registry,
		logger:      logger,
		database:    database,
		serverTools: serverTools,
		agent:       agent,
		permissions: pm,
		startTime:   time.Now(),
	}

	handler := newServerAPIMux(s)

	return &testAPIEnv{
		server:  s,
		handler: handler,
	}
}

func TestAPI_PostEvent_PerUserKey(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnvWithPerms(t)

	body := `{"source":"webhook","event_type":"deploy","summary":"Deploy started","event_id":"per-user-1"}`
	req := httptest.NewRequest("POST", "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer alice-secret-key")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusAccepted, w.Body.String())
	}

	env.server.agentWg.Wait()
}

func TestAPI_PostEvent_GlobalKey(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnvWithPerms(t)

	body := `{"source":"webhook","event_type":"deploy","summary":"Deploy started","event_id":"global-1"}`
	req := httptest.NewRequest("POST", "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer global-secret")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusAccepted, w.Body.String())
	}

	env.server.agentWg.Wait()
}

func TestAPI_PostEvent_InvalidKey(t *testing.T) {
	t.Parallel()
	env := newTestAPIEnvWithPerms(t)

	body := `{"source":"webhook","event_type":"deploy","summary":"Deploy started"}`
	req := httptest.NewRequest("POST", "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
