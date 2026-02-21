# Plan: Multi-User Permissions, Debug Enhancement, Setup Wizard, Dashboard Foundation & README Update

## Summary

This plan adds a multi-user permission system (separate `permissions.toml` with user+channel layers), a `permissions_manage` LLM tool for natural language admin, NickServ identity verification, per-user API keys for webhooks, scheduled task permission tracking, enhanced debug channel logging, a comprehensive interactive setup wizard, the foundation for a Vue.js web dashboard (Go backend + build pipeline), and a README update documenting all new features.

## Key Design Decisions

1. **Separate `permissions.toml`**: Machine-managed file for user/channel permissions. No comments to preserve. Main `server.toml` references it via `security.permissions_file`. Cleanest TOML writing approach.
2. **Layered auth**: `security.allowed_users` gates basic access. `[users.*]` in permissions.toml adds role-based permissions on top. Both coexist.
3. **NickServ verification**: Required when `[users]` config exists. Prevents nick spoofing for privilege escalation.
4. **Scheduled task permissions**: Tasks store `created_by` nick. At execution time, use creator's current effective permissions. Events use admin-equivalent.
5. **Per-user API keys**: Each user can have an `api_key` for webhooks. Events received with that key are processed with that user's permissions.
6. **Natural language + IRC commands**: Both `!user`/`!channel` commands AND a `permissions_manage` LLM tool (admin-only) for natural language permission management.
7. **Cross-channel output**: Permission filtering applies to tool *availability* (input), not where tool *output* goes.
8. **Debug enhancement**: Enhance existing slog calls, not a new system. Add `[debug]` config section with granular toggles.
9. **Dashboard**: WebSocket-to-IRC bridge, each browser session = own IRC connection, SASL auth against Ergo. Part of the same `murmur server` binary. IRC server password auto-sent from server-side config.
10. **Setup wizard**: Bash script with optional IRC server password step. Ergo account registration via IRC connection after ircd starts.

---

## Phase 1: Foundation

### Task 1: Permission Config Structs and Loading

**Files:** `internal/config/server_config.go`, `internal/config/permissions.go` (new)

**Details:**
- Create `internal/config/permissions.go` with types:
  - `PermissionsConfig` — top-level structure of permissions.toml with `Users map[string]UserPermissions` and `Channels map[string]ChannelPermissions`
  - `UserPermissions` — Role, Tools, DenyTools, Autonomy, AllowedModels, DenyModels, MaxMessagesPerHour, APIKey
  - `ChannelPermissions` — Tools, DenyTools, Autonomy, AllowedModels
  - `EffectivePermissions` — Tools (resolved names), Autonomy (most restrictive), Models (resolved names), IsAdmin, RateLimit
- Add `LoadPermissionsConfig(path string) (*PermissionsConfig, error)` — reads and parses permissions.toml. Returns empty config (not error) if file doesn't exist.
- Add `ResolveEffectivePermissions(user UserPermissions, channel ChannelPermissions, allToolNames, allModelNames []string) EffectivePermissions`:
  - Expand `"*"` to all tool/model names
  - Expand prefix globs (`note_*`) to matching tool names
  - Compute intersection: `(expanded_user_tools ∩ expanded_channel_tools) - user_deny - channel_deny`
  - Autonomy: most restrictive wins (`report` > `approve` > `auto`)
  - Models: `(expanded_user_models ∩ expanded_channel_models) - user_deny_models`
- Add `MatchesToolPattern(pattern, toolName string) bool` — `"*"` matches all, `"note_*"` matches prefix, exact match otherwise
- Add `MostRestrictiveAutonomy(a, b string) string`
- Add `PermissionsFile string` to `SecurityConfig` in server_config.go (default: `<data_dir>/permissions.toml`)
- Validation: role must be "admin" or "user" (default "user"), autonomy must be "report"/"approve"/"auto" (default "approve")

