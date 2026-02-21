# Phase 2 — Agent Core

## Summary

Build the brain of Murmur: multi-provider LLM integration, the agent loop, tool routing, SQLite conversation memory, built-in `!` commands, `!model` switching, and the secrets vault. After this phase, a user can talk to the agent in IRC, the agent calls the LLM, routes tool calls to connected clients, stores conversation history in SQLite, and responds. The `!` commands provide quick status/management without LLM involvement.

## Analysis

### Current state (Phase 1 complete)
- **Working:** CLI (`murmur server`/`client`/`version`), TOML config loading, IRC connection with reconnect, bus protocol (JSON messages over IRC), client registration/deregistration/heartbeat, server-side client registry with timeout monitoring, Docker deployment files.
- **Missing:** LLM integration, agent loop, tool routing (server→client→server), conversation memory (SQLite), `!` commands, secrets vault, multi-provider support, `!model` switching.

### What Phase 2 delivers
After this phase:
1. User sends a message in `#murmur` → server builds context (system prompt + history + tools) → calls LLM → responds in IRC
2. If LLM returns a tool call → server routes to correct client via bus → waits for response → feeds back to LLM → loops
3. Conversation history stored in SQLite, retrieved for context
4. `!status`, `!clients`, `!tools`, `!model`, `!history`, `!forget` commands work without LLM
5. Multiple LLM providers configured, switchable at runtime via `!model` (global scope — per-channel switching deferred)
6. Secrets vault encrypts API keys and sensitive config values
7. System prompt loaded from file, included in every LLM call
8. Bus messages authenticated with HMAC-SHA256 when bus_key is configured
9. Large bus messages (tool responses) handled via multi-part splitting/reassembly

### Dependencies introduced
- `modernc.org/sqlite` — Pure Go SQLite driver (no CGO)
- `golang.org/x/crypto` — Argon2id key derivation for vault

### Key design decisions
- **Tool response routing:** Concurrent-safe `map[string]chan *bus.ToolResponseMessage` keyed by RequestID. Agent loop sends request, blocks on channel with timeout. TypeToolResponse handler dispatches to correct channel. Explicit cleanup on timeout/cancel/completion prevents leaks.
- **Request ID generation:** `crypto/rand` 8-byte hex string (`req-<16 hex chars>`) for collision resistance across restarts.
- **Agent loop per-message:** Each user message spawns a goroutine running the agent loop. A `chanMu sync.Mutex` protects the `chanLocks map[string]*sync.Mutex` which provides per-channel serialization.
- **LLM provider interface:** Single `Provider` interface with `ChatCompletion(ctx, messages, tools) (Response, error)`. All providers implement it via the OpenAI-compatible `/v1/chat/completions` endpoint. Includes a `MockProvider` for testing.
- **LLM error handling:** Retry with exponential backoff (max 3 retries) on 5xx/timeout errors. No automatic fallback to other providers (user must `!model` switch manually). Context-too-long errors return a helpful message suggesting `!forget`.
- **Tool format conversion:** `bus.ToolDef` → OpenAI function calling format happens in `internal/llm/tools.go`. The `llm` package depends on `bus` (not vice versa) — correct dependency direction.
- **Bus message size for tool responses:** Multi-part support implemented BEFORE the tool router. Sender splits large messages into numbered chunks with envelope metadata. Receiver reassembles by MessageID with 30s timeout for incomplete messages.
- **Bus authentication:** HMAC-SHA256 signing/verification when `bus_key` is configured. Signature covers the full message body. Unsigned messages accepted when no bus_key is set (backward compatible).
- **`!` command detection:** `handleUserMessage` checks for `!` prefix before entering agent loop. Commands are dispatched to a command handler map.
- **`!model` scope:** Global (server-wide) in Phase 2. Per-channel switching is a future enhancement.
- **Conversation history limits:** Hard cap at `maxHistory` messages per channel. When exceeded, oldest messages are deleted (FIFO eviction). Summarization replaces this in Phase 4.
- **Vault:** Separate SQLite database (`vault.db`), AES-256-GCM encryption with unique 12-byte nonce per entry, Argon2id key derivation with stored salt. `vault:` prefix in config resolved at startup. CLI subcommands for management.
- **Security:** `handleUserMessage` checks nick against `AllowedUsers` AND verifies NickServ identification (when `require_nickserv` is true) before processing commands or agent messages.
- **Tool timeout:** Default 2 minutes, configurable. Router uses `context.WithTimeout` and cleans up on cancel/timeout/completion.

