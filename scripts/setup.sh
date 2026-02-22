#!/usr/bin/env bash
# Murmur interactive setup wizard.
# Sets up a complete Murmur deployment — server, client, IRC, vault, and tools.
#
# Usage:
#   ./scripts/setup.sh              # full server+client setup (interactive)
#   ./scripts/setup.sh client       # standalone client setup (remote machine)
#   ./scripts/setup.sh --dry-run    # show what would be done without writing
#   ./scripts/setup.sh --help       # show help
#
# Environment variables (override prompts):
#   MURMUR_VAULT_PASS   — vault passphrase
#   LLM_API_KEY         — LLM provider API key
#   MURMUR_ADMIN_NICK   — admin IRC nickname
#   MURMUR_ADMIN_PASS   — admin IRC password
#   MURMUR_MODE         — "docker" or "bare"

set -euo pipefail

# ─── Project paths (set before sourcing libs) ───────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly PROJECT_DIR

# ─── Source library modules ──────────────────────────────────────────────────
# Load order: common.sh first (defines constants/helpers), then others.

_LIB_DIR="$SCRIPT_DIR/lib"
# shellcheck source=lib/common.sh
source "$_LIB_DIR/common.sh"
# shellcheck source=lib/vault.sh
source "$_LIB_DIR/vault.sh"
# shellcheck source=lib/config.sh
source "$_LIB_DIR/config.sh"
# shellcheck source=lib/docker.sh
source "$_LIB_DIR/docker.sh"
# shellcheck source=lib/nickserv.sh
source "$_LIB_DIR/nickserv.sh"

# ─── Parse arguments ────────────────────────────────────────────────────────

DRY_RUN="false"
SHOW_HELP="false"
SETUP_MODE="server"

for arg in "$@"; do
	case "$arg" in
	--dry-run) DRY_RUN="true" ;;
	--help | -h) SHOW_HELP="true" ;;
	client) SETUP_MODE="client" ;;
	*)
		err "Unknown argument: $arg"
		exit 1
		;;
	esac
done

if [[ "$SHOW_HELP" == "true" ]]; then
	cat <<EOF
Murmur Setup Wizard

Usage:
  ./scripts/setup.sh              Full server + local client setup
  ./scripts/setup.sh client       Standalone client setup (for remote machines)
  ./scripts/setup.sh --dry-run    Preview without writing files
  ./scripts/setup.sh --help       Show this help

Environment overrides:
  MURMUR_VAULT_PASS    Vault passphrase
  LLM_API_KEY          LLM provider API key
  MURMUR_ADMIN_NICK    Admin IRC nickname
  MURMUR_ADMIN_PASS    Admin IRC password
  MURMUR_MODE          "docker" or "bare"
EOF
	exit 0
fi

# ─── Main ────────────────────────────────────────────────────────────────────

banner

if [[ "$DRY_RUN" == "true" ]]; then
	warn "Dry-run mode — no files will be written, no commands will be executed."
	echo ""
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Client Setup Mode
# ═══════════════════════════════════════════════════════════════════════════════

