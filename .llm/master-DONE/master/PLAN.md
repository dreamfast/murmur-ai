# Phase 3A — Client Infrastructure + First Tools

## Summary

Phase 3A adds the first real tool implementations to Murmur clients. This includes:
1. Extending client config to support tool-specific TOML sections
2. Wiring tool dispatch in the client with concurrency limits and panic recovery
3. Implementing three tools: `system_info` (8 safe system queries), `shell` (Docker-sandboxed command execution with strict whitelists), and `code_exec` (Piston-based code execution)

After this phase, a client can register tools, receive requests from the server's agent loop, execute them, and return results.

---

## Task 1: Client Config Extensions

**Files:** `internal/config/client_config.go` (modify), `internal/config/config_test.go` (modify), `configs/client.toml.example` (modify)

### Config Structs

Add `Tools` field to `ClientConfig`:

```go
type ClientConfig struct {
    Client    ClientSection        `toml:"client"`
    IRC       IRCConfig            `toml:"irc"`
    Heartbeat HeartbeatConfig      `toml:"heartbeat"`
    Security  ClientSecurityConfig `toml:"security"`
    Tools     ToolsConfig          `toml:"tools"`
}
```

New structs:

```go
type ToolsConfig struct {
    SystemInfo *SystemInfoConfig `toml:"systeminfo"`
    Shell      *ShellConfig      `toml:"shell"`
    CodeExec   *CodeExecConfig   `toml:"code_exec"`
}

type SystemInfoConfig struct {
    Enabled bool `toml:"enabled"`
}

type ShellConfig struct {
    Enabled     bool     `toml:"enabled"`
    DockerImage string   `toml:"docker_image"`
    Network     bool     `toml:"network"`
    MemoryLimit string   `toml:"memory_limit"`
    CPULimit    string   `toml:"cpu_limit"`
    Timeout     string   `toml:"timeout"`
    Workspace   string   `toml:"workspace"`
    Whitelist   []string `toml:"whitelist"`
}

type CodeExecConfig struct {
    Enabled        bool   `toml:"enabled"`
    PistonURL      string `toml:"piston_url"`
    DefaultLang    string `toml:"default_language"`
    RunTimeout     int    `toml:"run_timeout"`      // milliseconds
    RunMemoryLimit int    `toml:"run_memory_limit"` // bytes
}
```

### Validation

In `Validate()`:
- If `Shell` non-nil and `Shell.Enabled`: default `DockerImage` to `"ubuntu:24.04"` if empty; validate `Timeout` if set
- If `CodeExec` non-nil and `CodeExec.Enabled`: error if `PistonURL` empty; default `RunTimeout` to 30000; default `RunMemoryLimit` to 268435456
- If `(Shell.Enabled || CodeExec.Enabled) && Security.BusKey == ""`: log WARNING about missing bus authentication

### Helpers

```go
func (s *ShellConfig) ParseTimeout() (time.Duration, error)  // default 30s
```

### Tests (7)

- `TestLoadClientConfig_WithSystemInfoTool`
- `TestLoadClientConfig_WithShellTool`
- `TestLoadClientConfig_WithCodeExecTool`
- `TestLoadClientConfig_ShellDefaults`
- `TestLoadClientConfig_CodeExecMissingURL`
- `TestLoadClientConfig_NoToolsIsValid`
- `TestShellConfig_ParseTimeout`

---

## Task 2: Tool Helpers, Builder + Client Tool Dispatch

**Files:** `internal/tools/tool.go` (modify), `internal/tools/helpers.go` (create), `internal/tools/helpers_test.go` (create), `internal/tools/builder.go` (create), `internal/tools/builder_test.go` (create), `internal/client/client.go` (modify), `internal/client/client_test.go` (modify)

### Shared Helpers (`internal/tools/helpers.go`)

```go
const MaxOutputBytes = 25 * 1024  // 25KB truncation limit

func TruncateOutput(output string) string
func RequireStringArg(args map[string]any, key string) (string, error)
func OptionalStringArg(args map[string]any, key, defaultVal string) string
func OptionalStringSliceArg(args map[string]any, key string) []string
func RunCommand(ctx context.Context, name string, args ...string) (string, error)
```

### Tool Conversion (`internal/tools/tool.go`)

```go
func ToBusToolDefs(tools []Tool) []bus.ToolDef
```

### Builder (`internal/tools/builder.go`)

```go
func BuildTools(cfg *config.ToolsConfig, logger *slog.Logger) ([]Tool, error)
```

Checks each config section; if non-nil and Enabled, calls the corresponding constructor.

### Client Changes (`internal/client/client.go`)

Add fields:
```go
toolHandlers map[string]tools.Tool  // keyed by name for O(1) dispatch
toolSem      chan struct{}           // concurrency semaphore (cap 5)
```

