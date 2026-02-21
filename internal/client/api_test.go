package client

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"murmur/internal/api"
	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/tools"
)

// testClientAPIEnv holds the components needed for client API handler tests.
type testClientAPIEnv struct {
	client  *Client
	handler http.Handler
}

// newTestClientAPIEnv creates a minimal Client with the API mux wired up.
// If connected is true, the client reports itself as IRC-connected (via
// isConnectedFunc override). If false, conn is nil and isConnectedFunc is
// not set, simulating a disconnected state.
func newTestClientAPIEnv(t *testing.T, connected bool) *testClientAPIEnv {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.ClientConfig{}
	cfg.Client.ID = "test-client"
	cfg.Client.Hostname = "test-host"
	cfg.Client.Autonomy = "auto"
	cfg.API.Enabled = true
	cfg.API.Listen = "127.0.0.1:0"
	cfg.API.APIKey = "test-secret"

	// Create a minimal sender (nil connection — we override Send).
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)

	// Create a cron runner with no jobs.
	cronRunner, err := NewCronRunner(nil, nil, sender, "test-client", logger)
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	c := &Client{
		cfg:          cfg,
		sender:       sender,
		tools:        []bus.ToolDef{{Name: "shell", Description: "Run commands"}},
		toolHandlers: make(map[string]tools.Tool),
		toolSem:      make(chan struct{}, maxConcurrentTools),
		cronRunner:   cronRunner,
		startTime:    time.Now(),
		logger:       logger,
		// conn is nil — isConnectedFunc controls connectivity for tests.
	}

	if connected {
		c.isConnectedFunc = func() bool { return true }
	}

	handler := newClientAPIMux(c)

	return &testClientAPIEnv{
		client:  c,
		handler: handler,
	}
}

// doRequest is a helper that creates and executes an HTTP request against the
// test handler, returning the response recorder.
func (env *testClientAPIEnv) doRequest(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
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

// parseClientResponse decodes the JSON response envelope.
func parseClientResponse(t *testing.T, w *httptest.ResponseRecorder) api.Response {
	t.Helper()
	var resp api.Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
	return resp
}

func TestClientAPI_Health(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, false)

	w := env.doRequest(t, "GET", "/api/v1/health", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseClientResponse(t, w)
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

func TestClientAPI_Status(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, false)

	w := env.doRequest(t, "GET", "/api/v1/status", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	resp := parseClientResponse(t, w)
	if !resp.OK {
		t.Error("expected ok=true")
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T, want map", resp.Data)
	}
	if data["client_id"] != "test-client" {
		t.Errorf("client_id = %v, want test-client", data["client_id"])
	}
	if data["hostname"] != "test-host" {
		t.Errorf("hostname = %v, want test-host", data["hostname"])
	}
	if data["autonomy"] != "auto" {
		t.Errorf("autonomy = %v, want auto", data["autonomy"])
	}
	if data["api_version"] != "v1" {
		t.Errorf("api_version = %v, want v1", data["api_version"])
	}

	// Tools should be a list with one entry.
	tools, ok := data["tools"].([]any)
	if !ok {
		t.Fatalf("tools type = %T, want []any", data["tools"])
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
	if tools[0] != "shell" {
		t.Errorf("tools[0] = %v, want shell", tools[0])
	}

	// Connected should be false (no IRC connection in test).
	if data["connected"] != false {
		t.Errorf("connected = %v, want false", data["connected"])
	}

	// Uptime should be a non-empty string.
	uptime, ok := data["uptime"].(string)
	if !ok || uptime == "" {
		t.Errorf("uptime = %v, want non-empty string", data["uptime"])
	}
}

func TestClientAPI_PostEvent_IRCDisconnected(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, false)

	// conn is nil — treated as disconnected, should return 503.
	body := `{"source":"test","event_type":"test","summary":"test event"}`
	w := env.doRequest(t, "POST", "/api/v1/events", body)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}

	resp := parseClientResponse(t, w)
	if resp.OK {
		t.Error("expected ok=false for disconnected client")
	}
}

func TestClientAPI_PostEvent_MissingFields(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, true)

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

func TestClientAPI_PostEvent_InvalidJSON(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, true)

	w := env.doRequest(t, "POST", "/api/v1/events", "not json")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestClientAPI_PostEvent_ForwardingError(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, true)

	// The sender has a nil IRC connection, so SendEvent returns an error.
	// The handler should return 500 Internal Server Error.
	body := `{"source":"webhook","event_type":"push","summary":"new commit on main"}`
	w := env.doRequest(t, "POST", "/api/v1/events", body)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}

	resp := parseClientResponse(t, w)
	if resp.OK {
		t.Error("expected ok=false for forwarding error")
	}
}

func TestClientAPI_AuthMiddleware_InvalidKey(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, false)

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestClientAPI_AuthMiddleware_MissingKey(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, false)

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestClientAPI_AuthMiddleware_EmptyConfigKey(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	cronRunner, err := NewCronRunner(nil, nil, sender, "test-client", logger)
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	cfg := &config.ClientConfig{}
	cfg.Client.ID = "test"
	cfg.API.APIKey = "" // Empty — no auth required.

	c := &Client{
		cfg:          cfg,
		sender:       sender,
		tools:        nil,
		toolHandlers: make(map[string]tools.Tool),
		toolSem:      make(chan struct{}, maxConcurrentTools),
		cronRunner:   cronRunner,
		startTime:    time.Now(),
		logger:       logger,
	}

	handler := newClientAPIMux(c)

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (empty key should bypass auth)", w.Code, http.StatusOK)
	}
}

func TestClientAPI_Status_NoCronJobs(t *testing.T) {
	t.Parallel()
	env := newTestClientAPIEnv(t, false)

	w := env.doRequest(t, "GET", "/api/v1/status", "")

	resp := parseClientResponse(t, w)
	data := resp.Data.(map[string]any)

	cronJobs := data["cron_jobs"].(float64)
	if cronJobs != 0 {
		t.Errorf("cron_jobs = %v, want 0", cronJobs)
	}
}