if [[ "$SETUP_MODE" == "client" ]]; then
	TOTAL_STEPS=4

	info "${BOLD}Standalone client setup${RESET}"
	info "This configures a Murmur client to connect to an existing server."
	echo ""

	# ─── Step 1: Client Identity ─────────────────────────────────────────────

	step 1 "Client Identity"
	info "Each client needs a unique ID and hostname."

	CLIENT_ID=""
	ask CLIENT_ID "Client ID (unique name for this machine)" "$(hostname -s 2>/dev/null || echo "my-client")"

	CLIENT_HOSTNAME=""
	ask CLIENT_HOSTNAME "Hostname" "$(hostname -s 2>/dev/null || echo "unknown")"

	CLIENT_AUTONOMY=""
	ask_choice CLIENT_AUTONOMY "Autonomy level" "auto|approve|report" "auto"

	success "Client: ${BOLD}$CLIENT_ID${RESET} ($CLIENT_HOSTNAME, $CLIENT_AUTONOMY)"

	# ─── Step 2: IRC Connection ──────────────────────────────────────────────

	step 2 "IRC Connection"
	info "Connect to the same IRC server your Murmur server uses."

	IRC_SERVER=""
	ask IRC_SERVER "IRC server address" "localhost"

	IRC_PORT=""
	ask IRC_PORT "IRC port" "6667"
	if ! [[ "$IRC_PORT" =~ ^[0-9]+$ ]] || ((IRC_PORT < 1 || IRC_PORT > 65535)); then
		err "IRC port must be a number between 1 and 65535."
		exit 1
	fi

	IRC_TLS="false"
	if ask_yesno "Use TLS?" "n"; then
		IRC_TLS="true"
	fi

	CLIENT_NICK=""
	ask CLIENT_NICK "Client IRC nick" "murmur-${CLIENT_ID}"
	validate_irc_input "$CLIENT_NICK" "Client IRC nick"

	IRC_PASSWORD=""
	if ask_yesno "IRC server password required?" "n"; then
		ask_secret IRC_PASSWORD "IRC server password"
		validate_irc_input "$IRC_PASSWORD" "IRC server password"
	fi

	BUS_KEY=""
	ask BUS_KEY "Bus key (from server setup)" ""
	if [[ -n "$BUS_KEY" ]]; then
		validate_irc_input "$BUS_KEY" "Bus key"
	else
		warn "No bus key set — strongly recommended when shell/code_exec tools are enabled."
	fi

	success "IRC: ${BOLD}$IRC_SERVER:$IRC_PORT${RESET} (TLS: $IRC_TLS)"

	# ─── Step 3: Tools ───────────────────────────────────────────────────────

	step 3 "Client Tools"
	info "Select which tools this client provides."

	CLIENT_TOOLS=""
	ask_multi CLIENT_TOOLS "Enable client-side tools" \
		"systeminfo|shell|code_exec|mail_read|mail_send|web_search|rss|dns|git|file_ops|image_gen|searxng" \
		"systeminfo|shell"

	# Parse selections
	CT_SYSTEMINFO="false"
	CT_SHELL="false"
	CT_CODE_EXEC="false"
	CT_MAIL_READ="false"
	CT_MAIL_SEND="false"
	CT_WEB_SEARCH="false"
	CT_RSS="false"
	CT_DNS="false"
	CT_GIT="false"
	CT_FILE_OPS="false"
	CT_IMAGE_GEN="false"
	CT_SEARXNG="false"

	IFS='|' read -ra CT_SELECTED <<<"$CLIENT_TOOLS"
	for tool in "${CT_SELECTED[@]}"; do
		case "$tool" in
		systeminfo) CT_SYSTEMINFO="true" ;;
		shell) CT_SHELL="true" ;;
		code_exec) CT_CODE_EXEC="true" ;;
		mail_read) CT_MAIL_READ="true" ;;
		mail_send) CT_MAIL_SEND="true" ;;
		web_search) CT_WEB_SEARCH="true" ;;
		rss) CT_RSS="true" ;;
		dns) CT_DNS="true" ;;
		git) CT_GIT="true" ;;
		file_ops) CT_FILE_OPS="true" ;;
		image_gen) CT_IMAGE_GEN="true" ;;
		searxng) CT_SEARXNG="true" ;;
		esac
	done

	success "Tools: ${CT_SELECTED[*]:-none}"

	# ─── Step 4: Vault (optional) ────────────────────────────────────────────

	step 4 "Vault & API"
	info "Optional: enable the encrypted vault for secrets and the REST API."

	VAULT_ENABLED="false"
	VAULT_PASS=""
	API_ENABLED="false"
	API_KEY=""

	if ask_yesno "Enable vault for this client?" "n"; then
		VAULT_ENABLED="true"
		ask_secret VAULT_PASS "Vault passphrase"
		if [[ -z "$VAULT_PASS" ]]; then
			VAULT_PASS="$(generate_passphrase)"
			success "Generated passphrase: ${BOLD}$VAULT_PASS${RESET}"
		fi
	fi

	if ask_yesno "Enable REST API for this client?" "n"; then
		API_ENABLED="true"
		API_KEY="$(generate_api_key)"
		if [[ "$VAULT_ENABLED" != "true" ]]; then
			warn "REST API requires vault for secure key storage — enabling vault."
			VAULT_ENABLED="true"
			ask_secret VAULT_PASS "Vault passphrase"
			if [[ -z "$VAULT_PASS" ]]; then
				VAULT_PASS="$(generate_passphrase)"
				success "Generated passphrase: ${BOLD}$VAULT_PASS${RESET}"
			fi
		fi
		success "Generated API key"
	fi

	# ─── Generate client config ──────────────────────────────────────────────

	divider
	info "${BOLD}Generating client configuration...${RESET}"

	IRC_PASSWORD_TOML="$(toml_escape "$IRC_PASSWORD")"
	CLIENT_NICK_SAFE="$(toml_escape "$CLIENT_NICK")"
	BUS_KEY_TOML="$(toml_escape "$BUS_KEY")"

	CLIENT_CONFIG="$(generate_standalone_client_config)"
	write_file "$DEFAULT_DATA_DIR/client.toml" "$CLIENT_CONFIG"

	# Store vault secrets if vault enabled
	if [[ "$VAULT_ENABLED" == "true" && "$DRY_RUN" != "true" ]]; then
		# Need to build binary first if not present
		MURMUR_BIN=""
		if [[ -x "$PROJECT_DIR/bin/murmur" ]]; then
			MURMUR_BIN="$PROJECT_DIR/bin/murmur"
		elif command -v murmur &>/dev/null; then
			MURMUR_BIN="murmur"
		elif check_command go; then
			info "Building murmur binary..."
			make -C "$PROJECT_DIR" build
			if [[ -x "$PROJECT_DIR/bin/murmur" ]]; then
				MURMUR_BIN="$PROJECT_DIR/bin/murmur"
				success "Binary built"
			fi
		fi

		if [[ -n "$MURMUR_BIN" ]]; then
			if [[ "$API_ENABLED" == "true" && -n "$API_KEY" ]]; then
				printf '%s' "$API_KEY" | MURMUR_VAULT_PASS="$VAULT_PASS" "$MURMUR_BIN" \
					vault set "api-key" --db "$DEFAULT_DATA_DIR/vault.db"
				success "Stored api-key"
			fi
		elif [[ "$API_ENABLED" == "true" ]]; then
			err "Cannot store API key in vault — murmur binary not found and Go not installed."
			warn "Install Go and run manually from the project directory:"
			warn "  make build && echo '<api-key>' | MURMUR_VAULT_PASS='<passphrase>' ./bin/murmur vault set api-key --db $DEFAULT_DATA_DIR/vault.db"
			warn "Until then, the REST API will not authenticate correctly."
		fi
	elif [[ "$VAULT_ENABLED" == "true" && "$DRY_RUN" == "true" ]]; then
		info "[dry-run] Would store vault secrets"
	fi

	# ─── Client Summary ──────────────────────────────────────────────────────

	divider
	echo ""
	echo "${GREEN}${BOLD}  Client setup complete!${RESET}"
	echo ""
	echo "  ${BOLD}Config file:${RESET}"
	echo "    ${DIM}$DEFAULT_DATA_DIR/client.toml${RESET}"
	echo ""
	echo "  ${BOLD}Start the client:${RESET}"
	if [[ "$VAULT_ENABLED" == "true" ]]; then
		echo "    ${DIM}MURMUR_VAULT_PASS=\"$VAULT_PASS\" murmur client --config $DEFAULT_DATA_DIR/client.toml${RESET}"
	else
		echo "    ${DIM}murmur client --config $DEFAULT_DATA_DIR/client.toml${RESET}"
	fi
	echo ""
	if [[ -n "$BUS_KEY" ]]; then
		echo "  ${BOLD}Bus key:${RESET} ${DIM}$BUS_KEY${RESET}"
	fi
	if [[ "$API_ENABLED" == "true" ]]; then
		echo "  ${BOLD}API key:${RESET} ${DIM}$API_KEY${RESET}"
	fi
	if [[ "$VAULT_ENABLED" == "true" ]]; then
		echo "  ${BOLD}Vault passphrase:${RESET} ${YELLOW}$VAULT_PASS${RESET}"
	fi
	echo ""
	echo "  ${DIM}Some tools (web_search, mail, git, file_ops, image_gen) need manual config — edit client.toml.${RESET}"
	echo ""
	exit 0
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Server Setup Mode (default)
# ═══════════════════════════════════════════════════════════════════════════════

