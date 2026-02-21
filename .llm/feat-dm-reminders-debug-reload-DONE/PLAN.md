# Plan: DM Support, Reminders, Debug Channel, Hot Reload

> Branch: `feat/dm-reminders-debug-reload`
> Created: 2026-02-21

## Overview

Four independent features that improve Murmur's usability and operability:

1. **DM (Private Message) Support** — Users can talk to the bot in IRC private messages
2. **Calendar/Reminder Tool** — LLM can set one-time reminders that fire at a specific time
3. **Debug IRC Channel** — Toggleable IRC channel that receives live slog output
4. **Hot Config Reload** — Reload config without restarting via SIGHUP or `!reload`

---

## Task 1: DM (Private Message) Support

**Goal**: Users can talk to the bot in IRC private messages. PMs are treated as channels keyed by the sender's nick.

### Design

- **DM swap location**: In `internal/irc/handler.go` `route()` method. Before dispatching to `userHandler`, check if `channel` equals the bot's own nick (case-insensitive via `strings.EqualFold`). If so, set `channel = nick` (sender's nick). This keeps the IRC layer protocol-correct.
- Add `IsChannel(target string) bool` helper on `Connection` — checks for `#` prefix.
- In `agent.go` `buildSystemPrompt`: detect DM context (no `#` prefix), add "You are in a private conversation with <nick>".
- Skip cross-channel context for DMs.
- Skip `syncChannelTopic` for DMs.
- Commands (`!status`, `!model`, etc.) automatically work in DMs — responses go back to sender.

### Files to modify

- `internal/irc/handler.go` — DM swap in `route()`
- `internal/irc/connection.go` — Add `IsChannel()` helper
- `internal/server/agent.go` — DM-aware `buildSystemPrompt`, skip cross-channel for DMs
- `internal/server/server.go` — Skip `syncChannelTopic` for DMs

### Tests

- `internal/irc/handler_test.go` — Test DM swap routing
- `internal/irc/connection_test.go` — Test `IsChannel()` helper

---

## Task 2: Calendar/Reminder Tool (One-Shot Scheduling)

**Goal**: LLM can set one-time reminders that fire at a specific time, in addition to existing recurring cron tasks.

### Design

- **New DB migration (migration 7)**: `ALTER TABLE scheduled_tasks ADD COLUMN type TEXT NOT NULL DEFAULT 'cron'` and `ALTER TABLE scheduled_tasks ADD COLUMN run_at DATETIME`.
- `type` is `'cron'` (existing) or `'once'` (fire once then auto-disable).
- Add `Type` and `RunAt` fields to `ScheduledTask` struct.
- New method `AddOneShotTask(name string, runAt time.Time, action, channel string) (int64, error)`.
- **tick()** must branch on `task.Type` before calling `computeNextRun`. For one-shot tasks, skip next-run computation entirely. After execution, set `enabled = 0`.
- **EnableTask** must be type-aware — for one-shot tasks, don't try to parse empty cron schedule.
- New tool `reminder_add`:
  - Parameters: `message` (string, required), `time` (string, required — ISO 8601 like `2026-02-22T15:00:00Z` or relative like `+2h`, `+30m`, `+1d`)
  - Relative time parsing via regex `^\+(\d+)([hmd])$`
- Update `!tasks` command to show `[once]` vs `[cron]`, show `run_at` for one-shot tasks.
- Update ALL `Scan` calls (4+ locations: `getDueTasks`, `ListTasks`, `scheduler_tools.go`, `commands.go`).
- Periodic cleanup: delete disabled one-shot tasks older than 30 days in scheduler tick.

### Files to modify

- `internal/db/migrations.go` — Migration 7
- `internal/server/scheduler.go` — `ScheduledTask` struct, `tick()`, `getDueTasks`, `AddOneShotTask`, `EnableTask`, cleanup
- `internal/server/scheduler_tools.go` — New `reminder_add` tool, update existing tools for type awareness
- `internal/server/commands.go` — Update `cmdTasks` and `cmdTask` for type display

### Tests

- `internal/server/scheduler_test.go` — One-shot task lifecycle, cleanup, relative time parsing

