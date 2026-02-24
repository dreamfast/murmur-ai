package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"murmur/internal/db"
)

// AdminChecker determines whether an IRC nick has admin privileges.
// Implemented by server.PermissionManager.
type AdminChecker interface {
	IsAdmin(nick string) bool
}

// TaskInfo holds scheduled task data returned by the TaskManager.
// It mirrors the server.ScheduledTask struct without importing the server
// package, avoiding circular dependencies.
type TaskInfo struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule"`
	Action    string     `json:"action"`
	Channel   string     `json:"channel"`
	Enabled   bool       `json:"enabled"`
	LastRun   *time.Time `json:"last_run"`
	NextRun   *time.Time `json:"next_run"`
	Type      string     `json:"type"`
	RunAt     *time.Time `json:"run_at"`
	CreatedBy string     `json:"created_by"`
	Provider  string     `json:"provider"`
}

// TaskManager provides scheduled task operations. Implemented by
// server.Scheduler (via an adapter that converts ScheduledTask to TaskInfo).
type TaskManager interface {
	ListTasks() ([]TaskInfo, error)
	AddTask(name, schedule, action, channel, createdBy, provider string) (int64, error)
	AddOneShotTask(name string, runAt time.Time, action, channel, createdBy, provider string) (int64, error)
	RemoveTask(id int64) error
	EnableTask(id int64) error
	DisableTask(id int64) error
}

// ToolInfo describes a tool for the admin API response.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "server", "client", or "custom"
}

// ToolLister provides read-only access to the combined tool list.
type ToolLister interface {
	ListAllTools() []ToolInfo
}

// ChannelSettingsInfo holds per-channel settings for the admin API.
type ChannelSettingsInfo struct {
	Channel     string `json:"channel"`
	Provider    string `json:"provider"`
	AutoJoin    bool   `json:"auto_join"`
	TopicPrefix string `json:"topic_prefix"`
}

// ChannelLister provides channel settings operations.
type ChannelLister interface {
	ListChannels() ([]ChannelSettingsInfo, error)
	UpdateChannel(cs *ChannelSettingsInfo) error
}

// ProviderInfo describes a configured LLM provider.
type ProviderInfo struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	APIBase   string `json:"api_base"`
	IsDefault bool   `json:"is_default"`
}

// ProviderLister provides read-only access to configured LLM providers.
type ProviderLister interface {
	ListProviders() []ProviderInfo
}

// ConfigReloader triggers a server configuration reload.
type ConfigReloader interface {
	Reload() error
}

// StatsProvider provides usage statistics data for the admin API.
// Implemented by an adapter wrapping *db.DB in the server package.
// All methods accept a context.Context for request cancellation and timeouts.
type StatsProvider interface {
	GetSummary(ctx context.Context, q StatsQueryParams) (*StatsSummaryDTO, error)
	GetAggregate(ctx context.Context, q StatsQueryParams, period string) ([]StatsAggregationDTO, error)
	GetTopTools(ctx context.Context, q StatsQueryParams) ([]ToolUsageDTO, error)
	GetProviderBreakdown(ctx context.Context, q StatsQueryParams) ([]ProviderStatDTO, error)
	ListStats(ctx context.Context, q StatsQueryParams) ([]UsageStatDTO, int, error)
}

// StatsQueryParams holds validated query parameters for stats endpoints.
type StatsQueryParams struct {
	Channel     string
	Nick        string
	Provider    string
	RequestType string
	From        time.Time
	To          time.Time
	Limit       int
	Offset      int
}

// StatsSummaryDTO mirrors db.StatsSummary for the dashboard API.
type StatsSummaryDTO struct {
	TotalRequests  int     `json:"total_requests"`
	TotalTokens    int64   `json:"total_tokens"`
	TotalToolCalls int     `json:"total_tool_calls"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	ErrorCount     int     `json:"error_count"`
	TopProvider    string  `json:"top_provider"`
	TopChannel     string  `json:"top_channel"`
}

// StatsAggregationDTO mirrors db.StatsAggregation for the dashboard API.
type StatsAggregationDTO struct {
	Period                string  `json:"period"`
	TotalRequests         int     `json:"total_requests"`
	TotalPromptTokens     int64   `json:"total_prompt_tokens"`
	TotalCompletionTokens int64   `json:"total_completion_tokens"`
	TotalTokens           int64   `json:"total_tokens"`
	TotalToolCalls        int     `json:"total_tool_calls"`
	AvgLatencyMs          float64 `json:"avg_latency_ms"`
	ErrorCount            int     `json:"error_count"`
}

// ToolUsageDTO mirrors db.ToolUsageStat for the dashboard API.
type ToolUsageDTO struct {
	Name          string  `json:"name"`
	Count         int     `json:"count"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
	ErrorCount    int     `json:"error_count"`
}