TOTAL_STEPS=8

# ═══════════════════════════════════════════════════════════════════════════════
# Step 1: Installation Mode
# ═══════════════════════════════════════════════════════════════════════════════

step 1 "Installation Mode"

INSTALL_MODE="${MURMUR_MODE:-}"
if [[ -z "$INSTALL_MODE" ]]; then
	ask_choice INSTALL_MODE "How do you want to run Murmur?" "docker|bare" "docker"
fi

if [[ "$INSTALL_MODE" == "docker" ]]; then
	success "Docker Compose deployment"
	# Check prerequisites
	if ! check_command docker; then
		err "Docker is not installed. Install it from https://docs.docker.com/get-docker/"
		exit 1
	fi
	if ! docker compose version &>/dev/null; then
		err "Docker Compose v2 is not available. Update Docker or install the compose plugin."
		exit 1
	fi
	success "Docker and Docker Compose found"
else
	success "Bare metal deployment"
	if ! check_command go; then
		err "Go is not installed. Install Go 1.23+ from https://go.dev/dl/"
		exit 1
	fi
	GO_VERSION="$(go version | sed -n 's/.*go\([0-9]*\.[0-9]*\).*/\1/p')"
	success "Go $GO_VERSION found"
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Step 2: Vault Passphrase
# ═══════════════════════════════════════════════════════════════════════════════