## Tasks

### Task 0: SQLite Database Package (`internal/db/`)
- **Files to create:**
  - `internal/db/sqlite.go`
  - `internal/db/migrations.go`
  - `internal/db/db_test.go`
- **Details:**
  - **`sqlite.go`:** `DB` struct wrapping `*sql.DB`. `Open(path string) (*DB, error)` opens/creates SQLite database, enables WAL mode and foreign keys. `Close() error`. Uses `modernc.org/sqlite` driver registered as `"sqlite"`.
  - **`migrations.go`:** `(db *DB) Migrate() error` runs schema migrations. Uses a `schema_version` table to track applied migrations. Migration 1 creates all tables. Each migration is a numbered SQL string. Migrations run in a transaction.
  - **Schema (Migration 1):**
    ```sql
    CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);
    INSERT INTO schema_version (version) VALUES (0);

    CREATE TABLE conversations (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        channel TEXT NOT NULL,
        role TEXT NOT NULL,
        content TEXT NOT NULL,
        tool_name TEXT,
        tool_call_id TEXT,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    CREATE INDEX idx_conversations_channel ON conversations(channel);
    CREATE INDEX idx_conversations_channel_ts ON conversations(channel, timestamp);

    CREATE TABLE clients (
        client_id TEXT PRIMARY KEY,
        hostname TEXT,
        tools_json TEXT,
        last_heartbeat DATETIME,
        status TEXT DEFAULT 'online'
    );

    CREATE TABLE scheduled_tasks (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL,
        schedule TEXT NOT NULL,
        action TEXT NOT NULL,
        channel TEXT NOT NULL,
        enabled BOOLEAN DEFAULT 1,
        last_run DATETIME,
        next_run DATETIME
    );
    CREATE INDEX idx_scheduled_tasks_next_run ON scheduled_tasks(next_run);

    CREATE TABLE summaries (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        channel TEXT NOT NULL,
        summary TEXT NOT NULL,
        messages_start INTEGER,
        messages_end INTEGER,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    CREATE INDEX idx_summaries_channel ON summaries(channel);

    CREATE TABLE notes (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL,
        created DATETIME DEFAULT CURRENT_TIMESTAMP,
        updated DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    ```
- **Tests:**
  - Open in-memory database (`:memory:`)
  - Migrate creates all tables (verify with `sqlite_master` query)
  - Migrate is idempotent (running twice is safe)
  - Schema version tracking works (version increments)

### Task 1: Server Config Extensions
- **Files to modify:**
  - `internal/config/server_config.go`
  - `internal/config/config_test.go`
  - `configs/server.toml.example`
- **Details:**
  - Add `LLMConfig` struct to `ServerConfig`:
    ```go
    type LLMConfig struct {
        Default   string                       `toml:"default"`
        Providers map[string]LLMProviderConfig  `toml:"providers"`
    }
    type LLMProviderConfig struct {
        APIBase     string  `toml:"api_base"`
        APIKey      string  `toml:"api_key"`
        Model       string  `toml:"model"`
        MaxTokens   int     `toml:"max_tokens"`
        Temperature float64 `toml:"temperature"`
    }
    ```
  - Add `MemoryConfig` struct:
    ```go
    type MemoryConfig struct {
        DBPath       string `toml:"db_path"`
        MaxHistory   int    `toml:"max_history"`
        SummaryModel string `toml:"summary_model"`
    }
    ```
  - Add `VaultConfig` struct:
    ```go
    type VaultConfig struct {
        Enabled       bool   `toml:"enabled"`
        DBPath        string `toml:"db_path"`
        PassphraseEnv string `toml:"passphrase_env"`
    }
    ```
  - Add `SystemPromptFile string` field to `ServerSection`.
  - Add these to `ServerConfig`: `LLM LLMConfig`, `Memory MemoryConfig`, `Vault VaultConfig`.
  - Update `Validate()`:
    - If `LLM.Default` is set and `LLM.Providers` is non-empty, verify default exists in Providers map.
    - If `Memory.DBPath` is empty, default to `DataDir + "/memory.db"`.
    - If `Memory.MaxHistory` is 0, default to 100.
    - Expand `~` in `Memory.DBPath`, `Vault.DBPath`, `Server.SystemPromptFile`.
    - If `Vault.DBPath` is empty and `Vault.Enabled`, default to `DataDir + "/vault.db"`.
  - Update `configs/server.toml.example` to show the LLM, Memory, and Vault sections (uncommented with example values).