**Tests:** `internal/config/permissions_test.go`
- TestResolveEffectivePermissions — intersection, deny subtraction, autonomy, models
- TestResolveEffectivePermissions_AdminWildcard — admin with `tools=["*"]` in channel with `deny_tools=["shell"]`
- TestMatchesToolPattern — exact, `*`, prefix glob, no match
- TestMostRestrictiveAutonomy — all 9 combinations
- TestLoadPermissionsConfig — valid TOML, missing file, invalid TOML
- TestDefaultUserFallback — user not in config uses [users.default]
- TestEmptyPermissionsConfig — no file = no restrictions
- TestUserAPIKeyParsing

**Dependencies:** None

---

### Task 2: Permission Manager and Enforcement in Agent Loop

**Files:** `internal/server/permissions.go` (new), `internal/server/agent.go`

**Details:**
- Create `internal/server/permissions.go` with `PermissionManager`:
  - `NewPermissionManager(permPath string, permCfg *config.PermissionsConfig, logger *slog.Logger) *PermissionManager`
  - `GetEffective(nick, channel string, allToolNames, allModelNames []string) config.EffectivePermissions` — cached
  - `FilterTools(tools []bus.ToolDef, nick, channel string) []bus.ToolDef`
  - `IsAdmin(nick string) bool`
  - `CheckRateLimit(nick string) bool` — sliding window, per-hour
  - `IsModelAllowed(nick, channel, modelName string) bool`
  - `GetUserByAPIKey(apiKey string) string`
  - `Update(permCfg *config.PermissionsConfig)` — replaces config, invalidates cache
  - `InvalidateCache()`
  - `StartCleanup(ctx context.Context)` — evicts stale rate limit entries
- Modify `Agent` struct: add `permissions *PermissionManager` field (may be nil)
- Modify `Agent.HandleMessage(ctx, channel, nick, message)`: pass `nick` to `runLoop`
- Modify `Agent.RunScheduledTask(ctx, channel, taskDescription, createdBy string)`: pass `createdBy` to `runLoop`
- Modify `Agent.HandleEvent(ctx, channel, source, ...)`: pass `"_system"` as nick
- Modify `Agent.runLoop(ctx, channel, nick string)`:
  - After assembling busTools, if pm != nil and nick doesn't start with `_`: call `pm.FilterTools()`
  - Log filtered count
- Modify `Agent.resolveProvider(channel, nick string)`:
  - Check `pm.IsModelAllowed()` before using provider
  - Fall back to first allowed model if current not allowed
- Wire into Server.New() and Server.Reload()

**Tests:** `internal/server/permissions_test.go`
- TestFilterTools_AdminGetsAll, TestFilterTools_UserGetsIntersection, TestFilterTools_DenyListWins
- TestFilterTools_NilManager, TestFilterTools_SystemNick
- TestIsAdmin, TestCheckRateLimit, TestCheckRateLimit_Cleanup
- TestIsModelAllowed, TestGetUserByAPIKey, TestPermissionCache

**Dependencies:** Task 1

---

### Task 3: NickServ Identity Verification

**Files:** `internal/server/nickserv.go` (new), `internal/server/server.go`, `internal/irc/connection.go`

**Details:**
- Create `NickServVerifier` with WHOIS-based identity checking and caching (5m TTL)
- Add `Whois(nick string) (*WhoisResult, error)` to `irc.Connection` — sends WHOIS, collects RPL_WHOISUSER (311), RPL_WHOISACCOUNT (330), RPL_ENDOFWHOIS (318)
- Modify `Server.handleUserMessage()`: if `require_nickserv` is true AND permissions exist, verify nick identity before processing
- Default: `require_nickserv` = true when permissions.toml has [users] entries
- Cache TTL configurable via `security.nickserv_cache_ttl`

**Tests:** `internal/server/nickserv_test.go`
- TestIsIdentified_Cached, TestIsIdentified_Expired, TestInvalidateCache, TestNickServDisabled