step 2 "Vault Passphrase"
info "The vault encrypts secrets (API keys, passwords) at rest using AES-256-GCM."

VAULT_PASS="${MURMUR_VAULT_PASS:-}"
if [[ -z "$VAULT_PASS" ]]; then
	if ask_yesno "Auto-generate a secure passphrase?" "y"; then
		VAULT_PASS="$(generate_passphrase)"
		success "Generated passphrase: ${BOLD}$VAULT_PASS${RESET}"
		warn "Save this somewhere safe — you'll need it to start Murmur."
	else
		ask_secret VAULT_PASS "Enter vault passphrase"
		if [[ -z "$VAULT_PASS" ]]; then
			err "Passphrase cannot be empty."
			exit 1
		fi
	fi
fi
success "Vault passphrase set"

# ═══════════════════════════════════════════════════════════════════════════════
# Step 3: LLM Provider
# ═══════════════════════════════════════════════════════════════════════════════

step 3 "LLM Provider"
info "Murmur needs an LLM provider for its AI agent. Pick one (or add more later)."

LLM_PROVIDER=""
ask_choice LLM_PROVIDER "Select your LLM provider" "OpenRouter|OpenAI|Ollama|Custom" "OpenRouter"

case "$LLM_PROVIDER" in
OpenRouter)
	LLM_API_BASE="https://openrouter.ai/api/v1"
	LLM_DEFAULT_MODEL="anthropic/claude-sonnet-4-5"
	LLM_PROVIDER_ID="openrouter"
	;;
OpenAI)
	LLM_API_BASE="https://api.openai.com/v1"
	LLM_DEFAULT_MODEL="gpt-4.1"
	LLM_PROVIDER_ID="openai"
	;;
Ollama)
	LLM_API_BASE=""
	LLM_DEFAULT_MODEL="llama3.1:70b"
	LLM_PROVIDER_ID="ollama"
	if [[ "$INSTALL_MODE" == "docker" ]]; then
		ask LLM_API_BASE "Ollama API URL" "http://host.docker.internal:11434/v1"
	else
		ask LLM_API_BASE "Ollama API URL" "http://localhost:11434/v1"
	fi
	;;
Custom)
	LLM_PROVIDER_ID="custom"
	ask LLM_API_BASE "API base URL (OpenAI-compatible /v1)" ""
	LLM_DEFAULT_MODEL=""
	;;
esac

# API key
LLM_KEY="${LLM_API_KEY:-}"
if [[ "$LLM_PROVIDER" != "Ollama" ]]; then
	if [[ -z "$LLM_KEY" ]]; then
		ask_secret LLM_KEY "API key for $LLM_PROVIDER"
		if [[ -z "$LLM_KEY" ]]; then
			err "API key cannot be empty."
			exit 1
		fi
	fi
else
	LLM_KEY="${LLM_KEY:-ollama}"
fi

# Model
LLM_MODEL=""
ask LLM_MODEL "Model name" "$LLM_DEFAULT_MODEL"
if [[ -z "$LLM_MODEL" ]]; then
	LLM_MODEL="$LLM_DEFAULT_MODEL"
fi

success "Provider: ${BOLD}$LLM_PROVIDER${RESET} ($LLM_PROVIDER_ID)"
success "Model: ${BOLD}$LLM_MODEL${RESET}"

