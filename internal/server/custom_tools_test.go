package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"murmur/internal/db"
	"murmur/internal/tools"
)

// newTestDB creates an in-memory SQLite database with migrations applied.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("db.Migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// newTestManager creates a CustomToolManager with an in-memory DB and
// empty ToolRegistry for testing.
func newTestManager(t *testing.T) (*CustomToolManager, *ToolRegistry) {
	t.Helper()
	database := newTestDB(t)
	registry := NewToolRegistry()
	logger := newTestLogger()
	mgr := NewCustomToolManager(database, registry, "", logger)
	return mgr, registry
}

// newTestManagerWithPiston creates a CustomToolManager with a mock Piston server.
func newTestManagerWithPiston(t *testing.T, pistonURL string) (*CustomToolManager, *ToolRegistry) {
	t.Helper()
	database := newTestDB(t)
	registry := NewToolRegistry()
	logger := newTestLogger()
	mgr := NewCustomToolManager(database, registry, pistonURL, logger)
	return mgr, registry
}

func TestCustomToolManager_RegisterMetaTools(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	expectedTools := []string{"tool_create", "tool_list", "tool_delete", "tool_enable", "tool_disable"}
	for _, name := range expectedTools {
		if !registry.HasTool(name) {
			t.Errorf("expected meta-tool %q to be registered", name)
		}
	}
}

func TestCustomToolManager_CreateShellTool(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, ok := registry.Get("tool_create")
	if !ok {
		t.Fatal("tool_create not found")
	}

	result, err := tool.Handler(ctx, map[string]any{
		"name":           "echo_test",
		"description":    "Echoes a message",
		"parameters":     `{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]}`,
		"backend":        "shell",
		"backend_config": `{"command":"echo {{msg}}"}`,
	})
	if err != nil {
		t.Fatalf("tool_create: %v", err)
	}
	if !strings.Contains(result, "echo_test") {
		t.Errorf("expected result to contain tool name, got %q", result)
	}

	// Verify the tool is registered.
	if !registry.HasTool("echo_test") {
		t.Error("expected echo_test to be registered")
	}

	// Verify it's in the cache.
	ct, ok := mgr.getCachedTool("echo_test")
	if !ok {
		t.Fatal("expected echo_test to be in cache")
	}
	if ct.Backend != "shell" {
		t.Errorf("expected backend 'shell', got %q", ct.Backend)
	}
}

func TestCustomToolManager_CreateHTTPTool(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	result, err := tool.Handler(ctx, map[string]any{
		"name":           "fetch_data",
		"description":    "Fetches data from a URL",
		"parameters":     `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`,
		"backend":        "http",
		"backend_config": `{"url":"https://example.com/{{url}}","method":"GET"}`,
	})
	if err != nil {
		t.Fatalf("tool_create: %v", err)
	}
	if !strings.Contains(result, "fetch_data") {
		t.Errorf("expected result to contain tool name, got %q", result)
	}
	if !registry.HasTool("fetch_data") {
		t.Error("expected fetch_data to be registered")
	}
}

func TestCustomToolManager_CreateCodeExecTool(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManagerWithPiston(t, "http://localhost:2000")
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	result, err := tool.Handler(ctx, map[string]any{
		"name":           "run_python",
		"description":    "Runs Python code",
		"parameters":     `{"type":"object","properties":{"code":{"type":"string"}},"required":["code"]}`,
		"backend":        "code_exec",
		"backend_config": `{"language":"python","code":"print('{{code}}')"}`,
	})
	if err != nil {
		t.Fatalf("tool_create: %v", err)
	}
	if !strings.Contains(result, "run_python") {
		t.Errorf("expected result to contain tool name, got %q", result)
	}
}

func TestCustomToolManager_CreateCodeExecTool_NoPiston(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t) // no piston URL
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	_, err := tool.Handler(ctx, map[string]any{
		"name":           "run_python",
		"description":    "Runs Python code",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "code_exec",
		"backend_config": `{"language":"python","code":"print('hello')"}`,
	})
	if err == nil {
		t.Fatal("expected error for code_exec without Piston URL")
	}
	if !strings.Contains(err.Error(), "Piston") {
		t.Errorf("expected Piston-related error, got %q", err.Error())
	}
}

func TestCustomToolManager_CreateDuplicate(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	args := map[string]any{
		"name":           "my_tool",
		"description":    "A tool",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "shell",
		"backend_config": `{"command":"echo hello"}`,
	}

	// First create should succeed.
	if _, err := tool.Handler(ctx, args); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second create should fail.
	_, err := tool.Handler(ctx, args)
	if err == nil {
		t.Fatal("expected error for duplicate tool name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got %q", err.Error())
	}
}

