# Per-Channel State + Pipeline Custom Tools

## Problem

1. **Channels lost on reconnect**: Dynamically joined channels only tracked in-memory. On reconnect, only `main` + `bus` re-joined.
2. **Model selection is global**: `!model kimi` switches provider for ALL channels.
3. **Channel topic not aligned with bot state**: Topic never auto-reflects active model.
4. **Custom tools are single-shot**: Can't chain multiple tools in a reusable workflow.

## Solution

### New `channel_settings` table + per-channel model + topic sync + pipeline backend

## Tasks

### Task 1: Migration — `channel_settings` table
- **Files:** `internal/db/migrations.go`
- Add migration 5:
  ```sql
  CREATE TABLE channel_settings (
      channel TEXT PRIMARY KEY,
      provider TEXT NOT NULL DEFAULT '',
      auto_join BOOLEAN NOT NULL DEFAULT 0,
      topic_prefix TEXT NOT NULL DEFAULT '',
      created DATETIME DEFAULT CURRENT_TIMESTAMP,
      updated DATETIME DEFAULT CURRENT_TIMESTAMP
  );
  CREATE INDEX idx_channel_settings_auto_join ON channel_settings(auto_join) WHERE auto_join = 1;
  ```
- Update DB migration tests if they assert table/index lists.

### Task 2: Channel settings CRUD — `ChannelSettingsStore`
- **Files:** `internal/server/channel_settings.go` (new), `internal/server/channel_settings_test.go` (new)
- `normalizeChannel(ch string) string` — `strings.ToLower()`, applied in every method
- `ChannelSettings` struct: `Channel`, `Provider`, `AutoJoin`, `TopicPrefix`
- `ChannelSettingsStore` with methods: `Get`, `Upsert`, `SetProvider`, `SetAutoJoin`, `GetProvider`, `GetAutoJoinChannels`
- All upserts use `INSERT ... ON CONFLICT(channel) DO UPDATE`
- All exported functions have doc comments
- Tests: Get, Upsert, SetProvider, SetAutoJoin, GetAutoJoinChannels, GetProvider_NotFound, case-insensitive channel lookup

### Task 3: Per-channel provider selection in Agent
- **Files:** `internal/server/agent.go`, `internal/server/agent_test.go`, `internal/server/commands.go`, `internal/server/commands_test.go`, `internal/server/server.go`, `internal/server/api_test.go`
- Add `channelSettings *ChannelSettingsStore` to Agent (param 17 + TODO for options refactor)
- `resolveProvider(channel string) (llm.Provider, error)` — nil-safe: if `channelSettings == nil`, fall back to global
- Replace `getActiveProvider()` with `resolveProvider(channel)` in `runLoop()`
- Update `ModelSwitcher` interface: `SetProvider(channel, name)`, `GetProviderForChannel(channel)`
- `!model` shows channel-specific vs global; `!model <name>` sets per-channel; `!model default` resets
- `!status` shows channel-specific model
- Global default remains config-only
- Update all test call sites

### Task 4: Auto-join channels on reconnect
- **Files:** `internal/server/server.go`, `internal/tools/irc_manage.go`, `internal/tools/irc_manage_test.go`, `internal/tools/builder.go`
- OnConnect callback: join auto-join channels (skip duplicates with static channels)
- `ChannelPersister` interface in `builder.go`: `SetAutoJoin(channel, bool) error`
- `irc_manage` join → `SetAutoJoin(channel, true)` (if persister non-nil)
- `irc_manage` part → `SetAutoJoin(channel, false)` (voluntary part only)
- Kick events do NOT clear auto_join
- Ensure main channel marked `auto_join = true` on startup

### Task 5: Topic sync on model change
- **Files:** `internal/server/agent.go`, `internal/server/agent_test.go`, `internal/server/server.go`
- `syncChannelTopic(channel string)`: builds `[model: <name>]` or `[model: <name>] <prefix>`
- Guards: skip if `conn == nil`, not oper, bus channel
- Track `lastTopics map[string]string` in Agent — skip SetTopic if unchanged
- Called after `SetProvider()` succeeds
- Called on reconnect after OPER confirmed, sync all joined channels
- Topic format: `[model: kimi]` on all channels

### Task 6: System prompt — show per-channel model
- **Files:** `internal/server/agent.go`
- In `buildSystemPrompt(channel)`: add `- Active model: kimi (channel-specific)` or `- Active model: kimi (global default)`
- Nil-safe: if `channelSettings == nil`, show global default
- Extend `TestAgent_BuildSystemPrompt`

### Task 7: Pipeline backend for custom tools
- **Files:** `internal/db/migrations.go`, `internal/server/custom_tools.go`, `internal/server/custom_tools_test.go`, `internal/server/agent.go`, `internal/server/server.go`
- Migration 6: recreate `custom_tools` table with `pipeline` in CHECK constraint (in transaction)
- `ToolExecutor` interface: `ExecuteTool(ctx, toolName, args) (string, error)`
- Agent implements `ExecuteTool`: server tools direct, bus tools via router (skip approval)
- Shell tool special-casing: `ExecuteTool` delegates to shared shell routing for `toolName == "shell"`
- Pipeline config: `{"steps": [{"tool": "...", "args": {...}}]}`
- Data flow: `{{key}}` from input args, `{{_output}}` from previous step
- Shell-safe substitution for shell tool steps, plain for others
- Runtime depth guard via context value (max depth 1, no nesting)
- Max 10 steps, validated at creation time
- Overall pipeline timeout: 5 minutes
- Intermediate output truncated between steps (25KB)
- Hard validation: referenced tools must exist at creation time
- Recursion prevention: no self-reference, no pipeline-calls-pipeline
- Audit logging: each step logged with tool name, duration, result length
- Partial failure: stops on error, returns error with step number
- `SetExecutor(ToolExecutor)` on CustomToolManager, called after Agent creation
- Tests: Create, Execute (mock), OutputChaining, StepError, NoSteps, SelfReference, NestedPipeline, NoExecutor, DepthGuard, MaxSteps, ShellSubstitution
