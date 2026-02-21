# Plan: Phase 3B + 4A — Remaining Tools & Core Advanced Features

## Summary

Implement the remaining essential tools (mail read, mail send, web search) and core advanced features (notes/KV store with server-side tool pattern, server-side scheduler, client-side cron, conversation summarization).

## Tasks

### Task 1: Server-Side Tool Registry & Agent Integration

**Files:**
- Create `internal/server/server_tools.go`
- Create `internal/server/server_tools_test.go`
- Modify `internal/server/agent.go`
- Modify `internal/server/agent_test.go`
- Modify `internal/server/server.go`

**Details:**

`ToolRegistry` struct (in `package server`):
```go
type ToolRegistry struct {
    mu    sync.RWMutex
    tools map[string]tools.Tool
}
```

Methods:
- `NewToolRegistry() *ToolRegistry`
- `Register(t tools.Tool) error` — adds a tool, returns error on duplicate name
- `Get(name string) (tools.Tool, bool)` — returns tool by name
- `AllToolDefs() []bus.ToolDef` — returns all tools as `bus.ToolDef` (using `tools.ToBusToolDefs`)
- `Names() []string` — sorted list of registered tool names

Modify `Agent`:
- Add `serverTools *ToolRegistry` field
- Update `NewAgent` to accept `serverTools *ToolRegistry`
- In `HandleMessage`, change tool assembly:
  ```go
  busTools := append(a.serverTools.AllToolDefs(), a.registry.AllTools()...)
  tools := llm.ConvertBusTools(busTools)
  ```
- In `routeToolCall`, add server-local dispatch:
  ```go
  if st, ok := a.serverTools.Get(toolName); ok {
      var argsMap map[string]any
      if err := json.Unmarshal(arguments, &argsMap); err != nil {
          return "", fmt.Errorf("routeToolCall: unmarshal args for server tool %q: %w", toolName, err)
      }
      return st.Handler(ctx, argsMap)
  }
  return a.router.RouteToolCall(ctx, toolName, arguments, a.toolTimeout)
  ```

Modify `server.go`:
- Create `ToolRegistry` in `New()`
- Pass to `NewAgent`
- (No tools registered yet — Task 4 will register notes)

**Tests:**
- `server_tools_test.go`:
  - `TestToolRegistry_RegisterAndGet` — register tool, get by name, get unknown returns false
  - `TestToolRegistry_AllToolDefs` — returns correct bus.ToolDef slice
  - `TestToolRegistry_DuplicateReturnsError` — registering same name returns error
  - `TestToolRegistry_ConcurrentAccess` — parallel reads/writes don't race
- `agent_test.go` updates:
  - `TestAgent_ServerToolExecution` — register a server tool, send message that triggers it, verify local execution (no bus routing)
  - `TestAgent_ServerToolPriority` — server tool with same name as client tool, verify server tool wins

---

### Task 2: Mail Read Tool (Thunderbird mbox parser)

