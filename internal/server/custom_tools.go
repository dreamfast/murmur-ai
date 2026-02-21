package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"murmur/internal/db"
	"murmur/internal/tools"
)

// validToolName matches lowercase letters, digits, and underscores. Must start
// with a letter and be at least 2 characters long.
var validToolName = regexp.MustCompile(`^[a-z][a-z0-9_]+$`)

// ToolExecutor executes a named tool with the given arguments. It is used by
// the pipeline backend to invoke other tools (server-side, shell, or bus/client)
// without going through the LLM. The Agent implements this interface.
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, toolName string, args map[string]any) (string, error)
}

// pipelineDepthKey is the context key used to track pipeline nesting depth.
// Pipelines set this to prevent recursive pipeline invocations.
type pipelineDepthKey struct{}

// customToolHTTPTimeout is the default timeout for HTTP backend requests.
const customToolHTTPTimeout = 30 * time.Second

// customToolShellTimeout is the default timeout for shell backend commands.
const customToolShellTimeout = 60 * time.Second

// maxCustomToolOutputBytes limits the response body size for HTTP backend requests.
const maxCustomToolOutputBytes = 1024 * 1024 // 1MB

// pistonRunTimeout is the Piston execution timeout in milliseconds for
// custom tool code_exec backends. This is a client-side safety limit;
// Piston also enforces its own PISTON_RUN_TIMEOUT server-side.
const pistonRunTimeout = 30000

// pipelineTimeout is the overall timeout for a pipeline execution.
const pipelineTimeout = 5 * time.Minute

// maxPipelineSteps is the maximum number of steps allowed in a pipeline.
const maxPipelineSteps = 10

// maxPipelineIntermediateBytes limits intermediate output between pipeline steps.
const maxPipelineIntermediateBytes = 25 * 1024

// CustomToolManager manages runtime-created custom tools. It provides
// meta-tools for creating, listing, deleting, enabling, and disabling
// custom tools, and handles execution dispatch to shell, HTTP, or code_exec
// backends. An in-memory cache avoids repeated database reads during
// tool execution.
type CustomToolManager struct {
	db       *db.DB
	registry *ToolRegistry
	logger   *slog.Logger

	// pistonURL is the Piston API base URL for code_exec backend.
	// Empty string means code_exec backend is unavailable.
	pistonURL string

	// httpClient is a shared HTTP client for HTTP and code_exec backends.
	// It is safe for concurrent use.
	httpClient *http.Client

	// executor is set after Agent creation to allow pipeline steps to invoke
	// other tools. It is nil until SetExecutor is called.
	executor ToolExecutor

	mu    sync.RWMutex
	cache map[string]db.CustomTool
}

// NewCustomToolManager creates a new CustomToolManager. The pistonURL is
// used for the code_exec backend; pass empty string if Piston is not available.
func NewCustomToolManager(database *db.DB, registry *ToolRegistry, pistonURL string, logger *slog.Logger) *CustomToolManager {
	return &CustomToolManager{
		db:        database,
		registry:  registry,
		logger:    logger,
		pistonURL: pistonURL,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
		cache: make(map[string]db.CustomTool),
	}
}

// SetExecutor sets the ToolExecutor used by the pipeline backend to invoke
// other tools. This must be called after the Agent is created, since the Agent
// implements ToolExecutor and depends on CustomToolManager (circular dependency
// broken by late binding).
func (m *CustomToolManager) SetExecutor(executor ToolExecutor) {
	m.executor = executor
}

