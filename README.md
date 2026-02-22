<p align="center">
  <img src="https://murmur.dreamfast.solutions/murmurlogo.webp" alt="Murmur" width="300">
</p>

<p align="center">
  A distributed personal AI agent that lives on IRC. One server runs the brain (LLM agent loop), multiple clients on different machines provide tools (shell, email, file access, image generation, etc.). Everything coordinates over standard IRC channels.
</p>

The name comes from a "murmuration" -- the coordinated movement of a flock of starlings. Each client is a bird in the flock.

```
         You (IRC client)     Web Dashboard (:8082)     curl / webhooks
              |                      |                        |
     Private IRC Server        Murmur Server           REST API (:8080)
              |                      |
       Murmur Server          <-- LLM agent loop, memory, scheduler,
              |                   permissions, dashboard, REST API
       #murmur-bus (IRC)
              |
    +---------+---------+
    |         |         |
  laptop    vps1      gpu-rig   <-- Murmur clients (tool providers)
  mail,     shell,    image_gen,
  files     dns,rss   comfyui
```

## How It Compares

There are several personal AI assistant projects that let you run an LLM agent with tool access. They all solve the same core problem -- give an AI the ability to act on your behalf -- but take very different approaches to architecture and security.

Murmur's approach is to use IRC as the message bus and physically separate the LLM brain from tool execution. The server never touches your filesystem or runs shell commands -- clients on remote machines do that, each with their own autonomy level and tool whitelist. IRC handles message routing, presence, authentication, and access control, so Murmur doesn't need to reinvent any of that.

| | Murmur | OpenClaw | IronClaw | NanoClaw |
|---|---|---|---|---|
| **Language** | Go | TypeScript | Rust | Python |
| **Architecture** | Client-server over IRC (server = LLM brain, clients = tool providers on separate machines) | Client-server (Gateway + companion app nodes over WebSocket) | Monolithic binary with layered modules | Single process |
| **Communication bus** | IRC (battle-tested protocol with built-in auth, TLS, channel ACLs, flood protection) | WebSocket control plane (`ws://127.0.0.1:18789`) | Internal module calls | Internal function calls |
| **Chat channels** | IRC (any client) + web dashboard + REST API + DMs | 14+ (WhatsApp, Telegram, Slack, Discord, Signal, iMessage, Teams, etc.) | Claims 20 (Slack, Discord, Telegram, IRC, Matrix, Teams, etc.) | Telegram + CLI + web dashboard |
| **Execution isolation** | Docker containers with `--network=none`, `--cap-drop=ALL`, `--read-only` + physical network separation (clients on different machines) | Docker sandbox for non-main sessions; main session has full host access | Docker rootless / Bubblewrap / Native sandbox with seccomp profiles | Python-level filtering (workspace-only file access, blocked shell patterns) |
| **Approval flow** | Per-user + per-channel + per-client autonomy levels (`report`/`approve`/`auto`) — layered permissions with NickServ identity verification | DM pairing for unknown senders | RBAC with deny-precedence | Session budgets and rate limiting |
| **Command restriction** | Per-client whitelists, config key deny-lists, HMAC-authenticated bus | Per-channel allowlists, SSRF protection | 45+ blocked command patterns, static analysis, typosquatting scanner | Blocked dangerous commands, workspace-only file access |
| **Network isolation** | Tool clients can run on air-gapped machines; Docker `--network=none` for shell; bus channel invite-only + HMAC | Gateway on localhost; non-main sessions sandboxed | Sandbox profiles (Minimal to Unrestricted) | No open ports (Telegram polling, localhost dashboard) |
| **Secrets management** | Encrypted vault (AES-256-GCM + Argon2id), `vault:` config references | Unknown | AES-256-GCM encrypted memory | Config file |
| **Multi-LLM** | Any OpenAI-compatible endpoint (OpenRouter, Ollama, Kimi, GLM, etc.) — switch per-channel at runtime | Anthropic + OpenAI (via OAuth subscriptions) | 25+ providers claimed | OpenRouter, DeepSeek, Anthropic, OpenAI, Ollama |
| **Custom tools** | LLM creates tools at runtime (shell, HTTP, code, pipeline backends) — persisted in SQLite | Unknown | Skill system with Ed25519 signature verification | Python decorator skills |
| **Distributed** | Yes — multiple clients across machines, each with independent tools and security policies | Yes — companion app nodes connect to gateway | No | No |
| **Hot reload** | SIGHUP / `!reload` / auto-reload on config write | Unknown | Unknown | Unknown |
| **Scheduled tasks** | Server-side cron (LLM-driven) + client-side cron (zero token cost, change detection) | Unknown | DAG-based workflow engine | Cron-like scheduled jobs |
| **Memory** | SQLite per-channel + auto-summarization + cross-channel context | Unknown | Encrypted memory store | SQLite |
| **Self-hosted** | Fully (IRC server, LLM, all tools — zero external dependencies with Ollama) | Yes (npm/Docker/Nix) | Yes (single static binary) | Yes (pip/Docker) |
| **License** | MIT | MIT | Apache-2.0 | MIT |