**Files:**
- Add `MailReadConfig` to `internal/config/client_config.go`
- Create `internal/tools/mail_read.go`
- Create `internal/tools/mail_read_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`MailReadConfig`):
```go
type MailReadConfig struct {
    Enabled            bool   `toml:"enabled"`
    ThunderbirdProfile string `toml:"thunderbird_profile"`
    MailDir            string `toml:"mail_dir"`
}
```
Add `MailRead *MailReadConfig` to `ToolsConfig`. Validate: when enabled, `ThunderbirdProfile` required. Expand `~` in profile path.

Tool (`NewMailReadTool(cfg MailReadConfig) Tool`):
- Name: `mail_read`
- Parameters: `action` (required, enum: "unread", "search", "read"), `query` (optional), `message_id` (optional), `folder` (optional, default "Inbox"), `limit` (optional, default 10)
- Handler dispatches on action:
  - `unread`: scan mbox file for messages with `X-Mozilla-Status: 0000` (unread), return last N
  - `search`: scan mbox for messages matching query in From/Subject/body
  - `read`: find message by Message-ID, return full headers + plain text body

Mbox parsing (internal helpers, not exported):
- `parseMbox(reader io.Reader, limit int, filter func(msg) bool) ([]mailMessage, error)`
- `parseHeaders(headerBlock string) map[string]string`
- `parseMozillaStatus(status string) uint16`
- `isUnread(status uint16) bool` — `status & 0x0001 == 0`
- `extractPlainBody(bodyBlock string) string`

**Scope: headers + plain text body ONLY. No MIME decoding, no attachments, no HTML parsing.**

**Security: folder path validation** — reject folder names containing `..` or starting with `/`. Normalize to be relative to the configured MailDir.

**Tests:**
- `TestMailRead_Unread` — temp mbox with 3 messages (2 unread, 1 read), verify unread returns 2
- `TestMailRead_Search` — search by subject substring
- `TestMailRead_Read` — read by Message-ID
- `TestMailRead_MozillaStatus` — status bitmask parsing (0x0000=unread, 0x0001=read, 0x0009=read+deleted)
- `TestMailRead_EmptyMbox` — empty file returns "no messages"
- `TestMailRead_FolderNotFound` — missing folder returns clear error
- `TestMailRead_LimitRespected` — limit=2 with 5 unread returns only 2
- `TestMailRead_PathTraversal` — folder with `..` or absolute path rejected

---

### Task 3: Mail Send Tool (SMTP with security hardening)

**Files:**
- Add `MailSendConfig` to `internal/config/client_config.go`
- Create `internal/tools/mail_send.go`
- Create `internal/tools/mail_send_test.go`
- Create `internal/config/vault_resolve.go`
- Create `internal/config/vault_resolve_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`MailSendConfig`):
```go
type MailSendConfig struct {
    Enabled     bool   `toml:"enabled"`
    SMTPHost    string `toml:"smtp_host"`
    SMTPPort    int    `toml:"smtp_port"`     // default 587
    SMTPUser    string `toml:"smtp_user"`
    SMTPPass    string `toml:"smtp_password"` // supports "vault:" prefix
    FromAddress string `toml:"from_address"`
    RequireTLS  bool   `toml:"require_tls"`   // default true
}
```

**Vault resolution on client:** Add `ResolveVaultRef(value string) (string, error)` in `internal/config/vault_resolve.go`:
1. If value doesn't start with `vault:`, return as-is
2. Extract key name, open vault DB at `~/.murmur/vault.db`, derive key from `MURMUR_VAULT_PASS` env var
3. Error if env var not set or key not found

**Security measures:**
- CRLF injection prevention: `strings.NewReplacer("\r", "", "\n", "")` on to, cc, reply_to, subject
- `net/mail.ParseAddress` validation on recipient addresses
- STARTTLS required by default (port 587 only for MVP, no implicit TLS on 465)
- TLS config with `ServerName` set to SMTP host
- Plain text only — no HTML email

**Tests:**
- `TestMailSend_CRLFSanitization` — verify CRLF stripped from all header fields
- `TestMailSend_EmailValidation` — valid and invalid email addresses
- `TestMailSend_MessageFormat` — verify RFC 2822 message structure
- `TestMailSend_RequiredArgs` — missing to/subject/body returns error
- `TestMailSend_TLSRequired` — RequireTLS=true and no STARTTLS returns error
- `TestVaultResolve_PlainValue` — non-vault: prefix returns as-is
- `TestVaultResolve_VaultPrefix` — vault: prefix resolves

---

### Task 4: Notes/KV Store (First Server-Side Tool)

**Files:**
- Create `internal/server/notes.go`
- Create `internal/server/notes_test.go`
- Modify `internal/server/commands.go`
- Modify `internal/server/server.go`

**Details:**

`NotesStore` struct (wraps DB operations on `notes` table):
```go
type NotesStore struct {
    db     *db.DB
    logger *slog.Logger
}
```

Methods:
- `NewNotesStore(database *db.DB, logger *slog.Logger) *NotesStore`
- `Set(key, value string) error` — INSERT OR REPLACE
- `Get(key string) (string, error)` — returns `ErrNoteNotFound` if missing
- `List() ([]NoteEntry, error)` — all keys with timestamps
- `Delete(key string) error`
- `Search(query string) ([]NoteEntry, error)` — LIKE search on key and value