// LoadFromDB loads all enabled custom tools from the database, creates Tool
// wrappers, and registers them on the ToolRegistry. This should be called
// once during server startup. Tools that conflict with built-in tool names
// are skipped with a warning.
func (m *CustomToolManager) LoadFromDB() error {
	customTools, err := m.db.ListCustomTools(true)
	if err != nil {
		return fmt.Errorf("CustomToolManager.LoadFromDB: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ct := range customTools {
		if m.registry.HasTool(ct.Name) {
			m.logger.Warn("custom tool name conflicts with built-in tool, skipping",
				"name", ct.Name)
			continue
		}

		tool := m.wrapCustomTool(ct)
		if err := m.registry.Register(tool); err != nil {
			m.logger.Warn("failed to register custom tool from DB",
				"name", ct.Name, "error", err)
			continue
		}
		m.cache[ct.Name] = ct
		m.logger.Info("loaded custom tool from DB", "name", ct.Name, "backend", ct.Backend)
	}

	return nil
}

// RegisterMetaTools registers the 5 meta-tools (tool_create, tool_list,
// tool_delete, tool_enable, tool_disable) on the given ToolRegistry.
func (m *CustomToolManager) RegisterMetaTools(registry *ToolRegistry) error {
	metaTools := []tools.Tool{
		m.toolCreate(),
		m.toolList(),
		m.toolDelete(),
		m.toolEnable(),
		m.toolDisable(),
	}

	for _, t := range metaTools {
		if err := registry.Register(t); err != nil {
			return fmt.Errorf("RegisterMetaTools: %w", err)
		}
	}
	return nil
}

// toolCreate returns the tool_create meta-tool.
func (m *CustomToolManager) toolCreate() tools.Tool {
	return tools.Tool{
		Name:        "tool_create",
		Description: "Create a new custom tool with a shell, HTTP, code_exec, or pipeline backend. The tool becomes immediately available for use.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Unique tool name (lowercase, underscores allowed, e.g. 'check_weather')"
				},
				"description": {
					"type": "string",
					"description": "Human-readable description of what the tool does"
				},
				"parameters": {
					"type": "string",
					"description": "JSON Schema string defining the tool's input parameters (e.g. '{\"type\":\"object\",\"properties\":{\"city\":{\"type\":\"string\"}},\"required\":[\"city\"]}')"
				},
				"backend": {
					"type": "string",
					"enum": ["shell", "http", "code_exec", "pipeline"],
					"description": "Execution backend: 'shell' runs a Docker command, 'http' makes an HTTP request, 'code_exec' runs code via Piston, 'pipeline' chains multiple tool calls sequentially"
				},
				"backend_config": {
					"type": "string",
					"description": "JSON config for the backend. Shell: {\"command\":\"curl {{city}}\"}. HTTP: {\"url\":\"https://api.example.com/{{city}}\",\"method\":\"GET\"}. Code_exec: {\"language\":\"python\",\"code\":\"print('{{city}}')\"}.  Pipeline: {\"steps\":[{\"tool\":\"shell\",\"args\":{\"command\":\"echo {{input}}\"}}]}"
				}
			},
			"required": ["name", "description", "parameters", "backend", "backend_config"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return m.handleCreate(ctx, args)
		},
	}
}

// toolList returns the tool_list meta-tool.
func (m *CustomToolManager) toolList() tools.Tool {
	return tools.Tool{
		Name:        "tool_list",
		Description: "List all custom tools with their name, description, backend, and enabled status.",
		Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return m.handleList()
		},
	}
}

// toolDelete returns the tool_delete meta-tool.
func (m *CustomToolManager) toolDelete() tools.Tool {
	return tools.Tool{
		Name:        "tool_delete",
		Description: "Permanently delete a custom tool by name.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Name of the custom tool to delete"
				}
			},
			"required": ["name"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return m.handleDelete(ctx, args)
		},
	}
}

// toolEnable returns the tool_enable meta-tool.
func (m *CustomToolManager) toolEnable() tools.Tool {
	return tools.Tool{
		Name:        "tool_enable",
		Description: "Enable a disabled custom tool, making it available for use.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Name of the custom tool to enable"
				}
			},
			"required": ["name"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return m.handleEnable(ctx, args)
		},
	}
}

// toolDisable returns the tool_disable meta-tool.
func (m *CustomToolManager) toolDisable() tools.Tool {
	return tools.Tool{
		Name:        "tool_disable",
		Description: "Disable a custom tool without deleting it. The tool will no longer be available for use.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"description": "Name of the custom tool to disable"
				}
			},
			"required": ["name"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return m.handleDisable(ctx, args)
		},
	}
}