**Dependencies:** Task 2

---

### Task 4: Admin Commands (!user and !channel)

**Files:** `internal/server/commands.go`, `internal/server/commands_admin.go` (new), `internal/server/permissions_writer.go` (new)

**Details:**
- Create `PermissionsWriter`:
  - Read file → unmarshal → modify → marshal → validate → atomic write (temp + rename)
  - Mutex serializes concurrent writes
  - Methods: Read, WriteUser, RemoveUser, WriteChannel, RemoveChannel
- Create admin command handlers in `commands_admin.go`:
  - `!user list/info/add/remove` and `!user <nick> role/tools/deny/autonomy/model/ratelimit`
  - `!channel list/info` and `!channel <ch> tools/deny/autonomy/model`
  - All check `pm.IsAdmin(nick)` first
  - After each write: call writer, then Reload(), then confirm
- Add `!user` and `!channel` to HandleCommand dispatch
- Add PermissionManager and PermissionsWriter to CommandHandler
- Update cmdHelp
- Add `users.*` and `channels.*` to config_manage deny list

**Tests:** `internal/server/commands_admin_test.go`, `internal/server/permissions_writer_test.go`
- TestCmdUserList, TestCmdUserAdd, TestCmdUserRemove, TestCmdUserToolsAdd/Set
- TestCmdUserDenyAdd, TestCmdUserAdminOnly, TestCmdChannelToolsAllow/Deny
- TestPermissionsWriter_RoundTrip, TestPermissionsWriter_ConcurrentWrites
- TestPermissionsWriter_AtomicWrite, TestPermissionsWriter_ValidationOnWrite

**Dependencies:** Task 1, Task 2

---

### Task 5: Permissions Manage LLM Tool

**Files:** `internal/server/permissions_tool.go` (new)

**Details:**
- Server-side LLM tool `permissions_manage` for natural language admin
- Actions: list_users, get_user, set_user_tools, set_user_deny, set_user_role, set_user_autonomy, set_user_model, set_user_ratelimit, add_user, remove_user, list_channels, get_channel, set_channel_tools, set_channel_deny, set_channel_autonomy, set_channel_model
- Calls same PermissionsWriter methods as !user/!channel commands
- Only visible to admin users (filtered by PermissionManager)
- Defense-in-depth: handler validates admin status even though tool is only in admin tool list

**Tests:** `internal/server/permissions_tool_test.go`
- TestPermissionsManage_ListUsers, TestPermissionsManage_SetUserTools, TestPermissionsManage_NonAdminRejected

**Dependencies:** Task 4

---

### Task 6: Per-User Rate Limiting Integration

**Files:** `internal/server/server.go`

**Details:**
- In handleUserMessage(), after flood.allow(nick) check:
  - If PermissionManager available, call pm.CheckRateLimit(nick)
  - If rate limited, send "rate limit exceeded" and return
  - Admin with max_messages_per_hour = -1 bypasses
  - Existing flood guard still applies to everyone
- Sliding window: timestamps per nick, count within last hour
- Cleanup goroutine evicts entries older than 2 hours

**Tests:** Already specified in Task 2 permissions_test.go

**Dependencies:** Task 2

---

### Task 7: Per-User API Keys for Webhooks

**Files:** `internal/server/api.go`, `internal/server/server.go`

**Details:**
- Modify handlePostEvent: extract API key, check pm.GetUserByAPIKey(), resolve nick
- If per-user key matches: process event with user's permissions (pass nick to HandleEvent)
- If no per-user key: fall back to global api.api_key (admin-equivalent)
- If neither: return 401
- Modify Agent.HandleEvent: accept nick parameter
- Constant-time comparison for key checks

**Tests:** `internal/server/api_test.go`
- TestPostEvent_PerUserKey, TestPostEvent_GlobalKey, TestPostEvent_InvalidKey