**Key architectural differences:**

- **IRC does the heavy lifting**: Murmur doesn't implement its own WebSocket server, authentication system, or message routing. IRC provides all of that out of the box -- NickServ for auth, channel modes (+i, +k) for access control, TLS for encryption, and decades of battle-tested flood protection. The bus protocol is just JSON over PRIVMSGs.
- **Not monolithic — truly distributed**: Most AI assistants run as a single process on a single machine. Murmur is a client-server system where any number of clients can connect from different machines, each providing their own set of tools. A laptop provides mail and file access, a VPS provides shell and DNS, a GPU rig provides image generation -- all coordinated over IRC. Add or remove clients without touching the server.
- **Physical separation by design**: In OpenClaw, the gateway and main session run on the same machine with full host access. In Murmur, the server (LLM brain) and clients (tool providers) are separate processes that can run on entirely different machines, networks, or even air-gapped systems. The server literally cannot access the filesystem -- it has to ask a client to do it over IRC.
- **Security is structural, not layered**: IronClaw adds 13 security layers on top of a monolithic binary. Murmur's security comes from the architecture itself -- the LLM server has no shell access, no filesystem access, and no network access to the machines running tools. Each client independently declares its autonomy level and tool whitelist. You can't bypass what doesn't exist on the server.
- **The LLM never sees your secrets**: API keys, passwords, and tokens are stored in an encrypted vault (AES-256-GCM + Argon2id). Config files reference them with `vault:` prefixes, which are resolved at startup — the LLM only ever sees the resolved service connections, never the keys themselves. The `config_manage` tool has a hard-coded deny-list that blocks the LLM from reading or writing any vault, security, or API key config paths. Even if the LLM tries to read the config file directly, sensitive values are masked.
- **No vendor lock-in**: Works with any OpenAI-compatible LLM endpoint. Switch providers per-channel at runtime. Run fully offline with Ollama. No OAuth subscriptions required.

---

## Getting Started

Everything runs in Docker. You need **Docker** and **an LLM API key** (OpenRouter, OpenAI, or any OpenAI-compatible endpoint).

### Quick install (curl-pipe)

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

### The setup wizard

The interactive setup wizard handles everything -- building containers, configuring the IRC server, creating your admin account, storing secrets in the encrypted vault, and optionally enabling the web dashboard:

```bash
./scripts/setup.sh              # full server setup (interactive)
./scripts/setup.sh client       # standalone client setup (remote machine)
./scripts/setup.sh --dry-run    # preview without writing files
```

The wizard walks you through 8 steps:

1. **Installation mode** -- Docker (recommended) or bare metal
2. **Vault passphrase** -- encrypts your secrets at rest
3. **LLM provider** -- OpenRouter, OpenAI, Ollama, or custom endpoint + optional RAG memory search
4. **Admin account** -- your IRC nick and NickServ password (auto-registered on the bundled Ergo server)
5. **IRC server password** -- optional, protects the IRC server from unauthorized connections
6. **Web dashboard** -- optional browser-based chat interface (port 8082)
7. **Search** -- Brave Search API key (optional; SearXNG available as a tool)
8. **Server tools** -- shell, code execution (Piston), browser automation, OpenCode, SearXNG, systeminfo, and more + optional local client setup