// handleCreate validates inputs, inserts the custom tool into the database,
// creates a Tool wrapper, and registers it on the ToolRegistry.
func (m *CustomToolManager) handleCreate(_ context.Context, args map[string]any) (string, error) {
	name, err := tools.RequireStringArg(args, "name")
	if err != nil {
		return "", err
	}
	description, err := tools.RequireStringArg(args, "description")
	if err != nil {
		return "", err
	}
	parameters, err := tools.RequireStringArg(args, "parameters")
	if err != nil {
		return "", err
	}
	backend, err := tools.RequireStringArg(args, "backend")
	if err != nil {
		return "", err
	}
	backendConfig, err := tools.RequireStringArg(args, "backend_config")
	if err != nil {
		return "", err
	}

	// Normalize and validate tool name.
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("tool_create: name must not be empty")
	}
	if !validToolName.MatchString(name) {
		return "", fmt.Errorf("tool_create: name %q is invalid, must be lowercase letters, digits, and underscores (e.g. 'check_weather')", name)
	}

	// Validate backend.
	switch backend {
	case "shell", "http", "code_exec", "pipeline":
		// valid
	default:
		return "", fmt.Errorf("tool_create: invalid backend %q, must be shell, http, code_exec, or pipeline", backend)
	}

	// Validate code_exec backend requires Piston URL.
	if backend == "code_exec" && m.pistonURL == "" {
		return "", fmt.Errorf("tool_create: code_exec backend requires Piston to be configured (tools.code_exec.piston_url)")
	}

	// Validate parameters is valid JSON.
	if !json.Valid([]byte(parameters)) {
		return "", fmt.Errorf("tool_create: parameters must be valid JSON")
	}

	// Validate backend_config is valid JSON.
	if !json.Valid([]byte(backendConfig)) {
		return "", fmt.Errorf("tool_create: backend_config must be valid JSON")
	}

	// Pipeline-specific validation at creation time.
	if backend == "pipeline" {
		if err := m.validatePipelineConfig(name, backendConfig); err != nil {
			return "", err
		}
	}

	// Check for name collision with built-in tools.
	if m.registry.HasTool(name) {
		// Check if it's a custom tool we own (re-create scenario) vs built-in.
		m.mu.RLock()
		_, isCustom := m.cache[name]
		m.mu.RUnlock()
		if !isCustom {
			return "", fmt.Errorf("tool_create: name %q conflicts with a built-in tool", name)
		}
		return "", fmt.Errorf("tool_create: custom tool %q already exists, delete it first to recreate", name)
	}

	ct := &db.CustomTool{
		Name:          name,
		Description:   description,
		Parameters:    parameters,
		Backend:       backend,
		BackendConfig: backendConfig,
		Enabled:       true,
	}

	if err := m.db.InsertCustomTool(ct); err != nil {
		return "", fmt.Errorf("tool_create: %w", err)
	}

	// Register the tool on the registry and update cache.
	tool := m.wrapCustomTool(*ct)
	if err := m.registry.Register(tool); err != nil {
		// Rollback DB insert on registration failure.
		_ = m.db.DeleteCustomTool(name)
		return "", fmt.Errorf("tool_create: register: %w", err)
	}

	m.mu.Lock()
	m.cache[name] = *ct
	m.mu.Unlock()

	m.logger.Info("custom tool created", "name", name, "backend", backend)
	return fmt.Sprintf("Custom tool %q created and registered (backend: %s).", name, backend), nil
}

// handleList returns a formatted list of all custom tools from the database,
// including both enabled and disabled tools.
func (m *CustomToolManager) handleList() (string, error) {
	allTools, err := m.db.ListCustomTools(false)
	if err != nil {
		return "", fmt.Errorf("tool_list: %w", err)
	}

	if len(allTools) == 0 {
		return "No custom tools defined.", nil
	}

	var lines []string
	for _, ct := range allTools {
		status := "enabled"
		if !ct.Enabled {
			status = "disabled"
		}
		lines = append(lines, fmt.Sprintf("  %s [%s] (%s) — %s", ct.Name, status, ct.Backend, ct.Description))
	}
	return fmt.Sprintf("Custom tools (%d):\n%s", len(allTools), strings.Join(lines, "\n")), nil
}

// handleDelete removes a custom tool from the database, cache, and registry.
func (m *CustomToolManager) handleDelete(_ context.Context, args map[string]any) (string, error) {
	name, err := tools.RequireStringArg(args, "name")
	if err != nil {
		return "", err
	}

	if err := m.db.DeleteCustomTool(name); err != nil {
		return "", fmt.Errorf("tool_delete: %w", err)
	}

	m.registry.Unregister(name)

	m.mu.Lock()
	delete(m.cache, name)
	m.mu.Unlock()

	m.logger.Info("custom tool deleted", "name", name)
	return fmt.Sprintf("Custom tool %q deleted.", name), nil
}