**Dependencies:** Task 2

---

### Task 8: Scheduled Task Permission Tracking

**Files:** `internal/db/migrations.go`, `internal/server/scheduler.go`, `internal/server/scheduler_tools.go`, `internal/server/agent.go`

**Details:**
- Migration 8: `ALTER TABLE scheduled_tasks ADD COLUMN created_by TEXT NOT NULL DEFAULT ''`
- Modify Scheduler.AddTask/AddOneShotTask: accept createdBy parameter
- Modify scheduler tools (task_add, reminder_add): extract nick from context via context.WithValue
- Modify Scheduler.Run(): read created_by, pass to RunScheduledTask
- Modify Agent.RunScheduledTask: pass createdBy as nick to runLoop
- Display created_by in !tasks output

**Tests:** `internal/server/scheduler_test.go`
- TestAddTask_StoresCreatedBy, TestRunScheduledTask_UsesCreatorPermissions, TestRunScheduledTask_LegacyTask

**Dependencies:** Task 2

---

### Task 9: Enhanced Debug Channel Logging

**Files:** `internal/config/server_config.go`, `internal/server/agent.go`, `internal/server/server.go`

**Details:**
- Add DebugConfig to ServerConfig: Enabled, Channel, LogLevel, LogToolCalls, LogLLMRequests, LogBusProtocol, LogPermissions
- Backward compat: if server.debug_channel set but [debug] not, populate from old field
- Add structured slog calls:
  - After LLM call: provider, tokens, latency
  - After tool routing: tool, duration, status
  - Permission denials: nick, channel, tool, reason
  - Config reload: what changed
- Add timing measurement around LLM calls and tool executions
- Make debug config reloadable

**Tests:** `internal/config/server_config_test.go`
- TestDebugConfigParsing, TestDebugConfigDefaults, TestDebugChannelBackwardCompat

**Dependencies:** Task 2

---

### Task 10: Config Examples, System Prompt, and Deny List Updates

**Files:** `configs/server.toml.example`, `configs/server.docker.toml.example`, `configs/permissions.toml.example` (new), `configs/system_prompt.md`, `internal/tools/config_manage.go`

**Details:**
- Add permissions_file reference and [debug] section to server config examples
- Create configs/permissions.toml.example with [users.default] and commented examples
- Update system_prompt.md: mention permission-based tool filtering
- Add users.*, channels.*, security.permissions_file to config_manage deny list

**Dependencies:** Task 1

---

## Phase 2: Setup & Deployment

### Task 11: Interactive Setup Script (Docker Mode)

**Files:** `scripts/setup.sh` (rewrite)

**Details:**
- 8-step interactive wizard:
  1. Installation mode (Docker/bare metal)
  2. Vault passphrase (enter or auto-generate)
  3. LLM provider (OpenRouter/OpenAI/Anthropic/Ollama/Custom) + API key + model
  4. Admin account (IRC nick + password)
  5. IRC server password (optional)
  6. Dashboard (enable/disable + port)
  7. Search (SearXNG/Brave)
  8. Additional tools (code_exec, shell)
- Docker execution: prerequisites check, .env generation, config templating, permissions.toml with admin, docker compose build, start ircd, register admin via IRC, vault store, start all, health check, print info
- IRC server password: if set, configure in ergo.yaml (server.password bcrypt hash) + murmur configs (irc.password)
- Ergo account registration: start ircd, connect via temp IRC connection, REGISTER with NickServ
- --dry-run flag

**Dependencies:** Task 10

---

### Task 12: Client Setup Mode

**Files:** `scripts/setup.sh` (extend)

**Details:**
- `./scripts/setup.sh client` mode
- Steps: client ID, IRC server details (address, port, TLS, bus key, server password), tool selection
- Generate ~/.murmur/client.toml

**Dependencies:** Task 11

---