// ProviderStatDTO mirrors db.ProviderStat for the dashboard API.
type ProviderStatDTO struct {
	Provider              string  `json:"provider"`
	TotalRequests         int     `json:"total_requests"`
	TotalPromptTokens     int64   `json:"total_prompt_tokens"`
	TotalCompletionTokens int64   `json:"total_completion_tokens"`
	TotalTokens           int64   `json:"total_tokens"`
	TotalToolCalls        int     `json:"total_tool_calls"`
	AvgLatencyMs          float64 `json:"avg_latency_ms"`
	ErrorCount            int     `json:"error_count"`
}

// UsageStatDTO mirrors db.UsageStat for the dashboard API.
type UsageStatDTO struct {
	ID               int64     `json:"id"`
	Timestamp        time.Time `json:"timestamp"`
	Channel          string    `json:"channel"`
	Nick             string    `json:"nick"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	ToolCallsCount   int       `json:"tool_calls_count"`
	ToolDetails      string    `json:"tool_details"`
	LatencyMs        int64     `json:"latency_ms"`
	Iteration        int       `json:"iteration"`
	RequestType      string    `json:"request_type"`
	Status           string    `json:"status"`
	ErrorMessage     *string   `json:"error_message,omitempty"`
}

// AdminDeps bundles all dependencies needed by the admin API endpoints.
// Passed to NewHandler to keep the constructor signature manageable.
type AdminDeps struct {
	DB        *db.DB
	Admin     AdminChecker
	Tasks     TaskManager
	Tools     ToolLister
	Channels  ChannelLister
	Providers ProviderLister
	Reloader  ConfigReloader
	Stats     StatsProvider
}

// adminAPI groups the admin API dependencies on the Handler for cleaner access.
type adminAPI struct {
	database  *db.DB
	admin     AdminChecker
	tasks     TaskManager
	tools     ToolLister
	channels  ChannelLister
	providers ProviderLister
	reloader  ConfigReloader
	stats     StatsProvider
}

// enabled returns true if the admin API has been wired with dependencies.
func (a *adminAPI) enabled() bool {
	return a.database != nil && a.admin != nil
}

// routeAdminAPI dispatches /dashboard/api/* requests to the appropriate handler.
// Returns true if the request was handled, false if it should fall through to
// the default handler (e.g., static files).
func (h *Handler) routeAdminAPI(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/dashboard/api/") {
		return false
	}

	if !h.api.enabled() {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "admin API not configured"})
		return true
	}

	// All admin API endpoints require session + signature + admin role.
	sess, body, ok := h.requireAdmin(w, r)
	if !ok {
		return true
	}
	_ = sess

	// Verify Content-Type on POST/PUT to prevent CSRF via form submissions.
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			h.jsonResponse(w, http.StatusUnsupportedMediaType, errorResponse{
				Error: "Content-Type must be application/json",
			})
			return true
		}
	}

	// Strip the prefix to get the API path.
	apiPath := strings.TrimPrefix(r.URL.Path, "/dashboard/api")

	switch {
	// Users
	case r.Method == http.MethodGet && apiPath == "/users":
		h.handleAdminListUsers(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(apiPath, "/users/"):
		h.handleAdminGetUser(w, r, strings.TrimPrefix(apiPath, "/users/"))
	case r.Method == http.MethodPost && apiPath == "/users":
		h.handleAdminCreateUser(w, r, body)
	case r.Method == http.MethodPut && strings.HasPrefix(apiPath, "/users/"):
		h.handleAdminUpdateUser(w, r, strings.TrimPrefix(apiPath, "/users/"), body)
	case r.Method == http.MethodDelete && strings.HasPrefix(apiPath, "/users/"):
		h.handleAdminDeleteUser(w, r, strings.TrimPrefix(apiPath, "/users/"))

	// Tools
	case r.Method == http.MethodGet && apiPath == "/tools":
		h.handleAdminListTools(w, r)
	case r.Method == http.MethodGet && apiPath == "/tools/custom":
		h.handleAdminListCustomTools(w, r)
	case r.Method == http.MethodPost && apiPath == "/tools/custom":
		h.handleAdminCreateCustomTool(w, r, body)
	case r.Method == http.MethodPut && strings.HasPrefix(apiPath, "/tools/custom/"):
		h.handleAdminUpdateCustomTool(w, r, strings.TrimPrefix(apiPath, "/tools/custom/"), body)
	case r.Method == http.MethodDelete && strings.HasPrefix(apiPath, "/tools/custom/"):
		h.handleAdminDeleteCustomTool(w, r, strings.TrimPrefix(apiPath, "/tools/custom/"))
	case r.Method == http.MethodPost && strings.HasSuffix(apiPath, "/toggle") && strings.HasPrefix(apiPath, "/tools/custom/"):
		name := strings.TrimPrefix(apiPath, "/tools/custom/")
		name = strings.TrimSuffix(name, "/toggle")
		h.handleAdminToggleCustomTool(w, r, name, body)

	// Tasks
	case r.Method == http.MethodGet && apiPath == "/tasks":
		h.handleAdminListTasks(w, r)
	case r.Method == http.MethodPost && apiPath == "/tasks":
		h.handleAdminCreateTask(w, r, body, sess.Nick)
	case r.Method == http.MethodDelete && strings.HasPrefix(apiPath, "/tasks/"):
		h.handleAdminDeleteTask(w, r, strings.TrimPrefix(apiPath, "/tasks/"))
	case r.Method == http.MethodPost && strings.HasSuffix(apiPath, "/toggle") && strings.HasPrefix(apiPath, "/tasks/"):
		idStr := strings.TrimPrefix(apiPath, "/tasks/")
		idStr = strings.TrimSuffix(idStr, "/toggle")
		h.handleAdminToggleTask(w, r, idStr, body)

	// Channels
	case r.Method == http.MethodGet && apiPath == "/channels":
		h.handleAdminListChannels(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(apiPath, "/channels/"):
		h.handleAdminUpdateChannel(w, r, strings.TrimPrefix(apiPath, "/channels/"), body)

	// Providers
	case r.Method == http.MethodGet && apiPath == "/providers":
		h.handleAdminListProviders(w, r)

	// Statistics
	case r.Method == http.MethodGet && apiPath == "/stats/summary":
		h.handleAdminStatsSummary(w, r)
	case r.Method == http.MethodGet && apiPath == "/stats/aggregate":
		h.handleAdminStatsAggregate(w, r)
	case r.Method == http.MethodGet && apiPath == "/stats/tools":
		h.handleAdminStatsTools(w, r)
	case r.Method == http.MethodGet && apiPath == "/stats/providers":
		h.handleAdminStatsProviders(w, r)
	case r.Method == http.MethodGet && apiPath == "/stats":
		h.handleAdminStatsList(w, r)

	// System
	case r.Method == http.MethodPost && apiPath == "/system/reload":
		h.handleAdminReload(w, r)

	default:
		h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "not found"})
	}

	return true
}

// errorResponse is the standard error envelope for admin API responses.
type errorResponse struct {
	Error string `json:"error"`
}

// successResponse is the standard success envelope for admin API responses.
type successResponse struct {
	OK   bool `json:"ok"`
	Data any  `json:"data,omitempty"`
}

// requireAdmin validates session, signature, and admin role. Returns the
// session, the raw request body (needed for signature verification on
// POST/PUT), and true if all checks pass. On failure, it writes the
// appropriate error response and returns false.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (*Session, string, bool) {
	sess := h.sessions.GetFromRequest(r)
	if sess == nil {
		h.jsonResponse(w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return nil, "", false
	}

	// Read the body for signature verification (POST/PUT have bodies).
	var body string
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
		if err != nil {
			h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "failed to read request body"})
			return nil, "", false
		}
		body = string(bodyBytes)
	}

	if !h.verifySignature(r, sess, body) {
		h.jsonResponse(w, http.StatusForbidden, errorResponse{Error: "invalid or expired request signature"})
		return nil, "", false
	}

	if !h.api.admin.IsAdmin(sess.Nick) {
		h.jsonResponse(w, http.StatusForbidden, errorResponse{Error: "admin access required"})
		return nil, "", false
	}

	return sess, body, true
}

// --- Users API ---

// handleAdminListUsers returns all users.
func (h *Handler) handleAdminListUsers(w http.ResponseWriter, _ *http.Request) {
	users, err := h.api.database.ListUsers()
	if err != nil {
		h.logger.Error("admin API: list users", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to list users"})
		return
	}
	if users == nil {
		users = []db.UserRow{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: users})
}

// handleAdminGetUser returns a single user by nick.
func (h *Handler) handleAdminGetUser(w http.ResponseWriter, _ *http.Request, nick string) {
	if nick == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "nick is required"})
		return
	}

	user, err := h.api.database.GetUser(nick)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "user not found"})
			return
		}
		h.logger.Error("admin API: get user", "nick", nick, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to get user"})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: user})
}

// createUserRequest is the JSON body for POST /dashboard/api/users.
type createUserRequest struct {
	Nick               string   `json:"nick"`
	Role               string   `json:"role"`
	Tools              []string `json:"tools"`
	DenyTools          []string `json:"deny_tools"`
	Autonomy           string   `json:"autonomy"`
	AllowedModels      []string `json:"allowed_models"`
	DenyModels         []string `json:"deny_models"`
	MaxMessagesPerHour int      `json:"max_messages_per_hour"`
	APIKey             string   `json:"api_key"`
	NickServAccount    string   `json:"nickserv_account"`
}

// handleAdminCreateUser creates a new user.
func (h *Handler) handleAdminCreateUser(w http.ResponseWriter, _ *http.Request, body string) {
	var req createUserRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Nick == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "nick is required"})
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if req.Role != "admin" && req.Role != "user" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "role must be 'admin' or 'user'"})
		return
	}

	user := &db.UserRow{
		Nick:               req.Nick,
		Role:               req.Role,
		Tools:              defaultStringSlice(req.Tools, []string{"*"}),
		DenyTools:          defaultStringSlice(req.DenyTools, nil),
		Autonomy:           req.Autonomy,
		AllowedModels:      defaultStringSlice(req.AllowedModels, nil),
		DenyModels:         defaultStringSlice(req.DenyModels, nil),
		MaxMessagesPerHour: req.MaxMessagesPerHour,
		APIKey:             req.APIKey,
		NickServAccount:    req.NickServAccount,
	}

	if err := h.api.database.CreateUser(user); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			h.jsonResponse(w, http.StatusConflict, errorResponse{Error: fmt.Sprintf("user %q already exists", req.Nick)})
			return
		}
		h.logger.Error("admin API: create user", "nick", req.Nick, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to create user"})
		return
	}

	// Re-read the user to get the created/updated timestamps.
	created, err := h.api.database.GetUser(req.Nick)
	if err != nil {
		h.logger.Error("admin API: re-read created user", "nick", req.Nick, "error", err)
		h.jsonResponse(w, http.StatusCreated, successResponse{OK: true, Data: user})
		return
	}
	h.jsonResponse(w, http.StatusCreated, successResponse{OK: true, Data: created})
}

// updateUserRequest is the JSON body for PUT /dashboard/api/users/:nick.
type updateUserRequest struct {
	Role               *string  `json:"role"`
	Tools              []string `json:"tools"`
	DenyTools          []string `json:"deny_tools"`
	Autonomy           *string  `json:"autonomy"`
	AllowedModels      []string `json:"allowed_models"`
	DenyModels         []string `json:"deny_models"`
	MaxMessagesPerHour *int     `json:"max_messages_per_hour"`
	APIKey             *string  `json:"api_key"`
	NickServAccount    *string  `json:"nickserv_account"`
}

// handleAdminUpdateUser updates an existing user.
func (h *Handler) handleAdminUpdateUser(w http.ResponseWriter, _ *http.Request, nick, body string) {
	if nick == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "nick is required"})
		return
	}

	// Read existing user first.
	existing, err := h.api.database.GetUser(nick)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "user not found"})
			return
		}
		h.logger.Error("admin API: get user for update", "nick", nick, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to get user"})
		return
	}

	var req updateUserRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	// Apply partial updates — only update fields that are present in the request.
	if req.Role != nil {
		if *req.Role != "admin" && *req.Role != "user" {
			h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "role must be 'admin' or 'user'"})
			return
		}
		existing.Role = *req.Role
	}
	if req.Tools != nil {
		existing.Tools = req.Tools
	}
	if req.DenyTools != nil {
		existing.DenyTools = req.DenyTools
	}
	if req.Autonomy != nil {
		existing.Autonomy = *req.Autonomy
	}
	if req.AllowedModels != nil {
		existing.AllowedModels = req.AllowedModels
	}
	if req.DenyModels != nil {
		existing.DenyModels = req.DenyModels
	}
	if req.MaxMessagesPerHour != nil {
		existing.MaxMessagesPerHour = *req.MaxMessagesPerHour
	}
	if req.APIKey != nil {
		existing.APIKey = *req.APIKey
	}
	if req.NickServAccount != nil {
		existing.NickServAccount = *req.NickServAccount
	}

	if err := h.api.database.UpdateUser(existing); err != nil {
		h.logger.Error("admin API: update user", "nick", nick, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to update user"})
		return
	}

	// Re-read to get updated timestamp.
	updated, err := h.api.database.GetUser(nick)
	if err != nil {
		h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: existing})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: updated})
}

// handleAdminDeleteUser deletes a user by nick.
func (h *Handler) handleAdminDeleteUser(w http.ResponseWriter, _ *http.Request, nick string) {
	if nick == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "nick is required"})
		return
	}

	if err := h.api.database.DeleteUser(nick); err != nil {
		if errors.Is(err, db.ErrUserNotFound) {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "user not found"})
			return
		}
		h.logger.Error("admin API: delete user", "nick", nick, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete user"})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true})
}

// --- Tools API ---

// handleAdminListTools returns all tools (server + client + custom) with source.
func (h *Handler) handleAdminListTools(w http.ResponseWriter, _ *http.Request) {
	if h.api.tools == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "tool listing not available"})
		return
	}
	tools := h.api.tools.ListAllTools()
	if tools == nil {
		tools = []ToolInfo{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: tools})
}

// handleAdminListCustomTools returns only custom tools.
func (h *Handler) handleAdminListCustomTools(w http.ResponseWriter, _ *http.Request) {
	tools, err := h.api.database.ListCustomTools(false)
	if err != nil {
		h.logger.Error("admin API: list custom tools", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to list custom tools"})
		return
	}
	if tools == nil {
		tools = []db.CustomTool{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: tools})
}

// createCustomToolRequest is the JSON body for POST /dashboard/api/tools/custom.
type createCustomToolRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Parameters    string `json:"parameters"`
	Backend       string `json:"backend"`
	BackendConfig string `json:"backend_config"`
	Enabled       bool   `json:"enabled"`
}

// handleAdminCreateCustomTool creates a new custom tool.
func (h *Handler) handleAdminCreateCustomTool(w http.ResponseWriter, _ *http.Request, body string) {
	var req createCustomToolRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Name == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	if req.Backend == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "backend is required"})
		return
	}

	tool := &db.CustomTool{
		Name:          req.Name,
		Description:   req.Description,
		Parameters:    req.Parameters,
		Backend:       req.Backend,
		BackendConfig: req.BackendConfig,
		Enabled:       req.Enabled,
	}

	if err := h.api.database.InsertCustomTool(tool); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			h.jsonResponse(w, http.StatusConflict, errorResponse{Error: fmt.Sprintf("tool %q already exists", req.Name)})
			return
		}
		h.logger.Error("admin API: create custom tool", "name", req.Name, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to create custom tool"})
		return
	}

	// Re-read to get timestamps.
	created, err := h.api.database.GetCustomTool(req.Name)
	if err != nil {
		h.jsonResponse(w, http.StatusCreated, successResponse{OK: true, Data: tool})
		return
	}
	h.jsonResponse(w, http.StatusCreated, successResponse{OK: true, Data: created})
}

// updateCustomToolRequest is the JSON body for PUT /dashboard/api/tools/custom/:name.
type updateCustomToolRequest struct {
	Description   *string `json:"description"`
	Parameters    *string `json:"parameters"`
	Backend       *string `json:"backend"`
	BackendConfig *string `json:"backend_config"`
	Enabled       *bool   `json:"enabled"`
}

// handleAdminUpdateCustomTool updates an existing custom tool.
func (h *Handler) handleAdminUpdateCustomTool(w http.ResponseWriter, _ *http.Request, name, body string) {
	if name == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "tool name is required"})
		return
	}

	existing, err := h.api.database.GetCustomTool(name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "tool not found"})
			return
		}
		h.logger.Error("admin API: get custom tool for update", "name", name, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to get tool"})
		return
	}

	var req updateCustomToolRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.Parameters != nil {
		existing.Parameters = *req.Parameters
	}
	if req.Backend != nil {
		existing.Backend = *req.Backend
	}
	if req.BackendConfig != nil {
		existing.BackendConfig = *req.BackendConfig
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}

	if err := h.api.database.UpdateCustomTool(existing); err != nil {
		h.logger.Error("admin API: update custom tool", "name", name, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to update tool"})
		return
	}

	updated, err := h.api.database.GetCustomTool(name)
	if err != nil {
		h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: existing})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: updated})
}

// handleAdminDeleteCustomTool deletes a custom tool by name.
func (h *Handler) handleAdminDeleteCustomTool(w http.ResponseWriter, _ *http.Request, name string) {
	if name == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "tool name is required"})
		return
	}

	if err := h.api.database.DeleteCustomTool(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "tool not found"})
			return
		}
		h.logger.Error("admin API: delete custom tool", "name", name, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete tool"})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true})
}

// toggleRequest is the JSON body for toggle endpoints.
type toggleRequest struct {
	Enabled bool `json:"enabled"`
}

// handleAdminToggleCustomTool enables or disables a custom tool.
func (h *Handler) handleAdminToggleCustomTool(w http.ResponseWriter, _ *http.Request, name, body string) {
	if name == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "tool name is required"})
		return
	}

	var req toggleRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if err := h.api.database.SetCustomToolEnabled(name, req.Enabled); err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "tool not found"})
			return
		}
		h.logger.Error("admin API: toggle custom tool", "name", name, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to toggle tool"})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true})
}

// --- Tasks API ---

// handleAdminListTasks returns all scheduled tasks.
func (h *Handler) handleAdminListTasks(w http.ResponseWriter, _ *http.Request) {
	if h.api.tasks == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "task management not available"})
		return
	}
	tasks, err := h.api.tasks.ListTasks()
	if err != nil {
		h.logger.Error("admin API: list tasks", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to list tasks"})
		return
	}
	if tasks == nil {
		tasks = []TaskInfo{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: tasks})
}

// createTaskRequest is the JSON body for POST /dashboard/api/tasks.
type createTaskRequest struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"` // cron expression; empty for one-shot
	Action   string `json:"action"`
	Channel  string `json:"channel"`
	Type     string `json:"type"`     // "cron" or "once"
	RunAt    string `json:"run_at"`   // RFC3339 for one-shot tasks
	Provider string `json:"provider"` // optional LLM provider override
}