// handleEnable enables a custom tool and re-registers it on the ToolRegistry.
// The conflict check is performed before the DB update to avoid leaving the
// tool enabled in the database but unregistered at runtime.
func (m *CustomToolManager) handleEnable(_ context.Context, args map[string]any) (string, error) {
	name, err := tools.RequireStringArg(args, "name")
	if err != nil {
		return "", err
	}

	// Preflight: check for name collision with built-in tools before DB update.
	if m.registry.HasTool(name) {
		m.mu.RLock()
		_, isCustom := m.cache[name]
		m.mu.RUnlock()
		if !isCustom {
			return "", fmt.Errorf("tool_enable: name %q conflicts with a built-in tool", name)
		}
		// Already registered as a custom tool — will just update cache below.
	}

	if err := m.db.SetCustomToolEnabled(name, true); err != nil {
		return "", fmt.Errorf("tool_enable: %w", err)
	}

	// Fetch the full tool from DB to register it.
	ct, err := m.db.GetCustomTool(name)
	if err != nil {
		return "", fmt.Errorf("tool_enable: fetch tool: %w", err)
	}

	// Register if not already present.
	if !m.registry.HasTool(name) {
		tool := m.wrapCustomTool(*ct)
		if err := m.registry.Register(tool); err != nil {
			// Rollback DB state on registration failure.
			_ = m.db.SetCustomToolEnabled(name, false)
			return "", fmt.Errorf("tool_enable: register: %w", err)
		}
	}

	m.mu.Lock()
	m.cache[name] = *ct
	m.mu.Unlock()

	m.logger.Info("custom tool enabled", "name", name)
	return fmt.Sprintf("Custom tool %q enabled.", name), nil
}

// handleDisable disables a custom tool and unregisters it from the ToolRegistry.
func (m *CustomToolManager) handleDisable(_ context.Context, args map[string]any) (string, error) {
	name, err := tools.RequireStringArg(args, "name")
	if err != nil {
		return "", err
	}

	if err := m.db.SetCustomToolEnabled(name, false); err != nil {
		return "", fmt.Errorf("tool_disable: %w", err)
	}

	m.registry.Unregister(name)

	m.mu.Lock()
	delete(m.cache, name)
	m.mu.Unlock()

	m.logger.Info("custom tool disabled", "name", name)
	return fmt.Sprintf("Custom tool %q disabled.", name), nil
}

// wrapCustomTool creates a tools.Tool from a db.CustomTool. The returned
// tool's Handler dispatches to the appropriate backend (shell, HTTP, or
// code_exec) with argument substitution.
func (m *CustomToolManager) wrapCustomTool(ct db.CustomTool) tools.Tool {
	return tools.Tool{
		Name:        ct.Name,
		Description: ct.Description,
		Parameters:  json.RawMessage(ct.Parameters),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return m.execute(ctx, ct, args)
		},
	}
}

// execute dispatches a custom tool invocation to the appropriate backend.
func (m *CustomToolManager) execute(ctx context.Context, ct db.CustomTool, args map[string]any) (string, error) {
	switch ct.Backend {
	case "shell":
		return m.executeShell(ctx, ct, args)
	case "http":
		return m.executeHTTP(ctx, ct, args)
	case "code_exec":
		return m.executeCodeExec(ctx, ct, args)
	case "pipeline":
		return m.executePipeline(ctx, ct, args)
	default:
		return "", fmt.Errorf("execute: unknown backend %q for tool %q", ct.Backend, ct.Name)
	}
}

// shellBackendConfig is the expected JSON structure for shell backend config.
type shellBackendConfig struct {
	Command string `json:"command"`
}

// httpBackendConfig is the expected JSON structure for HTTP backend config.
type httpBackendConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// codeExecBackendConfig is the expected JSON structure for code_exec backend config.
type codeExecBackendConfig struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Version  string `json:"version"`
}

// substituteArgs performs simple string substitution on a template string,
// replacing {{key}} with the corresponding value from args. All values are
// converted to their string representation.
func substituteArgs(template string, args map[string]any) string {
	result := template
	for key, value := range args {
		strVal := fmt.Sprintf("%v", value)
		result = strings.ReplaceAll(result, "{{"+key+"}}", strVal)
	}
	return result
}

// shellQuoteArg wraps a value in single quotes for safe use in shell commands,
// escaping any embedded single quotes. This prevents command injection via
// argument values.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// substituteArgsShell performs shell-safe string substitution on a template,
// replacing {{key}} with single-quoted values to prevent command injection.
func substituteArgsShell(template string, args map[string]any) string {
	result := template
	for key, value := range args {
		strVal := fmt.Sprintf("%v", value)
		result = strings.ReplaceAll(result, "{{"+key+"}}", shellQuoteArg(strVal))
	}
	return result
}

