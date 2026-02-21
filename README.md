<p align="center">
  <img src="https://murmur.dreamfast.solutions/murmurlogo.webp" alt="Murmur" width="300">
</p>

<p align="center">
  A distributed personal AI agent that lives on IRC. One server runs the brain (LLM agent loop), multiple clients on different machines provide tools (shell, email, file access, image generation, etc.). Everything coordinates over standard IRC channels.
</p>

The name comes from a "murmuration" -- the coordinated movement of a flock of starlings. Each client is a bird in the flock.

```
         You (IRC client)          curl / webhooks
              |                         |
     Private IRC Server          REST API (:8080/:8081)
              |                         |
       Murmur Server          <-- LLM agent loop, memory, scheduler, REST API
              |
       #murmur-bus (IRC)
              |
    +---------+---------+
    |         |         |
  laptop    vps1      gpu-rig   <-- Murmur clients (tool providers)
  mail,     shell,    image_gen,
  files     dns,rss   comfyui
```

## Getting Started

Everything runs in Docker. You need **Docker** and **an LLM API key** (OpenRouter, OpenAI, or any OpenAI-compatible endpoint).

### 1. Clone and set up

```bash
git clone https://github.com/yourusername/murmur.git
cd murmur
```

### 2. Run the setup script

This builds the containers and stores your API key in the encrypted vault:

```bash
./scripts/setup.sh
```

It will ask for:
- **Vault passphrase** -- encrypts your secrets at rest. Pick something you'll remember.
- **LLM API key** -- your OpenRouter, OpenAI, or other provider key.

Or pass them as environment variables:

```bash
MURMUR_VAULT_PASS=mypassphrase LLM_API_KEY=sk-or-v1-... ./scripts/setup.sh
```

### 3. Start everything

```bash
MURMUR_VAULT_PASS=mypassphrase docker compose up -d
```

Or create a `.env` file (see `.env.example`):

```bash
cp .env.example .env
# Edit .env with your vault passphrase
docker compose up -d
```

But first, copy the example configs:

```bash
cp configs/server.docker.toml.example configs/server.docker.toml
cp configs/client.docker.toml.example configs/client.docker.toml
# Edit to taste (LLM provider, tools, etc.)
```

This starts three containers:
- **ircd** -- Ergo IRC server (port 6667)
- **murmur-server** -- the agent brain
- **murmur-client** -- a tool provider (system info, DNS, RSS out of the box)

### 4. Connect and talk

Point your IRC client at `localhost:6667` and join `#murmur`:

```
/server localhost 6667
/join #murmur
hey murmur, what's the uptime on this machine?
```

That's it. You're talking to your agent.

### 5. Check the logs

```bash
docker compose logs -f murmur-server
docker compose logs -f murmur-client
```

### Changing the LLM provider or model

Edit your `configs/server.docker.toml` (copied from the `.example`) and change the `[llm]` section. You can configure multiple providers and switch between them at runtime:

```toml
[llm]
default = "openrouter"

# OpenRouter (Claude, GPT, Gemini, open-source models)
[llm.providers.openrouter]
api_base = "https://openrouter.ai/api/v1"
api_key = "vault:llm-api-key"
model = "anthropic/claude-sonnet-4-5"

# Kimi (with reasoning/thinking mode)
[llm.providers.kimi]
api_base = "https://api.kimi.com/coding/v1"
api_key = "vault:kimi-api-key"
model = "kimi-for-coding"
reasoning = true

# Local Ollama
[llm.providers.ollama]
api_base = "http://host.docker.internal:11434/v1"
api_key = "dummy"
model = "llama3.1:70b"
```

Switch models at runtime in IRC:

```
!model                        # show current + list available
!model ollama                 # switch to local model for this channel
!model default                # reset to global default
```

Each channel can use a different provider. The setting is persisted across restarts.

Restart the server after config changes: `docker compose restart murmur-server`

### Adding more secrets to the vault

```bash
# Run vault commands inside the server container:
docker compose run --rm \
  -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server \
  vault set brave-search-key --db /data/vault.db --value "BSA..."

docker compose run --rm \
  -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server \
  vault list --db /data/vault.db
```

Then reference them in config files with the `vault:` prefix:

```toml
api_key = "vault:brave-search-key"
```

---

## Enabling More Tools

Edit `configs/client.docker.toml` and uncomment the tools you want. Restart the client after changes:

