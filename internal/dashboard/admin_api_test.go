package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"murmur/internal/config"
	"murmur/internal/db"
)

// --- Mock implementations ---

// mockAdminChecker implements AdminChecker for tests.
type mockAdminChecker struct {
	admins map[string]bool
}

func (m *mockAdminChecker) IsAdmin(nick string) bool {
	return m.admins[nick]
}

// mockTaskManager implements TaskManager for tests.
type mockTaskManager struct {
	tasks  []TaskInfo
	nextID int64
	addErr error
	delErr error
	togErr error
}

func (m *mockTaskManager) ListTasks() ([]TaskInfo, error) {
	return m.tasks, nil
}

func (m *mockTaskManager) AddTask(name, schedule, action, channel, createdBy, provider string) (int64, error) {
	if m.addErr != nil {
		return 0, m.addErr
	}
	m.nextID++
	m.tasks = append(m.tasks, TaskInfo{
		ID:        m.nextID,
		Name:      name,
		Schedule:  schedule,
		Action:    action,
		Channel:   channel,
		Enabled:   true,
		Type:      "cron",
		CreatedBy: createdBy,
		Provider:  provider,
	})
	return m.nextID, nil
}

func (m *mockTaskManager) AddOneShotTask(name string, runAt time.Time, action, channel, createdBy, provider string) (int64, error) {
	if m.addErr != nil {
		return 0, m.addErr
	}
	m.nextID++
	m.tasks = append(m.tasks, TaskInfo{
		ID:        m.nextID,
		Name:      name,
		Action:    action,
		Channel:   channel,
		Enabled:   true,
		Type:      "once",
		RunAt:     &runAt,
		CreatedBy: createdBy,
		Provider:  provider,
	})
	return m.nextID, nil
}