// handleAdminCreateTask creates a new scheduled task.
func (h *Handler) handleAdminCreateTask(w http.ResponseWriter, _ *http.Request, body, createdBy string) {
	if h.api.tasks == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "task management not available"})
		return
	}

	var req createTaskRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Name == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	if req.Action == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "action is required"})
		return
	}
	if req.Channel == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "channel is required"})
		return
	}

	req.Provider = strings.TrimSpace(req.Provider)

	var id int64
	var err error

	switch req.Type {
	case "once":
		if req.RunAt == "" {
			h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "run_at is required for one-shot tasks"})
			return
		}
		runAt, parseErr := time.Parse(time.RFC3339, req.RunAt)
		if parseErr != nil {
			h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "run_at must be RFC3339 format"})
			return
		}
		id, err = h.api.tasks.AddOneShotTask(req.Name, runAt, req.Action, req.Channel, createdBy, req.Provider)
	case "cron", "":
		if req.Schedule == "" {
			h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "schedule is required for cron tasks"})
			return
		}
		id, err = h.api.tasks.AddTask(req.Name, req.Schedule, req.Action, req.Channel, createdBy, req.Provider)
	default:
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "type must be 'cron' or 'once'"})
		return
	}

	if err != nil {
		h.logger.Error("admin API: create task", "name", req.Name, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: fmt.Sprintf("failed to create task: %v", err)})
		return
	}

	h.jsonResponse(w, http.StatusCreated, successResponse{OK: true, Data: map[string]int64{"id": id}})
}