The wizard generates all config files, builds Docker images, registers your admin account on the IRC server, stores secrets in the vault, starts all services, and runs a health check. You can opt out of the local client to run a server-only deployment and add remote clients later.

Use `--dry-run` to preview what the wizard would do without making changes.

### 3. Connect and talk

Point your IRC client at `localhost:6667` and join `#murmur`:

```
/server localhost 6667
/join #murmur
hey murmur, what's the uptime on this machine?
```

That's it. You're talking to your agent.

You can also DM the bot directly -- private messages work the same as channel messages, with separate conversation history per user. Or open the web dashboard at `http://localhost:8082` if you enabled it during setup.

### 4. Check the logs

```bash
docker compose logs -f murmur-server
docker compose logs -f murmur-client
```

### Setting up remote clients

The setup wizard supports configuring standalone clients on remote machines:

```bash
# On the remote machine — curl-pipe install
curl -fsSL https://raw.githubusercontent.com/dreamfast/murmur-ai/main/scripts/install.sh | bash -s -- client

# Or if already cloned
./scripts/setup.sh client
```

This walks you through client identity, IRC connection details, tool selection, and vault setup. See [Running Clients on Other Machines](#running-clients-on-other-machines) for manual setup.

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

Config changes can be applied without restarting -- see [Hot Config Reload](#hot-config-reload).

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

## Private Messages (DMs)

The bot responds to private messages the same way it responds in channels. Each user gets their own conversation history, separate from any channel.

- DM the bot's nick directly in your IRC client
- Commands (`!status`, `!model`, `!help`, etc.) work in DMs
- Cross-channel context is excluded from DMs to keep private conversations focused
- The system prompt tells the LLM it's in a private conversation with the user's nick

No configuration needed -- DM support is always enabled.

---

## Reminders

The LLM can set one-time reminders that fire at a specific time. Just ask naturally:

```
remind me to check the deployment in 2 hours
set a reminder for 2026-03-01T09:00:00Z to review the quarterly report
```

The LLM uses the `reminder_add` tool with either absolute (ISO 8601) or relative (`+2h`, `+30m`, `+1d`) times. When the reminder fires, the message is delivered to the channel where it was set.

Reminders are stored in SQLite alongside recurring scheduled tasks. After firing, they auto-disable and are cleaned up after 30 days.

Manage reminders with the same task commands:

```
!tasks                        # list all tasks and reminders
!task remove 5                # remove a reminder by ID
```

---

## Multi-User Permissions

Murmur supports fine-grained per-user and per-channel permissions via a separate `permissions.toml` file. This is optional -- without it, all users in `security.allowed_users` have full access.

### How it works

Permissions are layered: each user has permissions, each channel has permissions, and the effective permissions are the **intersection** (most restrictive wins). NickServ identity verification prevents nick spoofing.

```
User permissions  ∩  Channel permissions  =  Effective permissions
(what the user    ∩  (what the channel     =  (what this user can
 is allowed)         allows)                   do in this channel)
```

### Configuration

Create `permissions.toml` (default location: `<data_dir>/permissions.toml`):

```toml
# Default permissions for users not explicitly listed.
[users.default]
role = "user"
tools = ["*"]                    # allowed tools (* = all, prefix_* = glob)
deny_tools = []                  # deny list overrides allowed tools
autonomy = "approve"             # report | approve | auto
# allowed_models = ["*"]
# max_messages_per_hour = 60     # -1 = unlimited

# Admin user — full access, no rate limit.
[users.alice]
role = "admin"
tools = ["*"]
autonomy = "auto"
max_messages_per_hour = -1
api_key = "webhook-key-for-alice"  # optional per-user API key

# Restricted user — limited tools, report-only.
[users.guest]
role = "user"
tools = ["note_*", "rss_read"]
deny_tools = ["shell", "code_exec"]
autonomy = "report"
max_messages_per_hour = 10

# Channel restrictions — intersected with user permissions.
[channels."#public"]
tools = ["note_*", "rss_read", "web_search"]
deny_tools = ["shell"]
autonomy = "approve"
```

Reference it in `server.toml`:

```toml
[security]
permissions_file = "~/.murmur/permissions.toml"
require_nickserv = true           # verify nick identity via NickServ WHOIS
```

### Permission resolution

- **Tools**: `(user_tools ∩ channel_tools) - user_deny - channel_deny`
- **Autonomy**: most restrictive wins (`report` > `approve` > `auto`)
- **Models**: `(user_models ∩ channel_models) - user_deny_models`
- **Glob patterns**: `"*"` matches all, `"note_*"` matches prefix
- **Admin role**: admins get access to `!user`, `!channel` commands and the `permissions_manage` LLM tool

### Admin commands

Admins can manage permissions at runtime via IRC commands:

```
!user list                        # list all configured users
!user info alice                  # show alice's permissions
!user add bob admin               # add bob as admin
!user remove guest                # remove guest
!user bob tools shell,dns_check   # set bob's allowed tools
!user bob deny code_exec          # add to bob's deny list
!user bob autonomy auto           # set bob's autonomy level
!user bob ratelimit 30            # set bob's rate limit

!channel list                     # list configured channels
!channel info #public             # show channel permissions
!channel #public tools note_*,rss # set channel's allowed tools
!channel #public deny shell       # add to channel's deny list
!channel #public autonomy approve # set channel's autonomy
```

Changes are written atomically to `permissions.toml` and take effect immediately (auto-reload).

### Natural language management

The `permissions_manage` LLM tool lets admins manage permissions conversationally:

```
give bob access to the shell and dns tools
restrict #public to only note and rss tools
what permissions does alice have?
set the rate limit for guest to 5 messages per hour
```

The tool is only visible to admin users (defense-in-depth: the handler also validates admin status).

### Per-user API keys

Each user can have an `api_key` for webhook events. When an event arrives with a per-user key, it's processed with that user's permissions:

```toml
[users.alice]
api_key = "alices-webhook-key"
```

```bash
curl -X POST http://localhost:8080/api/v1/events \
  -H "Authorization: Bearer alices-webhook-key" \
  -d '{"source": "github", "summary": "new PR opened"}'
```

### NickServ verification

When `require_nickserv = true` (default when permissions are configured), the server verifies nick identity via WHOIS before processing messages. Unidentified nicks receive a message asking them to identify with NickServ. Results are cached for 5 minutes with singleflight deduplication.

---

## Web Dashboard

A browser-based chat interface for Murmur. Each browser session creates its own IRC connection, so you appear as your own nick in the channel.

### Features

- **Real-time chat** -- WebSocket bridge to IRC with message rendering (IRC colors, markdown, code blocks)
- **Server overview** -- status cards showing uptime, provider, connected clients, and tools
- **Admin panel** -- client list with tools and autonomy badges, quick action buttons
- **Tool call cards** -- collapsible cards for tool approval requests with approve/deny buttons
- **Command autocomplete** -- type `!` to see available commands with descriptions
- **Unread badges** -- notification count on the chat tab when messages arrive while viewing other pages
- **Mobile responsive** -- slide-out sidebar, hamburger menu, optimized layouts for phone and tablet
- **Dark theme** -- Hack monospace font, IRC-inspired color palette
- **HMAC-signed requests** -- every API call is signed with a per-session key (HMAC-SHA256, 30s timestamp window)

### Enabling the dashboard

Add to `server.toml`:

```toml
[dashboard]
enabled = true
listen = "127.0.0.1:8082"        # bind address (use 0.0.0.0 in Docker)
session_timeout = "24h"           # how long sessions stay valid
server_password = "vault:irc-server-password"  # auto-sent for dashboard users
```

Or enable it during the setup wizard (step 6).

### Authentication

Login with your IRC nick and NickServ password. The dashboard creates an IRC connection on your behalf and authenticates via NickServ IDENTIFY. The IRC server password (if configured) is sent automatically -- dashboard users don't need to know it.

Sessions use HttpOnly/Secure/SameSite=Strict cookies. Each session gets a unique signing key (32 bytes, crypto/rand) returned at login. All subsequent requests must include HMAC-SHA256 signatures with a fresh timestamp.

### Architecture

```
Browser  →  WebSocket  →  Dashboard Handler  →  IRC Bridge  →  Ergo IRC Server
                              (Go HTTP)           (girc)          (same as CLI)
```

Each browser tab gets its own IRC connection. The bridge relays PRIVMSG, JOIN, PART, TOPIC, MODE, and NAMES between WebSocket and IRC. The dashboard is served from the same `murmur server` binary -- no separate process needed.

### Building the frontend

The Vue.js frontend is built during `make build` and embedded into the Go binary via `//go:embed`. No separate frontend server is needed in production.

```bash
make build              # builds frontend (pnpm) + Go binary
make build-go-only      # skip frontend build (uses existing web/dist/)
```

Development:

```bash
cd web/frontend
pnpm install
pnpm dev                # Vite dev server with hot reload
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

When the `config_manage` tool writes a value, the config is automatically reloaded -- no restart needed.

---

## Hot Config Reload

Config changes can be applied without restarting the server. Three ways to trigger a reload:

1. **SIGHUP signal**: `kill -HUP <pid>` or `docker kill -s HUP murmur-server`
2. **IRC command**: `!reload`
3. **Automatic**: The `config_manage` tool triggers a reload after every successful write

### What reloads

| Setting | Reloadable |
|---------|-----------|
| LLM providers (add, remove, change models/keys) | Yes |
| Default provider | Yes |
| System prompt | Yes |
| `verbose` mode | Yes |
| `max_history` / `summary_threshold` | Yes |
| `cross_channel_context` | Yes |
| `approval_timeout` | Yes |
| `allowed_users` | Yes |
| Permissions (`permissions.toml`) | Yes |
| Debug config (`[debug]` section) | Yes |
| Debug channel (enable/disable) | Yes |
| IRC connection (server, port, nick, TLS) | No -- restart required |
| Database path | No -- restart required |
| Vault config | No -- restart required |
| API listen address | No -- restart required |
| Bus key | No -- restart required |
| Tool configs (shell, code_exec, etc.) | No -- restart required |

### Example

```bash
# Change the default model without restarting
docker exec murmur-server kill -HUP 1

# Or from IRC
!reload
```

---

## Debug IRC Channel

A dedicated IRC channel that receives live structured log output from the server. Useful for real-time debugging without tailing container logs.

```toml
[debug]
enabled = true
channel = "#murmur-debug"
log_level = "debug"              # minimum level: debug, info, warn, error
log_tool_calls = true            # tool call routing and results
log_llm_requests = true          # LLM API calls with provider, tokens, latency
log_bus_protocol = false         # bus protocol messages
log_permissions = true           # permission checks and denials
```

The server joins the debug channel on startup and forwards `slog` output as formatted IRC messages. Control it at runtime:

```
!debug              # toggle on/off
!debug on           # enable
!debug off          # disable
!debug level debug  # set minimum log level (debug, info, warn, error)
```

Log messages are batched (up to 5 per send, every 500ms) and use a drop-newest buffer (capacity 100) to avoid flooding IRC when log volume is high.

The granular toggles let you focus on specific subsystems. For example, enable only `log_permissions` to debug access control issues without the noise of every LLM call.

**Backward compatibility**: The old `server.debug_channel` field still works but is deprecated. If set without a `[debug]` section, it's automatically migrated.

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
| `!tasks` | List scheduled tasks and reminders |
| `!task` | Manage tasks (`add`, `remove`, `enable`, `disable`) |
| `!user` | Manage user permissions -- `list`, `info`, `add`, `remove`, field setters (admin only) |
| `!channel` | Manage channel permissions -- `list`, `info`, field setters (admin only) |
| `!debug` | Toggle debug IRC channel logging |
| `!reload` | Reload configuration from disk |
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
| `browser` | Headless browser automation (navigate, screenshot, extract, click) | Server | Playwright container |
| `opencode` | AI coding agent for code generation and editing | Server | OpenCode container |
| `irc_manage` | Join/part channels, send messages, set topics, kick/ban/op, read history | Server | Nothing extra |
| `config_manage` | Read/write server TOML config at runtime (auto-reloads) | Server | Nothing extra |
| `reminder_add` | Set one-time reminders with absolute or relative times | Server | Nothing extra |
| `note_*` | Persistent key-value notes | Server | Nothing extra |
| `permissions_manage` | Natural language permission management (admin only) | Server | permissions.toml |
| `tool_create/list/delete/enable/disable` | Create and manage custom tools at runtime | Server | Nothing extra |

## Autonomy Levels

Autonomy controls how tool calls are handled. It can be set at three levels -- the most restrictive wins:

| Level | Behavior |
|-------|----------|
| `report` | Tool calls are **blocked**. Read-only. |
| `approve` | Tool calls **wait for user approval** via `!approve`/`!deny`. |
| `auto` | Tool calls **execute immediately**. |

**Per-client** (in `client.toml`):

```toml
[client]
autonomy = "approve"
```

**Per-user** (in `permissions.toml`):

```toml
[users.guest]
autonomy = "report"             # this user can never execute tools
```

**Per-channel** (in `permissions.toml`):

```toml
[channels."#public"]
autonomy = "approve"            # all tool calls in this channel need approval
```

The effective autonomy is the most restrictive of all three: if the client is `auto`, the user is `auto`, but the channel is `approve`, the result is `approve`.

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

Conversation history is stored in SQLite per channel (and per user for DMs).

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

Cross-channel context is excluded from DMs to keep private conversations focused and avoid leaking channel activity.

### RAG Memory Search

When enabled, Murmur indexes conversation summaries and ingested files into an FTS5 full-text search index. The LLM can search past conversations and documents to recall information from weeks or months ago.

```toml
[memory.rag]
enabled = true
auto_ingest_summaries = true       # index conversation summaries automatically
# files = ["~/notes/project.md"]   # optional files to index on startup
```

RAG uses SQLite FTS5 -- no external vector database or embedding API required. Enable it during the setup wizard (Step 3) or add the config section manually.

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
│   ├── server/                      # Agent loop, memory, scheduler, commands, permissions,
│   │                                # custom tools, channel settings, REST API, hot reload
│   ├── client/                      # Tool dispatch, client-side cron, REST API
│   ├── dashboard/                   # Web dashboard (HTTP handler, WebSocket-IRC bridge, sessions)
│   ├── tools/                       # All tool implementations
│   ├── api/                         # Shared REST API helpers (JSON, auth, middleware)
│   ├── bus/                         # IRC bus protocol (chunking, HMAC, multi-part)
│   ├── irc/                         # IRC connection management (OPER, SAMODE, chanop, debug log handler)
│   ├── llm/                         # LLM provider interface (OpenAI-compatible)
│   ├── config/                      # TOML config loading and validation
│   ├── db/                          # SQLite + migrations
│   └── vault/                       # Encrypted secrets store
├── web/
│   ├── frontend/                    # Vue.js 3 dashboard (Vite, Tailwind CSS 4, pnpm)
│   ├── dist/                        # Built frontend (embedded into Go binary)
│   └── embed.go                     # go:embed directive for static files
├── configs/
│   ├── server.docker.toml.example   # Server config template for Docker Compose
│   ├── client.docker.toml.example   # Client config template for Docker Compose
│   ├── server.toml.example          # Server config reference (bare metal)
│   ├── client.toml.example          # Client config reference (bare metal)
│   ├── permissions.toml.example     # User/channel permissions reference
│   ├── system_prompt.md             # Default system prompt
│   └── ergo.yaml                    # Ergo IRC server config
├── scripts/
│   ├── setup.sh                     # Interactive setup wizard (server + client modes)
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
make build-all            # cross-compile for linux/darwin/windows
make test                 # run all tests
make lint                 # run golangci-lint
```

The `make build` target runs `pnpm install && pnpm build` in `web/frontend/` first, then compiles the Go binary with the built frontend embedded. If pnpm is not installed, the frontend build is skipped gracefully and the existing `web/dist/` is used.

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

### Per-user API keys

When multi-user permissions are configured, each user can have their own API key. Events sent with a per-user key are processed with that user's permissions (tool filtering, model restrictions, rate limits):

```toml
# permissions.toml
[users.alice]
api_key = "alices-webhook-key"
```

If no per-user key matches, the global `api.api_key` is used (admin-equivalent). All key comparisons use constant-time algorithms.

### Server Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST /api/v1/events` | Inject an event into the agent loop | Returns 202 Accepted |
| `GET /api/v1/events` | Query stored events (with pagination) | `?limit=50&source=&after_id=` |
| `GET /api/v1/status` | Server uptime, client count, LLM provider | |
| `GET /api/v1/clients` | List connected clients with tools | |
| `GET /api/v1/health` | Health check | Returns 200 OK |

### Dashboard Endpoints (port 8082)

| Method | Path | Description |
|--------|------|-------------|
| `POST /dashboard/login` | Authenticate with nick + NickServ password | Returns session cookie + signing key |
| `POST /dashboard/logout` | End session (requires signature) | |
| `GET /dashboard/status` | Server status (requires session + signature) | |
| `GET /ws` | WebSocket IRC bridge (requires signed query params) | |
| `GET /*` | Embedded Vue.js SPA | |

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

- **Multi-user permissions** -- per-user and per-channel tool/model/autonomy restrictions via `permissions.toml`. Effective permissions are the intersection (most restrictive wins). NickServ identity verification prevents nick spoofing. Admin commands and the `permissions_manage` tool have defense-in-depth admin checks.
- **Per-user rate limiting** -- configurable `max_messages_per_hour` per user (sliding window). Admins can set `-1` for unlimited. Stacks with the existing per-nick flood guard.
- **Bus channel** -- set `#murmur-bus` to invite-only on your IRC server. Enable `bus_key` for HMAC authentication.
- **Shell tool** -- runs inside Docker with `--cap-drop=ALL`, `--security-opt=no-new-privileges`, `--read-only`, `--network=none`. Use whitelists.
- **Code execution** -- Piston sandboxes code via Isolate with configurable memory and timeout limits.
- **Custom tools** -- shell backends use single-quote escaping to prevent injection. Pipeline backends prevent nesting.
- **File/git tools** -- only access explicitly allowlisted paths. Symlinks that escape the allowlist are rejected.
- **Config management** -- sensitive keys (vault, security, passwords, API keys, user/channel permissions) are protected by a deny-list. Writes auto-reload.
- **Vault** -- AES-256-GCM encryption, Argon2id key derivation. Never stored in plaintext.
- **Allowed users** -- set `security.allowed_users` to restrict who can talk to the bot. Reloadable without restart.
- **Autonomy levels** -- use `approve` for clients with dangerous tools. Per-user and per-channel autonomy overrides are intersected with client autonomy.
- **Flood protection** -- per-nick rate limiting (3 msgs/10s) and per-channel bounded queues (5 deep) prevent abuse.
- **Tool circuit breaker** -- tools that fail repeatedly are automatically disabled for the current request.
- **REST API** -- bind to `127.0.0.1` outside Docker. Use API keys. Per-user API keys scope webhook events to user permissions. The `http_request` tool blocks private IPs by default to prevent SSRF.
- **Web dashboard** -- HMAC-SHA256 signed requests with per-session keys (32 bytes, crypto/rand). HttpOnly/Secure/SameSite=Strict cookies. Login rate limiting (5/min/IP). WebSocket auth via signed query params. Security headers (X-Frame-Options DENY, CSP, X-Content-Type-Options). Path traversal protection on static file serving.
- **Setup wizard** -- IRC protocol injection prevention (`validate_irc_input` rejects CR/LF). Secure `.env` file permissions (umask 077). TOML escaping for all user input. Ergo config modifications go to a generated file (never modifies tracked config).
- **IRC operator** -- OPER credentials support `vault:` prefix. Input validation prevents IRC command injection.
- **DMs** -- private message conversations are isolated per user. Cross-channel context is excluded to prevent information leakage.
- **Hot reload** -- only safe fields are reloadable. IRC connection, database, vault, and tool configs require a restart.

## License

MIT