# ─── RAG Memory Search (sub-question within LLM step) ───────────────────────

echo ""
info "RAG memory search lets Murmur search its conversation history and ingested files."
info "It uses FTS5 full-text search (no external dependencies)."

RAG_ENABLED="false"
SUMMARY_MODEL=""
if ask_yesno "Enable RAG memory search?" "y"; then
	RAG_ENABLED="true"
	success "RAG memory search enabled"

	# Optional: summary model for cheaper summarization
	echo ""
	info "You can use a separate (cheaper/faster) LLM for conversation summarization."
	info "Leave empty to use the default provider ($LLM_PROVIDER_ID)."
	if ask_yesno "Configure a separate summary model?" "n"; then
		ask SUMMARY_MODEL "Summary model provider ID (must be configured in [llm.providers])" ""
		if [[ -n "$SUMMARY_MODEL" ]]; then
			success "Summary model: ${BOLD}$SUMMARY_MODEL${RESET}"
		else
			success "Using default provider for summaries"
		fi
	fi
else
	success "RAG disabled (enable later in config)"
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Step 4: Admin Account
# ═══════════════════════════════════════════════════════════════════════════════

step 4 "Admin Account"
info "Your IRC nick will be the admin account with full permissions."

ADMIN_NICK="${MURMUR_ADMIN_NICK:-}"
ADMIN_PASS="${MURMUR_ADMIN_PASS:-}"

if [[ -z "$ADMIN_NICK" ]]; then
	ask ADMIN_NICK "Your IRC nickname" ""
	if [[ -z "$ADMIN_NICK" ]]; then
		err "Admin nickname cannot be empty."
		exit 1
	fi
fi

if [[ -z "$ADMIN_PASS" ]]; then
	ask_secret ADMIN_PASS "Password for $ADMIN_NICK (NickServ registration)"
	if [[ -z "$ADMIN_PASS" ]]; then
		err "Admin password cannot be empty."
		exit 1
	fi
fi

validate_irc_input "$ADMIN_NICK" "Admin nickname"
validate_irc_input "$ADMIN_PASS" "Admin password"
success "Admin: ${BOLD}$ADMIN_NICK${RESET}"

# ═══════════════════════════════════════════════════════════════════════════════
# Step 5: IRC Server Password (optional)
# ═══════════════════════════════════════════════════════════════════════════════

step 5 "IRC Server Password"
info "An optional server-wide password restricts who can connect to the IRC server."
info "Leave empty to allow anyone on the network to connect."

IRC_SERVER_PASS=""
if ask_yesno "Set an IRC server password?" "n"; then
	ask_secret IRC_SERVER_PASS "IRC server password"
fi

if [[ -n "$IRC_SERVER_PASS" ]]; then
	validate_irc_input "$IRC_SERVER_PASS" "IRC server password"
	success "IRC server password set"
else
	success "No server password (open access)"
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Step 6: Dashboard
# ═══════════════════════════════════════════════════════════════════════════════

step 6 "Web Dashboard"
info "The web dashboard provides a browser-based chat interface to Murmur."

DASHBOARD_ENABLED="false"
DASHBOARD_PORT="8082"
if ask_yesno "Enable the web dashboard?" "n"; then
	DASHBOARD_ENABLED="true"
	ask DASHBOARD_PORT "Dashboard port" "8082"
fi

if [[ "$DASHBOARD_ENABLED" == "true" ]]; then
	success "Dashboard enabled on port ${BOLD}$DASHBOARD_PORT${RESET}"
else
	success "Dashboard disabled (enable later in config)"
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Step 7: Brave Search API Key (optional)
# ═══════════════════════════════════════════════════════════════════════════════

step 7 "Brave Search (optional)"
info "If you have a Brave Search API key, Murmur can use it for web search."
info "You can also use SearXNG (self-hosted) — select it in the next step."

BRAVE_KEY=""
SEARCH_PROVIDER="none"
if ask_yesno "Configure Brave Search API key?" "n"; then
	ask_secret BRAVE_KEY "Brave Search API key"
	if [[ -n "$BRAVE_KEY" ]]; then
		SEARCH_PROVIDER="brave"
		success "Brave Search configured"
	else
		warn "No API key provided — skipping Brave Search."
	fi
else
	success "Skipped (use SearXNG or add Brave later)"
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Step 8: Server Tools
# ═══════════════════════════════════════════════════════════════════════════════

