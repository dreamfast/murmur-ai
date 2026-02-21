# Plan: Phase 4B + 5 — Approval Flow, CLI Commands & Extensions

## Summary

Implement the remaining Phase 4 features (approval flow with autonomy levels, `murmur status`/`murmur send` CLI commands) and the highest-value Phase 5 extensions (git operations, RSS monitoring, DNS/SSL checks, image generation via ComfyUI, file operations). WhatsApp bridge is deferred — it requires a large external dependency (whatsmeow) with complex session management and is better as a standalone phase.

## Tasks

### Task 1: Approval Flow — Server-Side Approval Gate

**Files:**
- Create `internal/server/approval.go`
- Create `internal/server/approval_test.go`
- Modify `internal/server/agent.go`
- Modify `internal/server/server.go`
- Modify `internal/server/commands.go`
- Modify `internal/config/server_config.go`

**Details:**

The approval flow is implemented **entirely server-side**. The server checks the client's autonomy level before routing a tool call, and if approval is needed, holds the call and asks the user in IRC. No new bus protocol types are needed — the client is unaware of the approval gate.

`ApprovalManager` struct:
```go
type ApprovalManager struct {
    mu       sync.Mutex
    pending  map[string]*PendingApproval // keyed by approval ID
    logger   *slog.Logger
}

type PendingApproval struct {
    ID          string
    Channel     string
    ToolName    string
    Arguments   json.RawMessage
    ClientID    string
    RequestedAt time.Time
    ResultCh    chan ApprovalResult
}

type ApprovalResult struct {
    Approved bool
}
```

Methods:
- `NewApprovalManager(logger) *ApprovalManager`
- `RequestApproval(channel, toolName, arguments, clientID) (string, <-chan ApprovalResult)` — creates pending approval, returns ID and result channel
- `Resolve(id string, approved bool) error` — resolves a pending approval, sends result on channel
- `Cancel(id string)` — cancels a pending approval (timeout)
- `GetPending(channel string) []*PendingApproval` — returns pending approvals for a channel
- `Cleanup(maxAge time.Duration)` — removes expired approvals (called periodically)

Autonomy level resolution:
- The server needs to know each client's autonomy level. The `RegisterMessage` already carries `ClientID` — extend it to also carry `Autonomy string` (default `"auto"` for backward compat).
- Add `Autonomy string` field to `bus.RegisterMessage`.
- Store autonomy level in `Registry` alongside client info.
- Add `GetClientAutonomy(clientID string) string` method to `Registry`.

Agent integration (`agent.go`):
- Add `approvals *ApprovalManager` field to `Agent`
- In `routeToolCall`, before routing to a client tool:
  1. `registry.GetToolProvider(toolName)` to get clientID
  2. `registry.GetClientAutonomy(clientID)` to get autonomy level
  3. If `"auto"` → route immediately (current behavior)
  4. If `"report"` → return error "tool requires higher autonomy level"
  5. If `"approve"` → call `approvals.RequestApproval(...)`, send IRC message asking user, wait on result channel with timeout (2 minutes), then route or return denial
- Server-side tools always execute immediately (no approval needed).

User interaction:
- When approval is needed, the agent sends: `⚠ Tool call requires approval: <toolName>(<args summary>). Reply !approve or !deny`
- The approval ID is embedded in the pending state keyed by channel (only one pending per channel for simplicity).

Commands:
- `!approve` — approves the most recent pending approval in the channel
- `!deny` — denies the most recent pending approval in the channel
- `!pending` — lists pending approvals

Config:
- Add `ApprovalTimeout string` to `SchedulerConfig` (reuse section, or add new `[approval]` section). Default `"2m"`.

**Tests:**
- `TestApprovalManager_RequestAndResolve` — request approval, resolve it, verify result
- `TestApprovalManager_Timeout` — request approval, don't resolve, verify cleanup
- `TestApprovalManager_Cancel` — request and cancel
- `TestApprovalManager_GetPending` — multiple pending, filter by channel
- `TestAgent_ApprovalFlow_Auto` — auto autonomy, tool executes immediately
- `TestAgent_ApprovalFlow_Report` — report autonomy, tool rejected
- `TestAgent_ApprovalFlow_Approve` — approve autonomy, tool waits for approval

---

### Task 2: Approval Flow — Client Autonomy Registration

**Files:**
- Modify `internal/bus/protocol.go`
- Modify `internal/client/client.go`
- Modify `internal/server/registry.go`
- Modify `internal/server/registry_test.go`

**Details:**

Extend the bus protocol so clients advertise their autonomy level:

`RegisterMessage` — add field:
```go
Autonomy string `json:"autonomy,omitempty"` // "report", "approve", "auto"; default "auto"
```