- **Tests:**
  - Valid config with LLM providers loads correctly
  - Default provider validation (must exist in providers map)
  - Memory defaults (DBPath, MaxHistory)
  - Vault config parsing
  - Home expansion on new paths
  - Empty LLM config is valid (server starts without LLM for testing)

### Task 2: LLM Provider Package (`internal/llm/`)
- **Files to create:**
  - `internal/llm/provider.go`
  - `internal/llm/openai_compat.go`
  - `internal/llm/tools.go`
  - `internal/llm/llm_test.go`
- **Details:**
  - **`provider.go`:** Define the provider interface and message types:
    ```go
    // Provider defines the interface for LLM providers.
    type Provider interface {
        ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
        Name() string
    }

    // Role constants for message roles.
    const (
        RoleSystem    = "system"
        RoleUser      = "user"
        RoleAssistant = "assistant"
        RoleTool      = "tool"
    )

    type ChatRequest struct {
        Messages []Message
        Tools    []ToolDef
    }

    type Message struct {
        Role       string     `json:"role"`
        Content    string     `json:"content,omitempty"`
        ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
        ToolCallID string     `json:"tool_call_id,omitempty"`
        Name       string     `json:"name,omitempty"`
    }

    type ToolCall struct {
        ID       string       `json:"id"`
        Type     string       `json:"type"`
        Function FunctionCall `json:"function"`
    }

    type FunctionCall struct {
        Name      string `json:"name"`
        Arguments string `json:"arguments"`
    }

    type ToolDef struct {
        Type     string      `json:"type"`
        Function FunctionDef `json:"function"`
    }

    type FunctionDef struct {
        Name        string          `json:"name"`
        Description string          `json:"description"`
        Parameters  json.RawMessage `json:"parameters"`
    }

    type ChatResponse struct {
        Content   string
        ToolCalls []ToolCall
        Usage     Usage
    }

    type Usage struct {
        PromptTokens     int
        CompletionTokens int
        TotalTokens      int
    }

    // MockProvider is a test helper that returns predetermined responses.
    type MockProvider struct {
        NameVal   string
        Responses []*ChatResponse  // returned in order, cycling
        Errors    []error          // if set, returned instead of response
        Calls     []*ChatRequest   // records all calls for assertions
        callIdx   int
    }
    ```
  - **`openai_compat.go`:** `OpenAICompatProvider` implements `Provider`. Uses `net/http` directly (no external SDK). Constructs POST to `{api_base}/chat/completions` with JSON body. Non-streaming. Parses response JSON. Handles tool_calls in response. Sets `Authorization: Bearer {api_key}` header. Configurable model, max_tokens, temperature.
    - **Error handling:** Retry with exponential backoff on 5xx and timeout errors (max 3 retries, starting at 1s). Return error on 4xx (no retry). On rate limit (429), respect `Retry-After` header if present.
    ```go
    type OpenAICompatProvider struct {
        name        string
        apiBase     string
        apiKey      string
        model       string
        maxTokens   int
        temperature float64
        httpClient  *http.Client
    }
    func NewOpenAICompatProvider(name string, cfg config.LLMProviderConfig) *OpenAICompatProvider
    func (p *OpenAICompatProvider) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
    func (p *OpenAICompatProvider) Name() string
    ```
  - **`tools.go`:** Convert `[]bus.ToolDef` to `[]llm.ToolDef` for the LLM API:
    ```go
    func ConvertBusTools(busTools []bus.ToolDef) []ToolDef
    ```
    This wraps each bus tool in the OpenAI function calling format: `{"type": "function", "function": {"name": ..., "description": ..., "parameters": ...}}`.
- **Tests:**
  - ConvertBusTools conversion correctness (empty, single, multiple tools)
  - OpenAICompatProvider request building (use httptest server — verify JSON body, headers)
  - Response parsing: text response, tool call response, error response, multiple tool calls
  - Retry on 5xx (httptest returns 500 then 200)
  - No retry on 4xx
  - Usage token counting
  - MockProvider records calls and returns responses in order

### Task 3: Multi-Part Bus Messages
- **Files to modify:**
  - `internal/bus/protocol.go`
  - `internal/bus/sender.go`
  - `internal/bus/receiver.go`
  - `internal/bus/bus_test.go`