Register 5 server-side tools via `ToolRegistry`:
1. `note_set` — params: `key` (required), `value` (required)
2. `note_get` — params: `key` (required)
3. `note_list` — no params
4. `note_delete` — params: `key` (required)
5. `note_search` — params: `query` (required)

Add `!notes` command to `commands.go`:
- `!notes` — list all note keys
- `!notes get <key>` — get a note value
- `!notes set <key> <value>` — set a note
- `!notes delete <key>` — delete a note

Wire in `server.go`:
- Create `NotesStore` in `New()` after DB open
- Register note tools on `ToolRegistry`
- Pass `NotesStore` to `CommandHandler`

**Tests:**
- `TestNotesStore_SetAndGet`
- `TestNotesStore_GetNotFound`
- `TestNotesStore_Update` — verify updated timestamp changes
- `TestNotesStore_List`
- `TestNotesStore_Delete`
- `TestNotesStore_Search`
- `TestNotesCommand_List`
- `TestNotesCommand_GetSet`

---

### Task 5: Web Search Tool (Brave Search API)

**Files:**
- Add `WebSearchConfig` to `internal/config/client_config.go`
- Create `internal/tools/web_search.go`
- Create `internal/tools/web_search_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`WebSearchConfig`):
```go
type WebSearchConfig struct {
    Enabled    bool   `toml:"enabled"`
    APIKey     string `toml:"api_key"`      // supports "vault:" prefix
    MaxResults int    `toml:"max_results"`  // default 5, cap 20
}
```

Tool: Brave Search API only (no SearXNG for MVP).
- Name: `web_search`
- Parameters: `query` (required), `count` (optional int)
- HTTP GET to `https://api.search.brave.com/res/v1/web/search`
- Header: `X-Subscription-Token: <api_key>`
- `io.LimitReader` at 2MB
- 15s HTTP timeout
- Format: numbered list with title, URL, description
- Note: Brave free tier = 2000 requests/month

**Tests:**
- `TestWebSearch_Success` — httptest mock
- `TestWebSearch_EmptyResults`
- `TestWebSearch_APIError`
- `TestWebSearch_Timeout`
- `TestWebSearch_RequiredQuery`
- `TestWebSearch_MaxResultsCapped`

---

### Task 6: Server-Side Scheduler

**Files:**
- Create `internal/server/scheduler.go`
- Create `internal/server/scheduler_test.go`
- Modify `internal/server/commands.go`
- Modify `internal/server/agent.go`
- Modify `internal/server/server.go`
- Modify `internal/config/server_config.go`
- Modify `internal/db/migrations.go`
- Update `configs/server.toml.example`

**Details:**

Config additions to `SchedulerConfig`:
```go
TickInterval  string `toml:"tick_interval"`  // default "30s"
MaxConcurrent int    `toml:"max_concurrent"` // default 3
```

`Scheduler` struct with tick loop, semaphore for backpressure.

Query: `SELECT * FROM scheduled_tasks WHERE enabled=1 AND next_run <= ? ORDER BY next_run ASC LIMIT ?` (parameterized by maxConcurrent).

**Agent refactor:** Extract LLM iteration loop from `HandleMessage` into private `runLoop(ctx, channel)`. Add `RunScheduledTask(ctx, channel, taskDescription)` that stores as `role=system` with `[Scheduled Task]` prefix, then calls `runLoop`.

**Cron parsing:** Use `github.com/robfig/cron/v3` `ParseStandard` only (parser, not scheduler). All schedules in **UTC** (documented in config comments).

**Migration 2:** Add composite index `CREATE INDEX idx_scheduled_tasks_enabled_next ON scheduled_tasks(enabled, next_run)`.

**Commands:**
- `!tasks` — list all scheduled tasks
- `!task add <cron_expr> <description>` — add task (channel = current)
- `!task remove <id>` — remove by ID
- `!task enable <id>` / `!task disable <id>`