`ClientInfo` in registry — add field:
```go
Autonomy string
```

`Registry` changes:
- `Register(msg)` stores `msg.Autonomy` (default `"auto"` if empty for backward compat)
- Add `GetClientAutonomy(clientID string) string` method

`Client` changes:
- Include `cfg.Client.Autonomy` in the `RegisterMessage` sent on connect

**Tests:**
- `TestRegistry_AutonomyStored` — register with autonomy, verify GetClientAutonomy
- `TestRegistry_AutonomyDefaultAuto` — register without autonomy field, verify defaults to "auto"

---

### Task 3: `murmur send` CLI Command

**Files:**
- Modify `cmd/murmur/main.go`
- Modify `internal/config/server_config.go` (or create a minimal CLI config)

**Details:**

`murmur send "message"` — connects to IRC, sends a message to the agent's main channel, waits for a response, prints it, and disconnects.

Implementation:
- New function `runSend()` in `main.go`
- Usage: `murmur send [--config path] "message to send"`
- Loads server config (needs IRC connection details + main channel)
- Creates a temporary IRC connection with a unique nick (`murmur-cli-<random4>`)
- Joins the main channel
- Sends the message
- Waits for a response from the server's nick (with 60s timeout)
- Collects response lines (stop after 3s of silence from the bot)
- Prints response to stdout
- Disconnects

This is a "fire and forget with response" pattern. The CLI acts as a temporary IRC client.

**Tests:**
- Integration test is impractical (requires IRC server). Add a unit test for the response collection logic:
- `TestCollectResponse` — mock message stream, verify collection with silence timeout

---

### Task 4: `murmur status` CLI Command

**Files:**
- Modify `cmd/murmur/main.go`

**Details:**

`murmur status` — connects to IRC, sends `!status` to the main channel, collects the response, prints it, and disconnects.

Implementation:
- New function `runStatus()` in `main.go`
- Reuses the same IRC connect-send-collect-disconnect pattern from Task 3
- Sends `!status` as the message
- Same response collection logic (wait for bot nick, 3s silence timeout)

Since this shares infrastructure with `murmur send`, extract a shared helper:
```go
func ircSendAndCollect(cfg *config.ServerConfig, message string, timeout time.Duration) (string, error)
```

**Tests:**
- `TestIRCSendAndCollect_Timeout` — verify timeout behavior with no response

---

### Task 5: Git Operations Tool

**Files:**
- Add `GitConfig` to `internal/config/client_config.go`
- Create `internal/tools/git_ops.go`
- Create `internal/tools/git_ops_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`GitConfig`):
```go
type GitConfig struct {
    Enabled      bool     `toml:"enabled"`
    AllowedRepos []string `toml:"allowed_repos"` // absolute paths to allowed repos
}
```

Tool: `git_ops`
- Name: `git_ops`
- Parameters: `action` (required, enum: "log", "diff", "status", "branch", "show"), `repo` (required, path), `args` (optional string, extra args like `--oneline -10`)
- Handler dispatches on action:
  - `log` → `git -C <repo> log --oneline -20` (default, overridable with args)
  - `diff` → `git -C <repo> diff` (or `diff --staged` via args)
  - `status` → `git -C <repo> status --short`
  - `branch` → `git -C <repo> branch -a`
  - `show` → `git -C <repo> show --stat` (or specific commit via args)

**Security:**
- Repo path must be in `allowed_repos` list (exact prefix match after `filepath.Clean`)
- Path traversal prevention: reject `..` in repo path
- Read-only operations only — no `git push`, `git commit`, `git checkout`, etc.
- All commands run via `exec.CommandContext` with 30s timeout
- Output truncated to `MaxOutputBytes`

**Tests:**
- `TestGitOps_Status` — init temp repo, add file, verify status output
- `TestGitOps_Log` — temp repo with commits, verify log output
- `TestGitOps_Diff` — temp repo with uncommitted changes, verify diff
- `TestGitOps_RepoNotAllowed` — repo not in allowed list, verify rejection
- `TestGitOps_PathTraversal` — repo with `..`, verify rejection
- `TestGitOps_InvalidAction` — unknown action returns error

---

### Task 6: RSS Feed Monitor Tool

**Files:**
- Add `RSSConfig` to `internal/config/client_config.go`
- Create `internal/tools/rss.go`
- Create `internal/tools/rss_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`RSSConfig`):
```go
type RSSConfig struct {
    Enabled    bool     `toml:"enabled"`
    Feeds      []string `toml:"feeds"`       // default feed URLs (optional)
    MaxItems   int      `toml:"max_items"`   // default 10, cap 50
}
```