```bash
docker compose restart murmur-client
```

### Shell (Docker-sandboxed commands)

The shell tool runs commands inside ephemeral Docker containers. The client container needs access to the Docker socket:

1. Uncomment the Docker socket mount in `docker-compose.yml`:

```yaml
murmur-client:
  volumes:
    - /var/run/docker.sock:/var/run/docker.sock
```

2. Enable the shell tool in `configs/client.docker.toml`:

```toml
[tools.shell]
enabled = true
docker_image = "ubuntu:24.04"
network = false
memory_limit = "256m"
cpu_limit = "0.5"
timeout = "30s"
whitelist = ["df -h", "free -h", "uptime", "uname -a", "docker ps"]
```

3. Restart: `docker compose restart murmur-client`

### File Operations

Mount host directories into the client container and add them to the allowlist:

1. Add a volume mount in `docker-compose.yml`:

```yaml
murmur-client:
  volumes:
    - /home/user/documents:/data/files:ro
```

2. Enable in `configs/client.docker.toml`:

```toml
[tools.file_ops]
enabled = true
allowed_paths = ["/data/files"]
```

### Git Operations

Same pattern -- mount repos and allowlist them:

```yaml
# docker-compose.yml
murmur-client:
  volumes:
    - /home/user/projects/myapp:/data/repos/myapp:ro
```

```toml
# client.docker.toml
[tools.git]
enabled = true
allowed_repos = ["/data/repos/myapp"]
```

### Web Search (Brave API)

Store the API key in the vault, then enable:

```bash
docker compose run --rm -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server vault set brave-search-key --db /data/vault.db --value "BSA..."
```

```toml
# client.docker.toml
[tools.web_search]
enabled = true
api_key = "vault:brave-search-key"
```

### SearXNG Search (self-hosted, private)

A privacy-friendly alternative to Brave Search. Queries a self-hosted SearXNG instance that aggregates results from multiple search engines.

```bash
# Start with the search profile
docker compose --profile search up -d
```

```toml
# server.docker.toml
[tools.searxng]
enabled = true
url = "http://searxng:8080"
max_results = 10
```

Supports filtering by categories (`general`, `images`, `news`, `videos`, `it`, `science`), time range (`day`, `week`, `month`, `year`), and language.

### Image Generation (ComfyUI)

Point at a running ComfyUI instance:

```toml
# client.docker.toml
[tools.image_gen]
enabled = true
comfyui_host = "http://your-gpu-machine:8188"
output_dir = "/data/images"
checkpoint_name = "sd_xl_base_1.0.safetensors"
```

### Code Execution (Piston)

Execute code in 60+ languages via a self-hosted Piston engine. Sandboxed via Isolate.

```toml
# server.docker.toml or client.docker.toml
[tools.code_exec]
enabled = true
piston_url = "http://piston:2000"
default_language = "python"
# run_timeout = 30000          # ms, omit to use Piston's server-side default
# run_memory_limit = 256000000 # bytes, omit to use Piston's server-side default
```

The Piston container is included in the default Docker Compose setup. Languages are installed on first start via `scripts/piston-setup.sh`.

---

## IRC Channel Management

The `irc_manage` tool gives the LLM full control over IRC channels. It can join/part channels, send messages, set topics, read cross-channel history, and perform moderation actions.

### Actions

| Action | Description |
|--------|-------------|
| `join` | Join a channel (persists auto-join for reconnect) |
| `part` | Leave a channel (clears auto-join) |
| `send` | Send a message to a joined channel |
| `topic` | Set the channel topic |
| `list_channels` | List all currently joined channels |
| `read_history` | Read conversation history from another channel |
| `summarize_channel` | Get messages + total count for context |
| `kick` | Kick a user (requires chanop) |
| `ban` / `unban` | Set/remove channel bans |
| `op` / `deop` | Grant/remove channel operator |
| `voice` / `devoice` | Grant/remove voice |

### IRC Operator Support

When `oper_user` and `oper_password` are configured, the bot authenticates as an IRC server operator on connect. This enables:

- **Auto chanop**: Privileged operations (topic, kick, ban, mode changes) automatically acquire +o via `SAMODE` before executing.
- **Topic sync**: Channel topics are automatically updated to show the active LLM model when the provider changes.

```toml
[irc]
oper_user = "admin"
oper_password = "vault:irc-oper-password"
```