// executeShell runs a shell command in a Docker container with argument substitution.
func (m *CustomToolManager) executeShell(ctx context.Context, ct db.CustomTool, args map[string]any) (string, error) {
	var cfg shellBackendConfig
	if err := json.Unmarshal([]byte(ct.BackendConfig), &cfg); err != nil {
		return "", fmt.Errorf("custom tool %q: invalid shell backend_config: %w", ct.Name, err)
	}
	if cfg.Command == "" {
		return "", fmt.Errorf("custom tool %q: shell backend_config.command is required", ct.Name)
	}

	// Use shell-safe substitution to prevent command injection via arguments.
	rendered := substituteArgsShell(cfg.Command, args)

	ctx, cancel := context.WithTimeout(ctx, customToolShellTimeout)
	defer cancel()

	// Execute via Docker with security hardening, matching the shell tool pattern.
	dockerArgs := []string{
		"run",
		"--rm",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--read-only",
		"--network=none",
		"--memory=256m",
		"--cpus=0.5",
		"ubuntu:24.04",
		"bash", "-c", rendered,
	}

	output, err := tools.RunCommand(ctx, "docker", dockerArgs...)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("custom tool %q: shell command timed out: %w", ct.Name, ctx.Err())
		}
		// Return output alongside error for non-zero exit codes.
		if output != "" {
			return output, nil
		}
		return "", fmt.Errorf("custom tool %q: shell: %w", ct.Name, err)
	}

	return tools.TruncateOutput(output), nil
}

// executeHTTP makes an HTTP request with argument substitution.
func (m *CustomToolManager) executeHTTP(ctx context.Context, ct db.CustomTool, args map[string]any) (string, error) {
	var cfg httpBackendConfig
	if err := json.Unmarshal([]byte(ct.BackendConfig), &cfg); err != nil {
		return "", fmt.Errorf("custom tool %q: invalid http backend_config: %w", ct.Name, err)
	}
	if cfg.URL == "" {
		return "", fmt.Errorf("custom tool %q: http backend_config.url is required", ct.Name)
	}
	if cfg.Method == "" {
		cfg.Method = "GET"
	}

	renderedURL := substituteArgs(cfg.URL, args)
	renderedBody := substituteArgs(cfg.Body, args)

	ctx, cancel := context.WithTimeout(ctx, customToolHTTPTimeout)
	defer cancel()

	var bodyReader io.Reader
	if renderedBody != "" {
		bodyReader = strings.NewReader(renderedBody)
	}

	req, err := http.NewRequestWithContext(ctx, cfg.Method, renderedURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("custom tool %q: http: create request: %w", ct.Name, err)
	}

	for k, v := range cfg.Headers {
		req.Header.Set(k, substituteArgs(v, args))
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("custom tool %q: http request timed out: %w", ct.Name, ctx.Err())
		}
		return "", fmt.Errorf("custom tool %q: http: %w", ct.Name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxCustomToolOutputBytes)))
	if err != nil {
		return "", fmt.Errorf("custom tool %q: http: read response: %w", ct.Name, err)
	}

	result := fmt.Sprintf("HTTP %d %s\n%s", resp.StatusCode, resp.Status, string(body))
	return tools.TruncateOutput(result), nil
}

