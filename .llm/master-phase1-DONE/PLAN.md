# Phase 1 — Foundation

## Summary

Build the foundational skeleton of Murmur: the single binary with `server`/`client`/`version` subcommands, TOML configuration loading, IRC connection management, the bus protocol for server↔client communication, client registration/deregistration/heartbeat, and the server-side client registry. This phase produces a working system where a server and one or more clients can connect to IRC, discover each other via the bus, and maintain awareness of who's online — but no LLM, no tools, no agent loop yet.

## Analysis

### Current state
- **Exists:** `PLAN.md` (1526 lines, comprehensive spec), `.opencode/agents/` (16 agent files), git repo initialized (no commits)
- **Missing:** Everything — no `go.mod`, no source code, no `.gitignore`, no `Makefile`, no configs, no `Dockerfile`
- **Go version:** 1.25.7 available on the system
- **Module path:** `murmur` (local-only)

### What Phase 1 delivers
After this phase, you can:
1. Run `murmur server --config server.toml` → connects to IRC, joins `#murmur` and `#murmur-bus`, listens for client registrations
2. Run `murmur client --config client.toml` → connects to IRC, joins `#murmur-bus`, sends registration with (empty) tool list, sends heartbeats
3. Server tracks connected clients, detects disconnections via heartbeat timeout
4. `murmur version` prints version info
5. Bus messages are JSON-encoded, typed, and validated
6. Graceful shutdown on SIGINT/SIGTERM

### Dependencies introduced
- `github.com/lrstanley/girc` — IRC client
- `github.com/BurntSushi/toml` — TOML config parsing

No SQLite, no LLM, no vault yet — those come in Phase 2.

## Tasks

### Task 0: Project Scaffolding
- **Files to create:**
  - `.gitignore`
  - `go.mod` (via `go mod init murmur`)
  - `Makefile`
  - `README.md` (minimal)
  - `configs/server.toml.example`
  - `configs/client.toml.example`
  - `configs/system_prompt.md`
  - `internal/tools/tool.go` (Tool struct definition only)
- **Details:**
  - `.gitignore`: `bin/`, `*.exe`, `.env`, `*.db`, `vendor/`, `.idea/`, `.vscode/`, `*.swp`, `*.swo`, `*~`, `.DS_Store`, `murmur` (binary in root)
  - `go.mod`: `module murmur`, `go 1.23`
  - `Makefile`: build, build-all, test, vet, lint, clean targets. VERSION from git tags.
  - `configs/server.toml.example`: Full server config with Phase 1 fields. LLM/memory sections commented out.
  - `configs/client.toml.example`: Full client config with Phase 1 fields. Tools section commented out.
  - `configs/system_prompt.md`: Default system prompt from PLAN.md
  - `internal/tools/tool.go`: `Tool` struct with Name, Description, Parameters (json.RawMessage), Handler func. Referenced by bus protocol ToolDef.
- **Tests:** None (scaffolding only)

### Task 1: Configuration Package (`internal/config/`)
- **Files to create:**
  - `internal/config/server_config.go`
  - `internal/config/client_config.go`
  - `internal/config/config_test.go`
- **Details:**
  - **Shared `IRCConfig` struct** — server and client both embed it. Fields: Server, Port, TLS, Nick, User, Realname, Password, NickServPassword.
  - **`ServerConfig`:** ServerSection (DataDir), IRCConfig (embedded), ChannelsConfig (Main, Bus), SchedulerConfig (Enabled, HeartbeatInterval, ClientTimeout as duration strings), SecurityConfig (AllowedUsers, RequireNickServ, BusKey)
  - **`ClientConfig`:** ClientSection (ID, Hostname, Autonomy), IRCConfig (embedded), HeartbeatConfig (Interval as duration string), BusChannel string
  - Config key naming: Server uses `[irc.channels]` with `main` and `bus`. Client uses `[irc]` with `bus_channel`.
  - Duration fields: stored as string in TOML, parsed to `time.Duration` via helper.
  - Home expansion: `~` → `os.UserHomeDir()`
  - Validation: required fields, valid durations, non-empty IRC server/nick
  - Doc comments on all exported types and functions.