This only works on IRC servers where the bot has OPER credentials (e.g., the bundled Ergo server).

---

## Custom Tools

The LLM can create its own tools at runtime. Custom tools are persisted in SQLite and survive restarts.

### Meta-tools

| Tool | Description |
|------|-------------|
| `tool_create` | Create a new custom tool with name, description, parameters, and backend |
| `tool_list` | List all custom tools with status |
| `tool_delete` | Permanently delete a custom tool |
| `tool_enable` | Re-enable a disabled tool |
| `tool_disable` | Disable a tool without deleting it |

### Backends

| Backend | Description |
|---------|-------------|
| `shell` | Run a command in a Docker container (same sandboxing as the shell tool) |
| `http` | Make an HTTP request with argument substitution in URL, body, and headers |
| `code_exec` | Execute code via Piston with argument substitution |
| `pipeline` | Chain multiple tool calls sequentially (output of each step available as `{{_output}}` in the next) |

Arguments are substituted via `{{key}}` placeholders. Shell backends use single-quote escaping to prevent injection.

### Example

Ask the LLM: *"Create a tool called `weather` that fetches the weather for a city using wttr.in"*

The LLM will use `tool_create` to build it with an HTTP backend, and then the tool is immediately available for use.

---

## Config Management Tool

The LLM can read and modify the server's TOML configuration at runtime via the `config_manage` tool.

```toml
[tools.config_manage]
enabled = true
config_path = "/etc/murmur/server.toml"
```

**Actions**: `read`, `read_section`, `set`, `list_sections`

Sensitive keys (`security.*`, `vault.*`, `irc.password`, `api.api_key`, `llm.providers.*.api_key`) are protected -- the LLM cannot read or modify them. Vault references are masked in output.

---

## Running Clients on Other Machines

The Docker Compose setup runs one client locally. To add clients on other machines:

1. Build or copy the `murmur` binary to the remote machine:

```bash
# Build from source
make build
scp bin/murmur user@remote:~/

# Or cross-compile
GOOS=linux GOARCH=amd64 go build -o murmur-linux ./cmd/murmur
```

2. Create `~/.murmur/client.toml` on the remote machine:

```toml
[client]
id = "vps-nyc"                    # unique name
hostname = "nyc-1"
autonomy = "approve"              # require approval for tool calls

[irc]
server = "your-irc-server.com"    # must be reachable from this machine
port = 6667
tls = false
nick = "murmur-nyc"
bus_channel = "#murmur-bus"

[heartbeat]
interval = "30s"

[tools.systeminfo]
enabled = true

[tools.shell]
enabled = true
docker_image = "ubuntu:24.04"
whitelist = ["df -h", "free -h", "apt list --upgradable"]
```

3. Start it: `murmur client`

The server discovers the client automatically when it connects to IRC.

---

## Connecting to an Existing IRC Network

Murmur works with any IRC server -- you don't have to use the bundled Ergo. Point the server and clients at your existing IRC network:

1. Edit `configs/server.docker.toml`:

```toml
[irc]
server = "irc.libera.chat"    # or your own server
port = 6697
tls = true
nick = "murmur"
nickserv_password = "your-nickserv-pass"
max_line_len = 512             # standard IRC; use 8192 for Ergo
```

2. Edit `configs/client.docker.toml` with the same IRC server and `max_line_len`.

3. Remove or stop the `ircd` service: `docker compose up -d murmur-server murmur-client`

Make sure the bus channel (`#murmur-bus`) is private on the network -- set it to invite-only (+i) and keyed (+k).

**Note on `max_line_len`**: Standard IRC uses 512-byte lines. Ergo supports up to 8192. This setting controls how the bus protocol chunks large messages. Larger values mean fewer IRC messages for big tool responses. Both server and client must use the same value.

---

## IRC Commands

These are handled directly, no LLM involved:

| Command | Description |
|---------|-------------|
| `!status` | Server uptime, connected clients, active model, message count |
| `!clients` | List connected clients with hostname, tools, and last heartbeat |
| `!tools` | List all available tools across all clients |
| `!model [name]` | Show or switch LLM provider (per-channel) |
| `!history [n]` | Show last N messages (default 10, max 100) |
| `!forget` | Clear conversation history for this channel |
| `!flush` | Drop queued messages and clear history (use during floods) |
| `!notes` | Manage persistent notes (`get`, `set`, `delete`, `search`) |
| `!approve` | Approve the pending tool call |
| `!deny` | Deny the pending tool call |
| `!pending` | List pending tool call approvals |
| `!tasks` | List scheduled tasks |
| `!task` | Manage tasks (`add`, `remove`, `enable`, `disable`) |
| `!help` | Show all commands |