- **Details:**
  - Tool responses from clients can exceed the 400-byte bus message limit. Add multi-part message support.
  - **New envelope fields:**
    ```go
    type Envelope struct {
        Type      string `json:"type"`
        Signature string `json:"signature,omitempty"`
        PartIndex int    `json:"pi,omitempty"`   // 0-based part index
        PartTotal int    `json:"pt,omitempty"`   // total number of parts
        MessageID string `json:"mid,omitempty"`  // groups parts together
    }
    ```
  - **Sender changes:** `Send()` marshals the message. If it fits in `MaxBusMessageLen`, send as before (single message, no `pi`/`pt`/`mid`). If it exceeds the limit, split the JSON string into chunks, wrap each in a part envelope with `pi`, `pt`, `mid` fields, and send each as a separate IRC message. The `mid` is 8 hex chars from `crypto/rand`. Each part envelope is: `{"type":"_part","pi":N,"pt":TOTAL,"mid":"XXXXXXXX","d":"<chunk>"}`. The `d` field contains the chunk data.
  - **Receiver changes:** `HandleRaw()` checks if type is `"_part"`. If so, buffer the part in a `map[string]*partBuffer` (keyed by `mid`). When all parts received, reassemble the full JSON from chunks in order and dispatch normally. A background goroutine (or lazy cleanup on receive) removes incomplete buffers after 30s.
  - **Backward compatibility:** Single-part messages (no `_part` type) work exactly as before. Old clients that don't understand `_part` will log and drop them (existing unknown-type handling).
  - **Part size calculation:** Each part envelope has overhead (~60 bytes for the wrapper). The chunk size is `MaxBusMessageLen - overhead`.
- **Tests:**
  - Single-part messages unchanged (no `pi`/`pt`/`mid` in output)
  - Large message split into correct number of parts
  - Receiver reassembles multi-part message correctly
  - Incomplete multi-part times out and is cleaned up
  - Parts arriving out of order are reassembled correctly (by index)
  - Empty message still works
  - Exactly-at-limit message sent as single part

### Task 4: Bus Authentication (HMAC-SHA256)
- **Files to modify:**
  - `internal/bus/sender.go`
  - `internal/bus/receiver.go`
  - `internal/bus/errors.go`
  - `internal/bus/bus_test.go`
- **Details:**
  - When `busKey` is configured, sign and verify bus messages with HMAC-SHA256.
  - **Sender changes:** `NewSender` takes an optional `busKey string`. If non-empty, after marshaling the message JSON, compute `HMAC-SHA256(busKey, jsonBody)` and set the `Envelope.Signature` field to the hex-encoded digest. For multi-part messages, sign each part individually (the part envelope JSON is the signed payload).
  - **Receiver changes:** `NewReceiver` takes an optional `busKey string`. If non-empty, before dispatching, verify the signature. Extract `signature` from envelope, zero it out, re-marshal, compute HMAC, compare with `hmac.Equal()`. If verification fails, log warning and drop the message. If no busKey is configured, accept all messages (backward compatible).
  - **New error:** `ErrInvalidSignature = errors.New("bus message signature verification failed")`
  - **For multi-part:** Each part is individually signed and verified. Reassembly happens only after all parts pass verification.
- **Tests:**
  - Message signed and verified round-trip
  - Tampered message rejected
  - No busKey = no signature, no verification (backward compatible)
  - Multi-part messages: each part signed individually

### Task 5: Conversation Memory (`internal/server/memory.go`)
- **Files to create:**
  - `internal/server/memory.go`
  - `internal/server/memory_test.go`
- **Details:**
  - **`Memory`** struct wraps `*db.DB` and provides conversation history operations:
    ```go
    type Memory struct {
        db         *db.DB
        maxHistory int
        logger     *slog.Logger
    }
    func NewMemory(database *db.DB, maxHistory int, logger *slog.Logger) *Memory
    ```
  - **Methods:**
    - `AddMessage(channel, role, content, toolName, toolCallID string) error` — inserts into conversations table. After insert, if message count for channel exceeds `maxHistory`, delete oldest messages to bring count back to `maxHistory` (FIFO eviction). This is a single transaction.
    - `GetHistory(channel string, limit int) ([]llm.Message, error)` — retrieves last N messages for a channel, ordered by timestamp ASC (oldest first). Converts DB rows to `llm.Message` structs. Maps role "tool" to Message with ToolCallID and Name fields. Single query with `ORDER BY timestamp ASC LIMIT ?` using a subquery to get the last N.
    - `ClearHistory(channel string) error` — deletes all messages for a channel.
    - `GetHistoryCount(channel string) (int, error)` — counts messages for a channel.
  - Conversation summarization is deferred to Phase 4. Phase 2 uses hard cap with FIFO eviction.