// handleAdminDeleteTask deletes a scheduled task by ID.
func (h *Handler) handleAdminDeleteTask(w http.ResponseWriter, _ *http.Request, idStr string) {
	if h.api.tasks == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "task management not available"})
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid task ID"})
		return
	}

	if err := h.api.tasks.RemoveTask(id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "task not found"})
			return
		}
		h.logger.Error("admin API: delete task", "id", id, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete task"})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true})
}

// handleAdminToggleTask enables or disables a scheduled task.
func (h *Handler) handleAdminToggleTask(w http.ResponseWriter, _ *http.Request, idStr, body string) {
	if h.api.tasks == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "task management not available"})
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid task ID"})
		return
	}

	var req toggleRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Enabled {
		err = h.api.tasks.EnableTask(id)
	} else {
		err = h.api.tasks.DisableTask(id)
	}

	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.jsonResponse(w, http.StatusNotFound, errorResponse{Error: "task not found"})
			return
		}
		h.logger.Error("admin API: toggle task", "id", id, "enabled", req.Enabled, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: fmt.Sprintf("failed to toggle task: %v", err)})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true})
}

// --- Channels API ---

// handleAdminListChannels returns all channel settings.
func (h *Handler) handleAdminListChannels(w http.ResponseWriter, _ *http.Request) {
	if h.api.channels == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "channel management not available"})
		return
	}
	channels, err := h.api.channels.ListChannels()
	if err != nil {
		h.logger.Error("admin API: list channels", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to list channels"})
		return
	}
	if channels == nil {
		channels = []ChannelSettingsInfo{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: channels})
}