---

## Task 3: Debug IRC Channel

**Goal**: Toggleable IRC channel (e.g., `#murmur-debug`) that receives live slog output for real-time debugging.

### Design

- New config field: `debug_channel` in `ServerSection` (toml: `debug_channel`). Empty = disabled.
- **New file `internal/irc/log_handler.go`**: Custom `slog.Handler` implementation:
  - `NewIRCLogHandler(level slog.Level) *IRCLogHandler` — created without connection (lazy binding)
  - `SetConnection(conn *Connection)` — called after IRC connects
  - `Enabled()` returns false until connection is set
  - Uses **drop-newest** semantics: `select { case ch <- msg: default: }` on a buffered channel (capacity 100)
  - Background goroutine drains buffer every 500ms, batches up to 5 lines per send
  - `Close()` method for clean shutdown
  - Level toggle via `atomic.Int64` for lock-free concurrent access
- **Custom `MultiHandler`** (fan-out to stderr handler + IRC handler) — Go's slog has no built-in multi-handler.
- Setup in `server.go New()`: create handler if `cfg.Server.DebugChannel` is set. After IRC connects in `Run()`, call `SetConnection()` and join the debug channel.
- **`!debug` command** in `commands.go`: toggle on/off. `CommandHandler` needs a reference to the IRC log handler (or interface).

### Files to create

- `internal/irc/log_handler.go`
- `internal/irc/log_handler_test.go`

### Files to modify

- `internal/config/server_config.go` — Add `DebugChannel` field
- `internal/server/server.go` — Wire up IRC log handler, join debug channel
- `internal/server/commands.go` — Add `!debug` command
- `configs/server.docker.toml.example` — Add `debug_channel` example
- `configs/server.toml.example` — Add `debug_channel` example

---

## Task 4: Hot Config Reload (SIGHUP)

**Goal**: Reload config without restarting. SIGHUP or `!reload` command triggers re-read of TOML config.

### Design

- **SIGHUP setup ordering**: Move SIGHUP handler setup to AFTER `server.New()`. Keep SIGINT/SIGTERM in `signalContext()` for shutdown, add separate SIGHUP goroutine after server creation.
- Store `configPath string` on `Server` struct.
- **`Server.Reload()` method**: Re-read config, re-resolve vault refs, apply safe changes:
  - LLM providers: Use `atomic.Pointer[map[string]llm.Provider]` for the providers map. All reads through `.Load()`, writes through `.Store()`. Fixes pre-existing race.
  - `allowed_users`: Use `atomic.Pointer[[]string]` for both `Server` and `CommandHandler`.
  - Simple fields (verbose, systemPrompt, maxHistory, crossChCtx, approvalTimeout): Protected by expanding `Agent.mu` scope.
  - Memory settings: Add `Memory.UpdateConfig()` method.
  - Debug channel: Enable/disable the IRC log handler.
- **NOT reloadable** (requires restart): IRC connection, DB path, vault, API listen address, bus key, tool configs.
- **`!reload` command**: Calls `Reload()` directly.
- **config_manage integration**: After successful `set`, call `Reload()` directly via a callback. Change response from "restart required" to "config reloaded" for safe fields.
- **`Agent.UpdateProviders(providers, defaultName)`**: Swaps providers via atomic pointer.
- **`Agent.UpdateConfig(verbose, maxHistory, crossChCtx, approvalTimeout, systemPrompt)`**: Updates under `mu` write lock.
- **`CommandHandler.UpdateAllowedUsers(users []string)`**: Atomic pointer swap.

### Files to modify

- `cmd/murmur/main.go` — SIGHUP handler after server creation
- `internal/server/server.go` — `configPath` field, `Reload()` method
- `internal/server/agent.go` — `UpdateProviders()`, `UpdateConfig()`, atomic provider map
- `internal/server/commands.go` — `!reload` command, `UpdateAllowedUsers()`
- `internal/tools/config_manage.go` — Reload callback after `set`
- `internal/server/memory.go` — `UpdateConfig()` method

### Tests

- `internal/server/agent_test.go` — Provider swap, config update
- `internal/server/server_test.go` — Reload method