- **Tests:**
  - Add and retrieve messages (in-memory SQLite)
  - History ordering (oldest first)
  - Limit works correctly
  - FIFO eviction when exceeding maxHistory
  - Clear history
  - Tool messages stored and retrieved correctly (toolName, toolCallID)
  - Empty channel returns empty slice (not nil)
  - Assistant messages with tool_calls stored as JSON in content

### Task 6: Tool Router (`internal/server/router.go`)
- **Files to create:**
  - `internal/server/router.go`
  - `internal/server/router_test.go`
- **Details:**
  - **`Router`** manages pending tool requests and routes them to clients:
    ```go
    type Router struct {
        registry *Registry
        sender   *bus.Sender
        logger   *slog.Logger

        mu       sync.Mutex
        pending  map[string]chan *bus.ToolResponseMessage  // requestID -> response channel
    }
    func NewRouter(registry *Registry, sender *bus.Sender, logger *slog.Logger) *Router
    ```
  - **Methods:**
    - `RouteToolCall(ctx context.Context, toolName string, arguments json.RawMessage, timeout time.Duration) (string, error)`:
      1. Find provider via `registry.GetToolProvider(toolName)`. If not found, return `fmt.Errorf("RouteToolCall: tool %q not available, no online client provides it", toolName)`.
      2. Generate requestID: 8 bytes from `crypto/rand`, hex-encoded with `"req-"` prefix.
      3. Create buffered response channel (cap 1).
      4. Lock mutex, store in pending map, unlock.
      5. Send `ToolRequestMessage` via sender. If send fails, clean up pending entry and return error.
      6. Create timeout context: `context.WithTimeout(ctx, timeout)`.
      7. `select` on: response channel (success), timeout context done (timeout/cancel).
      8. On any exit path: lock mutex, delete from pending, unlock. This is done via `defer` after the channel is registered.
      9. On success: if `msg.Status == "error"`, return `fmt.Errorf("tool %q returned error: %s", toolName, msg.Result)`. Otherwise return `msg.Result, nil`.
    - `HandleToolResponse(nick string, msg *bus.ToolResponseMessage)`:
      1. Lock mutex, look up channel by `msg.RequestID`, delete from map, unlock.
      2. If found, send response on channel (non-blocking, buffered chan).
      3. If not found, log warning "received tool response for unknown request" (stale/duplicate).
  - **Default tool timeout:** 2 minutes. Configurable via a future config field.
- **Tests:**
  - HandleToolResponse delivers to waiting RouteToolCall (goroutine test)
  - Unknown tool returns descriptive error
  - Timeout returns error and cleans up pending entry
  - Stale response (no pending request) logged and dropped, no panic
  - Concurrent requests with different IDs don't interfere
  - Context cancellation cleans up

### Task 7: Built-in Commands (`internal/server/commands.go`)
- **Files to create:**
  - `internal/server/commands.go`
  - `internal/server/commands_test.go`
- **Details:**
  - **`CommandHandler`** dispatches `!` commands:
    ```go
    type CommandHandler struct {
        registry     *Registry
        memory       *Memory
        conn         *irc.Connection
        agent        *Agent          // for model switching
        allowedUsers []string
        startTime    time.Time
        logger       *slog.Logger
    }
    func NewCommandHandler(registry *Registry, memory *Memory, conn *irc.Connection, agent *Agent, allowedUsers []string, startTime time.Time, logger *slog.Logger) *CommandHandler
    ```
  - **`HandleCommand(channel, nick, message string) bool`** — returns true if message was a `!` command (handled), false if not (pass to agent loop). Parses command and args from message.
  - **Security:** If `allowedUsers` is non-empty, check `nick` is in the list. If not allowed, respond with "unauthorized" and return true (consumed the command).
  - **Commands:**
    - `!status` — server uptime (from startTime), connected clients count, current model name, conversation message count for current channel
    - `!clients` — list connected clients: clientID, hostname, tools count, status, last heartbeat relative time
    - `!tools` — list all available tools across all online clients with tool name and description
    - `!model` — show current provider name and model, list all available provider names
    - `!model <name>` — switch to named provider via `agent.SetProvider(name)`. Respond with confirmation or error.
    - `!history [n]` — show last N messages for current channel (default 10). Format: `[role] content` per line.
    - `!forget` — clear conversation history for current channel via `memory.ClearHistory(channel)`. Respond with confirmation.
    - `!help` — list available commands with brief descriptions
  - Responses sent via `conn.Send(channel, response)`.