Everything else you type goes to the LLM agent loop.

## Tools Reference

Tools are capabilities provided by clients or the server. The server discovers client tools dynamically when they connect.

| Tool | Description | Runs on | Needs |
|------|-------------|---------|-------|
| `system_info` | Uptime, disk, memory, CPU, OS info, docker ps, apt updates, systemctl | Client | Nothing extra |
| `shell` | Run commands in Docker containers (sandboxed) | Server/Client | Docker socket |
| `code_exec` | Execute code in 60+ languages via Piston | Server/Client | Piston instance |
| `mail_read` | Read emails from Thunderbird mbox storage | Client | Thunderbird profile |
| `mail_send` | Send emails via SMTP | Client | SMTP server |
| `web_search` | Search the web via Brave Search API | Server/Client | API key |
| `searxng_search` | Search via self-hosted SearXNG | Server | SearXNG instance |
| `git_ops` | Read-only git: log, diff, status, branch, show | Client | Git repos |
| `rss_read` | Fetch and parse RSS/Atom feeds | Server/Client | Nothing extra |
| `dns_check` | DNS lookup, SSL cert inspection, whois expiry | Server/Client | Nothing extra |
| `image_gen` | Generate images via ComfyUI (Stable Diffusion) | Client | ComfyUI instance |
| `file_ops` | Read, list, search, stat files | Client | Mounted directories |
| `http_request` | Make outbound HTTP requests with SSRF protection | Server | Nothing extra |
| `irc_manage` | Join/part channels, send messages, set topics, kick/ban/op, read history | Server | Nothing extra |
| `config_manage` | Read/write server TOML config at runtime | Server | Nothing extra |
| `note_*` | Persistent key-value notes | Server | Nothing extra |
| `tool_create/list/delete/enable/disable` | Create and manage custom tools at runtime | Server | Nothing extra |

## Autonomy Levels

Each client declares how tool calls are handled:

| Level | Behavior |
|-------|----------|
| `report` | Tool calls are **blocked**. Client is read-only. |
| `approve` | Tool calls **wait for user approval** via `!approve`/`!deny`. |
| `auto` | Tool calls **execute immediately**. |

Set in the client config:

```toml
[client]
autonomy = "approve"
```

## Flood Protection

The server implements two-layer flood protection to prevent abuse:

- **Per-nick rate limiting**: Each nick is allowed 3 messages per 10-second window. Exceeding this triggers a 30-second cooldown where all messages from that nick are silently dropped.
- **Per-channel bounded queue**: Each channel has a queue of 5 messages processed sequentially. When the queue is full, new messages are dropped.

Commands (`!status`, `!flush`, etc.) bypass flood protection so they always work. Use `!flush` to drain queued messages and clear history during a flood.

## Tool Failure Circuit Breaker

When a tool fails 2 times consecutively during a single agent loop, it is automatically removed from the available tools list. The LLM receives a message telling it the tool is unavailable and to respond with what it has. This prevents infinite retry loops (e.g., a misconfigured Piston endpoint). A successful call resets the counter.

## Client-Side Cron

Scheduled jobs that run on clients without LLM involvement. Zero token cost. Results sent to the server only when conditions are met.

```toml
# In client.docker.toml (or client.toml)

[[cron]]
name = "disk-check"
schedule = "0 */6 * * *"          # every 6 hours (5-field cron, UTC)
command = "df -h"
tool = "shell"
notify = true
notify_only_on_change = true       # only notify when output changes

[[cron]]
name = "blog-health"
schedule = "*/5 * * * *"
command = "curl -sf https://myblog.com -o /dev/null"
tool = "shell"
notify = true
notify_only_on_error = true        # silent unless it fails
```

## Server-Side Scheduled Tasks

LLM-driven recurring tasks. The agent decides what tools to use.

```
!task add 0 8 * * 1-5 Check my email and summarize anything important
!task add 0 9 * * 1 Check for package updates on all connected clients
!tasks
!task remove 3
```

## Conversation Memory

Conversation history is stored in SQLite per channel.

