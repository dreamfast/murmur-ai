# Plan: Unified Tools, Search, OpenCode, IRC Management & Runtime Tools

## Summary

A major architectural enhancement to murmur that:
1. **Unifies the tool config** — server and client share the same `ToolsConfig` struct and `BuildTools()` function
2. **Adds SearXNG search** — native Go HTTP client alongside existing Brave Search
3. **Adds OpenCode integration** — Docker-based coding agent tool via REST+SSE API
4. **Adds IRC management tool** — join/part channels, set topics, send messages cross-channel, read other channels' history, copy/summarize context across channels
5. **Adds config management tool** — read/write TOML config with security deny-list
6. **Adds LLM-created runtime tools** — persist in SQLite, execute via shell/HTTP/code_exec backends
7. **Fixes client vault resolution** — existing bug where `vault:` refs don't work on clients
8. **Includes Docker infrastructure** — SearXNG and OpenCode in docker-compose with profiles

## Analysis

### Current State

**Tool sharing is 80% there.** `internal/tools/` has all implementations as standalone `Tool` structs. Both server and client use the same constructors. The gap is the config layer: Client uses `ToolsConfig` (11 configs in `client_config.go`), Server uses `ServerToolConfig` (6 configs in `server_config.go`). Server manually constructs tools in `server.go:147-244` duplicating `BuildTools()` logic. `HTTPToolConfig` lives in `server_config.go`, all other tool configs in `client_config.go`.