func (m *mockTaskManager) RemoveTask(id int64) error {
	if m.delErr != nil {
		return m.delErr
	}
	for i, t := range m.tasks {
		if t.ID == id {
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("RemoveTask: task %d not found", id)
}

func (m *mockTaskManager) EnableTask(id int64) error {
	if m.togErr != nil {
		return m.togErr
	}
	for i, t := range m.tasks {
		if t.ID == id {
			m.tasks[i].Enabled = true
			return nil
		}
	}
	return fmt.Errorf("EnableTask: task %d not found", id)
}

func (m *mockTaskManager) DisableTask(id int64) error {
	if m.togErr != nil {
		return m.togErr
	}
	for i, t := range m.tasks {
		if t.ID == id {
			m.tasks[i].Enabled = false
			return nil
		}
	}
	return fmt.Errorf("DisableTask: task %d not found", id)
}

// mockToolLister implements ToolLister for tests.
type mockToolLister struct {
	tools []ToolInfo
}

func (m *mockToolLister) ListAllTools() []ToolInfo {
	return m.tools
}

// mockChannelLister implements ChannelLister for tests.
type mockChannelLister struct {
	channels []ChannelSettingsInfo
}

func (m *mockChannelLister) ListChannels() ([]ChannelSettingsInfo, error) {
	return m.channels, nil
}

func (m *mockChannelLister) UpdateChannel(cs *ChannelSettingsInfo) error {
	for i, ch := range m.channels {
		if ch.Channel == cs.Channel {
			m.channels[i] = *cs
			return nil
		}
	}
	m.channels = append(m.channels, *cs)
	return nil
}

// mockProviderLister implements ProviderLister for tests.
type mockProviderLister struct {
	providers []ProviderInfo
}

func (m *mockProviderLister) ListProviders() []ProviderInfo {
	return m.providers
}

// mockStatsProvider implements StatsProvider for tests.
type mockStatsProvider struct {
	summary   *StatsSummaryDTO
	aggregate []StatsAggregationDTO
	tools     []ToolUsageDTO
	providers []ProviderStatDTO
	stats     []UsageStatDTO
	total     int
	err       error
}

func (m *mockStatsProvider) GetSummary(_ context.Context, _ StatsQueryParams) (*StatsSummaryDTO, error) {
	return m.summary, m.err
}

func (m *mockStatsProvider) GetAggregate(_ context.Context, _ StatsQueryParams, _ string) ([]StatsAggregationDTO, error) {
	return m.aggregate, m.err
}

func (m *mockStatsProvider) GetTopTools(_ context.Context, _ StatsQueryParams) ([]ToolUsageDTO, error) {
	return m.tools, m.err
}

func (m *mockStatsProvider) GetProviderBreakdown(_ context.Context, _ StatsQueryParams) ([]ProviderStatDTO, error) {
	return m.providers, m.err
}

func (m *mockStatsProvider) ListStats(_ context.Context, _ StatsQueryParams) ([]UsageStatDTO, int, error) {
	return m.stats, m.total, m.err
}

// mockConfigReloader implements ConfigReloader for tests.
type mockConfigReloader struct {
	err    error
	called bool
}

func (m *mockConfigReloader) Reload() error {
	m.called = true
	return m.err
}

// --- Test helpers ---

// newTestDB creates an in-memory database with all migrations applied.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// testAdminHandler creates a Handler wired with admin API dependencies.
func testAdminHandler(t *testing.T, database *db.DB) (*Handler, *SessionStore) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewSessionStore(time.Hour, logger)

	cfg := config.DashboardConfig{
		Enabled:        true,
		Listen:         "127.0.0.1:8082",
		SessionTimeout: "1h",
	}
	ircCfg := config.IRCConfig{
		Server: "localhost",
		Port:   6667,
		Channels: config.ChannelsConfig{
			Main: "#murmur",
		},
	}

	admin := &AdminDeps{
		DB: database,
		Admin: &mockAdminChecker{
			admins: map[string]bool{"admin": true},
		},
		Tasks: &mockTaskManager{nextID: 0},
		Tools: &mockToolLister{
			tools: []ToolInfo{
				{Name: "shell", Description: "Execute shell commands", Source: "client"},
				{Name: "web_search", Description: "Search the web", Source: "server"},
			},
		},
		Channels: &mockChannelLister{
			channels: []ChannelSettingsInfo{
				{Channel: "#murmur", Provider: "openrouter", AutoJoin: true},
			},
		},
		Providers: &mockProviderLister{
			providers: []ProviderInfo{
				{Name: "openrouter", Model: "anthropic/claude-sonnet-4-5", APIBase: "https://openrouter.ai/api/v1", IsDefault: true},
			},
		},
		Reloader: &mockConfigReloader{},
		Stats: &mockStatsProvider{
			summary: &StatsSummaryDTO{
				TotalRequests:  42,
				TotalTokens:    12345,
				TotalToolCalls: 10,
				AvgLatencyMs:   250.5,
				ErrorCount:     2,
				TopProvider:    "openrouter",
				TopChannel:     "#murmur",
			},
			aggregate: []StatsAggregationDTO{
				{Period: "2026-02-24", TotalRequests: 20, TotalTokens: 6000},
				{Period: "2026-02-25", TotalRequests: 22, TotalTokens: 6345},
			},
			tools: []ToolUsageDTO{
				{Name: "shell", Count: 15, AvgDurationMs: 1200.0, ErrorCount: 1},
			},
			providers: []ProviderStatDTO{
				{Provider: "openrouter", TotalRequests: 42, TotalTokens: 12345},
			},
			stats: []UsageStatDTO{
				{ID: 1, Channel: "#murmur", Nick: "user1", Provider: "openrouter", Model: "claude", Status: "ok", RequestType: "chat"},
			},
			total: 1,
		},
	}

	h := NewHandler(store, cfg, ircCfg, nil, admin, logger)
	h.verify = noopVerifier
	return h, store
}

// adminRequest creates a signed admin API request with the given session.
func adminRequest(t *testing.T, method, path string, body interface{}, sess *Session) *http.Request {
	t.Helper()

	var bodyReader io.Reader
	var bodyStr string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyStr = string(data)
		bodyReader = bytes.NewReader(data)
	}

	req := httptest.NewRequest(method, path, bodyReader)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	if method == http.MethodPost || method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	testSignRequest(t, req, sess, bodyStr)
	return req
}