```toml
[memory]
max_history = 100
summary_model = "ollama"       # use a cheap model for summarization (optional)
summary_threshold = 80         # summarize at this message count (default: 80% of max_history)
cross_channel_context = 10     # recent messages from other channels in system prompt (-1 to disable)
```

### Summarization

When history exceeds `summary_threshold`, the older half of messages is summarized using the configured `summary_model` (can be a cheaper/faster provider to minimize cost). The summary is stored separately and prepended to future conversations. Original messages are deleted. This keeps context windows manageable while preserving important information.

### Cross-Channel Context

The LLM receives recent messages from up to 5 other joined channels as part of its system prompt. This gives the agent awareness of activity happening elsewhere -- for example, news posted to `#news` can be referenced from `#murmur`. Set `cross_channel_context = -1` to disable.

`!forget` clears all history and summaries for the current channel.

## Secrets Vault

Secrets are encrypted at rest with AES-256-GCM (Argon2id key derivation). Config files reference vault entries with the `vault:` prefix.

```bash
# Store a secret
docker compose run --rm -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server vault set my-secret --db /data/vault.db --value "secret-value"

# List secrets
docker compose run --rm -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server vault list --db /data/vault.db

# Use in config
# api_key = "vault:my-secret"
```

## Bus Protocol

Server and clients communicate via JSON messages on `#murmur-bus`. Features:

- Client registration and heartbeat monitoring
- Tool request/response routing
- Multi-part message chunking for payloads exceeding the IRC line limit
- Optional HMAC-SHA256 authentication via shared `bus_key`
- Configurable line length (`max_line_len`) -- 512 for standard IRC, up to 8192 for Ergo

Set `bus_key` in both server and client configs when shell or code execution tools are enabled.

## Project Structure

```
murmur/
├── cmd/murmur/main.go              # CLI entry point
├── internal/
│   ├── server/                      # Agent loop, memory, scheduler, commands, flood protection,
│   │                                # custom tools, channel settings, REST API
│   ├── client/                      # Tool dispatch, client-side cron, REST API
│   ├── tools/                       # All tool implementations
│   ├── api/                         # Shared REST API helpers (JSON, auth, middleware)
│   ├── bus/                         # IRC bus protocol (chunking, HMAC, multi-part)
│   ├── irc/                         # IRC connection management (OPER, SAMODE, chanop)
│   ├── llm/                         # LLM provider interface (OpenAI-compatible)
│   ├── config/                      # TOML config loading and validation
│   ├── db/                          # SQLite + migrations
│   └── vault/                       # Encrypted secrets store
├── configs/
│   ├── server.docker.toml.example   # Server config template for Docker Compose
│   ├── client.docker.toml.example   # Client config template for Docker Compose
│   ├── server.toml.example          # Server config reference (bare metal)
│   ├── client.toml.example          # Client config reference (bare metal)
│   ├── system_prompt.md             # Default system prompt
│   └── ergo.yaml                    # Ergo IRC server config
├── scripts/
│   └── setup.sh                     # First-time setup script
├── Makefile
├── Dockerfile
├── docker-compose.yml
└── .env.example
```

## Building from Source

```bash
make build                # bin/murmur for current platform
make build-all            # cross-compile for linux/darwin/windows
make test                 # run all tests
make lint                 # run golangci-lint
```

Run without Docker:

```bash
murmur server --config ~/.murmur/server.toml
murmur client --config ~/.murmur/client.toml
murmur send "hello"                              # one-shot message
murmur status                                    # query server status
murmur vault set my-key                          # manage secrets
```

## REST API

Both the server and clients expose an optional REST API for external integrations. Events sent via the API are injected into the agent loop -- the LLM sees them and can respond.

### Enabling the API

Add to `server.docker.toml`:

```toml
[api]
enabled = true
listen = "0.0.0.0:8080"       # use 127.0.0.1:8080 outside Docker
api_key = "vault:api-key"      # or a plaintext key
event_retention_days = 30
```

Add to `client.docker.toml`:

```toml
[api]
enabled = true
listen = "0.0.0.0:8081"
api_key = "vault:api-key"
```

Store the API key in the vault:

```bash
docker compose run --rm -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server vault set api-key --db /data/vault.db --value "your-secret-key"
```