Tool: `rss_read`
- Name: `rss_read`
- Parameters: `url` (required, feed URL), `count` (optional int, default from config)
- Handler:
  1. HTTP GET the feed URL (15s timeout, `io.LimitReader` at 2MB)
  2. Parse as RSS 2.0 or Atom (detect by root element)
  3. Return formatted list: numbered items with title, link, published date, description snippet (first 200 chars)

RSS/Atom parsing — use Go stdlib `encoding/xml`:
- RSS 2.0: `<rss><channel><item><title><link><pubDate><description>`
- Atom: `<feed><entry><title><link href="..."><published><summary>`
- No external dependency needed.

**Security:**
- URL validation: must be `http://` or `https://`
- `io.LimitReader` at 2MB
- 15s HTTP timeout
- Output truncated to `MaxOutputBytes`

**Tests:**
- `TestRSS_ParseRSS2` — httptest mock with RSS 2.0 feed
- `TestRSS_ParseAtom` — httptest mock with Atom feed
- `TestRSS_EmptyFeed` — empty feed returns "no items"
- `TestRSS_InvalidURL` — non-http URL rejected
- `TestRSS_MaxItemsCapped` — count > max returns max
- `TestRSS_HTTPError` — 404 returns error
- `TestRSS_Timeout` — slow server returns timeout error

---

### Task 7: DNS/SSL Check Tool

**Files:**
- Add `DNSConfig` to `internal/config/client_config.go`
- Create `internal/tools/dns_check.go`
- Create `internal/tools/dns_check_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`DNSConfig`):
```go
type DNSConfig struct {
    Enabled bool `toml:"enabled"`
}
```

Tool: `dns_check`
- Name: `dns_check`
- Parameters: `action` (required, enum: "lookup", "ssl", "whois_expiry"), `domain` (required)
- Handler dispatches on action:
  - `lookup` → `net.LookupHost(domain)` + `net.LookupMX(domain)` + `net.LookupTXT(domain)` — returns A/AAAA, MX, TXT records
  - `ssl` → `tls.Dial("tcp", domain+":443", ...)` — returns cert subject, issuer, expiry date, days until expiry, SAN list
  - `whois_expiry` → shell out to `whois <domain>` and parse expiry date (best-effort regex for common registrars)

All operations use Go stdlib (`net`, `crypto/tls`) except whois (shells out).

**Security:**
- Domain validation: must match `^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`
- 10s timeout on all network operations
- No arbitrary command execution (whois is the only shell-out, with validated domain)

**Tests:**
- `TestDNSCheck_Lookup` — lookup a well-known domain (use `localhost` or mock)
- `TestDNSCheck_SSL` — connect to a test TLS server (httptest.NewTLSServer)
- `TestDNSCheck_InvalidDomain` — domain with special chars rejected
- `TestDNSCheck_InvalidAction` — unknown action returns error
- `TestDNSCheck_SSLExpiry` — verify days-until-expiry calculation

---

### Task 8: Image Generation Tool (ComfyUI)

**Files:**
- Add `ImageGenConfig` to `internal/config/client_config.go`
- Create `internal/tools/image_gen.go`
- Create `internal/tools/image_gen_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`ImageGenConfig`):
```go
type ImageGenConfig struct {
    Enabled     bool   `toml:"enabled"`
    ComfyUIHost string `toml:"comfyui_host"` // e.g., "http://gpu-rig:8188"
    OutputDir   string `toml:"output_dir"`   // local dir for saving images
    UploadURL   string `toml:"upload_url"`   // optional URL to upload images for sharing
}
```

Tool: `image_gen`
- Name: `image_gen`
- Parameters: `prompt` (required), `negative_prompt` (optional), `width` (optional, default 1024), `height` (optional, default 1024), `steps` (optional, default 20), `seed` (optional, default random)
- Handler:
  1. Build a ComfyUI workflow JSON (simple txt2img workflow with SDXL-compatible structure)
  2. POST to `{comfyui_host}/prompt` with the workflow
  3. Poll `{comfyui_host}/history/{prompt_id}` until complete (5s intervals, 5min timeout)
  4. Download the output image from `{comfyui_host}/view?filename=...`
  5. Save to `output_dir`
  6. If `upload_url` is configured, POST the image there and return the URL
  7. Return result: "Image generated: <filename> (<width>x<height>)" or "Image generated: <url>"

ComfyUI API (HTTP, no SDK needed):
- `POST /prompt` — submit workflow, returns `{"prompt_id": "..."}`
- `GET /history/{prompt_id}` — poll for completion, returns outputs when done
- `GET /view?filename=...&subfolder=...&type=output` — download output image

The workflow JSON is a hardcoded template with placeholder substitution for prompt, dimensions, steps, seed. This mirrors the aibird approach.