Modify `New()`:
1. Call `tools.BuildTools(&cfg.Tools, logger)`
2. Build `toolHandlers` map
3. Convert to `[]bus.ToolDef` via `tools.ToBusToolDefs()`
4. Init `toolSem` as `make(chan struct{}, 5)`

Modify `handleToolRequest()`:
1. Look up tool — silently ignore if not ours (return without sending response)
2. Acquire semaphore (non-blocking; reject if full)
3. Panic recovery via `defer recover()`
4. Unmarshal `msg.Arguments` to `map[string]any`
5. Execute with timeout context (2 min)
6. Audit log: tool name, request_id, duration, success/failure
7. Send response (success or error)

### Tests (12+)

**helpers_test.go:**
- `TestTruncateOutput_Short`
- `TestTruncateOutput_Long`
- `TestRequireStringArg_Present`
- `TestRequireStringArg_Missing`
- `TestRequireStringArg_WrongType`
- `TestOptionalStringArg_Present`
- `TestOptionalStringArg_Missing`

**builder_test.go:**
- `TestBuildTools_Empty`
- `TestBuildTools_SystemInfoOnly`
- `TestToBusToolDefs`

**client_test.go:**
- `TestHandleToolRequest_Dispatch`
- `TestHandleToolRequest_UnknownTool` (silent ignore)
- `TestHandleToolRequest_HandlerError`
- `TestHandleToolRequest_ConcurrencyLimit`
- `TestHandleToolRequest_PanicRecovery`

---

## Task 3: System Info Tool

**Files:** `internal/tools/systeminfo.go` (create), `internal/tools/systeminfo_test.go` (create)

### Constructor

```go
func NewSystemInfoTool() Tool
```

### JSON Schema

```json
{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["uptime", "disk", "memory", "cpu", "os_info", "docker_ps", "apt_upgradable", "systemctl_status"],
      "description": "The system info query to run"
    },
    "service": {
      "type": "string",
      "description": "Service name for systemctl_status action"
    }
  },
  "required": ["action"]
}
```

### 8 Actions

1. `uptime` — Read `/proc/uptime`, format human-readable
2. `disk` — `RunCommand(ctx, "df", "-h")`
3. `memory` — Read `/proc/meminfo`, format total/used/available/swap
4. `cpu` — Read `/proc/loadavg` + `/proc/cpuinfo` (core count)
5. `os_info` — Read `/etc/os-release`
6. `docker_ps` — `RunCommand(ctx, "docker", "ps", "--format", "table {{.Names}}\t{{.Status}}\t{{.Image}}")`
7. `apt_upgradable` — `RunCommand(ctx, "apt", "list", "--upgradable")`
8. `systemctl_status` — Validate service name `^[a-zA-Z0-9_@.-]+$`, then `RunCommand(ctx, "systemctl", "status", service, "--no-pager", "-l")`

Non-Linux: return clear error for /proc-based actions.

Wire into builder when `cfg.SystemInfo != nil && cfg.SystemInfo.Enabled`.

### Tests (11)

- `TestSystemInfoTool_Name`
- `TestSystemInfoTool_Parameters`
- `TestSystemInfoTool_Uptime` (Linux only)
- `TestSystemInfoTool_Memory` (Linux only)
- `TestSystemInfoTool_CPU` (Linux only)
- `TestSystemInfoTool_OSInfo` (Linux only)
- `TestSystemInfoTool_Disk`
- `TestSystemInfoTool_InvalidAction`
- `TestSystemInfoTool_SystemctlMissingService`
- `TestSystemInfoTool_SystemctlInvalidServiceName`
- `TestSystemInfoTool_ContextCancellation`

---

## Task 4: Shell Tool (Docker-Sandboxed)

**Files:** `internal/tools/shell.go` (create), `internal/tools/shell_test.go` (create)

### Constructor

```go
type ShellToolConfig struct {
    DockerImage string
    Network     bool
    MemoryLimit string
    CPULimit    string
    Timeout     time.Duration
    Workspace   string
    Whitelist   []string
}

func NewShellTool(cfg ShellToolConfig) Tool
```

### JSON Schema

```json
{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command to execute inside a Docker container"
    }
  },
  "required": ["command"]
}
```

### Handler Logic