### Server Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST /api/v1/events` | Inject an event into the agent loop | Returns 202 Accepted |
| `GET /api/v1/events` | Query stored events (with pagination) | `?limit=50&source=&after_id=` |
| `GET /api/v1/status` | Server uptime, client count, LLM provider | |
| `GET /api/v1/clients` | List connected clients with tools | |
| `GET /api/v1/health` | Health check | Returns 200 OK |

### Client Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST /api/v1/events` | Forward an event to the server via IRC bus | Returns 202 or 503 if IRC is down |
| `GET /api/v1/status` | Client uptime, tools, cron jobs | |
| `GET /api/v1/health` | Health check | Returns 200 OK |

### Sending Events

```bash
# Send an event to the server (direct)
curl -X POST http://localhost:8080/api/v1/events \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{
    "source": "github",
    "event_type": "push",
    "summary": "3 new commits pushed to main by alice",
    "data": "commit abc123: fix login bug\ncommit def456: update deps",
    "event_id": "gh-push-12345"
  }'

# Send an event via a client (forwarded over IRC bus)
curl -X POST http://localhost:8081/api/v1/events \
  -H "Authorization: Bearer your-secret-key" \
  -H "Content-Type: application/json" \
  -d '{"source": "cron", "event_type": "alert", "summary": "disk usage above 90%"}'

# Check server health
curl http://localhost:8080/api/v1/health

# Query recent events
curl -H "Authorization: Bearer your-secret-key" \
  "http://localhost:8080/api/v1/events?limit=10&source=github"
```

Events have an optional `event_id` field for idempotency -- duplicate event IDs are silently ignored.

### Docker Port Mapping

The default Docker Compose config maps:
- Server API: `${MURMUR_API_PORT:-8080}:8080`
- Client API: `${MURMUR_CLIENT_API_PORT:-8081}:8081`

Override with environment variables or `.env`:

```bash
MURMUR_API_PORT=9090 MURMUR_CLIENT_API_PORT=9091 docker compose up -d
```

## HTTP Request Tool

The `http_request` tool lets the LLM agent make outbound HTTP requests with built-in SSRF protection. It runs server-side.

### Configuration

```toml
# server.docker.toml
[tools.http]
enabled = true
timeout = "30s"
max_response_bytes = 1048576          # 1 MB
# allowed_domains = ["api.example.com", "*.trusted.org"]
block_private_ips = true              # default: true
```

### SSRF Protection

- **URL scheme validation** -- only `http://` and `https://` are allowed
- **Method allowlist** -- only GET, POST, PUT, PATCH, DELETE, HEAD
- **Domain allowlist** -- optional glob patterns (`*.example.com` matches one subdomain level)
- **Private IP blocking** -- blocks requests to 10/8, 172.16/12, 192.168/16, 127/8, 169.254/16, 100.64/10, IPv6 link-local/unique-local, and IPv4-mapped IPv6 addresses
- **DNS rebinding prevention** -- IPs are validated at dial time, not at URL parse time
- **Redirect blocking** -- redirects are returned as-is (302 with Location header), not followed

## Security Notes

- **Bus channel** -- set `#murmur-bus` to invite-only on your IRC server. Enable `bus_key` for HMAC authentication.
- **Shell tool** -- runs inside Docker with `--cap-drop=ALL`, `--security-opt=no-new-privileges`, `--read-only`, `--network=none`. Use whitelists.
- **Code execution** -- Piston sandboxes code via Isolate with configurable memory and timeout limits.
- **Custom tools** -- shell backends use single-quote escaping to prevent injection. Pipeline backends prevent nesting.
- **File/git tools** -- only access explicitly allowlisted paths. Symlinks that escape the allowlist are rejected.
- **Config management** -- sensitive keys (vault, security, passwords, API keys) are protected by a deny-list.
- **Vault** -- AES-256-GCM encryption, Argon2id key derivation. Never stored in plaintext.
- **Allowed users** -- set `security.allowed_users` to restrict who can talk to the bot.
- **Autonomy levels** -- use `approve` for clients with dangerous tools.
- **Flood protection** -- per-nick rate limiting (3 msgs/10s) and per-channel bounded queues (5 deep) prevent abuse.
- **Tool circuit breaker** -- tools that fail repeatedly are automatically disabled for the current request.
- **REST API** -- bind to `127.0.0.1` outside Docker. Use API keys. The `http_request` tool blocks private IPs by default to prevent SSRF.
- **IRC operator** -- OPER credentials support `vault:` prefix. Input validation prevents IRC command injection.

## License

MIT