- **Tests:**
  - Valid server config loading
  - Valid client config loading
  - Missing required fields → error
  - Invalid duration → error
  - Home expansion

### Task 2: IRC Connection Package (`internal/irc/`)
- **Files to create:**
  - `internal/irc/connection.go`
  - `internal/irc/handler.go`
  - `internal/irc/format.go`
  - `internal/irc/irc_test.go`
- **Details:**
  - **`connection.go`:** Thin wrapper around girc. NewConnection, Connect (blocking, uses girc reconnect), Send (user messages — applies line splitting), SendRaw (bus messages — NO splitting, single PRIVMSG), OnMessage callback, OnConnect callback, Close.
  - **CRITICAL:** `Send()` splits long messages for user channels. `SendRaw()` does NOT split — bus messages must fit in a single IRC message. If a bus message exceeds the limit, the caller (bus.Sender) must handle it.
  - **NickServ:** On CONNECTED event, if nickserv_password set, send IDENTIFY.
  - **Reconnection:** Delegated to girc's built-in reconnect with exponential backoff.
  - **`handler.go`:** MessageHandler routes messages by channel. RegisterBusHandler for bus channel, RegisterUserHandler for user channels.
  - **`format.go`:** SplitMessage (word-boundary splitting, maxLen=400), StripFormatting (remove IRC color/bold codes). MaxMessageLen constant.
- **Tests:**
  - SplitMessage: short, long, no spaces, multiline, empty, unicode
  - StripFormatting: removes IRC formatting codes

### Task 3: Bus Protocol Package (`internal/bus/`)
- **Files to create:**
  - `internal/bus/protocol.go`
  - `internal/bus/sender.go`
  - `internal/bus/receiver.go`
  - `internal/bus/errors.go`
  - `internal/bus/bus_test.go`
- **Details:**
  - **`protocol.go`:** Message type constants. Envelope struct with Type and optional Signature field (unused Phase 1). All message structs: RegisterMessage, DeregisterMessage, HeartbeatMessage (with LoadInfo), ToolRequestMessage, ToolResponseMessage, CronResultMessage, CronAddMessage, CronRemoveMessage, CronListMessage, CronListResponseMessage. ToolDef struct (Name, Description, Parameters json.RawMessage). ParseMessage and MarshalMessage functions.
  - **`sender.go`:** Sender wraps irc.Connection + bus channel. Send marshals to JSON and calls SendRaw. Returns error if marshaled message exceeds MaxBusMessageLen (400 bytes). Convenience methods for each message type.
  - **`receiver.go`:** Receiver with handler map. On(msgType, handler). HandleRaw parses JSON, dispatches. Logs and drops invalid messages — never panics.
  - **`errors.go`:** ErrMessageTooLarge, ErrUnknownMessageType, ErrInvalidJSON typed errors.
  - **Bus message size:** Sender rejects messages > 400 bytes with ErrMessageTooLarge. Phase 2 will add multi-part support for large payloads.
- **Tests:**
  - Marshal/parse round-trips for all message types
  - Unknown type → ErrUnknownMessageType
  - Invalid JSON → ErrInvalidJSON
  - Oversized message → ErrMessageTooLarge
  - Cron message types (defined, parseable)

### Task 4: Server Core (`internal/server/`)
- **Files to create:**
  - `internal/server/server.go`
  - `internal/server/registry.go`
  - `internal/server/server_test.go`
