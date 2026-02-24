<p align="center">
  <img src="https://murmur.dreamfast.solutions/murmurlogo.webp" alt="Murmur" width="300">
</p>

<p align="center">
  A personal AI agent that lives on IRC. The server runs the brain — an LLM agent loop with built-in memory, scheduling, notes, permissions, and 20+ tools. Optional clients on other machines extend its reach with shell access, email, file operations, image generation, and more. Everything coordinates over standard IRC.
</p>

The name comes from a "murmuration" — the coordinated movement of a flock of starlings. Each client is a bird in the flock.

```
       You (IRC)       Web Dashboard       curl / webhooks       murmur send
            |                |                    |                    |
     +----- Private IRC Server ------+      REST API (:8080)          |
     |                               |            |                   |
     |        Murmur Server          +------------+-------------------+
     |   LLM agent loop, memory,     |
     |   scheduler, notes, RAG,      |
     |   permissions, custom tools,  |
     |   docker, IRC management,     |
     |   shell*, code_exec*, HTTP,   |
     |   browser*, dashboard, API    |
     |                               |
     |        #murmur-bus (IRC)      |
     |               |               |
     |     +---------+---------+     |
     |     |         |         |     |
     |   laptop    vps1      gpu     |
     |   mail,     shell,    image,  |
     |   files     dns,rss   comfyui |
     +-------------------------------+

     * = also runs on clients
     Clients are optional — the server works standalone.
```

## Table of Contents