**Security:**
- `output_dir` must exist and be writable
- `upload_url` is optional — if not set, images are only saved locally
- 5-minute timeout on generation
- Validate dimensions: min 64, max 2048, must be divisible by 8

**Tests:**
- `TestImageGen_BuildWorkflow` — verify workflow JSON has correct prompt/dimensions
- `TestImageGen_SubmitAndPoll` — httptest mock for ComfyUI API
- `TestImageGen_InvalidDimensions` — dimensions out of range rejected
- `TestImageGen_Timeout` — generation exceeds timeout
- `TestImageGen_RequiredPrompt` — missing prompt returns error

---

### Task 9: File Operations Tool

**Files:**
- Add `FileOpsConfig` to `internal/config/client_config.go`
- Create `internal/tools/file_ops.go`
- Create `internal/tools/file_ops_test.go`
- Modify `internal/tools/builder.go`
- Update `configs/client.toml.example`

**Details:**

Config (`FileOpsConfig`):
```go
type FileOpsConfig struct {
    Enabled      bool     `toml:"enabled"`
    AllowedPaths []string `toml:"allowed_paths"` // directories the tool can access
}
```

Tool: `file_ops`
- Name: `file_ops`
- Parameters: `action` (required, enum: "read", "list", "search", "stat"), `path` (required), `query` (optional, for search), `limit` (optional, default 50 for list)
- Handler dispatches on action:
  - `read` → read file contents (truncated to `MaxOutputBytes`). Binary files detected and rejected.
  - `list` → list directory contents with file sizes and modification times
  - `search` → recursive grep-like search for `query` in files under `path` (max depth 5, skip binary, max 50 results)
  - `stat` → file/directory metadata (size, permissions, mod time, type)

**Security:**
- Path must be under one of the `allowed_paths` (after `filepath.Clean` and symlink resolution via `filepath.EvalSymlinks`)
- Path traversal prevention: reject `..` after cleaning
- Binary file detection: check first 512 bytes with `http.DetectContentType`
- Read limit: `MaxOutputBytes` (25KB)
- Search depth limit: 5 levels
- Search result limit: 50 matches

**Tests:**
- `TestFileOps_Read` — read a temp file
- `TestFileOps_ReadBinary` — binary file rejected
- `TestFileOps_List` — list temp directory
- `TestFileOps_Search` — search for string in temp files
- `TestFileOps_Stat` — stat a temp file
- `TestFileOps_PathNotAllowed` — path outside allowed_paths rejected
- `TestFileOps_PathTraversal` — path with `..` rejected
- `TestFileOps_Symlink` — symlink escaping allowed_paths rejected

---

### Task 10: Wire Everything Together & Update Configs

**Files:**
- Modify `internal/server/server.go` — wire ApprovalManager into Agent
- Modify `internal/server/commands.go` — add `!approve`, `!deny`, `!pending` commands
- Update `configs/server.toml.example` — add approval timeout config
- Update `configs/client.toml.example` — add git, rss, dns, image_gen, file_ops tool configs
- Update `configs/system_prompt.md` (if exists) — mention approval flow

**Details:**

Server wiring:
- Create `ApprovalManager` in `server.New()`
- Pass to `NewAgent()`
- Register `!approve`, `!deny`, `!pending` commands in `CommandHandler`

Command implementations:
- `!approve` — calls `approvals.Resolve(latestPendingID, true)` for the current channel
- `!deny` — calls `approvals.Resolve(latestPendingID, false)` for the current channel
- `!pending` — lists pending approvals with tool name, args summary, and age

Config example updates:
- Server: add `[approval]` section with `timeout = "2m"`
- Client: add all new tool sections (git, rss, dns, image_gen, file_ops) as commented-out examples

**Tests:**
- `TestApproveCommand` — verify !approve resolves pending approval
- `TestDenyCommand` — verify !deny resolves pending approval
- `TestPendingCommand` — verify !pending lists pending approvals
- `TestApproveNoPending` — verify !approve with no pending returns message

## Risks

1. Approval flow adds latency to tool calls — mitigated by only applying to `"approve"` autonomy level
2. ComfyUI API may vary between versions — hardcoded workflow template may need updates
3. RSS parsing edge cases (malformed feeds) — mitigated by `io.LimitReader` and error handling
4. Whois output format varies by registrar — best-effort parsing with fallback
5. File operations symlink resolution may have edge cases on different OS — mitigated by `filepath.EvalSymlinks`
6. `murmur send`/`murmur status` require IRC server to be reachable — documented limitation
7. Per-channel single pending approval simplification may be limiting — can be extended later to support multiple pending per channel
8. Git tool relies on `git` binary being installed — documented requirement