// handleAdminUpdateChannel updates channel settings. The channel name is
// URL-decoded from the path parameter (e.g., %23murmur → #murmur).
func (h *Handler) handleAdminUpdateChannel(w http.ResponseWriter, _ *http.Request, channel, body string) {
	if h.api.channels == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "channel management not available"})
		return
	}

	// URL-decode the channel name — clients encode # as %23 in paths.
	decoded, err := url.PathUnescape(channel)
	if err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid channel name encoding"})
		return
	}
	channel = decoded

	if channel == "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "channel name is required"})
		return
	}

	var req ChannelSettingsInfo
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.Channel = channel

	if err := h.api.channels.UpdateChannel(&req); err != nil {
		h.logger.Error("admin API: update channel", "channel", channel, "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to update channel"})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true})
}

// --- Providers API ---

// handleAdminListProviders returns all configured LLM providers.
func (h *Handler) handleAdminListProviders(w http.ResponseWriter, _ *http.Request) {
	if h.api.providers == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "provider listing not available"})
		return
	}
	providers := h.api.providers.ListProviders()
	if providers == nil {
		providers = []ProviderInfo{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: providers})
}

// --- System API ---

// handleAdminReload triggers a server configuration reload.
func (h *Handler) handleAdminReload(w http.ResponseWriter, _ *http.Request) {
	if h.api.reloader == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "config reload not available"})
		return
	}

	if err := h.api.reloader.Reload(); err != nil {
		h.logger.Error("admin API: reload config", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: fmt.Sprintf("reload failed: %v", err)})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true})
}