// executeCodeExec runs code via the Piston API with argument substitution.
func (m *CustomToolManager) executeCodeExec(ctx context.Context, ct db.CustomTool, args map[string]any) (string, error) {
	if m.pistonURL == "" {
		return "", fmt.Errorf("custom tool %q: code_exec backend requires Piston to be configured", ct.Name)
	}

	var cfg codeExecBackendConfig
	if err := json.Unmarshal([]byte(ct.BackendConfig), &cfg); err != nil {
		return "", fmt.Errorf("custom tool %q: invalid code_exec backend_config: %w", ct.Name, err)
	}
	if cfg.Language == "" {
		return "", fmt.Errorf("custom tool %q: code_exec backend_config.language is required", ct.Name)
	}
	if cfg.Code == "" {
		return "", fmt.Errorf("custom tool %q: code_exec backend_config.code is required", ct.Name)
	}

	renderedCode := substituteArgs(cfg.Code, args)

	version := cfg.Version
	if version == "" {
		version = "*"
	}

	// Build Piston request.
	pistonReq := map[string]any{
		"language": cfg.Language,
		"version":  version,
		"files": []map[string]string{
			{"content": renderedCode},
		},
		"run_timeout": pistonRunTimeout,
	}

	reqBody, err := json.Marshal(pistonReq)
	if err != nil {
		return "", fmt.Errorf("custom tool %q: code_exec: marshal request: %w", ct.Name, err)
	}

	url := strings.TrimRight(m.pistonURL, "/") + "/api/v2/execute"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("custom tool %q: code_exec: create request: %w", ct.Name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("custom tool %q: code_exec: request timed out: %w", ct.Name, ctx.Err())
		}
		return "", fmt.Errorf("custom tool %q: code_exec: %w", ct.Name, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxCustomToolOutputBytes)))
	if err != nil {
		return "", fmt.Errorf("custom tool %q: code_exec: read response: %w", ct.Name, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("custom tool %q: code_exec: Piston returned HTTP %d: %s", ct.Name, resp.StatusCode, string(respBody))
	}

	// Parse Piston response to extract output.
	var pistonResp struct {
		Language string `json:"language"`
		Version  string `json:"version"`
		Run      struct {
			Stdout string  `json:"stdout"`
			Stderr string  `json:"stderr"`
			Code   int     `json:"code"`
			Signal *string `json:"signal"`
		} `json:"run"`
		Compile *struct {
			Stderr string `json:"stderr"`
			Code   int    `json:"code"`
		} `json:"compile"`
	}
	if err := json.Unmarshal(respBody, &pistonResp); err != nil {
		return "", fmt.Errorf("custom tool %q: code_exec: parse response: %w", ct.Name, err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Language: %s %s\n", pistonResp.Language, pistonResp.Version))

	if pistonResp.Compile != nil && pistonResp.Compile.Code != 0 {
		sb.WriteString(fmt.Sprintf("Compile error (exit %d):\n%s", pistonResp.Compile.Code, pistonResp.Compile.Stderr))
		return tools.TruncateOutput(sb.String()), nil
	}

	sb.WriteString(fmt.Sprintf("Exit code: %d\n", pistonResp.Run.Code))
	if pistonResp.Run.Signal != nil {
		sb.WriteString(fmt.Sprintf("Signal: %s\n", *pistonResp.Run.Signal))
	}
	if pistonResp.Run.Stdout != "" {
		sb.WriteString("Stdout:\n")
		sb.WriteString(pistonResp.Run.Stdout)
	}
	if pistonResp.Run.Stderr != "" {
		sb.WriteString("Stderr:\n")
		sb.WriteString(pistonResp.Run.Stderr)
	}

	return tools.TruncateOutput(sb.String()), nil
}

// pipelineBackendConfig is the expected JSON structure for pipeline backend config.
type pipelineBackendConfig struct {
	Steps []pipelineStep `json:"steps"`
}

// pipelineStep is a single step in a pipeline.
type pipelineStep struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// validatePipelineConfig validates a pipeline backend_config at creation time.
// It checks structural validity, step count limits, self-reference prevention,
// and that all referenced tools exist (either as server tools, custom tools, or
// client tools).
func (m *CustomToolManager) validatePipelineConfig(toolName, backendConfig string) error {
	var cfg pipelineBackendConfig
	if err := json.Unmarshal([]byte(backendConfig), &cfg); err != nil {
		return fmt.Errorf("tool_create: invalid pipeline backend_config: %w", err)
	}

	if len(cfg.Steps) == 0 {
		return fmt.Errorf("tool_create: pipeline must have at least one step")
	}
	if len(cfg.Steps) > maxPipelineSteps {
		return fmt.Errorf("tool_create: pipeline exceeds maximum of %d steps (got %d)", maxPipelineSteps, len(cfg.Steps))
	}

	// Hold the read lock for the entire loop to get a consistent snapshot
	// of the cache. This prevents TOCTOU issues where a pipeline tool could
	// be created between checking individual steps.
	m.mu.RLock()
	defer m.mu.RUnlock()

	for i, step := range cfg.Steps {
		if step.Tool == "" {
			return fmt.Errorf("tool_create: pipeline step %d has no tool name", i+1)
		}

		// Prevent self-reference (pipeline calling itself).
		if step.Tool == toolName {
			return fmt.Errorf("tool_create: pipeline step %d references itself (%q)", i+1, toolName)
		}

		// Prevent pipeline-calls-pipeline: check if the referenced tool is
		// a pipeline custom tool in the cache.
		if ct, ok := m.cache[step.Tool]; ok && ct.Backend == "pipeline" {
			return fmt.Errorf("tool_create: pipeline step %d references another pipeline tool %q (nesting not allowed)", i+1, step.Tool)
		}

		// Verify the referenced tool exists somewhere (server tools, custom
		// tools cache, or client registry).
		if !m.registry.HasTool(step.Tool) {
			return fmt.Errorf("tool_create: pipeline step %d references unknown tool %q", i+1, step.Tool)
		}
	}

	return nil
}

// executePipeline runs a pipeline by executing each step sequentially, passing
// the output of each step to the next via the {{_output}} placeholder. The
// pipeline has an overall timeout and a depth guard to prevent recursive
// invocations.
func (m *CustomToolManager) executePipeline(ctx context.Context, ct db.CustomTool, args map[string]any) (string, error) {
	// Depth guard: prevent nested pipeline execution.
	if ctx.Value(pipelineDepthKey{}) != nil {
		return "", fmt.Errorf("pipeline %q: nested pipeline execution is not allowed", ct.Name)
	}
	ctx = context.WithValue(ctx, pipelineDepthKey{}, true)

	if m.executor == nil {
		return "", fmt.Errorf("pipeline %q: no executor configured (pipeline backend requires SetExecutor)", ct.Name)
	}

	var cfg pipelineBackendConfig
	if err := json.Unmarshal([]byte(ct.BackendConfig), &cfg); err != nil {
		return "", fmt.Errorf("pipeline %q: invalid backend_config: %w", ct.Name, err)
	}

	if len(cfg.Steps) == 0 {
		return "", fmt.Errorf("pipeline %q: no steps defined", ct.Name)
	}

	// Apply overall pipeline timeout.
	ctx, cancel := context.WithTimeout(ctx, pipelineTimeout)
	defer cancel()

	var lastOutput string

	for i, step := range cfg.Steps {
		if ctx.Err() != nil {
			return "", fmt.Errorf("pipeline %q: timed out at step %d: %w", ct.Name, i+1, ctx.Err())
		}

		stepStart := time.Now()

		// Build the substitution map: input args + _output from previous step.
		subArgs := make(map[string]any, len(args)+1)
		for k, v := range args {
			subArgs[k] = v
		}
		if i > 0 {
			subArgs["_output"] = lastOutput
		}

		// Build the step's argument map with substitution applied.
		// For shell tool steps, only the "command" key uses shell-safe
		// substitution (single-quoting to prevent injection). Other keys
		// like "target" use plain substitution so they match host names
		// correctly during routing.
		stepArgs := make(map[string]any, len(step.Args))
		for k, v := range step.Args {
			strVal, ok := v.(string)
			if ok {
				if step.Tool == "shell" && k == "command" {
					stepArgs[k] = substituteArgsShell(strVal, subArgs)
				} else {
					stepArgs[k] = substituteArgs(strVal, subArgs)
				}
			} else {
				stepArgs[k] = v
			}
		}

		// Execute the step via the ToolExecutor.
		result, err := m.executor.ExecuteTool(ctx, step.Tool, stepArgs)
		stepDuration := time.Since(stepStart)

		if err != nil {
			m.logger.Error("pipeline step failed",
				"pipeline", ct.Name,
				"step", i+1,
				"tool", step.Tool,
				"duration", stepDuration,
				"error", err,
			)
			return "", fmt.Errorf("pipeline %q: step %d (%s) failed: %w", ct.Name, i+1, step.Tool, err)
		}

		m.logger.Info("pipeline step completed",
			"pipeline", ct.Name,
			"step", i+1,
			"tool", step.Tool,
			"duration", stepDuration,
			"result_bytes", len(result),
		)

		// Truncate intermediate output to prevent memory bloat between steps.
		if len(result) > maxPipelineIntermediateBytes {
			result = result[:maxPipelineIntermediateBytes] + "\n... [intermediate output truncated]"
		}

		lastOutput = result
	}

	return tools.TruncateOutput(lastOutput), nil
}

// getCachedTool returns a cached custom tool by name and true, or a zero
// value and false if not found in cache. This is primarily used for testing.
func (m *CustomToolManager) getCachedTool(name string) (db.CustomTool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ct, ok := m.cache[name]
	return ct, ok
}