1. Extract `command` via `RequireStringArg`
2. Whitelist validation (when non-empty):
   - Reject shell metacharacters: `;`, `&&`, `||`, `|`, `` ` ``, `$(`, `>`, `<`, `\n`
   - Match: exact match or glob suffix (prefix + single arg, no spaces in remainder)
3. Build Docker args:
   - `--rm`, `--cap-drop=ALL`, `--security-opt=no-new-privileges`, `--read-only`
   - `--network=none` (unless cfg.Network)
   - `--memory=<limit>` (if set)
   - `--cpus=<limit>` (if set)
   - `-v <workspace>:/workspace:ro` (if workspace set)
   - `<image>`, `bash`, `-c`, `<command>`
4. Execute with `exec.CommandContext`
5. Truncate via `TruncateOutput()`

### Exported Functions (for testing)

```go
func buildDockerArgs(cfg ShellToolConfig, command string) []string
func isAllowed(command string, whitelist []string) bool
func containsShellMetachars(command string) bool
```

Wire into builder when `cfg.Shell != nil && cfg.Shell.Enabled`.

### Tests (13, table-driven where appropriate)

- `TestContainsShellMetachars` (table-driven)
- `TestIsAllowed_ExactMatch`
- `TestIsAllowed_GlobMatch`
- `TestIsAllowed_GlobRejectsMultipleArgs`
- `TestIsAllowed_NoMatch`
- `TestIsAllowed_EmptyWhitelist`
- `TestBuildDockerArgs_Defaults`
- `TestBuildDockerArgs_WithNetwork`
- `TestBuildDockerArgs_WithLimits`
- `TestBuildDockerArgs_WithWorkspace`
- `TestShellTool_Name`
- `TestShellTool_WhitelistRejectMetachars`
- `TestShellTool_MissingCommand`

---

## Task 5: Code Execution Tool (Piston)

**Files:** `internal/tools/code_exec.go` (create), `internal/tools/code_exec_test.go` (create)

### Constructor

```go
type CodeExecToolConfig struct {
    PistonURL      string
    DefaultLang    string
    RunTimeout     int // ms
    RunMemoryLimit int // bytes
}

func NewCodeExecTool(cfg CodeExecToolConfig) Tool
```

### JSON Schema

```json
{
  "type": "object",
  "properties": {
    "language": {
      "type": "string",
      "description": "Programming language (e.g., 'python', 'javascript', 'go', 'rust', 'c', 'bash')"
    },
    "version": {
      "type": "string",
      "description": "Optional: specific language version. Defaults to latest installed."
    },
    "code": {
      "type": "string",
      "description": "The code to execute"
    },
    "stdin": {
      "type": "string",
      "description": "Optional: stdin input for the program"
    },
    "args": {
      "type": "array",
      "items": { "type": "string" },
      "description": "Optional: command-line arguments"
    }
  },
  "required": ["language", "code"]
}
```

### Piston API Types

```go
type pistonRequest struct {
    Language       string       `json:"language"`
    Version        string       `json:"version,omitempty"`
    Files          []pistonFile `json:"files"`
    Stdin          string       `json:"stdin,omitempty"`
    Args           []string     `json:"args,omitempty"`
    RunTimeout     int          `json:"run_timeout,omitempty"`
    RunMemoryLimit int          `json:"run_memory_limit,omitempty"`
}

type pistonFile struct {
    Name    string `json:"name,omitempty"`
    Content string `json:"content"`
}

type pistonResponse struct {
    Language string        `json:"language"`
    Version  string        `json:"version"`
    Run      pistonResult  `json:"run"`
    Compile  *pistonResult `json:"compile,omitempty"`
}

type pistonResult struct {
    Stdout string  `json:"stdout"`
    Stderr string  `json:"stderr"`
    Code   int     `json:"code"`
    Signal *string `json:"signal"`
    Output string  `json:"output"`
}
```

### Handler Logic

1. Extract `language` (fall back to DefaultLang), `code` (required)
2. Extract optional: `version`, `stdin`, `args`
3. Build `pistonRequest` with single file
4. POST to `<PistonURL>/api/v2/execute` using shared `http.Client` with context
5. Parse response, check HTTP status
6. Format result via `formatPistonResult()`
7. Truncate via `TruncateOutput()`

### Result Formatting

```go
func formatPistonResult(resp *pistonResponse) string
```

- Compile error: show compile stderr + exit code, note "program did not run"
- Run success/error: show language/version, exit code, stdout, stderr

Wire into builder when `cfg.CodeExec != nil && cfg.CodeExec.Enabled`.

### Tests (14, all httptest mocked)

- `TestCodeExecTool_Name`
- `TestCodeExecTool_Parameters`
- `TestCodeExecTool_SuccessfulExecution`
- `TestCodeExecTool_CompileError`
- `TestCodeExecTool_RuntimeError`
- `TestCodeExecTool_MissingLanguage`
- `TestCodeExecTool_DefaultLanguage`
- `TestCodeExecTool_MissingCode`
- `TestCodeExecTool_PistonUnavailable`
- `TestCodeExecTool_PistonBadJSON`
- `TestCodeExecTool_OutputTruncation`
- `TestCodeExecTool_ContextCancellation`
- `TestFormatPistonResult_Success`
- `TestFormatPistonResult_CompileError`