- **Tests:**
  - `!status` produces expected format
  - `!clients` lists registered clients
  - `!tools` lists tools from registry
  - `!model` shows current and lists available
  - `!model <name>` switches provider
  - `!model <invalid>` returns error
  - `!history` returns formatted messages
  - `!forget` clears history
  - `!help` lists commands
  - Non-`!` message returns false (not handled)
  - Unauthorized user gets rejection message, returns true

### Task 8: Agent Loop (`internal/server/agent.go`)
- **Files to create:**
  - `internal/server/agent.go`
  - `internal/server/agent_test.go`
- **Details:**
  - **`Agent`** runs the LLM agent loop:
    ```go
    type Agent struct {
        providers      map[string]llm.Provider
        activeProvider string
        mu             sync.RWMutex  // protects activeProvider
        registry       *Registry
        memory         *Memory
        router         *Router
        conn           *irc.Connection
        systemPrompt   string
        toolTimeout    time.Duration
        logger         *slog.Logger

        // Per-channel locks to prevent concurrent agent loops
        chanMu    sync.Mutex
        chanLocks map[string]*sync.Mutex
    }
    func NewAgent(providers map[string]llm.Provider, defaultProvider string, registry *Registry, memory *Memory, router *Router, conn *irc.Connection, systemPrompt string, toolTimeout time.Duration, logger *slog.Logger) *Agent
    ```
  - **`HandleMessage(ctx context.Context, channel, nick, message string)`** — the main agent loop:
    1. Acquire per-channel lock: lock `chanMu`, get or create mutex for channel in `chanLocks`, unlock `chanMu`, lock the channel mutex. Defer unlock.
    2. Store user message in memory: `memory.AddMessage(channel, "user", nick+": "+message, "", "")`
    3. Loop (max 10 iterations):
       a. Build messages array: system prompt message + conversation history from `memory.GetHistory(channel, maxHistory)`
       b. Get available tools: `registry.AllTools()` → `llm.ConvertBusTools()`
       c. Get active provider (read-lock `mu`)
       d. Call LLM: `provider.ChatCompletion(ctx, request)`
       e. If LLM error: send error message to IRC, return
       f. If response has tool calls:
          - Store assistant message in memory (content may be empty, tool_calls serialized)
          - For each tool call: `router.RouteToolCall(ctx, name, args, toolTimeout)`
          - Store each tool result in memory (role="tool", toolName, toolCallID)
          - Continue loop
       g. If response is text only: store in memory, send to IRC via `conn.Send(channel, response)`, return
    4. If max iterations reached: send "I've reached the maximum number of tool calls for this message. Please try again." to IRC.
  - **`SetProvider(name string) error`** — validates name exists in providers map, write-locks `mu`, sets `activeProvider`. Returns error if name not found.
  - **`GetProvider() string`** — read-locks `mu`, returns `activeProvider`.
  - **`GetProviderModel() (providerName, modelName string)`** — returns both the provider key and the model name for display.
  - **System prompt loading:** `LoadSystemPrompt(path string) (string, error)` — reads file. If path is empty or file doesn't exist, returns a built-in default prompt.
- **Tests (using MockProvider):**
  - Text-only response: user message → LLM returns text → sent to IRC
  - Tool call flow: user message → LLM returns tool call → router returns result → LLM returns text → sent to IRC
  - Multiple tool calls in one response
  - Max iterations cap (mock LLM always returns tool calls)
  - LLM error: error message sent to IRC
  - Tool routing error: error fed back to LLM as tool result
  - Provider switching: SetProvider changes active, GetProvider reflects it
  - Per-channel locking: concurrent HandleMessage on different channels don't block each other
  - Context cancellation stops the loop

### Task 9: Server Integration — Wire Everything Together
- **Files to modify:**
  - `internal/server/server.go`
  - `internal/server/server_test.go`
  - `cmd/murmur/main.go`