### Task 13: Multi-Stage Dockerfile with Vue.js Build

**Files:** `Dockerfile`, `web/embed.go` (new), `web/frontend/` (scaffold), `Makefile`, `.gitignore`

**Details:**
- web/dist/.gitkeep placeholder for go:embed
- web/embed.go with `//go:embed all:dist`
- Minimal Vue 3 scaffold: package.json, vite.config.js, index.html, src/main.js, src/App.vue, src/router/index.js, src/styles/main.css
- Three-stage Dockerfile: Node → Go → Alpine
- Makefile: build-frontend, build (depends on frontend), build-go-only
- .gitignore: web/dist/, web/frontend/node_modules/

**Dependencies:** None (parallel with Phase 1)

---

### Task 14: Dashboard Backend — WebSocket Handler and IRC Bridge

**Files:** `internal/dashboard/handler.go` (new), `internal/dashboard/bridge.go` (new), `internal/dashboard/session.go` (new), `internal/config/server_config.go`

**Details:**
- DashboardConfig: Enabled, Listen, AuthMethod, SessionTimeout, ServerPassword (IRC server password, auto-sent for dashboard users)
- SessionStore: in-memory, crypto/rand IDs, HttpOnly/Secure/SameSite cookies, cleanup goroutine
- DashboardHandler: POST /dashboard/login (SASL/PASS auth), POST /dashboard/logout, GET /ws (WebSocket), GET /* (embedded static files). Login rate limiting (5/min/IP).
- Bridge: WebSocket↔IRC relay. IRC→WS: PRIVMSG, JOIN, PART, TOPIC, MODE, NAMES. WS→IRC: message, join, part.
- Use nhooyr.io/websocket
- Wire into Server.Run() as separate HTTP server
- Auto-send server_password via PASS command when creating user IRC connections

**Tests:** session_test.go, handler_test.go
- TestSessionCreateRetrieve, TestSessionExpiry, TestLoginRateLimit, TestWebSocketRequiresSession

**Dependencies:** Task 13

---

## Phase 3: Dashboard Frontend (Outlined — to be refined after Phase 2)

### Task 15: Vue.js App Shell — Login, Router, Dark Theme
### Task 16: Dashboard Overview Page
### Task 17: Chat Client — Message List and Input
### Task 18: IRC Color Parsing (mIRC codes 0-98)
### Task 19: Markdown Rendering for Bot Messages
### Task 20: Command Autocomplete
### Task 21: Channel Sidebar and User List
### Task 22: Admin Panel (User Manager, Channel Manager, Permissions Editor)
### Task 23: Tool Call Rendering (Collapsible Cards, Approve/Deny)
### Task 24: Mobile/Tablet Responsive Views

---

## Phase 4: Polish & README

### Task 25: Notification Badges (Unread Counts)

### Task 26: README Update
- Multi-User Permissions section (permissions.toml, resolution, admin commands, LLM tool, security)
- Web Dashboard section (architecture, auth, features, build)
- Setup Wizard section (Docker flow, client mode)
- Updated Debug Channel section ([debug] config, granular toggles)
- Updated comparison table
- Updated Getting Started, IRC Commands, Security, REST API sections
- Dashboard config examples

---

## Risks

- **TOML writing**: Mitigated by separate machine-managed permissions.toml with marshal/unmarshal round-trips
- **Nick identity trust**: Mitigated by NickServ verification (Task 3)
- **Agent loop signature change**: Compiler catches missing arguments (static typing)
- **Permission resolution performance**: Pre-computed and cached, invalidated on reload
- **Rate limit memory**: Cleanup goroutine evicts stale entries
- **WebSocket scalability**: Fine for personal agent (1-5 users), note for Phase 3 that pooling needed for >10
- **New dependencies**: nhooyr.io/websocket, possibly pelletier/go-toml/v2 for marshaling
- **Ergo account registration**: Must start ircd first, register via IRC connection