// --- Statistics API ---

// maxStatsLimit is the maximum number of rows returned by the stats list endpoint.
const maxStatsLimit = 200

// maxDateRangeDays is the maximum date range span allowed for stats queries.
const maxDateRangeDays = 365

// allowedPeriods is the set of valid aggregation periods.
var allowedPeriods = map[string]bool{
	"hour":  true,
	"day":   true,
	"week":  true,
	"month": true,
}

// allowedRequestTypes is the set of valid request_type filter values.
var allowedRequestTypes = map[string]bool{
	"chat":              true,
	"task":              true,
	"event":             true,
	"summary":           true,
	"iteration_summary": true,
	"pause_summary":     true,
}

// parseStatsQuery extracts and validates StatsQueryParams from URL query
// parameters. Returns an error string if validation fails.
func parseStatsQuery(q url.Values) (StatsQueryParams, string) {
	var p StatsQueryParams

	p.Channel = q.Get("channel")
	p.Nick = q.Get("nick")
	p.Provider = q.Get("provider")

	if rt := q.Get("request_type"); rt != "" {
		if !allowedRequestTypes[rt] {
			return p, "request_type must be one of: chat, task, event, summary, iteration_summary, pause_summary"
		}
		p.RequestType = rt
	}

	if fromStr := q.Get("from"); fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return p, "from must be RFC3339 format"
		}
		p.From = t
	}

	if toStr := q.Get("to"); toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return p, "to must be RFC3339 format"
		}
		p.To = t
	}

	// Validate date range span.
	if !p.From.IsZero() && !p.To.IsZero() {
		if p.To.Before(p.From) {
			return p, "to must be after from"
		}
		if p.To.Sub(p.From).Hours()/24 > maxDateRangeDays {
			return p, fmt.Sprintf("date range must not exceed %d days", maxDateRangeDays)
		}
	}

	if limitStr := q.Get("limit"); limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit < 1 {
			return p, "limit must be a positive integer"
		}
		if limit > maxStatsLimit {
			limit = maxStatsLimit
		}
		p.Limit = limit
	}

	if offsetStr := q.Get("offset"); offsetStr != "" {
		offset, err := strconv.Atoi(offsetStr)
		if err != nil || offset < 0 {
			return p, "offset must be a non-negative integer"
		}
		p.Offset = offset
	}

	return p, ""
}