step 8 "Server Tools"
info "Select which tools to enable on the server."
info "Tools marked with * are selected by default."

TOOLS_SELECTED=""
ask_multi TOOLS_SELECTED "Enable server-side tools" \
	"shell|code_exec|systeminfo|rss|dns|http|irc_manage|config_manage|searxng|browser|opencode" \
	"shell|code_exec|systeminfo|rss|dns|http|irc_manage|config_manage"

# Parse selections into flags
TOOL_SHELL="false"
TOOL_CODE_EXEC="false"
TOOL_SYSTEMINFO="false"
TOOL_RSS="false"
TOOL_DNS="false"
TOOL_HTTP="false"
TOOL_IRC_MANAGE="false"
TOOL_CONFIG_MANAGE="false"
TOOL_SEARXNG="false"
TOOL_BROWSER="false"
TOOL_OPENCODE="false"

OPENCODE_API_KEY=""

IFS='|' read -ra SELECTED_TOOLS <<<"$TOOLS_SELECTED"
for tool in "${SELECTED_TOOLS[@]}"; do
	case "$tool" in
	shell) TOOL_SHELL="true" ;;
	code_exec) TOOL_CODE_EXEC="true" ;;
	systeminfo) TOOL_SYSTEMINFO="true" ;;
	rss) TOOL_RSS="true" ;;
	dns) TOOL_DNS="true" ;;
	http) TOOL_HTTP="true" ;;
	irc_manage) TOOL_IRC_MANAGE="true" ;;
	config_manage) TOOL_CONFIG_MANAGE="true" ;;
	searxng)
		TOOL_SEARXNG="true"
		SEARCH_PROVIDER="searxng"
		add_docker_profile "search"
		;;
	browser)
		TOOL_BROWSER="true"
		add_docker_profile "browser"
		;;
	opencode)
		TOOL_OPENCODE="true"
		add_docker_profile "opencode"
		;;
	esac
done

# Prompt for OpenCode API key if selected
if [[ "$TOOL_OPENCODE" == "true" ]]; then
	echo ""
	info "OpenCode requires an OpenRouter API key for its coding agent."
	ask_secret OPENCODE_API_KEY "OpenRouter API key for OpenCode"
	if [[ -z "$OPENCODE_API_KEY" ]]; then
		warn "No API key — OpenCode may not work. Set OPENROUTER_API_KEY in .env later."
	fi
fi

success "Tools: ${SELECTED_TOOLS[*]:-none}"

# ─── Local Client (sub-question within tools step) ──────────────────────────

echo ""
info "A local client runs alongside the server and provides tool execution."
info "You can skip this if you'll connect remote clients instead."

SETUP_LOCAL_CLIENT="true"
if ! ask_yesno "Set up a local client alongside the server?" "y"; then
	SETUP_LOCAL_CLIENT="false"
	success "Server-only mode — no local client"
	info "Run ${DIM}./scripts/setup.sh client${RESET} on a remote machine to add clients later."
else
	success "Local client will be configured"
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Generate Configuration
# ═══════════════════════════════════════════════════════════════════════════════

divider
echo ""
info "${BOLD}Generating configuration...${RESET}"
echo ""

BUS_KEY="$(generate_bus_key)"
API_KEY="$(generate_api_key)"

# Escape user-provided values for safe TOML embedding
ADMIN_NICK_TOML="$(toml_escape "$ADMIN_NICK")"
ADMIN_NICK_KEY="$(toml_key "$ADMIN_NICK")"
IRC_SERVER_PASS_TOML="$(toml_escape "$IRC_SERVER_PASS")"

# ─── Write configuration files ──────────────────────────────────────────────

