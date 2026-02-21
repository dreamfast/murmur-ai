# Plan: REST API + HTTP Request Tool

## Summary

Two features: (1) REST API on server and clients for receiving external events/data, with events injected into the agent loop. (2) A dedicated `http_request` server-side tool with SSRF protection. IRC remains the inter-component bus.

## Tasks

### Task 1: API Config Structures

- **Files:** `internal/config/server_config.go`, `internal/config/client_config.go`, `configs/server.docker.toml`, `configs/client.docker.toml`
- **Details:**
  - `APIConfig` struct: `Enabled bool`, `Listen string` (default `127.0.0.1:8080`), `APIKey string` (vault-resolvable), `EventRetentionDays int` (default 30)
  - Same struct reused for client (default `127.0.0.1:8081`)
  - `HTTPToolConfig` added to `ServerToolConfig`: `Enabled bool`, `Timeout string`, `MaxResponseBytes int`, `AllowedDomains []string`, `BlockPrivateIPs bool` (default true)
  - Validation: listen address format, timeout parseable as duration
  - Vault resolution for `APIKey` must happen alongside existing vault resolution in `server.New()` / `client.New()`
- **Tests:** Parse API config from TOML, test defaults, test validation errors
- **Dependencies:** None

### Task 2: Event Bus Message Type

- **Files:** `internal/bus/protocol.go`, `internal/bus/sender.go`, `internal/bus/bus_test.go`
- **Details:**
  - `TypeEvent = "event"`
  - `EventMessage`: `Type`, `ClientID`, `Source`, `EventType`, `Summary`, `Data` (optional), `EventID` (optional idempotency key), `Timestamp`
  - Add to `ParseMessage()` switch, add `SendEvent()` to Sender
- **Tests:** `TestParseEvent`, `TestRoundTrip_Event`
- **Dependencies:** None

### Task 3: Events Database Table

- **Files:** `internal/db/migrations.go`
- **Details:**
  - Migration 3: `CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT, source TEXT NOT NULL, event_type TEXT NOT NULL, summary TEXT NOT NULL, data TEXT, channel TEXT NOT NULL DEFAULT '#murmur', processed_at DATETIME, timestamp DATETIME DEFAULT CURRENT_TIMESTAMP)`
  - Indexes: `idx_events_timestamp`, `idx_events_source`, `idx_events_event_id` (unique, for idempotency)
  - Add retention config to `APIConfig`: `EventRetentionDays int` (default 30)
- **Tests:** Migration runs, table exists, basic insert/query
- **Dependencies:** None

### Task 4: Shared API Package

- **Files:** `internal/api/api.go` (new), `internal/api/middleware.go` (new)
- **Details:**
  - `JSONResponse(w, status, data)` — consistent `{"ok": true/false, "data": ..., "error": "..."}` envelope
  - `APIKeyMiddleware(key string) func(http.Handler) http.Handler` — constant-time comparison, skip if key empty
  - `NewHTTPServer(listen string, handler http.Handler, logger *slog.Logger) *http.Server` — configured with timeouts (read: 10s, write: 30s, idle: 60s)
  - `GracefulShutdown(ctx, server)` helper
- **Tests:** `internal/api/api_test.go` — test JSON envelope, test middleware accepts/rejects, test empty key bypasses auth
- **Dependencies:** None

### Task 5: Agent Event Handling

- **Files:** `internal/server/agent.go`
- **Details:**
  - New `HandleEvent(ctx context.Context, channel, source, eventType, summary, data string) error` method
  - Acquires channel lock (same pattern as `HandleMessage`)
  - Formats event as system message: `[Event from {source}] {eventType}: {summary}\n{data}`
  - Calls `Memory.AddMessage()` then `runLoop()` to trigger agent processing
  - Agent sees the event in context and can respond/act
- **Tests:** `internal/server/agent_test.go` — test HandleEvent triggers LLM call with event in context
- **Dependencies:** None

### Task 6: Server REST API

- **Files:** `internal/server/api.go` (new), `internal/server/server.go`
- **Details:**
  - `newServerAPIMux(s *Server) http.Handler` using shared API package
  - Endpoints:
    - `POST /api/v1/events` — validate payload, check idempotency (event_id), store in events table, call `agent.HandleEvent()`, return 202 Accepted
    - `GET /api/v1/events` — query events table with `?limit=50&source=&after_id=` pagination
    - `GET /api/v1/status` — uptime, client count, tool count, LLM provider
    - `GET /api/v1/clients` — list connected clients with tools and autonomy level
    - `GET /api/v1/health` — 200 OK
  - Add `httpServer *http.Server` to `Server` struct
  - In `Run()`: start HTTP goroutine in `monitorWg`, graceful shutdown on context cancel