// decodeResponse decodes a JSON response body into the given target.
func decodeResponse(t *testing.T, w *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}
}

// --- Tests ---

func TestAdminAPIAuth_NoSession(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, _ := testAdminHandler(t, database)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/users", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAdminAPIAuth_NoSignature(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/users", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestAdminAPIAuth_NonAdmin(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("regularuser") // not in admins map

	req := adminRequest(t, http.MethodGet, "/dashboard/api/users", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp errorResponse
	decodeResponse(t, w, &resp)
	if resp.Error != "admin access required" {
		t.Errorf("error = %q, want %q", resp.Error, "admin access required")
	}
}

func TestAdminAPIUsersCRUD(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// 1. List users — should be empty.
	req := adminRequest(t, http.MethodGet, "/dashboard/api/users", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list users: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var listResp successResponse
	decodeResponse(t, w, &listResp)
	if !listResp.OK {
		t.Fatal("list users: OK = false")
	}

	// 2. Create a user.
	createBody := createUserRequest{
		Nick:     "testuser",
		Role:     "user",
		Tools:    []string{"shell", "web_search"},
		Autonomy: "approve",
	}
	req = adminRequest(t, http.MethodPost, "/dashboard/api/users", createBody, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create user: status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}

	// 3. Get the user.
	req = adminRequest(t, http.MethodGet, "/dashboard/api/users/testuser", nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get user: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var getResp struct {
		OK   bool       `json:"ok"`
		Data db.UserRow `json:"data"`
	}
	decodeResponse(t, w, &getResp)
	if getResp.Data.Nick != "testuser" {
		t.Errorf("get user: nick = %q, want %q", getResp.Data.Nick, "testuser")
	}
	if getResp.Data.Role != "user" {
		t.Errorf("get user: role = %q, want %q", getResp.Data.Role, "user")
	}
	if getResp.Data.Autonomy != "approve" {
		t.Errorf("get user: autonomy = %q, want %q", getResp.Data.Autonomy, "approve")
	}

	// 4. Update the user.
	newRole := "admin"
	updateBody := updateUserRequest{
		Role: &newRole,
	}
	req = adminRequest(t, http.MethodPut, "/dashboard/api/users/testuser", updateBody, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update user: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify the update.
	req = adminRequest(t, http.MethodGet, "/dashboard/api/users/testuser", nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	decodeResponse(t, w, &getResp)
	if getResp.Data.Role != "admin" {
		t.Errorf("updated user: role = %q, want %q", getResp.Data.Role, "admin")
	}

	// 5. Delete the user.
	req = adminRequest(t, http.MethodDelete, "/dashboard/api/users/testuser", nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("delete user: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify deletion.
	req = adminRequest(t, http.MethodGet, "/dashboard/api/users/testuser", nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("deleted user: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAdminAPIUsers_DuplicateCreate(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	body := createUserRequest{Nick: "dupuser", Role: "user"}

	// First create should succeed.
	req := adminRequest(t, http.MethodPost, "/dashboard/api/users", body, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d, want %d", w.Code, http.StatusCreated)
	}

	// Second create should conflict.
	req = adminRequest(t, http.MethodPost, "/dashboard/api/users", body, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate create: status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestAdminAPIUsers_UpdateNonexistent(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	newRole := "admin"
	body := updateUserRequest{Role: &newRole}
	req := adminRequest(t, http.MethodPut, "/dashboard/api/users/nonexistent", body, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("update nonexistent: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAdminAPIUsers_DeleteNonexistent(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodDelete, "/dashboard/api/users/nonexistent", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("delete nonexistent: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAdminAPITasksCRUD(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// 1. List tasks — should be empty.
	req := adminRequest(t, http.MethodGet, "/dashboard/api/tasks", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list tasks: status = %d, want %d", w.Code, http.StatusOK)
	}

	// 2. Create a cron task.
	createBody := createTaskRequest{
		Name:     "test-task",
		Schedule: "*/5 * * * *",
		Action:   "check the weather",
		Channel:  "#murmur",
		Type:     "cron",
	}
	req = adminRequest(t, http.MethodPost, "/dashboard/api/tasks", createBody, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create task: status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}

	var createResp struct {
		OK   bool           `json:"ok"`
		Data map[string]int `json:"data"`
	}
	decodeResponse(t, w, &createResp)
	if !createResp.OK {
		t.Fatal("create task: OK = false")
	}
	taskID := createResp.Data["id"]
	if taskID == 0 {
		t.Fatal("create task: id = 0")
	}

	// 3. Toggle task (disable).
	toggleBody := toggleRequest{Enabled: false}
	req = adminRequest(t, http.MethodPost, fmt.Sprintf("/dashboard/api/tasks/%d/toggle", taskID), toggleBody, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("toggle task: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// 4. Delete the task.
	req = adminRequest(t, http.MethodDelete, fmt.Sprintf("/dashboard/api/tasks/%d", taskID), nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("delete task: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestAdminAPITasks_OneShotCreate(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	runAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	createBody := createTaskRequest{
		Name:    "reminder",
		Action:  "remind me to eat",
		Channel: "#murmur",
		Type:    "once",
		RunAt:   runAt,
	}
	req := adminRequest(t, http.MethodPost, "/dashboard/api/tasks", createBody, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create one-shot task: status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestAdminAPITasks_ValidationErrors(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	tests := []struct {
		name       string
		body       createTaskRequest
		wantStatus int
	}{
		{
			name:       "missing name",
			body:       createTaskRequest{Action: "do stuff", Channel: "#ch", Type: "cron", Schedule: "* * * * *"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing action",
			body:       createTaskRequest{Name: "t", Channel: "#ch", Type: "cron", Schedule: "* * * * *"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing channel",
			body:       createTaskRequest{Name: "t", Action: "do", Type: "cron", Schedule: "* * * * *"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "cron missing schedule",
			body:       createTaskRequest{Name: "t", Action: "do", Channel: "#ch", Type: "cron"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "once missing run_at",
			body:       createTaskRequest{Name: "t", Action: "do", Channel: "#ch", Type: "once"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid type",
			body:       createTaskRequest{Name: "t", Action: "do", Channel: "#ch", Type: "invalid"},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := adminRequest(t, http.MethodPost, "/dashboard/api/tasks", tt.body, sess)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestAdminAPIToolsList(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/tools", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list tools: status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		OK   bool       `json:"ok"`
		Data []ToolInfo `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if !resp.OK {
		t.Fatal("list tools: OK = false")
	}
	if len(resp.Data) != 2 {
		t.Fatalf("list tools: got %d tools, want 2", len(resp.Data))
	}

	// Verify sources.
	sources := make(map[string]string)
	for _, tool := range resp.Data {
		sources[tool.Name] = tool.Source
	}
	if sources["shell"] != "client" {
		t.Errorf("shell source = %q, want %q", sources["shell"], "client")
	}
	if sources["web_search"] != "server" {
		t.Errorf("web_search source = %q, want %q", sources["web_search"], "server")
	}
}

func TestAdminAPICustomToolsCRUD(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// 1. Create a custom tool.
	createBody := createCustomToolRequest{
		Name:          "my_tool",
		Description:   "A test tool",
		Parameters:    `{"type":"object","properties":{}}`,
		Backend:       "shell",
		BackendConfig: `{"command":"echo hello"}`,
		Enabled:       true,
	}
	req := adminRequest(t, http.MethodPost, "/dashboard/api/tools/custom", createBody, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create custom tool: status = %d, want %d (body: %s)", w.Code, http.StatusCreated, w.Body.String())
	}

	// 2. List custom tools.
	req = adminRequest(t, http.MethodGet, "/dashboard/api/tools/custom", nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list custom tools: status = %d, want %d", w.Code, http.StatusOK)
	}

	var listResp struct {
		OK   bool            `json:"ok"`
		Data []db.CustomTool `json:"data"`
	}
	decodeResponse(t, w, &listResp)
	if len(listResp.Data) != 1 {
		t.Fatalf("list custom tools: got %d, want 1", len(listResp.Data))
	}
	if listResp.Data[0].Name != "my_tool" {
		t.Errorf("custom tool name = %q, want %q", listResp.Data[0].Name, "my_tool")
	}

	// 3. Update the custom tool.
	newDesc := "Updated description"
	updateBody := updateCustomToolRequest{
		Description: &newDesc,
	}
	req = adminRequest(t, http.MethodPut, "/dashboard/api/tools/custom/my_tool", updateBody, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update custom tool: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// 4. Toggle the custom tool.
	toggleBody := toggleRequest{Enabled: false}
	req = adminRequest(t, http.MethodPost, "/dashboard/api/tools/custom/my_tool/toggle", toggleBody, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("toggle custom tool: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// 5. Delete the custom tool.
	req = adminRequest(t, http.MethodDelete, "/dashboard/api/tools/custom/my_tool", nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("delete custom tool: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify deletion.
	req = adminRequest(t, http.MethodGet, "/dashboard/api/tools/custom", nil, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	decodeResponse(t, w, &listResp)
	if len(listResp.Data) != 0 {
		t.Errorf("after delete: got %d custom tools, want 0", len(listResp.Data))
	}
}

func TestAdminAPIChannels(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// List channels.
	req := adminRequest(t, http.MethodGet, "/dashboard/api/channels", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list channels: status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		OK   bool                  `json:"ok"`
		Data []ChannelSettingsInfo `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("list channels: got %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Channel != "#murmur" {
		t.Errorf("channel = %q, want %q", resp.Data[0].Channel, "#murmur")
	}

	// Update channel using URL-encoded name (%23 = #).
	updateBody := ChannelSettingsInfo{
		Provider:    "kimi",
		AutoJoin:    false,
		TopicPrefix: "[test]",
	}
	req = adminRequest(t, http.MethodPut, "/dashboard/api/channels/%23murmur", updateBody, sess)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("update channel: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify the channel was stored with the decoded name (#murmur, not %23murmur).
	channelLister := h.api.channels.(*mockChannelLister)
	found := false
	for _, ch := range channelLister.channels {
		if ch.Channel == "#murmur" && ch.Provider == "kimi" {
			found = true
			break
		}
	}
	if !found {
		t.Error("channel should be stored with decoded name #murmur, not %23murmur")
	}
}

func TestAdminAPIProviders(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/providers", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list providers: status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		OK   bool           `json:"ok"`
		Data []ProviderInfo `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("list providers: got %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Name != "openrouter" {
		t.Errorf("provider name = %q, want %q", resp.Data[0].Name, "openrouter")
	}
	if !resp.Data[0].IsDefault {
		t.Error("provider should be default")
	}
}

func TestAdminAPISystemReload(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodPost, "/dashboard/api/system/reload", struct{}{}, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("reload: status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify the reloader was called.
	reloader := h.api.reloader.(*mockConfigReloader)
	if !reloader.called {
		t.Error("reloader was not called")
	}
}

func TestAdminAPISystemReload_Error(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// Set reloader to return an error.
	h.api.reloader = &mockConfigReloader{err: fmt.Errorf("config parse error")}

	req := adminRequest(t, http.MethodPost, "/dashboard/api/system/reload", struct{}{}, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("reload error: status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestAdminAPINotFound(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/nonexistent", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAdminAPIContentTypeValidation(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// POST without Content-Type: application/json should be rejected.
	body := `{"nick":"test","role":"user"}`
	req := httptest.NewRequest(http.MethodPost, "/dashboard/api/users", bytes.NewReader([]byte(body)))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	testSignRequest(t, req, sess, body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}
}

func TestAdminAPIUsers_InvalidRole(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	body := createUserRequest{Nick: "baduser", Role: "superadmin"}
	req := adminRequest(t, http.MethodPost, "/dashboard/api/users", body, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp errorResponse
	decodeResponse(t, w, &resp)
	if resp.Error != "role must be 'admin' or 'user'" {
		t.Errorf("error = %q, want %q", resp.Error, "role must be 'admin' or 'user'")
	}
}

// --- Statistics API Tests ---

func TestAdminAPIStats_Summary(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/summary", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var resp struct {
		OK   bool            `json:"ok"`
		Data StatsSummaryDTO `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if !resp.OK {
		t.Error("expected ok=true")
	}
	if resp.Data.TotalRequests != 42 {
		t.Errorf("TotalRequests = %d, want 42", resp.Data.TotalRequests)
	}
	if resp.Data.TopProvider != "openrouter" {
		t.Errorf("TopProvider = %q, want %q", resp.Data.TopProvider, "openrouter")
	}
}

func TestAdminAPIStats_SummaryWithFilters(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet,
		"/dashboard/api/stats/summary?channel=%23murmur&provider=openrouter&from=2026-01-01T00:00:00Z&to=2026-12-31T23:59:59Z",
		nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestAdminAPIStats_Aggregate(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/aggregate?period=day", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var resp struct {
		OK   bool                  `json:"ok"`
		Data []StatsAggregationDTO `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if !resp.OK {
		t.Error("expected ok=true")
	}
	if len(resp.Data) != 2 {
		t.Errorf("got %d aggregation buckets, want 2", len(resp.Data))
	}
}

func TestAdminAPIStats_AggregateDefaultPeriod(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// No period param — should default to "day".
	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/aggregate", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestAdminAPIStats_AggregateInvalidPeriod(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/aggregate?period=year", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAdminAPIStats_Tools(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/tools", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var resp struct {
		OK   bool           `json:"ok"`
		Data []ToolUsageDTO `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if !resp.OK {
		t.Error("expected ok=true")
	}
	if len(resp.Data) != 1 {
		t.Fatalf("got %d tools, want 1", len(resp.Data))
	}
	if resp.Data[0].Name != "shell" {
		t.Errorf("tool name = %q, want %q", resp.Data[0].Name, "shell")
	}
}

func TestAdminAPIStats_Providers(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/providers", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var resp struct {
		OK   bool              `json:"ok"`
		Data []ProviderStatDTO `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if !resp.OK {
		t.Error("expected ok=true")
	}
	if len(resp.Data) != 1 {
		t.Fatalf("got %d providers, want 1", len(resp.Data))
	}
	if resp.Data[0].Provider != "openrouter" {
		t.Errorf("provider = %q, want %q", resp.Data[0].Provider, "openrouter")
	}
}

func TestAdminAPIStats_List(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats?limit=50&offset=0", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}

	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Stats []UsageStatDTO `json:"stats"`
			Total int            `json:"total"`
		} `json:"data"`
	}
	decodeResponse(t, w, &resp)
	if !resp.OK {
		t.Error("expected ok=true")
	}
	if resp.Data.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Data.Total)
	}
	if len(resp.Data.Stats) != 1 {
		t.Fatalf("got %d stats, want 1", len(resp.Data.Stats))
	}
}

func TestAdminAPIStats_InvalidRequestType(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/summary?request_type=invalid", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAdminAPIStats_InvalidDateRange(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// to before from
	req := adminRequest(t, http.MethodGet,
		"/dashboard/api/stats/summary?from=2026-12-31T00:00:00Z&to=2026-01-01T00:00:00Z",
		nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp errorResponse
	decodeResponse(t, w, &resp)
	if resp.Error != "to must be after from" {
		t.Errorf("error = %q, want %q", resp.Error, "to must be after from")
	}
}

func TestAdminAPIStats_DateRangeTooLarge(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	// 400 days range
	req := adminRequest(t, http.MethodGet,
		"/dashboard/api/stats/summary?from=2025-01-01T00:00:00Z&to=2026-02-05T00:00:00Z",
		nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAdminAPIStats_InvalidFromDate(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/summary?from=not-a-date", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAdminAPIStats_InvalidLimit(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	h, store := testAdminHandler(t, database)
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats?limit=-5", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAdminAPIStats_NilStatsProvider(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewSessionStore(time.Hour, logger)

	cfg := config.DashboardConfig{
		Enabled:        true,
		Listen:         "127.0.0.1:8082",
		SessionTimeout: "1h",
	}
	ircCfg := config.IRCConfig{
		Server:   "localhost",
		Port:     6667,
		Channels: config.ChannelsConfig{Main: "#murmur"},
	}

	admin := &AdminDeps{
		DB:    database,
		Admin: &mockAdminChecker{admins: map[string]bool{"admin": true}},
		// Stats is nil — should return 503.
	}

	h := NewHandler(store, cfg, ircCfg, nil, admin, logger)
	h.verify = noopVerifier
	sess, _ := store.Create("admin")

	req := adminRequest(t, http.MethodGet, "/dashboard/api/stats/summary", nil, sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestParseStatsQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		query   string
		wantErr string
		check   func(t *testing.T, q StatsQueryParams)
	}{
		{
			name:  "empty query",
			query: "",
			check: func(t *testing.T, q StatsQueryParams) {
				if q.Channel != "" || q.Nick != "" || q.Provider != "" {
					t.Error("expected empty filters")
				}
			},
		},
		{
			name:  "all filters",
			query: "channel=%23murmur&nick=user1&provider=openrouter&request_type=chat&limit=100&offset=10",
			check: func(t *testing.T, q StatsQueryParams) {
				if q.Channel != "#murmur" {
					t.Errorf("Channel = %q, want %q", q.Channel, "#murmur")
				}
				if q.Nick != "user1" {
					t.Errorf("Nick = %q, want %q", q.Nick, "user1")
				}
				if q.Provider != "openrouter" {
					t.Errorf("Provider = %q, want %q", q.Provider, "openrouter")
				}
				if q.RequestType != "chat" {
					t.Errorf("RequestType = %q, want %q", q.RequestType, "chat")
				}
				if q.Limit != 100 {
					t.Errorf("Limit = %d, want 100", q.Limit)
				}
				if q.Offset != 10 {
					t.Errorf("Offset = %d, want 10", q.Offset)
				}
			},
		},
		{
			name:    "invalid request_type",
			query:   "request_type=invalid",
			wantErr: "request_type must be one of: chat, task, event, summary, iteration_summary, pause_summary",
		},
		{
			name:    "invalid from date",
			query:   "from=not-a-date",
			wantErr: "from must be RFC3339 format",
		},
		{
			name:    "invalid to date",
			query:   "to=not-a-date",
			wantErr: "to must be RFC3339 format",
		},
		{
			name:    "to before from",
			query:   "from=2026-12-31T00:00:00Z&to=2026-01-01T00:00:00Z",
			wantErr: "to must be after from",
		},
		{
			name:    "date range too large",
			query:   "from=2025-01-01T00:00:00Z&to=2026-02-05T00:00:00Z",
			wantErr: "date range must not exceed 365 days",
		},
		{
			name:    "negative limit",
			query:   "limit=-1",
			wantErr: "limit must be a positive integer",
		},
		{
			name:    "zero limit",
			query:   "limit=0",
			wantErr: "limit must be a positive integer",
		},
		{
			name:    "non-numeric limit",
			query:   "limit=abc",
			wantErr: "limit must be a positive integer",
		},
		{
			name:    "negative offset",
			query:   "offset=-1",
			wantErr: "offset must be a non-negative integer",
		},
		{
			name:  "limit capped at max",
			query: "limit=500",
			check: func(t *testing.T, q StatsQueryParams) {
				if q.Limit != maxStatsLimit {
					t.Errorf("Limit = %d, want %d (capped)", q.Limit, maxStatsLimit)
				}
			},
		},
		{
			name:  "valid date range",
			query: "from=2026-01-01T00:00:00Z&to=2026-06-01T00:00:00Z",
			check: func(t *testing.T, q StatsQueryParams) {
				if q.From.IsZero() || q.To.IsZero() {
					t.Error("expected non-zero From and To")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			q, errMsg := parseStatsQuery(values)
			if tt.wantErr != "" {
				if errMsg != tt.wantErr {
					t.Errorf("error = %q, want %q", errMsg, tt.wantErr)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected error: %s", errMsg)
			}
			if tt.check != nil {
				tt.check(t, q)
			}
		})
	}
}