- **Details:**
  - **`server.go`:** Server struct holds config, IRC connection, message handler, bus sender/receiver, registry. New() creates all components. Run(ctx) connects IRC, wires bus handlers (Register/Deregister/Heartbeat), wires user handler (log only in Phase 1), starts registry monitor, blocks until context cancelled. Graceful shutdown: stop accepting work → deregister → close IRC → wait for goroutines.
  - **`registry.go`:** Registry with sync.RWMutex, clients map, clientTimeout. ClientInfo: ClientID, Hostname, Tools, LastHeartbeat, Status, RegisteredAt, Load. Methods: Register, Deregister, Heartbeat, GetClient, GetOnlineClients, GetToolProvider (prefer most recent heartbeat), AllTools. StartMonitor goroutine: ticks every 30s, marks stale clients offline.
  - **In-memory only.** No SQLite persistence in Phase 1. Clients re-register on reconnect.
- **Tests:**
  - Registry Register/Deregister
  - Heartbeat updates
  - Heartbeat timeout marks offline
  - GetToolProvider with single/multiple clients
  - AllTools aggregation
  - Re-registration updates tools

### Task 5: Client Core (`internal/client/`)
- **Files to create:**
  - `internal/client/client.go`
  - `internal/client/registration.go`
  - `internal/client/client_test.go`
- **Details:**
  - **`client.go`:** Client struct holds config, IRC connection, bus sender/receiver, tools list (empty Phase 1), start time. New() creates components. Run(ctx) connects IRC, on connect sends registration, starts heartbeat goroutine, wires bus receiver for ToolRequest (log + error response in Phase 1), blocks until context cancelled. Shutdown: send deregister, stop heartbeat, close IRC.
  - **`registration.go`:** register() builds and sends RegisterMessage. deregister() sends DeregisterMessage. startHeartbeat(ctx) goroutine with ticker, sends HeartbeatMessage with uptime and system load. getSystemLoad() reads /proc/loadavg and /proc/meminfo on Linux, fallback to zero values.
  - **Goroutine lifecycle:** heartbeat goroutine uses context.Context for cancellation. Client.Run waits for goroutine exit before returning.
- **Tests:**
  - Registration message well-formed
  - Deregistration message well-formed
  - getSystemLoad returns non-negative values

### Task 6: CLI Entry Point (`cmd/murmur/`)
- **Files to create:**
  - `cmd/murmur/main.go`
- **Details:**
  - Subcommands via os.Args: `server [--config path]`, `client [--config path]`, `version`, help (no args)
  - Default config paths: `~/.murmur/server.toml`, `~/.murmur/client.toml`
  - Signal handling: SIGINT/SIGTERM → cancel context → graceful shutdown
  - `var version = "dev"` overridden by ldflags
  - Logging: log/slog with text handler
  - Exit codes: 0 clean, 1 error
  - Usage/help: print subcommands and flags
- **Tests:** None (wiring only)

### Task 7: Docker & Deployment Files
- **Files to create:**
  - `Dockerfile`
  - `docker-compose.yml`
- **Details:**
  - Dockerfile: Multi-stage, golang:1.23-alpine builder, CGO_ENABLED=0, alpine:latest final with ca-certificates + docker-cli
  - docker-compose.yml: Ergo IRC + murmur-server, volumes for data

### Task 8: Final Verification
- Run `go mod tidy`
- Quality gates: `go vet ./...`, `go test ./...`, `go build ./cmd/murmur`
- Verify: `./bin/murmur version`, `./bin/murmur help`
- Commit all files

## Risks
1. **IRC message size for bus:** Registration with many tools exceeds 512 bytes. Phase 1 rejects oversized messages. Phase 2 adds chunking.
2. **No integration tests:** Unit tests only. Manual testing with Ergo IRC.
3. **In-memory registry:** Loses state on restart. Self-healing via re-registration.
4. **No bus authentication:** Phase 2 adds HMAC when bus_key is set. Phase 1 relies on IRC channel modes (+i/+k).
5. **girc + Go 1.25:** Low risk — Go backward compatibility is excellent.