- [Design Philosophy](#design-philosophy)
- [Getting Started](#getting-started)
- [LLM Configuration](#llm-configuration)
- [Tools Reference](#tools-reference)
- [Tool Configuration](#tool-configuration)
- [Custom Tools](#custom-tools)
- [Docker Container Management](#docker-container-management)
- [Scheduling and Reminders](#scheduling-and-reminders)
- [Conversation Memory](#conversation-memory)
- [Multi-User Permissions](#multi-user-permissions)
- [Web Dashboard](#web-dashboard)
- [IRC](#irc)
- [REST API](#rest-api)
- [Configuration](#configuration)
- [Security](#security)
- [Running Clients on Other Machines](#running-clients-on-other-machines)
- [Bus Protocol](#bus-protocol)
- [CLI Reference](#cli-reference)
- [Project Structure](#project-structure)
- [Building from Source](#building-from-source)

---

## Design Philosophy

- **IRC does the heavy lifting.** Authentication (NickServ), message routing, TLS encryption, channel access control (+i, +k), flood protection — IRC provides all of this out of the box. The bus protocol is just JSON over PRIVMSGs. No custom WebSocket server, no reinvented auth.

- **The server is self-sufficient.** The server runs 20+ built-in tools (notes, scheduling, RAG memory, permissions, Docker management, IRC control, HTTP requests, custom tool creation) and can also run shared tools like shell, code execution, and browser automation directly. Clients are optional — they extend the server's reach to remote machines, but the server works fine on its own.

- **Distributed when you need it.** Any number of clients can connect from different machines, each providing their own tools. A laptop provides mail and file access, a VPS provides shell and DNS, a GPU rig provides image generation — all coordinated over IRC. Add or remove clients without touching the server.

- **Security is structural, not bolted on.** When clients are used, the LLM brain and tool execution are physically separated across machines and networks. Each client independently controls its autonomy level and tool whitelist. The encrypted vault (AES-256-GCM + Argon2id) stores secrets that are resolved at startup — the LLM never sees API keys or passwords. The config management tool has a hard-coded deny-list blocking access to sensitive paths.

- **No vendor lock-in.** Works with any OpenAI-compatible endpoint — OpenRouter, OpenAI, Ollama, Kimi, GLM, or any custom API. Switch providers per-channel at runtime. Run fully offline with Ollama. No subscriptions or OAuth required.

---

## Getting Started

Everything runs in Docker. You need **Docker** and **an LLM API key** (OpenRouter, OpenAI, or any OpenAI-compatible endpoint).

### Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/dreamfast/murmur-ai/main/scripts/install.sh | bash
```

This clones the repo to `~/murmur` and runs the setup wizard. Re-running pulls the latest version. Customize with environment variables:

```bash
MURMUR_INSTALL_DIR=/opt/murmur MURMUR_BRANCH=develop curl -fsSL ... | bash
```

### Manual install

```bash
git clone https://github.com/dreamfast/murmur-ai.git
cd murmur-ai
./scripts/setup.sh
```

### Setup wizard

The interactive wizard handles building containers, configuring IRC, creating your admin account, storing secrets in the vault, and optionally enabling the web dashboard:

```bash
./scripts/setup.sh              # full server setup (interactive)
./scripts/setup.sh client       # standalone client setup (remote machine)
./scripts/setup.sh --dry-run    # preview without writing files
```

The wizard walks you through:

1. **Installation mode** — Docker (recommended) or bare metal
2. **Vault passphrase** — encrypts your secrets at rest
3. **LLM provider** — OpenRouter, OpenAI, Ollama, or custom endpoint + optional RAG memory search
4. **Admin account** — your IRC nick and NickServ password
5. **IRC server password** — optional, protects the IRC server
6. **Web dashboard** — optional browser-based chat interface (port 8082)
7. **Search** — Brave Search API key (optional; SearXNG also available)
8. **Server tools** — shell, code execution, browser, Docker management, and more + optional local client

Use `--dry-run` to preview what the wizard would do without making changes.

### Connect and talk

Point your IRC client at `localhost:6667` and join `#murmur`:

```
/server localhost 6667
/join #murmur
hey murmur, what's the uptime on this machine?
```

You can also DM the bot directly — private messages work the same as channel messages, with separate conversation history per user. Or open the web dashboard at `http://localhost:8082` if you enabled it.

### Managing the stack

The `murmur.sh` helper script wraps Docker Compose (or bare-metal processes) for day-to-day management:

```bash
./murmur.sh start                 # start all services
./murmur.sh stop                  # stop all services
./murmur.sh restart               # restart everything
./murmur.sh status                # show service status
./murmur.sh logs                  # tail all logs
./murmur.sh logs murmur-server    # tail specific service
./murmur.sh reload                # hot-reload server config (SIGHUP)
./murmur.sh send "hello"          # send a message to the agent
./murmur.sh vault set my-key      # store a secret
./murmur.sh update                # pull latest, rebuild, restart
./murmur.sh shell                 # open a shell in the server container
./murmur.sh piston-setup          # install Piston language runtimes
```

The script auto-detects whether Docker is available. If not, it falls back to managing bare-metal processes via the compiled binary. It also resolves Docker Compose profiles (browser, search, opencode) from your `.env` so `start` brings up everything you've configured.

See [CLI Reference](#cli-reference) for the full command list.

---

## LLM Configuration

Configure multiple providers and switch between them at runtime:

```toml
[llm]
default = "openrouter"

[llm.providers.openrouter]
api_base = "https://openrouter.ai/api/v1"
api_key = "vault:llm-api-key"
model = "anthropic/claude-sonnet-4-5"

[llm.providers.kimi]
api_base = "https://api.kimi.com/coding/v1"
api_key = "vault:kimi-api-key"
model = "kimi-for-coding"
reasoning = true

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

Each channel can use a different provider. The setting persists across restarts. Config changes can be applied without restarting — see [Hot Config Reload](#hot-config-reload).

---

## Tools Reference

Tools are capabilities provided by the server and optionally by connected clients. The server discovers client tools automatically when they connect via IRC. Server tools always take priority — if both the server and a client register a tool with the same name, the server version is used.

### Server-Only Tools

These require server-internal state (database, scheduler, permissions, IRC connection) and cannot run on clients.

| Tool | Description |
|------|-------------|
| `note_set` / `note_get` / `note_list` / `note_delete` / `note_search` | Persistent key-value notes stored in SQLite |
| `memory_search` / `memory_ingest` | RAG memory — full-text search over conversation summaries and ingested files (SQLite FTS5, no external vector DB) |
| `task_add` / `task_list` / `task_remove` / `task_enable` / `task_disable` | Manage server-side scheduled tasks (LLM-driven cron) |
| `reminder_add` | Set one-time reminders with absolute (ISO 8601) or relative (`+2h`, `+30m`) times |
| `irc_manage` | Join/part channels, send messages, set topics, kick/ban/op, read cross-channel history |
| `http_request` | Outbound HTTP requests with SSRF protection (private IP blocking, DNS rebinding prevention) |
| `config_manage` | Read/write server TOML config at runtime (auto-reloads, sensitive keys protected) |
| `permissions_manage` | Natural language permission management (admin only) |
| `docker_manage` | Full Docker container lifecycle — create, exec, logs, stop, start, remove, list, inspect, build |
| `tool_create` / `tool_list` / `tool_delete` / `tool_enable` / `tool_disable` | Create and manage custom tools at runtime (persisted in SQLite) |

### Shared Tools (Server & Client)

These can run on either the server or clients, depending on where they're configured. Enable them in `server.toml` or `client.toml` under `[tools.*]`.

| Tool | Description | Needs |
|------|-------------|-------|
| `system_info` | Uptime, disk, memory, CPU, OS info, docker ps, apt updates, systemctl | Nothing extra |
| `shell` | Run commands in ephemeral Docker containers (sandboxed) | Docker socket |
| `code_exec` | Execute code in 60+ languages via Piston (sandboxed via Isolate) | Piston instance |
| `web_search` | Search the web via Brave Search API | API key |
| `searxng_search` | Search via self-hosted SearXNG (privacy-friendly) | SearXNG instance |
| `browser` | Headless browser automation — navigate, screenshot, click, type, scroll, evaluate JS, extract content. Session persistence across calls. SSRF-protected. | Playwright container |
| `opencode` | AI coding agent — send tasks, manage sessions, SSE-based completion detection. Multi-session support with Basic Auth. | OpenCode container |
| `mail_read` | Read emails from Thunderbird mbox storage | Thunderbird profile |
| `mail_send` | Send emails via SMTP | SMTP server |
| `git_ops` | Read-only git operations: log, diff, status, branch, show | Git repos |
| `rss_read` | Fetch and parse RSS/Atom feeds | Nothing extra |
| `dns_check` | DNS lookup, SSL cert inspection, whois expiry | Nothing extra |
| `image_gen` | Generate images via ComfyUI (Stable Diffusion) | ComfyUI instance |
| `file_ops` | Read, list, search, stat files (allowlisted paths only) | Mounted directories |

---

## Tool Configuration

Enable tools by adding their config section with `enabled = true`. Server tools go in `server.toml`, client tools in `client.toml`. Restart after changes.

### Shell (Docker-sandboxed)

```toml
[tools.shell]
enabled = true
docker_image = "ubuntu:24.04"
network = false                    # --network=none
memory_limit = "256m"
cpu_limit = "0.5"
timeout = "30s"
whitelist = ["df -h", "free -h", "uptime", "uname -a"]
```

Requires Docker socket access. In Docker Compose, mount `/var/run/docker.sock`.

### Code Execution (Piston)

```toml
[tools.code_exec]
enabled = true
piston_url = "http://piston:2000"
default_language = "python"
```

Executes code in 60+ languages. Sandboxed via Isolate. The Piston container is included in the default Docker Compose setup.

### Web Search (Brave API)

```toml
[tools.web_search]
enabled = true
api_key = "vault:brave-search-key"
```

### SearXNG Search (self-hosted)

```toml
[tools.searxng]
enabled = true
url = "http://searxng:8080"
max_results = 10
```

Start with `docker compose --profile search up -d`. Supports filtering by category, time range, and language.

### Browser Automation

```toml
[tools.browser]
enabled = true
url = "http://playwright:3000"
max_content_length = 8000
```

Actions: navigate, screenshot, click, type, scroll, evaluate (JavaScript), content extraction. Supports session persistence across multiple calls. SSRF-protected — blocks private IPs and dangerous URL schemes.

### OpenCode (AI Coding Agent)

```toml
[tools.opencode]
enabled = true
url = "http://opencode:3000"
timeout = "5m"
```

Actions: chat (send tasks to the coding agent), list_sessions, get_session. Uses SSE for completion detection.

### Image Generation (ComfyUI)

```toml
[tools.image_gen]
enabled = true
comfyui_host = "http://your-gpu-machine:8188"
output_dir = "/data/images"
checkpoint_name = "sd_xl_base_1.0.safetensors"
```

### File Operations

```toml
[tools.file_ops]
enabled = true
allowed_paths = ["/data/files"]
```

Mount host directories into the container and add them to the allowlist. Symlinks that escape the allowlist are rejected.

### Git Operations

```toml
[tools.git]
enabled = true
allowed_repos = ["/data/repos/myapp"]
```

Read-only: log, diff, status, branch, show. Mount repos and allowlist them.

### HTTP Requests

```toml
[tools.http]
enabled = true
timeout = "30s"
max_response_bytes = 1048576
block_private_ips = true
```

SSRF protection: URL scheme validation, method allowlist, optional domain allowlist, private IP blocking, DNS rebinding prevention, redirect blocking.

---

## Custom Tools

The LLM can create its own tools at runtime. Custom tools are persisted in SQLite and survive restarts.

### Meta-tools

| Tool | Description |
|------|-------------|
| `tool_create` | Create a new custom tool with name, description, parameters, and backend |
| `tool_list` | List all custom tools with status |
| `tool_delete` | Permanently delete a custom tool |
| `tool_enable` / `tool_disable` | Enable or disable a tool without deleting it |

### Backends

| Backend | Description |
|---------|-------------|
| `shell` | Run a command in a Docker container (same sandboxing as the shell tool) |
| `http` | Make an HTTP request with `{{key}}` argument substitution in URL, body, and headers |
| `code_exec` | Execute code via Piston with argument substitution |
| `pipeline` | Chain multiple tool calls sequentially (`{{_output}}` carries results between steps) |

Shell backends use single-quote escaping to prevent injection. Pipeline backends prevent nesting.

**Example:** Ask the LLM *"Create a tool called `weather` that fetches the weather for a city using wttr.in"* — it will use `tool_create` with an HTTP backend, and the tool is immediately available.

---

## Docker Container Management

The `docker_manage` tool gives the LLM full Docker container lifecycle control. Containers are tracked in SQLite and reconciled with actual Docker state on server startup.

```toml
[tools.docker_manage]
enabled = true
max_containers = 10
memory_limit = "512m"
cpu_limit = "1.0"
pids_limit = 256
network = ""                       # Docker network (empty = default bridge)
allow_build = false                # Dockerfile builds (admin only)
allow_network = true               # containers can access the network
read_only = false                  # read-only root filesystem
allowed_images = []                # glob patterns (empty = all allowed)
timeout = "5m"
```

### Actions

| Action | Description |
|--------|-------------|
| `create` | Create a container with security hardening (see below) |
| `exec` | Execute a command inside a running container |
| `logs` | Retrieve container logs |
| `stop` / `start` | Stop or start a container |
| `remove` | Remove a container |
| `list` | List all tracked containers |
| `inspect` | Inspect container details |
| `build` | Build a Docker image from a Dockerfile (admin only) |

### Security

- Containers are auto-created with `--cap-drop=ALL`, `--security-opt=no-new-privileges`, PID limits, memory limits, and CPU limits
- Dangerous Docker flags are blocked: `--privileged`, `--volume`, `--mount`, `--cap-add`, `--device`, `--pid`, `--userns`, `--network=host`, `--ipc=host`
- Image allowlisting via glob patterns
- Ownership tracking — non-admin users can only operate on their own containers
- All container names are auto-prefixed with `murmur-`

---

## Scheduling and Reminders

### Server-side scheduled tasks

LLM-driven recurring tasks. The agent decides what tools to use.

```
!task add 0 8 * * 1-5 Check my email and summarize anything important
!task add 0 9 * * 1 Check for package updates on all connected clients
!tasks                            # list all tasks
!task remove 3                    # remove by ID
```

### Reminders

One-time reminders that fire at a specific time. Ask naturally:

```
remind me to check the deployment in 2 hours
set a reminder for 2026-03-01T09:00:00Z to review the quarterly report
```

The LLM uses `reminder_add` with absolute (ISO 8601) or relative (`+2h`, `+30m`, `+1d`) times. Reminders are stored in SQLite, auto-disable after firing, and are cleaned up after 30 days.

### Client-side cron

Scheduled jobs that run on clients without LLM involvement. Zero token cost. Results sent to the server only when conditions are met.

```toml
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

---

## Conversation Memory

Conversation history is stored in SQLite per channel (and per user for DMs).

```toml
[memory]
max_history = 100
summary_model = "ollama"           # use a cheap model for summarization (optional)
summary_threshold = 80             # summarize at this message count (default: 80% of max_history)
cross_channel_context = 10         # recent messages from other channels (-1 to disable)
```

### Summarization

When history exceeds `summary_threshold`, the older half is summarized using the configured `summary_model` (can be a cheaper provider to minimize cost). The summary is stored separately and prepended to future conversations. Original messages are deleted. This keeps context windows manageable while preserving important information.

### Cross-channel context

The LLM receives recent messages from up to 5 other joined channels as part of its system prompt — for example, news posted to `#news` can be referenced from `#murmur`. Cross-channel context is excluded from DMs to prevent information leakage. Set `cross_channel_context = -1` to disable.

### RAG memory search

When enabled, Murmur indexes conversation summaries and ingested files into an FTS5 full-text search index. The LLM can search past conversations and documents to recall information from weeks or months ago. No external vector database or embedding API required — it's all SQLite FTS5.

```toml
[memory.rag]
enabled = true
auto_ingest_summaries = true       # index conversation summaries automatically
# files = ["~/notes/project.md"]   # optional files to index on startup
```

`!forget` clears all history and summaries for the current channel.

---

## Multi-User Permissions

Fine-grained per-user and per-channel permissions via `permissions.toml`. Optional — without it, all users in `security.allowed_users` have full access.

Permissions are layered: each user has permissions, each channel has permissions, and the effective permissions are the **intersection** (most restrictive wins). NickServ identity verification prevents nick spoofing.

```
User permissions  ∩  Channel permissions  =  Effective permissions
```

### Configuration

```toml
# permissions.toml

[users.default]
role = "user"
tools = ["*"]                      # * = all, prefix_* = glob
deny_tools = []
autonomy = "approve"               # report | approve | auto

[users.alice]
role = "admin"
tools = ["*"]
autonomy = "auto"
max_messages_per_hour = -1         # unlimited
api_key = "webhook-key-for-alice"  # optional per-user API key

[users.guest]
role = "user"
tools = ["note_*", "rss_read"]
deny_tools = ["shell", "code_exec"]
autonomy = "report"
max_messages_per_hour = 10

[channels."#public"]
tools = ["note_*", "rss_read", "web_search"]
deny_tools = ["shell"]
autonomy = "approve"
```

Reference it in `server.toml`:

```toml
[security]
permissions_file = "~/.murmur/permissions.toml"
require_nickserv = true
```

### Permission resolution

- **Tools**: `(user_tools ∩ channel_tools) - user_deny - channel_deny`
- **Autonomy**: most restrictive wins (`report` > `approve` > `auto`)
- **Models**: `(user_models ∩ channel_models) - user_deny_models`
- **Admin role**: admins get `!user`, `!channel` commands and the `permissions_manage` LLM tool

### Admin commands

```
!user list                        # list all configured users
!user info alice                  # show alice's permissions
!user add bob admin               # add bob as admin
!user remove guest                # remove guest
!user bob tools shell,dns_check   # set allowed tools
!user bob deny code_exec          # add to deny list
!user bob autonomy auto           # set autonomy level
!user bob ratelimit 30            # set rate limit

!channel list                     # list configured channels
!channel info #public             # show channel permissions
!channel #public tools note_*,rss # set allowed tools
!channel #public deny shell       # add to deny list
!channel #public autonomy approve # set autonomy
```

Changes are written atomically and take effect immediately (auto-reload).

The `permissions_manage` LLM tool lets admins manage permissions conversationally: *"give bob access to the shell and dns tools"*, *"restrict #public to only note and rss tools"*.

---

## Web Dashboard

A browser-based chat interface. Each browser session creates its own IRC connection, so you appear as your own nick in the channel.

### Features

- **Real-time chat** — WebSocket bridge to IRC with message rendering (IRC colors, markdown, code blocks)
- **Server overview** — status cards showing uptime, provider, connected clients, and tools
- **Admin panel** — client list with tools and autonomy badges, quick action buttons
- **Tool call cards** — collapsible cards for tool approval requests with approve/deny buttons
- **Command autocomplete** — type `!` to see available commands
- **Mobile responsive** — slide-out sidebar, hamburger menu, optimized for phone and tablet
- **Dark theme** — Hack monospace font, IRC-inspired color palette

### Enabling

```toml
[dashboard]
enabled = true
listen = "127.0.0.1:8082"
session_timeout = "24h"
server_password = "vault:irc-server-password"
```

Or enable during the setup wizard (step 6).

### Authentication

Login with your IRC nick and NickServ password. The dashboard creates an IRC connection on your behalf. Sessions use HttpOnly/Secure/SameSite=Strict cookies. All requests are HMAC-SHA256 signed with per-session keys.

### Architecture

```
Browser  →  WebSocket  →  Dashboard Handler  →  IRC Bridge  →  Ergo IRC Server
                              (Go HTTP)           (girc)          (same as CLI)
```

Each browser tab gets its own IRC connection. The dashboard is served from the same `murmur server` binary — no separate process needed. The Vue.js frontend is built during `make build` and embedded into the Go binary via `//go:embed`.

---

## IRC

### Commands

These are handled directly — no LLM involved:

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
| `!approve` / `!deny` | Approve or deny the pending tool call |
| `!pending` | List pending tool call approvals |
| `!tasks` | List scheduled tasks and reminders |
| `!task` | Manage tasks (`add`, `remove`, `enable`, `disable`) |
| `!user` | Manage user permissions (admin only) |
| `!channel` | Manage channel permissions (admin only) |
| `!debug` | Toggle debug IRC channel logging |
| `!reload` | Reload configuration from disk |
| `!help` | Show all commands |

Everything else goes to the LLM agent loop.

### Channel management

The `irc_manage` tool gives the LLM control over IRC channels:

| Action | Description |
|--------|-------------|
| `join` / `part` | Join or leave a channel (persists auto-join) |
| `send` | Send a message to a joined channel |
| `topic` | Set the channel topic |
| `list_channels` | List all currently joined channels |
| `read_history` / `summarize_channel` | Read or summarize another channel's history |
| `kick` / `ban` / `unban` | Moderation (requires chanop) |
| `op` / `deop` / `voice` / `devoice` | Manage channel privileges |

### IRC operator support

When `oper_user` and `oper_password` are configured, the bot authenticates as an IRC server operator. This enables auto chanop via `SAMODE` for privileged operations and automatic topic sync when the LLM provider changes.

```toml
[irc]
oper_user = "admin"
oper_password = "vault:irc-oper-password"
```

### Private messages (DMs)

The bot responds to DMs the same way it responds in channels. Each user gets their own conversation history. Commands work in DMs. Cross-channel context is excluded to keep private conversations focused. No configuration needed.

### Debug channel

A dedicated IRC channel that receives live structured log output from the server.

```toml
[debug]
enabled = true
channel = "#murmur-debug"
log_level = "debug"                # debug, info, warn, error
log_tool_calls = true
log_llm_requests = true
log_bus_protocol = false
log_permissions = true
```

Control at runtime: `!debug on`, `!debug off`, `!debug level debug`. Log messages are batched and use a drop-newest buffer to avoid flooding.

### Connecting to an existing IRC network

Murmur works with any IRC server — you don't have to use the bundled Ergo:

```toml
[irc]
server = "irc.libera.chat"
port = 6697
tls = true
nick = "murmur"
nickserv_password = "your-nickserv-pass"
max_line_len = 512                 # standard IRC; use 8192 for Ergo
```

Set the same `max_line_len` on both server and client. Make sure `#murmur-bus` is private (+i, +k).

---

## REST API

Both the server and clients expose an optional REST API. Events sent via the API are injected into the agent loop — the LLM sees them and can respond. Events are processed in an ephemeral context (isolated from channel history) so they don't pollute ongoing conversations.

### Enabling

```toml
# server.toml
[api]
enabled = true
listen = "0.0.0.0:8080"
api_key = "vault:api-key"
event_retention_days = 30

# client.toml
[api]
enabled = true
listen = "0.0.0.0:8081"
api_key = "vault:api-key"
```

### Server endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST /api/v1/events` | Inject an event into the agent loop | Returns 202 |
| `GET /api/v1/events` | Query stored events | `?limit=50&source=&after_id=` |
| `GET /api/v1/status` | Server uptime, client count, LLM provider | |
| `GET /api/v1/clients` | List connected clients with tools | |
| `GET /api/v1/health` | Health check | Returns 200 |

### Client endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST /api/v1/events` | Forward an event to the server via IRC bus | Returns 202 or 503 |
| `GET /api/v1/status` | Client uptime, tools, cron jobs | |
| `GET /api/v1/health` | Health check | Returns 200 |

### Dashboard endpoints (port 8082)

| Method | Path | Description |
|--------|------|-------------|
| `POST /dashboard/login` | Authenticate with nick + NickServ password | |
| `POST /dashboard/logout` | End session | |
| `GET /dashboard/status` | Server status | |
| `GET /ws` | WebSocket IRC bridge | |

### Sending events

```bash
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
```

Events have an optional `event_id` for idempotency — duplicate IDs are silently ignored. Per-user API keys (from `permissions.toml`) scope webhook events to that user's permissions.

### Docker port mapping

Default Docker Compose ports: server API `8080`, client API `8081`. Override with environment variables:

```bash
MURMUR_API_PORT=9090 MURMUR_CLIENT_API_PORT=9091 docker compose up -d
```

---

## Configuration

### Hot config reload

Config changes can be applied without restarting. Three ways to trigger:

1. **SIGHUP signal**: `kill -HUP <pid>` or `docker kill -s HUP murmur-server`
2. **IRC command**: `!reload`
3. **Automatic**: The `config_manage` tool triggers a reload after every write

| Setting | Reloadable |
|---------|-----------|
| LLM providers (add, remove, change models/keys) | Yes |
| Default provider, system prompt | Yes |
| `verbose`, `max_history`, `summary_threshold` | Yes |
| `cross_channel_context`, `approval_timeout` | Yes |
| `allowed_users`, permissions (`permissions.toml`) | Yes |
| Debug config (`[debug]` section) | Yes |
| IRC connection, database, vault, API listen, bus key, tool configs | No — restart required |

### Config management tool

The LLM can read and modify the server's TOML config at runtime via `config_manage`:

```toml
[tools.config_manage]
enabled = true
config_path = "/etc/murmur/server.toml"
```

Actions: `read`, `read_section`, `set`, `list_sections`. Sensitive keys (`security.*`, `vault.*`, `irc.password`, `api.api_key`, `llm.providers.*.api_key`) are protected — the LLM cannot read or modify them.

### Secrets vault

Secrets are encrypted at rest with AES-256-GCM (Argon2id key derivation). Config files reference vault entries with the `vault:` prefix, which is resolved at startup.

```bash
# Store a secret
docker compose run --rm -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server vault set brave-search-key --db /data/vault.db --value "BSA..."

# List secrets
docker compose run --rm -e MURMUR_VAULT_PASS=mypassphrase \
  murmur-server vault list --db /data/vault.db
```

```toml
# Reference in config
api_key = "vault:brave-search-key"
```

---

## Security

### Execution isolation

- **Shell tool**: Docker containers with `--cap-drop=ALL`, `--security-opt=no-new-privileges`, `--read-only`, `--network=none`. Configurable whitelists.
- **Code execution**: Piston sandboxes code via Isolate with configurable memory and timeout limits.
- **Docker management**: `--cap-drop=ALL`, `--security-opt=no-new-privileges`, PID/memory/CPU limits. Dangerous flags blocked. Image allowlisting.
- **Custom tools**: Shell backends use single-quote escaping to prevent injection. Pipeline backends prevent nesting.

### Access control

- **Multi-user permissions**: Per-user and per-channel tool/model/autonomy restrictions. Effective permissions are the intersection (most restrictive wins).
- **NickServ verification**: When enabled, nick identity is verified via WHOIS before processing messages. Results cached for 5 minutes with singleflight deduplication.
- **Per-user rate limiting**: Configurable `max_messages_per_hour` per user (sliding window).
- **Autonomy levels**: `report` (read-only), `approve` (wait for user), `auto` (execute immediately). Set per-client, per-user, and per-channel — most restrictive wins.

### Network security

- **Bus channel**: Set `#murmur-bus` to invite-only. Enable `bus_key` for HMAC-SHA256 authentication.
- **HTTP requests**: SSRF protection — URL scheme validation, method allowlist, private IP blocking, DNS rebinding prevention, redirect blocking.
- **File/git tools**: Only access explicitly allowlisted paths. Symlinks that escape the allowlist are rejected.
- **Physical separation**: When using clients, the LLM brain and tool execution are on different machines/networks.

### Secrets

- **Vault**: AES-256-GCM encryption, Argon2id key derivation. Never stored in plaintext.
- **Config management**: Sensitive keys protected by a deny-list. Vault references masked in output.
- **LLM isolation**: The LLM never sees API keys, passwords, or tokens — only resolved service connections.

### Resilience

- **Flood protection**: Per-nick rate limiting (3 msgs/10s) and per-channel bounded queues (5 deep). Commands bypass flood protection. Use `!flush` during floods.
- **Tool circuit breaker**: Tools that fail 2 times consecutively during a single agent loop are automatically disabled. The LLM is told to respond with what it has.
- **Dashboard**: HMAC-SHA256 signed requests, per-session keys, login rate limiting (5/min/IP), security headers (X-Frame-Options DENY, CSP, X-Content-Type-Options).

---

## Running Clients on Other Machines

The setup wizard supports standalone client setup on remote machines:

```bash
# Curl-pipe install
curl -fsSL https://raw.githubusercontent.com/dreamfast/murmur-ai/main/scripts/install.sh | bash -s -- client

# Or if already cloned
./scripts/setup.sh client
```

### Manual setup

1. Build or copy the binary:

```bash
make build
scp bin/murmur user@remote:~/
```

2. Create `~/.murmur/client.toml`:

```toml
[client]
id = "vps-nyc"
hostname = "nyc-1"
autonomy = "approve"

[irc]
server = "your-irc-server.com"
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

3. Start: `murmur client`

The server discovers the client automatically when it connects to IRC.

---

## Bus Protocol

Server and clients communicate via JSON messages on `#murmur-bus`:

- **Message types**: register, deregister, heartbeat, tool_request, tool_response, cron_result, cron_add, cron_remove, cron_list, cron_list_response, event
- **Multi-part chunking**: Large payloads are split across multiple IRC messages and reassembled
- **HMAC-SHA256 authentication**: Optional via shared `bus_key` in both server and client configs
- **Configurable line length**: `max_line_len` — 512 for standard IRC, up to 8192 for Ergo. Both sides must match.

---

## CLI Reference

### murmur.sh (stack management)

The recommended way to manage Murmur. Auto-detects Docker vs bare-metal mode.

```bash
./murmur.sh start   [service...]   # start all services (or specific ones)
./murmur.sh stop    [service...]   # stop all services (or specific ones)
./murmur.sh restart [service...]   # restart all services (or specific ones)
./murmur.sh reload                 # hot-reload server config (SIGHUP)
./murmur.sh status                 # show service status
./murmur.sh logs    [service...]   # tail logs (all or specific services)
./murmur.sh build                  # build/rebuild Docker images or binary
./murmur.sh vault   <sub> [args]   # manage secrets (set/get/list/delete)
./murmur.sh send    "message"      # send a message to the agent
./murmur.sh update                 # pull latest code, rebuild, restart
./murmur.sh shell                  # open a shell in the server container (Docker only)
./murmur.sh piston-setup           # install Piston language runtimes (Docker only)
./murmur.sh help                   # show help
```

**Services:** `ircd`, `piston`, `murmur-server`, `murmur-client`, `browser`, `searxng`, `opencode`

**Environment variables:**
- `MURMUR_DIR` — project directory (default: script location)
- `MURMUR_VAULT_PASS` — vault passphrase (prompted if not set)
- `COMPOSE_PROFILES` — Docker Compose profiles, also read from `.env`

When no services are specified, `start` brings up all core services plus any profile-gated services (browser, searxng, opencode) configured in your `.env`.

### murmur binary

The compiled binary for direct usage or bare-metal deployments:

```bash
murmur server [--config path]     # start the server (default: ~/.murmur/server.toml)
murmur client [--config path]     # start a client (default: ~/.murmur/client.toml)
murmur send [--config path] "msg" # send a message, print the response, exit
murmur status [--config path]     # query server status, print it, exit
murmur vault set [key]            # store a secret (prompts for value)
murmur vault get <key>            # retrieve a secret
murmur vault list                 # list all secret keys
murmur vault delete <key>         # remove a secret
murmur version                    # print version
murmur help                       # show usage
```

**`send` and `status`** create a temporary IRC connection with a random nick (`murmur-cli-<hex>`), send the message, collect the response (3-second silence timeout), and disconnect. Useful for scripting and health checks.

**Vault** reads the passphrase from `MURMUR_VAULT_PASS` environment variable or prompts interactively. Use `--db path` to specify a non-default vault location (default: `~/.murmur/vault.db`).

---

## Project Structure

```
murmur/
├── cmd/murmur/main.go              # CLI entry point
├── internal/
│   ├── server/                      # Agent loop, memory, scheduler, commands, permissions,
│   │                                # custom tools, channel settings, REST API, hot reload,
│   │                                # docker management, notes, RAG, statistics
│   ├── client/                      # Tool dispatch, client-side cron, REST API
│   ├── dashboard/                   # Web dashboard (HTTP handler, WebSocket-IRC bridge, sessions)
│   ├── tools/                       # All tool implementations (shared between server and client)
│   ├── api/                         # Shared REST API helpers (JSON, auth, middleware)
│   ├── bus/                         # IRC bus protocol (chunking, HMAC, multi-part)
│   ├── irc/                         # IRC connection management (OPER, SAMODE, chanop, debug)
│   ├── llm/                         # LLM provider interface (OpenAI-compatible)
│   ├── config/                      # TOML config loading and validation
│   ├── db/                          # SQLite + migrations
│   └── vault/                       # Encrypted secrets store
├── web/
│   ├── frontend/                    # Vue.js 3 dashboard (Vite, Tailwind CSS 4, pnpm)
│   ├── dist/                        # Built frontend (embedded into Go binary)
│   └── embed.go                     # go:embed directive
├── configs/
│   ├── server.docker.toml.example   # Server config template (Docker)
│   ├── client.docker.toml.example   # Client config template (Docker)
│   ├── server.toml.example          # Server config reference (bare metal)
│   ├── client.toml.example          # Client config reference (bare metal)
│   ├── permissions.toml.example     # User/channel permissions reference
│   ├── system_prompt.md             # Default system prompt
│   └── ergo.yaml                    # Ergo IRC server config
├── scripts/
│   ├── setup.sh                     # Interactive setup wizard
│   └── install.sh                   # Curl-pipe bootstrap installer
├── Makefile
├── Dockerfile                       # Multi-stage: Node (pnpm) → Go → Alpine
├── docker-compose.yml
└── .env.example
```

## Building from Source

```bash
make build                # build frontend (pnpm) + Go binary → bin/murmur
make build-go-only        # skip frontend, use existing web/dist/
make build-all            # cross-compile for linux/darwin/windows (amd64 + arm64)
make test                 # run all tests
make lint                 # run golangci-lint
```

The `make build` target runs `pnpm install && pnpm build` in `web/frontend/` first, then compiles the Go binary with the built frontend embedded. If pnpm is not installed, the frontend build is skipped and the existing `web/dist/` is used.

Run without Docker:

```bash
murmur server --config ~/.murmur/server.toml
murmur client --config ~/.murmur/client.toml
```

## License

MIT