func TestCustomToolManager_CreateConflictsWithBuiltIn(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)

	// Register a "built-in" tool.
	builtIn := tools.Tool{
		Name:        "note_set",
		Description: "Built-in note tool",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
	if err := registry.Register(builtIn); err != nil {
		t.Fatalf("Register built-in: %v", err)
	}

	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	_, err := tool.Handler(ctx, map[string]any{
		"name":           "note_set",
		"description":    "Override notes",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "shell",
		"backend_config": `{"command":"echo hello"}`,
	})
	if err == nil {
		t.Fatal("expected error for name conflict with built-in tool")
	}
	if !strings.Contains(err.Error(), "conflicts with a built-in tool") {
		t.Errorf("expected 'conflicts' error, got %q", err.Error())
	}
}

func TestCustomToolManager_CreateInvalidBackend(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	_, err := tool.Handler(ctx, map[string]any{
		"name":           "bad_tool",
		"description":    "A tool",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "invalid",
		"backend_config": `{"command":"echo hello"}`,
	})
	if err == nil {
		t.Fatal("expected error for invalid backend")
	}
	if !strings.Contains(err.Error(), "invalid backend") {
		t.Errorf("expected 'invalid backend' error, got %q", err.Error())
	}
}

func TestCustomToolManager_CreateInvalidJSON(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	tests := []struct {
		name       string
		toolName   string
		parameters string
		config     string
		wantErr    string
	}{
		{
			name:       "invalid parameters JSON",
			toolName:   "json_test_params",
			parameters: `{not valid json}`,
			config:     `{"command":"echo"}`,
			wantErr:    "parameters must be valid JSON",
		},
		{
			name:       "invalid backend_config JSON",
			toolName:   "json_test_config",
			parameters: `{"type":"object","properties":{}}`,
			config:     `{not valid}`,
			wantErr:    "backend_config must be valid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Handler(ctx, map[string]any{
				"name":           tt.toolName,
				"description":    "A tool",
				"parameters":     tt.parameters,
				"backend":        "shell",
				"backend_config": tt.config,
			})
			if err == nil {
				t.Fatal("expected error for invalid JSON")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestCustomToolManager_List(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")
	listTool, _ := registry.Get("tool_list")

	// Empty list.
	result, err := listTool.Handler(ctx, nil)
	if err != nil {
		t.Fatalf("tool_list (empty): %v", err)
	}
	if !strings.Contains(result, "No custom tools") {
		t.Errorf("expected 'No custom tools' message, got %q", result)
	}

	// Create two tools.
	for _, name := range []string{"tool_a", "tool_b"} {
		if _, err := createTool.Handler(ctx, map[string]any{
			"name":           name,
			"description":    "Tool " + name,
			"parameters":     `{"type":"object","properties":{}}`,
			"backend":        "shell",
			"backend_config": `{"command":"echo"}`,
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	// List should show both.
	result, err = listTool.Handler(ctx, nil)
	if err != nil {
		t.Fatalf("tool_list: %v", err)
	}
	if !strings.Contains(result, "tool_a") || !strings.Contains(result, "tool_b") {
		t.Errorf("expected both tools in list, got %q", result)
	}
	if !strings.Contains(result, "2") {
		t.Errorf("expected count '2' in list, got %q", result)
	}
}

func TestCustomToolManager_Delete(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")
	deleteTool, _ := registry.Get("tool_delete")

	// Create a tool.
	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "deletable",
		"description":    "Will be deleted",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "shell",
		"backend_config": `{"command":"echo"}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if !registry.HasTool("deletable") {
		t.Fatal("expected tool to be registered before delete")
	}

	// Delete it.
	result, err := deleteTool.Handler(ctx, map[string]any{"name": "deletable"})
	if err != nil {
		t.Fatalf("tool_delete: %v", err)
	}
	if !strings.Contains(result, "deleted") {
		t.Errorf("expected 'deleted' in result, got %q", result)
	}

	// Verify it's gone from registry.
	if registry.HasTool("deletable") {
		t.Error("expected tool to be unregistered after delete")
	}

	// Verify it's gone from cache.
	_, inCache := mgr.getCachedTool("deletable")
	if inCache {
		t.Error("expected tool to be removed from cache after delete")
	}
}

func TestCustomToolManager_DeleteNonexistent(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	deleteTool, _ := registry.Get("tool_delete")

	_, err := deleteTool.Handler(ctx, map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Fatal("expected error for deleting nonexistent tool")
	}
}

func TestCustomToolManager_EnableDisable(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")
	disableTool, _ := registry.Get("tool_disable")
	enableTool, _ := registry.Get("tool_enable")

	// Create a tool.
	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "toggleable",
		"description":    "Can be toggled",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "shell",
		"backend_config": `{"command":"echo"}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Disable it.
	result, err := disableTool.Handler(ctx, map[string]any{"name": "toggleable"})
	if err != nil {
		t.Fatalf("tool_disable: %v", err)
	}
	if !strings.Contains(result, "disabled") {
		t.Errorf("expected 'disabled' in result, got %q", result)
	}

	// Verify it's unregistered.
	if registry.HasTool("toggleable") {
		t.Error("expected tool to be unregistered after disable")
	}

	// Verify it's gone from cache.
	_, inCache := mgr.getCachedTool("toggleable")
	if inCache {
		t.Error("expected tool to be removed from cache after disable")
	}

	// Enable it.
	result, err = enableTool.Handler(ctx, map[string]any{"name": "toggleable"})
	if err != nil {
		t.Fatalf("tool_enable: %v", err)
	}
	if !strings.Contains(result, "enabled") {
		t.Errorf("expected 'enabled' in result, got %q", result)
	}

	// Verify it's re-registered.
	if !registry.HasTool("toggleable") {
		t.Error("expected tool to be registered after enable")
	}

	// Verify it's back in cache.
	ct, ok := mgr.getCachedTool("toggleable")
	if !ok {
		t.Fatal("expected tool to be in cache after enable")
	}
	if !ct.Enabled {
		t.Error("expected tool to be enabled in cache")
	}
}

func TestCustomToolManager_EnableNonexistent(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	enableTool, _ := registry.Get("tool_enable")

	_, err := enableTool.Handler(ctx, map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Fatal("expected error for enabling nonexistent tool")
	}
}

func TestCustomToolManager_DisableNonexistent(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	disableTool, _ := registry.Get("tool_disable")

	_, err := disableTool.Handler(ctx, map[string]any{"name": "nonexistent"})
	if err == nil {
		t.Fatal("expected error for disabling nonexistent tool")
	}
}

func TestCustomToolManager_LoadFromDB(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	registry := NewToolRegistry()
	logger := newTestLogger()

	// Insert tools directly into DB.
	enabledTool := &db.CustomTool{
		Name:          "loaded_tool",
		Description:   "Loaded from DB",
		Parameters:    `{"type":"object","properties":{}}`,
		Backend:       "shell",
		BackendConfig: `{"command":"echo loaded"}`,
		Enabled:       true,
	}
	disabledTool := &db.CustomTool{
		Name:          "disabled_tool",
		Description:   "Disabled tool",
		Parameters:    `{"type":"object","properties":{}}`,
		Backend:       "shell",
		BackendConfig: `{"command":"echo disabled"}`,
		Enabled:       false,
	}

	if err := database.InsertCustomTool(enabledTool); err != nil {
		t.Fatalf("insert enabled tool: %v", err)
	}
	if err := database.InsertCustomTool(disabledTool); err != nil {
		t.Fatalf("insert disabled tool: %v", err)
	}

	mgr := NewCustomToolManager(database, registry, "", logger)
	if err := mgr.LoadFromDB(); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	// Only enabled tool should be registered.
	if !registry.HasTool("loaded_tool") {
		t.Error("expected loaded_tool to be registered")
	}
	if registry.HasTool("disabled_tool") {
		t.Error("expected disabled_tool to NOT be registered")
	}

	// Verify cache.
	ct, ok := mgr.getCachedTool("loaded_tool")
	if !ok {
		t.Fatal("expected loaded_tool to be in cache")
	}
	if ct.Description != "Loaded from DB" {
		t.Errorf("expected description 'Loaded from DB', got %q", ct.Description)
	}
}

func TestCustomToolManager_LoadFromDB_SkipsConflicts(t *testing.T) {
	t.Parallel()

	database := newTestDB(t)
	registry := NewToolRegistry()
	logger := newTestLogger()

	// Register a built-in tool first.
	builtIn := tools.Tool{
		Name:        "note_set",
		Description: "Built-in",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
	if err := registry.Register(builtIn); err != nil {
		t.Fatalf("Register built-in: %v", err)
	}

	// Insert a custom tool with the same name.
	ct := &db.CustomTool{
		Name:          "note_set",
		Description:   "Custom override",
		Parameters:    `{"type":"object","properties":{}}`,
		Backend:       "shell",
		BackendConfig: `{"command":"echo"}`,
		Enabled:       true,
	}
	if err := database.InsertCustomTool(ct); err != nil {
		t.Fatalf("insert: %v", err)
	}

	mgr := NewCustomToolManager(database, registry, "", logger)
	if err := mgr.LoadFromDB(); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	// Built-in should still be there with original description.
	tool, ok := registry.Get("note_set")
	if !ok {
		t.Fatal("expected note_set to still be registered")
	}
	if tool.Description != "Built-in" {
		t.Errorf("expected built-in description, got %q", tool.Description)
	}
}

func TestCustomToolManager_SubstituteArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		args     map[string]any
		want     string
	}{
		{
			name:     "single substitution",
			template: "echo {{msg}}",
			args:     map[string]any{"msg": "hello"},
			want:     "echo hello",
		},
		{
			name:     "multiple substitutions",
			template: "curl {{url}} -d {{data}}",
			args:     map[string]any{"url": "http://example.com", "data": "test"},
			want:     "curl http://example.com -d test",
		},
		{
			name:     "no substitutions",
			template: "echo hello",
			args:     map[string]any{},
			want:     "echo hello",
		},
		{
			name:     "numeric value",
			template: "echo {{count}}",
			args:     map[string]any{"count": float64(42)},
			want:     "echo 42",
		},
		{
			name:     "missing arg leaves placeholder",
			template: "echo {{missing}}",
			args:     map[string]any{"other": "value"},
			want:     "echo {{missing}}",
		},
		{
			name:     "repeated placeholder",
			template: "{{x}} and {{x}}",
			args:     map[string]any{"x": "val"},
			want:     "val and val",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := substituteArgs(tt.template, tt.args)
			if got != tt.want {
				t.Errorf("substituteArgs(%q, %v) = %q, want %q", tt.template, tt.args, got, tt.want)
			}
		})
	}
}

func TestCustomToolManager_SubstituteArgsShell(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		args     map[string]any
		want     string
	}{
		{
			name:     "simple value is quoted",
			template: "echo {{msg}}",
			args:     map[string]any{"msg": "hello"},
			want:     "echo 'hello'",
		},
		{
			name:     "value with single quotes is escaped",
			template: "echo {{msg}}",
			args:     map[string]any{"msg": "it's a test"},
			want:     "echo 'it'\\''s a test'",
		},
		{
			name:     "command injection is neutralized",
			template: "echo {{msg}}",
			args:     map[string]any{"msg": "$(rm -rf /)"},
			want:     "echo '$(rm -rf /)'",
		},
		{
			name:     "backtick injection is neutralized",
			template: "echo {{msg}}",
			args:     map[string]any{"msg": "`whoami`"},
			want:     "echo '`whoami`'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := substituteArgsShell(tt.template, tt.args)
			if got != tt.want {
				t.Errorf("substituteArgsShell(%q, %v) = %q, want %q", tt.template, tt.args, got, tt.want)
			}
		})
	}
}

func TestCustomToolManager_CreateInvalidName(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "uppercase letters",
			input:   "MyTool",
			wantErr: "is invalid",
		},
		{
			name:    "spaces",
			input:   "my tool",
			wantErr: "is invalid",
		},
		{
			name:    "starts with number",
			input:   "1tool",
			wantErr: "is invalid",
		},
		{
			name:    "hyphens",
			input:   "my-tool",
			wantErr: "is invalid",
		},
		{
			name:    "single char",
			input:   "x",
			wantErr: "is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Handler(ctx, map[string]any{
				"name":           tt.input,
				"description":    "A tool",
				"parameters":     `{"type":"object","properties":{}}`,
				"backend":        "shell",
				"backend_config": `{"command":"echo"}`,
			})
			if err == nil {
				t.Fatalf("expected error for invalid name %q", tt.input)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestCustomToolManager_ExecuteHTTP(t *testing.T) {
	t.Parallel()

	// Create a test HTTP server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Hello from %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	// Create an HTTP tool pointing to our test server.
	backendConfig := fmt.Sprintf(`{"url":"%s/api/{{resource}}","method":"GET"}`, server.URL)
	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "http_test",
		"description":    "Test HTTP tool",
		"parameters":     `{"type":"object","properties":{"resource":{"type":"string"}},"required":["resource"]}`,
		"backend":        "http",
		"backend_config": backendConfig,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Execute the custom tool.
	httpTool, ok := registry.Get("http_test")
	if !ok {
		t.Fatal("http_test not found in registry")
	}

	result, err := httpTool.Handler(ctx, map[string]any{"resource": "users"})
	if err != nil {
		t.Fatalf("execute http_test: %v", err)
	}
	if !strings.Contains(result, "Hello from GET /api/users") {
		t.Errorf("expected response content, got %q", result)
	}
	if !strings.Contains(result, "HTTP 200") {
		t.Errorf("expected HTTP 200 in result, got %q", result)
	}
}

func TestCustomToolManager_ExecuteHTTP_WithHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		fmt.Fprintf(w, "auth=%s", auth)
	}))
	defer server.Close()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	backendConfig := fmt.Sprintf(`{"url":"%s/api","method":"GET","headers":{"Authorization":"Bearer {{token}}"}}`, server.URL)
	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "auth_test",
		"description":    "Test HTTP with headers",
		"parameters":     `{"type":"object","properties":{"token":{"type":"string"}},"required":["token"]}`,
		"backend":        "http",
		"backend_config": backendConfig,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	httpTool, _ := registry.Get("auth_test")
	result, err := httpTool.Handler(ctx, map[string]any{"token": "my-secret"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result, "auth=Bearer my-secret") {
		t.Errorf("expected auth header in response, got %q", result)
	}
}