if [[ "$INSTALL_MODE" == "docker" ]]; then
	# Docker mode: write configs to project configs/ directory

	# Generate NickServ and OPER passwords (needed for config generation)
	BOT_NICKSERV_PASS="$(generate_passphrase)"
	CLIENT_NICKSERV_PASS=""
	if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
		CLIENT_NICKSERV_PASS="$(generate_passphrase)"
	fi
	OPER_PASS="$(generate_passphrase)"

	# Generate OPER bcrypt hash (needed for ergo config)
	OPER_BCRYPT_HASH=""
	if [[ "$DRY_RUN" != "true" ]]; then
		info "Generating OPER credentials..."
		OPER_BCRYPT_HASH="$(generate_bcrypt_hash "$OPER_PASS")"
		if [[ -n "$OPER_BCRYPT_HASH" ]]; then
			success "OPER credentials generated"
		else
			warn "Could not generate OPER bcrypt hash — OPER will not be available."
			OPER_PASS=""
		fi
	fi

	# Generate and write configs
	SERVER_CONFIG="$(generate_server_config)"
	PERMISSIONS_CONFIG="$(generate_permissions_config)"
	ENV_CONTENT="$(generate_env_file)"

	write_file "$CONFIGS_DIR/server.docker.toml" "$SERVER_CONFIG"
	write_file "$CONFIGS_DIR/permissions.toml" "$PERMISSIONS_CONFIG"
	write_file_secure "$PROJECT_DIR/.env" "$ENV_CONTENT"

	if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
		CLIENT_CONFIG="$(generate_client_config)"
		write_file "$CONFIGS_DIR/client.docker.toml" "$CLIENT_CONFIG"
	fi

	# Handle ergo config (server password + OPER credentials)
	docker_setup_ergo_config

	# Build Docker images
	docker_build

	# Start IRC server and register nicks
	docker_start_ircd
	docker_register_nicks

	# Store vault secrets (including NickServ and OPER passwords)
	docker_store_secrets
	docker_store_nickserv_secrets

	# Start all services
	docker_start_all

else
	# ─── Bare metal mode ─────────────────────────────────────────────────────

	# Generate NickServ and OPER passwords (needed for config generation)
	BOT_NICKSERV_PASS="$(generate_passphrase)"
	CLIENT_NICKSERV_PASS=""
	if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
		CLIENT_NICKSERV_PASS="$(generate_passphrase)"
	fi
	OPER_PASS="$(generate_passphrase)"

	SERVER_CONFIG="$(generate_server_config)"
	PERMISSIONS_CONFIG="$(generate_permissions_config)"

	write_file "$DEFAULT_DATA_DIR/server.toml" "$SERVER_CONFIG"
	write_file "$DEFAULT_DATA_DIR/permissions.toml" "$PERMISSIONS_CONFIG"

	if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
		CLIENT_CONFIG="$(generate_client_config)"
		write_file "$DEFAULT_DATA_DIR/client.toml" "$CLIENT_CONFIG"
	fi

	# Copy system prompt
	if [[ -f "$CONFIGS_DIR/system_prompt.md" ]]; then
		if [[ "$DRY_RUN" != "true" ]]; then
			mkdir -p "$DEFAULT_DATA_DIR"
			cp "$CONFIGS_DIR/system_prompt.md" "$DEFAULT_DATA_DIR/system_prompt.md"
			success "Copied system_prompt.md"
		else
			info "[dry-run] Would copy system_prompt.md"
		fi
	fi

	# Build binary
	divider
	info "${BOLD}Building murmur binary...${RESET}"
	if [[ "$DRY_RUN" != "true" ]]; then
		make -C "$PROJECT_DIR" build
		success "Binary built: bin/murmur"
	else
		info "[dry-run] Would run: make build"
	fi

	# Store vault secrets
	divider
	info "${BOLD}Storing secrets in vault...${RESET}"
	if [[ "$DRY_RUN" != "true" ]]; then
		vault_store_bare "llm-api-key" "$LLM_KEY"
		vault_store_bare "api-key" "$API_KEY"
		vault_store_bare "nickserv-password" "$BOT_NICKSERV_PASS"
		if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
			vault_store_bare "client-nickserv-password" "$CLIENT_NICKSERV_PASS"
		fi
		vault_store_bare "oper-password" "$OPER_PASS"

		if [[ "$SEARCH_PROVIDER" == "brave" && -n "$BRAVE_KEY" ]]; then
			vault_store_bare "brave-search-key" "$BRAVE_KEY"
		fi

		if [[ -n "$IRC_SERVER_PASS" ]]; then
			vault_store_bare "irc-server-password" "$IRC_SERVER_PASS"
		fi
	else
		info "[dry-run] Would store vault secrets"
	fi

	echo ""
	warn "Bare metal mode: register nicks with NickServ manually on your IRC server."
	warn "Connect as each nick and run:"
	echo "    ${DIM}$ADMIN_NICK:      /msg NickServ REGISTER <your admin password>${RESET}"
	echo "    ${DIM}murmur:           /msg NickServ REGISTER $BOT_NICKSERV_PASS${RESET}"
	if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
		echo "    ${DIM}murmur-client:    /msg NickServ REGISTER $CLIENT_NICKSERV_PASS${RESET}"
	fi
	warn "These passwords are stored in the vault — they must match what NickServ has."