// handleAdminStatsSummary returns a high-level summary of usage statistics.
func (h *Handler) handleAdminStatsSummary(w http.ResponseWriter, r *http.Request) {
	if h.api.stats == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "statistics not available"})
		return
	}

	q, errMsg := parseStatsQuery(r.URL.Query())
	if errMsg != "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: errMsg})
		return
	}

	summary, err := h.api.stats.GetSummary(r.Context(), q)
	if err != nil {
		h.logger.Error("admin API: stats summary", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to get stats summary"})
		return
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: summary})
}

// handleAdminStatsAggregate returns time-bucketed aggregation of usage statistics.
func (h *Handler) handleAdminStatsAggregate(w http.ResponseWriter, r *http.Request) {
	if h.api.stats == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "statistics not available"})
		return
	}

	q, errMsg := parseStatsQuery(r.URL.Query())
	if errMsg != "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: errMsg})
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "day"
	}
	if !allowedPeriods[period] {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: "period must be one of: hour, day, week, month"})
		return
	}

	agg, err := h.api.stats.GetAggregate(r.Context(), q, period)
	if err != nil {
		h.logger.Error("admin API: stats aggregate", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to get stats aggregate"})
		return
	}
	if agg == nil {
		agg = []StatsAggregationDTO{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: agg})
}