func TestCustomToolManager_ExecuteCodeExec(t *testing.T) {
	t.Parallel()

	// Create a mock Piston server.
	piston := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/execute" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"language": "python",
			"version":  "3.10.0",
			"run": map[string]any{
				"stdout": "hello world\n",
				"stderr": "",
				"code":   0,
			},
		})
	}))
	defer piston.Close()

	mgr, registry := newTestManagerWithPiston(t, piston.URL)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "py_hello",
		"description":    "Runs Python hello",
		"parameters":     `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`,
		"backend":        "code_exec",
		"backend_config": `{"language":"python","code":"print('hello {{name}}')"}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	pyTool, ok := registry.Get("py_hello")
	if !ok {
		t.Fatal("py_hello not found in registry")
	}

	result, err := pyTool.Handler(ctx, map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("execute py_hello: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected 'hello world' in result, got %q", result)
	}
	if !strings.Contains(result, "python") {
		t.Errorf("expected 'python' in result, got %q", result)
	}
}

func TestCustomToolManager_ExecuteCodeExec_CompileError(t *testing.T) {
	t.Parallel()

	piston := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"language": "go",
			"version":  "1.21.0",
			"compile": map[string]any{
				"stderr": "syntax error\n",
				"code":   1,
			},
			"run": map[string]any{
				"stdout": "",
				"stderr": "",
				"code":   0,
			},
		})
	}))
	defer piston.Close()

	mgr, registry := newTestManagerWithPiston(t, piston.URL)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "go_compile",
		"description":    "Runs Go code",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "code_exec",
		"backend_config": `{"language":"go","code":"invalid go code"}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	goTool, _ := registry.Get("go_compile")
	result, err := goTool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result, "Compile error") {
		t.Errorf("expected compile error in result, got %q", result)
	}
}