- **Tests:** `internal/server/api_test.go` — test each endpoint with httptest, test auth, test event idempotency, test pagination
- **Dependencies:** Task 1, Task 3, Task 4, Task 5

### Task 7: Server Event Bus Handler

- **Files:** `internal/server/server.go`
- **Details:**
  - In `registerBusHandlers()`, add handler for `TypeEvent`
  - Store event in events table
  - Call `agent.HandleEvent()` with event details
  - This handles events forwarded from clients via the bus
- **Tests:** Covered by integration tests in Task 6
- **Dependencies:** Task 2, Task 5, Task 6

### Task 8: Client REST API

- **Files:** `internal/client/api.go` (new), `internal/client/client.go`
- **Details:**
  - `newClientAPIMux(c *Client) http.Handler` using shared API package
  - Endpoints:
    - `POST /api/v1/events` — validate payload, forward via `sender.SendEvent()`, return 202. If IRC disconnected, return 503.
    - `GET /api/v1/status` — uptime, tools, cron jobs
    - `GET /api/v1/health` — 200 OK
  - Add `httpServer *http.Server` to `Client` struct
  - Same goroutine/shutdown pattern as server
- **Tests:** `internal/client/api_test.go` — test endpoints, test 503 on IRC disconnect, test event forwarding
- **Dependencies:** Task 1, Task 2, Task 4

### Task 9: HTTP Request Tool

- **Files:** `internal/tools/http_request.go` (new), `internal/tools/http_request_test.go` (new), `internal/server/server.go`
- **Details:**
  - `HTTPRequestToolConfig`: `Timeout`, `MaxResponseBytes`, `AllowedDomains`, `BlockPrivateIPs`, `HTTPClient` (injectable)
  - Tool name: `http_request`
  - Parameters: `method` (enum, default GET), `url` (required), `headers` (optional object), `body` (optional string)
  - SSRF protection:
    1. Validate URL scheme (http/https only)
    2. If `AllowedDomains` non-empty, check domain against list (glob matching)
    3. If `BlockPrivateIPs` (default true): resolve DNS, check all IPs against private ranges (10/8, 172.16/12, 192.168/16, 127/8, 169.254/16, ::1, fe80::/10), reject if any match
    4. Use custom `http.Transport` with `DialContext` that validates resolved IPs before connecting (prevents DNS rebinding)
    5. Disable redirect following (or follow with same IP validation on each hop)
  - Read response body up to `MaxResponseBytes` (default 1MB)
  - Output format: `HTTP {status_code} {status_text}\nContent-Type: {ct}\nContent-Length: {cl}\n\n{body}`
  - `TruncateOutput()` on final result
  - Register in `server.go` following existing pattern
- **Tests:** GET/POST with httptest, domain allowlist blocking, private IP blocking, redirect handling, max response truncation, invalid URL rejection, timeout
- **Dependencies:** Task 1

### Task 10: Docker & Config Updates

- **Files:** `docker-compose.yml`, `configs/server.docker.toml`, `configs/client.docker.toml`
- **Details:**
  - Server: `ports: ["${MURMUR_API_PORT:-8080}:8080"]`, update listen to `0.0.0.0:8080` in docker config (container-internal)
  - Client: `ports: ["${MURMUR_CLIENT_API_PORT:-8081}:8081"]`, same pattern
  - Add `[api]` and `[tools.http]` sections to example configs
- **Tests:** Manual — `docker compose up`, `curl localhost:8080/api/v1/health`
- **Dependencies:** Task 6, Task 8, Task 9

## Risks

- **SSRF via DNS rebinding:** Mitigated by validating IPs at dial time, not just at URL parse time
- **Agent concurrency:** `HandleEvent` acquires the same channel lock as `HandleMessage`, so events queue behind user messages. This is correct behavior but means burst events won't be processed in parallel.
- **Event storms:** A misbehaving script could flood the API with events. Rate limiting is not in v1 but the API key + private bind address provides basic protection.
- **SQLite write contention:** Events table writes happen from HTTP goroutine while agent reads from its goroutine. SQLite WAL mode handles this, but high-frequency events could cause `SQLITE_BUSY`. Retention cleanup should run during low-activity periods.