// handleAdminStatsTools returns aggregated tool usage statistics.
func (h *Handler) handleAdminStatsTools(w http.ResponseWriter, r *http.Request) {
	if h.api.stats == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "statistics not available"})
		return
	}

	q, errMsg := parseStatsQuery(r.URL.Query())
	if errMsg != "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: errMsg})
		return
	}

	tools, err := h.api.stats.GetTopTools(r.Context(), q)
	if err != nil {
		h.logger.Error("admin API: stats tools", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to get tool stats"})
		return
	}
	if tools == nil {
		tools = []ToolUsageDTO{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: tools})
}

// handleAdminStatsProviders returns aggregated per-provider statistics.
func (h *Handler) handleAdminStatsProviders(w http.ResponseWriter, r *http.Request) {
	if h.api.stats == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "statistics not available"})
		return
	}

	q, errMsg := parseStatsQuery(r.URL.Query())
	if errMsg != "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: errMsg})
		return
	}

	providers, err := h.api.stats.GetProviderBreakdown(r.Context(), q)
	if err != nil {
		h.logger.Error("admin API: stats providers", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to get provider stats"})
		return
	}
	if providers == nil {
		providers = []ProviderStatDTO{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: providers})
}

// handleAdminStatsList returns a paginated list of raw usage statistics.
func (h *Handler) handleAdminStatsList(w http.ResponseWriter, r *http.Request) {
	if h.api.stats == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "statistics not available"})
		return
	}

	q, errMsg := parseStatsQuery(r.URL.Query())
	if errMsg != "" {
		h.jsonResponse(w, http.StatusBadRequest, errorResponse{Error: errMsg})
		return
	}

	stats, total, err := h.api.stats.ListStats(r.Context(), q)
	if err != nil {
		h.logger.Error("admin API: stats list", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "failed to list stats"})
		return
	}
	if stats == nil {
		stats = []UsageStatDTO{}
	}
	h.jsonResponse(w, http.StatusOK, successResponse{OK: true, Data: map[string]any{
		"stats": stats,
		"total": total,
	}})
}

// --- Helpers ---

// defaultStringSlice returns the input slice if non-nil, otherwise the default.
func defaultStringSlice(input, def []string) db.StringSlice {
	if input != nil {
		return db.StringSlice(input)
	}
	if def != nil {
		return db.StringSlice(def)
	}
	return db.StringSlice{}
}