**Per-channel sessions already work.** The `conversations` table is keyed by `channel`. `Memory.GetHistory/ClearHistory/MaybeSummarize` all operate per-channel. The agent has per-channel locks (`chanLocks`). `!forget` clears only the channel it's typed in. What's missing is: (a) the ability to dynamically join new channels, (b) cross-channel operations (send to another channel, read another channel's history, copy context between channels).

**IRC is limited.** `Connection` only exposes `Send`, `SendRaw`, `OnConnect`, `OnMessage`. The underlying `girc.Client` supports `Join`, `Part`, `Topic` but these aren't exposed. The bot joins channels at connect time from a static list.

**Client vault is broken.** `client.go` passes `nil` as `SecretResolver` to `BuildTools()`, meaning `vault:` references in client tool configs silently fail.

## Tasks

### Task 1: Create Feature Branch
- **Files:** none (git operation)
- **Details:** Create `feature/unified-tools-and-search` branch from master
- **Tests:** N/A
- **Dependencies:** none

### Task 2: Reorganize Tool Config Structs
- **Files:**
  - `internal/config/tools_config.go` (create)
  - `internal/config/client_config.go` (modify — remove tool config structs)
  - `internal/config/server_config.go` (modify — remove `ServerToolConfig`, `HTTPToolConfig`)
  - `internal/config/config_test.go` (modify)
- **Details:**
  - Create `tools_config.go` containing ALL tool config structs moved from both files: `ToolsConfig`, `SystemInfoConfig`, `ShellConfig`, `CodeExecConfig`, `MailReadConfig`, `MailSendConfig`, `WebSearchConfig`, `GitConfig`, `RSSConfig`, `DNSConfig`, `ImageGenConfig`, `FileOpsConfig`, `HTTPToolConfig`, and their methods
  - Delete `ServerToolConfig` from `server_config.go`
  - Change `ServerConfig.Tools` field type to `ToolsConfig`
  - Add new tool config structs to `ToolsConfig`:
    - `SearXNG *SearXNGConfig` — `Enabled bool`, `URL string`, `MaxResults int`
    - `OpenCode *OpenCodeConfig` — `Enabled bool`, `URL string`, `Username string`, `Password string` (vault:), `DefaultDirectory string`, `SessionTimeout string`
    - `ConfigManage *ConfigManageConfig` — `Enabled bool`, `ConfigPath string`
    - `IRCManage *IRCManageConfig` — `Enabled bool`
    - `HTTP *HTTPToolConfig` — add to `ToolsConfig` (was server-only)
  - Move HTTP tool validation from `ServerConfig.Validate()` to `ToolsConfig.validate()`
  - Add validation for new configs: SearXNG URL required when enabled, OpenCode URL required, ConfigManage path required
  - Existing TOML compatibility: server configs with `[tools.shell]` etc. continue to parse since field names match
- **Tests:**
  - `TestLoadServerConfig_UnifiedTools` — server config loads with full `ToolsConfig`
  - `TestToolsConfig_Validate_SearXNG`, `_OpenCode`, `_HTTP`, `_ConfigManage`
  - Backward compat: existing server TOML still parses correctly
- **Dependencies:** Task 1

### Task 3: Fix Client Vault Resolution
- **Files:**
  - `internal/config/client_config.go` (modify — add `Vault VaultConfig` field)
  - `internal/client/client.go` (modify)
  - `internal/vault/vault.go` (modify — add `ResolveClientVaultRefs`)
- **Details:**
  - Add `Vault VaultConfig` field to `ClientConfig` (reuse existing `VaultConfig` struct)
  - Create `vault.ResolveClientVaultRefs(v *Vault, cfg *ClientConfig)` that walks client tool config fields with vault: prefixes: `Tools.WebSearch.APIKey`, `Tools.MailSend.SMTPPass`, `Tools.OpenCode.Password`, `Security.BusKey`
  - In `client.New()`: if vault enabled and passphrase env set, open vault, call `ResolveClientVaultRefs`, close vault — same pattern as server
  - Pass `nil` as resolver to `BuildTools` (secrets already resolved in config)
  - This fixes the existing bug where `mail_send.smtp_password = "vault:smtp-pass"` silently fails
  - Update `configs/client.toml.example` with `[vault]` section
- **Tests:**
  - `TestClientVaultResolution` — verify vault: refs resolved
  - `TestClientVaultDisabled` — nil resolver when vault not configured
- **Dependencies:** Task 2

### Task 4: Unify BuildTools (Phase 1 — Existing Tools + HTTP)
- **Files:**
  - `internal/tools/builder.go` (modify)
  - `internal/tools/builder_test.go` (modify)
- **Details:**
  - Change signature to `BuildToolsOpts` struct:
    ```go
    type BuildToolsOpts struct {
        Config     *config.ToolsConfig
        Logger     *slog.Logger
        Resolver   SecretResolver
        IRCManager IRCManager
    }
    ```
  - Define `IRCManager` interface in `tools` package:
    ```go
    type IRCManager interface {
        Join(channel string) error
        Part(channel string) error
        Send(channel, message string)
        SetTopic(channel, topic string) error
        Channels() []string
    }
    ```
  - Define `MemoryReader` interface in `tools` package:
    ```go
    type MemoryReader interface {
        GetHistory(channel string, limit int) ([]MemoryMessage, error)
        GetHistoryCount(channel string) (int, error)
    }
    ```
  - Add `MemoryReader` to `BuildToolsOpts`
  - Add `BuildTools` case for `HTTP` tool: move timeout parsing and `MaxResponseBytes` defaulting from `server.go:221-238` into `BuildTools`. Add `ParseTimeout()` method to `HTTPToolConfig`.
  - Update all callers: `client.go`, `server.go`, tests
  - Do NOT add new tool cases yet — added alongside implementations
- **Tests:**
  - `TestBuildTools_HTTP` — enabled/disabled, timeout defaults
  - `TestBuildTools_OptsSignature` — new opts struct works
  - Update existing builder tests for new signature
- **Dependencies:** Task 2

### Task 5: Refactor Server to Use BuildTools
- **Files:**
  - `internal/server/server.go` (modify)
  - `internal/server/server_test.go` (modify)
  - `internal/vault/vault.go` (modify — update `ResolveVaultRefs` for `ToolsConfig`)
- **Details:**
  - Remove manual tool construction block (lines ~147-244)
  - Server passes `nil` resolver (vault refs already resolved upfront)
  - Call `BuildTools` and loop-register into `ToolRegistry`
  - Keep `RegisterNoteTools` and `RegisterSchedulerTools` as separate server-only registrations
  - Update `vault.ResolveVaultRefs` for new config type
  - `irc.Connection` must satisfy `tools.IRCManager` interface (methods added in Task 6)
- **Tests:**
  - `TestServerNew_UsesBuiltTools` — verify server tools built from config
  - Update existing server tests for `ToolsConfig`
- **Dependencies:** Task 4

### Task 6: IRC Management Tool with Cross-Channel Context
- **Files:**
  - `internal/irc/connection.go` (modify)
  - `internal/tools/irc_manage.go` (create)
  - `internal/tools/irc_manage_test.go` (create)
- **Details:**
  - Expose IRC operations on `Connection` satisfying `tools.IRCManager` interface:
    - `Join(channel string) error`, `Part(channel string) error`
    - `SetTopic(channel, topic string) error`, `Channels() []string`
    - Track joined channels in `sync.RWMutex`-protected `map[string]struct{}`
    - Register girc handlers for JOIN, PART, KICK events for accurate state
  - Create `irc_manage` tool with `MemoryReader` for cross-channel history:
    - Actions: join, part, send, topic, list_channels, read_history, summarize_channel
    - Safety: prevent parting bus channel, validate # prefix, rate-limit joins, only send/read from joined channels
  - Add `BuildTools` case for IRCManage
- **Tests:**
  - Join, part, send, topic, list_channels, read_history, summarize_channel
  - Safety: bus channel protection, invalid channel, rate limit, not-joined rejection
- **Dependencies:** Task 4

### Task 7: SearXNG Search Tool
- **Files:**
  - `internal/tools/searxng.go` (create)
  - `internal/tools/searxng_test.go` (create)
  - `internal/tools/builder.go` (modify — add SearXNG case)
- **Details:**
  - Native Go HTTP client to SearXNG JSON API
  - Tool name: `searxng_search`, parameters: query, count, categories, time_range, language
  - Validate Content-Type is JSON, format as numbered list, truncate
- **Tests:** Success, empty results, categories, timeout, invalid content type
- **Dependencies:** Task 4

### Task 8: OpenCode Tool
- **Files:**
  - `internal/tools/opencode.go` (create)
  - `internal/tools/opencode_test.go` (create)
  - `internal/tools/builder.go` (modify — add OpenCode case)
- **Details:**
  - Single tool with action parameter (chat, list_sessions, get_session)
  - Chat: create session -> SSE subscribe -> send message -> wait for session.idle -> return result
  - SSE parsing with multi-line data field handling, 5min hard timeout
  - Basic auth, context.WithTimeout for goroutine cleanup
  - Distinct from code_exec (multi-step agent vs one-shot sandbox)
- **Tests:** Success, create session, timeout, busy error, list sessions, basic auth, unreachable
- **Dependencies:** Task 4

### Task 9: Config Management Tool
- **Files:**
  - `internal/tools/config_manage.go` (create)
  - `internal/tools/config_manage_test.go` (create)
  - `internal/tools/builder.go` (modify — add ConfigManage case)
- **Details:**
  - Read: BurntSushi/toml, mask vault: values
  - Write: targeted string replacement, atomic write (temp file + os.Rename)
  - Security deny-list: security.*, vault.*, irc.password, irc.nickserv_password, api.api_key, llm.providers.*.api_key, vault: prefixed values
  - Restart hint after writes
- **Tests:** Read, read section, set, set denied, mask vault, list sections, atomic write
- **Dependencies:** Task 4

### Task 10: Runtime Custom Tools — Database Schema
- **Files:**
  - `internal/db/migrations.go` (modify — add migration 4)
  - `internal/db/custom_tools.go` (create)
  - `internal/db/custom_tools_test.go` (create)
- **Details:**
  - Migration 4: custom_tools table (name, description, parameters, backend, backend_config, enabled, timestamps)
  - CRUD methods on db.DB
- **Tests:** Insert, duplicate, list, update, delete, enable/disable
- **Dependencies:** Task 1

### Task 11: Runtime Custom Tools — Meta-Tool & Executor
- **Files:**
  - `internal/server/custom_tools.go` (create)
  - `internal/server/custom_tools_test.go` (create)
- **Details:**
  - Server-only CustomToolManager with meta-tools: tool_create, tool_list, tool_delete, tool_enable, tool_disable
  - Simple string substitution (NOT text/template) for argument rendering
  - Shell: env vars + Docker sandbox. HTTP: reuse ssrfSafeDialer. Code_exec: Piston.
  - In-memory cache populated on startup, invalidated on CRUD
  - Name collision prevention with built-in tools
- **Tests:** Create (shell/http/code_exec), duplicate, conflict, delete, execute, template rendering, startup loading
- **Dependencies:** Task 5, Task 10

### Task 12: Docker Infrastructure — SearXNG & OpenCode
- **Files:**
  - `docker-compose.yml` (modify)
  - `Dockerfile.opencode` (create)
  - `searxng/settings.yml` (create)
  - `opencode/opencode.json` (create)
- **Details:**
  - SearXNG service with profile "search", OpenCode service with profile "opencode"
  - Profile "full" for everything. Existing services remain in default profile.
  - Healthchecks for both services
- **Tests:** Manual Docker verification
- **Dependencies:** Task 1

### Task 13: Update Example Configs
- **Files:**
  - `configs/server.toml.example` (modify)
  - `configs/client.toml.example` (modify)
  - `configs/server.docker.toml` (modify)
  - `configs/client.docker.toml` (modify)
- **Details:**
  - Add all new tool sections to both server and client examples
  - Add client-only tools to server example (commented)
  - Add vault section to client example
  - Update Docker configs with SearXNG/OpenCode URLs
- **Tests:** Verify example configs parse without error
- **Dependencies:** Tasks 2-9

### Task 14: Integration & Quality Gate
- **Files:** All modified files
- **Details:**
  - Full flow verification, quality gate (test, lint, build, vet)
  - Cross-channel workflow: run tool in #dev, send result to #reports, read #dev history from #reports
- **Tests:** All tools enabled, server/client overlap, custom tools startup, cross-channel workflow
- **Dependencies:** All previous tasks

## Risks

### Breaking Changes
- `ServerToolConfig` removal breaks Go type references. TOML structure compatible.
- `BuildTools` signature change from positional args to `BuildToolsOpts` struct.

### Complexity
- OpenCode SSE: most complex new code. Multi-line data fields, timeout, goroutine cleanup.
- Cross-channel history: `MemoryReader` interface couples tools to message types.
- Custom tool argument injection: simple string substitution + env vars, Docker-sandboxed.

### Security
- Config deny-list: security.*, vault.*, irc passwords, api keys, vault: prefixed values.
- IRC tool: prevent parting bus channel, rate-limit joins, only send/read from joined channels.
- Custom tool HTTP backend: reuse ssrfSafeDialer from http_request.go.
- Cross-channel history: operator responsibility for channel trust levels.

### Migration
- No breaking DB migration — custom_tools table is additive (migration 4).
- Existing TOML configs continue to work.
- Docker Compose profiles ensure new services are opt-in.
- Client configs gain optional [vault] section (backward compatible).