fi

# ═══════════════════════════════════════════════════════════════════════════════
# Summary
# ═══════════════════════════════════════════════════════════════════════════════

divider
echo ""
echo "${GREEN}${BOLD}  Setup complete!${RESET}"
echo ""

if [[ "$INSTALL_MODE" == "docker" ]]; then
	echo "  ${BOLD}Services:${RESET}"
	echo "    IRC server     ${DIM}localhost:6667${RESET}"
	echo "    REST API       ${DIM}localhost:8080${RESET}"
	if [[ "$DASHBOARD_ENABLED" == "true" ]]; then
		echo "    Dashboard      ${DIM}localhost:$DASHBOARD_PORT${RESET}"
	fi
	if [[ -n "$IRC_SERVER_PASS" || -n "${OPER_BCRYPT_HASH:-}" ]]; then
		echo ""
		info "ergo.generated.yaml was created with custom credentials and auto-mounted."
	fi
	echo ""
	echo "  ${BOLD}Connect your IRC client:${RESET}"
	echo "    Server:   ${CYAN}localhost:6667${RESET}"
	if [[ -n "$IRC_SERVER_PASS" ]]; then
		echo "    Password: ${DIM}the server password you set${RESET}"
	fi
	echo "    Nick:     ${CYAN}$ADMIN_NICK${RESET}"
	echo "    Channel:  ${CYAN}#murmur${RESET}"
	echo ""
	echo "  ${BOLD}After connecting, identify with NickServ:${RESET}"
	echo "    ${DIM}/msg NickServ IDENTIFY $ADMIN_NICK <your-password>${RESET}"
	echo ""
	if [[ "$SETUP_LOCAL_CLIENT" != "true" ]]; then
		echo "  ${BOLD}Add a client later:${RESET}"
		echo "    ${DIM}./scripts/setup.sh client${RESET}  — on a remote machine"
		echo ""
	fi
	echo "  ${BOLD}Manage services:${RESET}"
	echo "    ${DIM}docker compose ps${RESET}          — check status"
	echo "    ${DIM}docker compose logs -f${RESET}     — view logs"
	echo "    ${DIM}docker compose restart${RESET}     — restart all"
	echo "    ${DIM}docker compose down${RESET}        — stop all"
	echo ""
else
	echo "  ${BOLD}Config files:${RESET}"
	echo "    Server:      ${DIM}$DEFAULT_DATA_DIR/server.toml${RESET}"
	if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
		echo "    Client:      ${DIM}$DEFAULT_DATA_DIR/client.toml${RESET}"
	fi
	echo "    Permissions: ${DIM}$DEFAULT_DATA_DIR/permissions.toml${RESET}"
	echo ""
	echo "  ${BOLD}Start the server:${RESET}"
	echo "    ${DIM}MURMUR_VAULT_PASS=$VAULT_PASS $PROJECT_DIR/bin/murmur server --config $DEFAULT_DATA_DIR/server.toml${RESET}"
	echo ""
	if [[ "$SETUP_LOCAL_CLIENT" == "true" ]]; then
		echo "  ${BOLD}Start the client (separate terminal):${RESET}"
		echo "    ${DIM}MURMUR_VAULT_PASS=$VAULT_PASS $PROJECT_DIR/bin/murmur client --config $DEFAULT_DATA_DIR/client.toml${RESET}"
		echo ""
	fi
	echo "  ${BOLD}Note:${RESET} You'll need an IRC server (e.g., Ergo) running on localhost:6667."
	echo "  See: ${DIM}https://ergo.chat/manual.html${RESET}"
	echo ""
fi

echo "  ${BOLD}Important — save these values:${RESET}"
echo "    Vault passphrase: ${YELLOW}$VAULT_PASS${RESET}"
echo "    Bus key:          ${DIM}$BUS_KEY${RESET}"
echo "    REST API key:     ${DIM}$API_KEY${RESET}"
echo ""
echo "  ${DIM}Happy hacking! Say hi to Murmur in #murmur.${RESET}"
echo ""