**Backpressure:** Semaphore (chan struct{}) with maxConcurrent=3. If full, skip task this tick (logged as warning). 30s tick granularity documented in help text.

**Tests:**
- `TestScheduler_AddAndListTasks`
- `TestScheduler_RemoveTask`
- `TestScheduler_TickFiresDueTask` — mock agent
- `TestScheduler_SkipsDisabledTasks`
- `TestScheduler_BackpressureSkips`
- `TestScheduler_NextRunComputed`
- `TestComputeNextRun`
- `TestTaskCommands`

---

### Task 7: Client-Side Cron

**Files:**
- Add `CronJobConfig` to `internal/config/client_config.go`
- Modify `internal/bus/protocol.go` (extend `CronJob`)
- Create `internal/client/cron.go`
- Create `internal/client/cron_test.go`
- Modify `internal/client/client.go`
- Modify `internal/server/server.go` (cron result handler)
- Update `configs/client.toml.example`

**Details:**

**Protocol extension** — add to `CronJob`:
```go
NotifyOnlyOnChange bool `json:"notify_only_on_change,omitempty"`
NotifyOnlyOnError  bool `json:"notify_only_on_error,omitempty"`
```

**Notify precedence rules:**
- `Notify: false` → never notify (overrides everything)
- `Notify: true` + both new fields false → always notify (backward compat)
- `Notify: true` + `NotifyOnlyOnError: true` → only on error
- `Notify: true` + `NotifyOnlyOnChange: true` → only on change
- Both new fields true → notify on error OR change

**Client config:** `Cron []CronJobConfig` on `ClientConfig`.

**CronRunner:** Executes jobs via tool handlers, SHA256 change detection, smart notification. 1-minute ticker resolution. All schedules in **UTC**.

**Bus handlers:** CronAdd, CronRemove, CronList — with client_id filtering (ignore messages not addressed to this client). Only processed when `bus_key` is configured and HMAC validates.

**Server-side:** CronResult handler formats and sends to main channel.

**Tests:**
- `TestCronRunner_ExecutesJob`
- `TestCronRunner_ChangeDetection`
- `TestCronRunner_ErrorOnlyNotification`
- `TestCronRunner_AddRemoveJob`
- `TestCronRunner_ListJobs`
- `TestCronRunner_ClientIDFiltering`
- `TestCronRunner_ScheduleParsing`

---

### Task 8: Conversation Summarization

**Files:**
- Modify `internal/server/memory.go`
- Modify `internal/server/memory_test.go`
- Modify `internal/config/server_config.go`
- Modify `internal/server/server.go`
- Update `configs/server.toml.example`

**Details:**

Config additions to `MemoryConfig`:
```go
SummaryThreshold int `toml:"summary_threshold"` // default 80 (80% of MaxHistory)
```

Extend `Memory` with `summaryThreshold int` and `summaryProvider llm.Provider` (may be nil).

`MaybeSummarize(channel string) error` — triggered after AddMessage:
1. Check count > threshold
2. Summarize older half using summary_model provider
3. Store in `summaries` table
4. Delete summarized messages
5. Insert synthetic system message with summary

`GetHistory` prepends most recent summary as system message.

Summarization failure is non-fatal (logged, AddMessage still succeeds). If provider is nil, MaybeSummarize is a no-op.

**Tests:**
- `TestMemory_SummarizationTriggered` — mock provider
- `TestMemory_SummarizationDisabled` — nil provider
- `TestMemory_SummaryIncludedInHistory`
- `TestMemory_SummarizationFailureDoesntBlockAdd`
- `TestMemory_SummaryThresholdConfig`

## Risks

1. Mbox parsing edge cases (mitigated by plain-text-only scope)
2. Cron expression parsing (mitigated by robfig/cron/v3 parser)
3. Summarization quality with cheap models (configurable, optional)
4. SMTP testing requires mock servers
5. 30s scheduler tick granularity (documented)
6. Vault resolution on client requires local vault DB
7. Server tool name collisions (server wins, documented)
8. Migration 2 for composite index