func TestCustomToolManager_ToolRegistryUnregister(t *testing.T) {
	t.Parallel()

	registry := NewToolRegistry()

	tool := tools.Tool{
		Name:        "test_tool",
		Description: "Test",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}

	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if !registry.HasTool("test_tool") {
		t.Fatal("expected tool to be registered")
	}

	registry.Unregister("test_tool")

	if registry.HasTool("test_tool") {
		t.Error("expected tool to be unregistered")
	}

	// Unregister nonexistent should not panic.
	registry.Unregister("nonexistent")
}

func TestCustomToolManager_CreateEmptyName(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	tool, _ := registry.Get("tool_create")

	_, err := tool.Handler(ctx, map[string]any{
		"name":           "  ",
		"description":    "Empty name",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "shell",
		"backend_config": `{"command":"echo"}`,
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "name must not be empty") {
		t.Errorf("expected 'name must not be empty' error, got %q", err.Error())
	}
}

func TestCustomToolManager_FullLifecycle(t *testing.T) {
	t.Parallel()

	// Test the full lifecycle: create -> list -> disable -> list -> enable -> delete -> list
	mgr, registry := newTestManager(t)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")
	listTool, _ := registry.Get("tool_list")
	disableTool, _ := registry.Get("tool_disable")
	enableTool, _ := registry.Get("tool_enable")
	deleteTool, _ := registry.Get("tool_delete")

	// 1. Create
	_, err := createTool.Handler(ctx, map[string]any{
		"name":           "lifecycle_tool",
		"description":    "Lifecycle test",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "shell",
		"backend_config": `{"command":"echo lifecycle"}`,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 2. List — should show enabled
	result, _ := listTool.Handler(ctx, nil)
	if !strings.Contains(result, "lifecycle_tool") || !strings.Contains(result, "enabled") {
		t.Errorf("list after create: expected enabled tool, got %q", result)
	}

	// 3. Disable
	if _, err := disableTool.Handler(ctx, map[string]any{"name": "lifecycle_tool"}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// 4. List — should show disabled
	result, _ = listTool.Handler(ctx, nil)
	if !strings.Contains(result, "disabled") {
		t.Errorf("list after disable: expected disabled, got %q", result)
	}

	// 5. Enable
	if _, err := enableTool.Handler(ctx, map[string]any{"name": "lifecycle_tool"}); err != nil {
		t.Fatalf("enable: %v", err)
	}

	// 6. Delete
	if _, err := deleteTool.Handler(ctx, map[string]any{"name": "lifecycle_tool"}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 7. List — should be empty
	result, _ = listTool.Handler(ctx, nil)
	if !strings.Contains(result, "No custom tools") {
		t.Errorf("list after delete: expected empty, got %q", result)
	}
}

// --- Pipeline backend tests ---

// mockToolExecutor is a test double for ToolExecutor that records calls and
// returns configurable results.
type mockToolExecutor struct {
	calls   []mockToolCall
	results map[string]mockToolResult
}

type mockToolCall struct {
	ToolName string
	Args     map[string]any
}

type mockToolResult struct {
	Output string
	Err    error
}

func (m *mockToolExecutor) ExecuteTool(_ context.Context, toolName string, args map[string]any) (string, error) {
	m.calls = append(m.calls, mockToolCall{ToolName: toolName, Args: args})
	if r, ok := m.results[toolName]; ok {
		return r.Output, r.Err
	}
	return "", fmt.Errorf("mock: unknown tool %q", toolName)
}

// newTestManagerWithExecutor creates a CustomToolManager with a mock executor
// and pre-registers a set of "existing" tools so pipeline validation passes.
func newTestManagerWithExecutor(t *testing.T, executor ToolExecutor, existingTools []string) (*CustomToolManager, *ToolRegistry) {
	t.Helper()
	database := newTestDB(t)
	registry := NewToolRegistry()
	logger := newTestLogger()
	mgr := NewCustomToolManager(database, registry, "", logger)
	mgr.SetExecutor(executor)

	// Register placeholder tools so pipeline validation can find them.
	for _, name := range existingTools {
		tool := tools.Tool{
			Name:        name,
			Description: "Test tool " + name,
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				return "placeholder", nil
			},
		}
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register placeholder tool %q: %v", name, err)
		}
	}

	return mgr, registry
}

func TestCustomToolManager_CreatePipelineTool(t *testing.T) {
	t.Parallel()

	executor := &mockToolExecutor{
		results: map[string]mockToolResult{
			"fetch_url": {Output: "page content", Err: nil},
		},
	}
	mgr, registry := newTestManagerWithExecutor(t, executor, []string{"fetch_url", "summarize"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	result, err := createTool.Handler(ctx, map[string]any{
		"name":        "fetch_and_summarize",
		"description": "Fetches a URL and summarizes it",
		"parameters":  `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`,
		"backend":     "pipeline",
		"backend_config": `{"steps":[
			{"tool":"fetch_url","args":{"url":"{{url}}"}},
			{"tool":"summarize","args":{"text":"{{_output}}"}}
		]}`,
	})
	if err != nil {
		t.Fatalf("tool_create pipeline: %v", err)
	}
	if !strings.Contains(result, "fetch_and_summarize") {
		t.Errorf("expected result to contain tool name, got %q", result)
	}
	if !registry.HasTool("fetch_and_summarize") {
		t.Error("expected pipeline tool to be registered")
	}

	ct, ok := mgr.getCachedTool("fetch_and_summarize")
	if !ok {
		t.Fatal("expected pipeline tool to be in cache")
	}
	if ct.Backend != "pipeline" {
		t.Errorf("expected backend 'pipeline', got %q", ct.Backend)
	}
}

func TestCustomToolManager_ExecutePipeline(t *testing.T) {
	t.Parallel()

	executor := &mockToolExecutor{
		results: map[string]mockToolResult{
			"step_one": {Output: "step one output", Err: nil},
			"step_two": {Output: "final result", Err: nil},
		},
	}
	mgr, registry := newTestManagerWithExecutor(t, executor, []string{"step_one", "step_two"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":        "two_step",
		"description": "Two step pipeline",
		"parameters":  `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`,
		"backend":     "pipeline",
		"backend_config": `{"steps":[
			{"tool":"step_one","args":{"data":"{{input}}"}},
			{"tool":"step_two","args":{"data":"{{_output}}"}}
		]}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	pipelineTool, ok := registry.Get("two_step")
	if !ok {
		t.Fatal("two_step not found in registry")
	}

	result, err := pipelineTool.Handler(ctx, map[string]any{"input": "hello"})
	if err != nil {
		t.Fatalf("execute pipeline: %v", err)
	}
	if result != "final result" {
		t.Errorf("expected 'final result', got %q", result)
	}

	// Verify both steps were called.
	if len(executor.calls) != 2 {
		t.Fatalf("expected 2 executor calls, got %d", len(executor.calls))
	}
	if executor.calls[0].ToolName != "step_one" {
		t.Errorf("step 1: expected tool 'step_one', got %q", executor.calls[0].ToolName)
	}
	if executor.calls[1].ToolName != "step_two" {
		t.Errorf("step 2: expected tool 'step_two', got %q", executor.calls[1].ToolName)
	}
}

func TestCustomToolManager_PipelineOutputChaining(t *testing.T) {
	t.Parallel()

	executor := &mockToolExecutor{
		results: map[string]mockToolResult{
			"producer": {Output: "produced-data", Err: nil},
			"consumer": {Output: "consumed", Err: nil},
		},
	}
	mgr, registry := newTestManagerWithExecutor(t, executor, []string{"producer", "consumer"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":        "chained",
		"description": "Chained pipeline",
		"parameters":  `{"type":"object","properties":{}}`,
		"backend":     "pipeline",
		"backend_config": `{"steps":[
			{"tool":"producer","args":{}},
			{"tool":"consumer","args":{"input":"{{_output}}"}}
		]}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	pipelineTool, _ := registry.Get("chained")
	if _, err := pipelineTool.Handler(ctx, map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Verify the consumer received the producer's output via {{_output}}.
	if len(executor.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(executor.calls))
	}
	consumerInput, ok := executor.calls[1].Args["input"].(string)
	if !ok {
		t.Fatal("expected consumer input to be a string")
	}
	if consumerInput != "produced-data" {
		t.Errorf("expected consumer input 'produced-data', got %q", consumerInput)
	}
}

func TestCustomToolManager_PipelineStepError(t *testing.T) {
	t.Parallel()

	executor := &mockToolExecutor{
		results: map[string]mockToolResult{
			"failing_step": {Output: "", Err: fmt.Errorf("step exploded")},
			"never_called": {Output: "should not run", Err: nil},
		},
	}
	mgr, registry := newTestManagerWithExecutor(t, executor, []string{"failing_step", "never_called"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":        "fail_pipeline",
		"description": "Pipeline that fails",
		"parameters":  `{"type":"object","properties":{}}`,
		"backend":     "pipeline",
		"backend_config": `{"steps":[
			{"tool":"failing_step","args":{}},
			{"tool":"never_called","args":{}}
		]}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	pipelineTool, _ := registry.Get("fail_pipeline")
	_, err := pipelineTool.Handler(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error from failing pipeline step")
	}
	if !strings.Contains(err.Error(), "step 1") {
		t.Errorf("expected error to mention step number, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "step exploded") {
		t.Errorf("expected error to contain original error, got %q", err.Error())
	}

	// Verify only the first step was called.
	if len(executor.calls) != 1 {
		t.Errorf("expected 1 call (second step should not run), got %d", len(executor.calls))
	}
}

func TestCustomToolManager_PipelineNoSteps(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManagerWithExecutor(t, &mockToolExecutor{}, nil)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	_, err := createTool.Handler(ctx, map[string]any{
		"name":           "empty_pipeline",
		"description":    "Empty pipeline",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": `{"steps":[]}`,
	})
	if err == nil {
		t.Fatal("expected error for pipeline with no steps")
	}
	if !strings.Contains(err.Error(), "at least one step") {
		t.Errorf("expected 'at least one step' error, got %q", err.Error())
	}
}

func TestCustomToolManager_PipelineSelfReference(t *testing.T) {
	t.Parallel()

	// Register a placeholder with the same name so HasTool returns true.
	mgr, registry := newTestManagerWithExecutor(t, &mockToolExecutor{}, []string{"self_ref"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	_, err := createTool.Handler(ctx, map[string]any{
		"name":           "self_ref",
		"description":    "Self-referencing pipeline",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": `{"steps":[{"tool":"self_ref","args":{}}]}`,
	})
	if err == nil {
		t.Fatal("expected error for self-referencing pipeline")
	}
	// The error could be "already exists" (name collision with placeholder)
	// or "references itself" depending on check order. Both are valid rejections.
	if !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "references itself") {
		t.Errorf("expected self-reference or collision error, got %q", err.Error())
	}
}

func TestCustomToolManager_PipelineNestedPipeline(t *testing.T) {
	t.Parallel()

	executor := &mockToolExecutor{
		results: map[string]mockToolResult{
			"inner_step": {Output: "inner", Err: nil},
		},
	}
	mgr, registry := newTestManagerWithExecutor(t, executor, []string{"inner_step"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	// Create the inner pipeline first.
	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "inner_pipeline",
		"description":    "Inner pipeline",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": `{"steps":[{"tool":"inner_step","args":{}}]}`,
	}); err != nil {
		t.Fatalf("create inner pipeline: %v", err)
	}

	// Try to create an outer pipeline that references the inner one.
	_, err := createTool.Handler(ctx, map[string]any{
		"name":           "outer_pipeline",
		"description":    "Outer pipeline",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": `{"steps":[{"tool":"inner_pipeline","args":{}}]}`,
	})
	if err == nil {
		t.Fatal("expected error for nested pipeline")
	}
	if !strings.Contains(err.Error(), "nesting not allowed") {
		t.Errorf("expected 'nesting not allowed' error, got %q", err.Error())
	}
}

func TestCustomToolManager_PipelineNoExecutor(t *testing.T) {
	t.Parallel()

	// Create a manager WITHOUT setting an executor.
	mgr, registry := newTestManagerWithExecutor(t, nil, []string{"some_tool"})
	// Explicitly clear the executor (newTestManagerWithExecutor sets it).
	mgr.SetExecutor(nil)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "no_exec_pipeline",
		"description":    "Pipeline without executor",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": `{"steps":[{"tool":"some_tool","args":{}}]}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	pipelineTool, _ := registry.Get("no_exec_pipeline")
	_, err := pipelineTool.Handler(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error when executor is nil")
	}
	if !strings.Contains(err.Error(), "no executor configured") {
		t.Errorf("expected 'no executor configured' error, got %q", err.Error())
	}
}

func TestCustomToolManager_PipelineDepthGuard(t *testing.T) {
	t.Parallel()

	executor := &mockToolExecutor{
		results: map[string]mockToolResult{
			"some_tool": {Output: "ok", Err: nil},
		},
	}
	mgr, registry := newTestManagerWithExecutor(t, executor, []string{"some_tool"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":           "depth_test",
		"description":    "Depth guard test",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": `{"steps":[{"tool":"some_tool","args":{}}]}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate calling the pipeline from within a pipeline context.
	ctx = context.WithValue(ctx, pipelineDepthKey{}, true)
	pipelineTool, _ := registry.Get("depth_test")
	_, err := pipelineTool.Handler(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error for nested pipeline execution")
	}
	if !strings.Contains(err.Error(), "nested pipeline execution") {
		t.Errorf("expected 'nested pipeline execution' error, got %q", err.Error())
	}
}

func TestCustomToolManager_PipelineMaxSteps(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManagerWithExecutor(t, &mockToolExecutor{}, []string{"some_tool"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	// Build a pipeline with 11 steps (exceeds max of 10).
	steps := make([]map[string]any, 11)
	for i := range steps {
		steps[i] = map[string]any{"tool": "some_tool", "args": map[string]any{}}
	}
	stepsJSON, _ := json.Marshal(map[string]any{"steps": steps})

	_, err := createTool.Handler(ctx, map[string]any{
		"name":           "too_many_steps",
		"description":    "Too many steps",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": string(stepsJSON),
	})
	if err == nil {
		t.Fatal("expected error for too many pipeline steps")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("expected 'exceeds maximum' error, got %q", err.Error())
	}
}

func TestCustomToolManager_PipelineShellSubstitution(t *testing.T) {
	t.Parallel()

	// Track what args the executor receives to verify shell-safe substitution.
	executor := &mockToolExecutor{
		results: map[string]mockToolResult{
			"shell": {Output: "shell output", Err: nil},
		},
	}
	mgr, registry := newTestManagerWithExecutor(t, executor, []string{"shell"})
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	if _, err := createTool.Handler(ctx, map[string]any{
		"name":        "shell_pipeline",
		"description": "Pipeline with shell step",
		"parameters":  `{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}`,
		"backend":     "pipeline",
		"backend_config": `{"steps":[
			{"tool":"shell","args":{"command":"echo {{cmd}}"}}
		]}`,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	pipelineTool, _ := registry.Get("shell_pipeline")
	_, err := pipelineTool.Handler(ctx, map[string]any{"cmd": "$(whoami)"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Verify the shell step received shell-safe quoted args.
	if len(executor.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(executor.calls))
	}
	command, ok := executor.calls[0].Args["command"].(string)
	if !ok {
		t.Fatal("expected command arg to be a string")
	}
	// Shell-safe substitution should quote the value.
	if !strings.Contains(command, "'$(whoami)'") {
		t.Errorf("expected shell-safe quoted command, got %q", command)
	}
}

func TestCustomToolManager_PipelineUnknownTool(t *testing.T) {
	t.Parallel()

	mgr, registry := newTestManagerWithExecutor(t, &mockToolExecutor{}, nil)
	if err := mgr.RegisterMetaTools(registry); err != nil {
		t.Fatalf("RegisterMetaTools: %v", err)
	}

	ctx := context.Background()
	createTool, _ := registry.Get("tool_create")

	_, err := createTool.Handler(ctx, map[string]any{
		"name":           "bad_ref_pipeline",
		"description":    "References unknown tool",
		"parameters":     `{"type":"object","properties":{}}`,
		"backend":        "pipeline",
		"backend_config": `{"steps":[{"tool":"nonexistent_tool","args":{}}]}`,
	})
	if err == nil {
		t.Fatal("expected error for unknown tool reference")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' error, got %q", err.Error())
	}
}