- **Details:**
  - **`server.go` changes:**
    - Add fields to `Server`: `db *db.DB`, `memory *Memory`, `router *Router`, `commands *CommandHandler`, `agent *Agent`.
    - Update `New()`:
      1. After creating registry (existing code), open database: `db.Open(cfg.Memory.DBPath)` → `db.Migrate()`
      2. Create Memory: `NewMemory(database, cfg.Memory.MaxHistory, logger)`
      3. Create LLM providers: iterate `cfg.LLM.Providers`, create `llm.NewOpenAICompatProvider(name, provCfg)` for each. Store in `map[string]llm.Provider`.
      4. Create Router: `NewRouter(registry, sender, logger)`
      5. Load system prompt: `LoadSystemPrompt(cfg.Server.SystemPromptFile)`
      6. Create Agent: `NewAgent(providers, cfg.LLM.Default, registry, memory, router, conn, systemPrompt, 2*time.Minute, logger)`
      7. Create CommandHandler: `NewCommandHandler(registry, memory, conn, agent, cfg.Security.AllowedUsers, time.Now(), logger)`
    - Update `registerBusHandlers()`: add `TypeToolResponse` handler that type-asserts and calls `router.HandleToolResponse(nick, msg)`
    - Update `handleUserMessage(channel, nick, message string)`:
      1. Check security: if `AllowedUsers` is non-empty, verify nick is in the list. If not, ignore (don't respond to unauthorized users in agent mode — only commands get rejection messages).
      2. Try command handler: `commands.HandleCommand(channel, nick, message)` → if true, return
      3. If no LLM providers configured, respond "no LLM configured" and return
      4. Otherwise: spawn goroutine running `agent.HandleMessage(s.ctx, channel, nick, message)` where `s.ctx` is the server's run context.
    - Update `Run()`: store context for goroutine use. Ensure DB is closed on shutdown (defer after open).
    - Pass `busKey` to Sender and Receiver constructors (from `cfg.Security.BusKey`).
  - **`main.go` changes:**
    - Add `vault` subcommand routing (dispatches to Task 10's vault CLI).
  - **Existing test updates:** Update `server_test.go` if the `New()` signature or required config fields changed. Ensure existing tests still pass with the expanded config (add LLM/Memory sections to test configs).
- **Tests:** Update existing server tests to work with new config requirements. No new test files — individual components have their own tests.

### Task 10: Secrets Vault (`internal/vault/`)
- **Files to create:**
  - `internal/vault/vault.go`
  - `internal/vault/vault_test.go`
- **Details:**
  - **`Vault`** provides encrypted key-value storage:
    ```go
    // Vault provides encrypted key-value storage using AES-256-GCM.
    type Vault struct {
        db  *sql.DB
        key []byte  // derived encryption key (32 bytes for AES-256)
    }
    ```
  - **`Open(dbPath, passphrase string) (*Vault, error)`** — opens SQLite database (using `modernc.org/sqlite`), creates tables if not exist, reads or generates salt, derives key via Argon2id, returns Vault. Argon2id parameters: time=1, memory=64MB, threads=4, keyLen=32.
  - **`Close() error`** — closes database.
  - **Key derivation:** On first open, generate 16-byte random salt via `crypto/rand`, store in `vault_meta` table with key `"salt"`. On subsequent opens, read salt from table. Derive key: `argon2.IDKey(passphrase, salt, 1, 64*1024, 4, 32)`.
  - **Schema:**
    ```sql
    CREATE TABLE IF NOT EXISTS vault_meta (
        key TEXT PRIMARY KEY,
        value BLOB NOT NULL
    );
    CREATE TABLE IF NOT EXISTS vault (
        key TEXT PRIMARY KEY,
        encrypted_value BLOB NOT NULL,
        nonce BLOB NOT NULL,
        created DATETIME DEFAULT CURRENT_TIMESTAMP,
        updated DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    ```
  - **Encryption:** AES-256-GCM. Each entry gets a unique 12-byte random nonce from `crypto/rand`. Encrypt: `aead.Seal(nil, nonce, plaintext, nil)`. Decrypt: `aead.Open(nil, nonce, ciphertext, nil)`.
  - **Methods:**
    - `Set(key, value string) error` — encrypt value, upsert into vault table (INSERT OR REPLACE).
    - `Get(key string) (string, error)` — read encrypted value + nonce, decrypt, return. Return `ErrKeyNotFound` if key doesn't exist.
    - `Delete(key string) error` — remove entry. No error if key doesn't exist.
    - `List() ([]string, error)` — return all keys sorted alphabetically (no values).
  - **Config resolution:** Standalone function (not a Vault method):
    ```go
    func ResolveVaultRefs(v *Vault, cfg *config.ServerConfig) error
    ```
    Walks specific config fields and replaces `"vault:keyname"` with the decrypted value from the vault. Fields to resolve: all `LLM.Providers[*].APIKey`, `Security.BusKey`, `IRC.Password`, `IRC.NickServPassword`. Returns error if any vault key is not found.
  - **Errors:**
    ```go
    var ErrKeyNotFound = errors.New("vault key not found")
    ```
- **Tests:**
  - Set and Get round-trip
  - Get nonexistent key returns ErrKeyNotFound
  - Delete removes entry
  - Delete nonexistent key is no-op
  - List returns all keys sorted
  - Different passphrase produces different key (can't decrypt)
  - ResolveVaultRefs replaces vault: references in config
  - ResolveVaultRefs returns error for missing vault key
  - Concurrent Set/Get operations are safe

### Task 11: Vault CLI Integration
- **Files to modify:**
  - `cmd/murmur/main.go`
- **Details:**
  - Implement `vault` subcommand with sub-subcommands:
    - `murmur vault set <key>` — reads value from stdin (one line). Encrypts and stores.
    - `murmur vault set <key> --value <value>` — takes value from flag.
    - `murmur vault get <key>` — decrypts and prints to stdout.
    - `murmur vault list` — lists all keys, one per line.
    - `murmur vault delete <key>` — removes entry. Prints confirmation.
  - **Passphrase:** Read from env var `MURMUR_VAULT_PASS`. If not set, prompt on stderr: `"Vault passphrase: "` and read from stdin (with terminal echo disabled if possible, fallback to plain read).
  - **Vault DB path:** Default `~/.murmur/vault.db`. Override with `--db <path>` flag.
  - **Error handling:** Clear error messages for: wrong passphrase (decrypt fails), key not found, missing arguments.
- **Tests:** None (CLI wiring only). Vault logic tested in Task 10.

### Task 12: Final Verification
- Run `go mod tidy`
- Quality gates: `golangci-lint run`, `go vet ./...`, `go test ./... -count=1`, `go build ./cmd/murmur`
- Verify: `./bin/murmur version`, `./bin/murmur help`
- Verify vault: `MURMUR_VAULT_PASS=test ./bin/murmur vault set testkey --value testval && MURMUR_VAULT_PASS=test ./bin/murmur vault get testkey`
- Verify all new packages compile and tests pass
- Check that Phase 1 tests still pass (no regressions)

## Risks

1. **Bus message size for tool responses:** Multi-part support (Task 3) is implemented early, before the tool router. This ensures tool responses of any size can be transmitted.
2. **LLM API compatibility:** Different providers may have subtle differences in tool calling format. OpenRouter normalizes most of this, but Kimi/GLM may need special handling. Start with OpenRouter, test others incrementally. The retry logic handles transient failures.
3. **SQLite concurrency:** `modernc.org/sqlite` supports WAL mode for concurrent reads. Write serialization is handled by SQLite itself. The agent loop's per-channel mutex helps reduce write contention. Memory operations use prepared statements for efficiency.
4. **Vault security:** The encryption key is derived from a passphrase and held in memory at runtime. This is acceptable for a personal tool — the vault protects at-rest secrets on disk, not runtime secrets in memory. The Argon2id parameters are tuned for reasonable security vs. startup time.
5. **Agent loop infinite loops:** The 10-iteration cap prevents runaway tool calling. The 2-minute tool timeout prevents indefinite blocking on unresponsive clients.
6. **IRC flood protection:** The agent loop may generate multiple IRC messages rapidly. girc has built-in flood protection that queues messages. Long responses are split by `Send()`.
7. **Conversation history growth:** Hard cap at `maxHistory` with FIFO eviction prevents unbounded growth. Phase 4 adds summarization for smarter context management.
8. **Bus authentication bypass:** HMAC is optional (backward compatible). When `bus_key` is not set, anyone on the bus channel can send messages. The IRC channel should be +i/+k for baseline security.
