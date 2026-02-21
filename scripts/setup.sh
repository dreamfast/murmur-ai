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

# ─── Constants ───────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly PROJECT_DIR
readonly CONFIGS_DIR="$PROJECT_DIR/configs"
readonly DEFAULT_DATA_DIR="$HOME/.murmur"

# ─── Color helpers ───────────────────────────────────────────────────────────

if [[ -t 1 ]] && command -v tput &>/dev/null; then
	BOLD="$(tput bold)"
	DIM="$(tput dim)"
	RESET="$(tput sgr0)"
	RED="$(tput setaf 1)"
	GREEN="$(tput setaf 2)"
	YELLOW="$(tput setaf 3)"
	BLUE="$(tput setaf 4)"
	MAGENTA="$(tput setaf 5)"
	CYAN="$(tput setaf 6)"
	WHITE="$(tput setaf 7)"
else
	BOLD="" DIM="" RESET=""
	RED="" GREEN="" YELLOW="" BLUE="" MAGENTA="" CYAN="" WHITE=""
fi

# ─── Output helpers ──────────────────────────────────────────────────────────

banner() {
	echo ""
	echo "${MAGENTA}${BOLD}"
	cat <<'ART'
                                          
    ___  ___                              
    |  \/  |                              
    | .  . |_   _ _ __ _ __ ___  _   _ _ __ 
    | |\/| | | | | '__| '_ ` _ \| | | | '__|
    | |  | | |_| | |  | | | | | | |_| | |   
    \_|  |_/\__,_|_|  |_| |_| |_|\__,_|_|   
                                          
ART
	echo "${RESET}"
	echo "  ${DIM}Distributed AI Agent System${RESET}"
	echo "  ${DIM}IRC-native  |  Multi-provider LLM  |  Tool ecosystem${RESET}"
	echo ""
}

info() { echo "  ${BLUE}${BOLD}>>>${RESET} $*"; }
success() { echo "  ${GREEN}${BOLD} ok${RESET} $*"; }
warn() { echo "  ${YELLOW}${BOLD}  !${RESET} $*"; }
err() { echo "  ${RED}${BOLD}err${RESET} $*" >&2; }
step() {
	echo ""
	echo "${CYAN}${BOLD}[$1/$TOTAL_STEPS]${RESET} ${BOLD}$2${RESET}"
	echo "${DIM}$(printf '%.0s─' {1..60})${RESET}"
}
divider() {
	echo ""
	echo "${DIM}$(printf '%.0s─' {1..60})${RESET}"
}

# ─── Input helpers ───────────────────────────────────────────────────────────

# ask VARNAME "prompt" [default]
ask() {
	local varname="$1" prompt="$2" default="${3:-}"
	local value
	if [[ -n "$default" ]]; then
		printf "  %s%s%s %s[%s]%s: " "$WHITE" "$prompt" "$RESET" "$DIM" "$default" "$RESET"
	else
		printf "  %s%s%s: " "$WHITE" "$prompt" "$RESET"
	fi
	read -r value
	value="${value:-$default}"
	printf -v "$varname" '%s' "$value"
}

# ask_secret VARNAME "prompt"
ask_secret() {
	local varname="$1" prompt="$2"
	local value
	printf "  %s%s%s: " "$WHITE" "$prompt" "$RESET"
	read -rs value
	echo ""
	printf -v "$varname" '%s' "$value"
}

# ask_yesno "prompt" [default: y/n] -> returns 0 for yes, 1 for no
ask_yesno() {
	local prompt="$1" default="${2:-y}"
	local hint value
	if [[ "$default" == "y" ]]; then hint="Y/n"; else hint="y/N"; fi
	printf "  %s%s%s %s[%s]%s: " "$WHITE" "$prompt" "$RESET" "$DIM" "$hint" "$RESET"
	read -r value
	value="${value:-$default}"
	[[ "${value,,}" == "y" || "${value,,}" == "yes" ]]
}

# ask_choice VARNAME "prompt" "opt1|opt2|opt3" [default]
ask_choice() {
	local varname="$1" prompt="$2" options_str="$3" default="${4:-}"
	local IFS='|'
	local -a options
	read -ra options <<<"$options_str"

	echo "  ${WHITE}$prompt${RESET}"
	local i=1
	for opt in "${options[@]}"; do
		local marker="  "
		if [[ "$opt" == "$default" ]]; then marker="${GREEN}*${RESET} "; fi
		echo "    ${marker}${BOLD}$i)${RESET} $opt"
		((i++))
	done

	local choice
	printf "  %sChoice [1-%d]%s: " "$DIM" "${#options[@]}" "$RESET"
	read -r choice

	if [[ -z "$choice" && -n "$default" ]]; then
		printf -v "$varname" '%s' "$default"
		return
	fi

	if [[ "$choice" =~ ^[0-9]+$ ]] && ((choice >= 1 && choice <= ${#options[@]})); then
		printf -v "$varname" '%s' "${options[$((choice - 1))]}"
	else
		warn "Invalid choice, using default: $default"
		printf -v "$varname" '%s' "$default"
	fi
}

# ask_multi VARNAME "prompt" "opt1|opt2|opt3" "default1|default2"
# Returns pipe-separated selected values.
ask_multi() {
	local varname="$1" prompt="$2" options_str="$3" defaults_str="${4:-}"
	local IFS='|'
	local -a options defaults
	read -ra options <<<"$options_str"
	if [[ -n "$defaults_str" ]]; then
		read -ra defaults <<<"$defaults_str"
	fi

	echo "  ${WHITE}$prompt${RESET} ${DIM}(space-separated numbers, or 'all'/'none')${RESET}"
	local i=1
	for opt in "${options[@]}"; do
		local marker="  "
		for d in "${defaults[@]+"${defaults[@]}"}"; do
			if [[ "$opt" == "$d" ]]; then
				marker="${GREEN}*${RESET} "
				break
			fi
		done
		echo "    ${marker}${BOLD}$i)${RESET} $opt"
		((i++))
	done

	local input
	printf "  %sSelection%s: " "$DIM" "$RESET"
	read -r input

	if [[ -z "$input" ]]; then
		printf -v "$varname" '%s' "$defaults_str"
		return
	fi

	if [[ "${input,,}" == "all" ]]; then
		printf -v "$varname" '%s' "$options_str"
		return
	fi

	if [[ "${input,,}" == "none" ]]; then
		printf -v "$varname" '%s' ""
		return
	fi

	# Read input into array safely (avoids glob expansion on *)
	local -a nums
	read -ra nums <<<"$input"
	local result=""
	for num in "${nums[@]}"; do
		if [[ "$num" =~ ^[0-9]+$ ]] && ((num >= 1 && num <= ${#options[@]})); then
			if [[ -n "$result" ]]; then result+="|"; fi
			result+="${options[$((num - 1))]}"
		fi
	done
	printf -v "$varname" '%s' "$result"
}

# ─── Utility functions ───────────────────────────────────────────────────────

generate_random() {
	local charset="$1" length="$2"
	# Read plenty of bytes to ensure enough survive filtering.
	# Avoids SIGPIPE from tr|head under pipefail by reading a fixed block.
	local raw result
	raw="$(dd if=/dev/urandom bs=4096 count=1 2>/dev/null | LC_ALL=C tr -dc "$charset")" || true
	result="${raw:0:$length}"
	if [[ ${#result} -lt $length ]]; then
		err "Failed to generate $length random characters from /dev/urandom"
		exit 1
	fi
	printf '%s' "$result"
}

generate_passphrase() { generate_random 'A-Za-z0-9' 24; }
generate_bus_key() { generate_random 'a-f0-9' 32; }
generate_api_key() { generate_random 'A-Za-z0-9' 40; }

check_command() {
	command -v "$1" &>/dev/null
}

# toml_escape STRING — escapes a string for safe TOML double-quoted value
toml_escape() {
	local s="$1"
	s="${s//\\/\\\\}"   # backslash
	s="${s//\"/\\\"}"   # double quote
	s="${s//$'\n'/\\n}" # newline
	s="${s//$'\r'/\\r}" # carriage return
	s="${s//$'\t'/\\t}" # tab
	printf '%s' "$s"
}

# toml_key STRING — returns a safe TOML key (quoted if needed)
toml_key() {
	local s="$1"
	# Bare keys: A-Za-z0-9, -, _
	if [[ "$s" =~ ^[A-Za-z0-9_-]+$ ]]; then
		printf '%s' "$s"
	else
		# Quoted key
		printf '"%s"' "$(toml_escape "$s")"
	fi
}

# validate_irc_input STRING LABEL — rejects CR/LF to prevent IRC protocol injection
validate_irc_input() {
	local s="$1" label="$2"
	if [[ "$s" == *$'\n'* || "$s" == *$'\r'* ]]; then
		err "$label cannot contain newlines or carriage returns."
		exit 1
	fi
}

# write_file PATH CONTENT — writes content to file (respects --dry-run)
write_file() {
	local path="$1" content="$2"
	if [[ "$DRY_RUN" == "true" ]]; then
		info "${DIM}[dry-run]${RESET} Would write: $path"
		return
	fi
	mkdir -p "$(dirname "$path")"
	printf '%s' "$content" >"$path"
	success "Wrote $path"
}

# write_file_secure PATH CONTENT — writes with 600 permissions (respects --dry-run)
write_file_secure() {
	local path="$1" content="$2"
	if [[ "$DRY_RUN" == "true" ]]; then
		info "${DIM}[dry-run]${RESET} Would write: $path (mode 600)"
		return
	fi
	mkdir -p "$(dirname "$path")"
	(umask 077 && printf '%s' "$content" >"$path")
	success "Wrote $path (mode 600)"
}

# vault_store_docker KEY VALUE — stores a secret via stdin (no argv exposure)
vault_store_docker() {
	local key="$1" value="$2"
	printf '%s' "$value" | docker compose --project-directory "$PROJECT_DIR" run --rm -T \
		-e MURMUR_VAULT_PASS="$VAULT_PASS" \
		murmur-server \
		vault set "$key" --db /data/vault.db
	success "Stored $key"
}

# vault_store_bare KEY VALUE — stores a secret via stdin (no argv exposure)
vault_store_bare() {
	local key="$1" value="$2"
	printf '%s' "$value" | MURMUR_VAULT_PASS="$VAULT_PASS" "$PROJECT_DIR/bin/murmur" \
		vault set "$key" --db "$DEFAULT_DATA_DIR/vault.db"
	success "Stored $key"
}

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

	generate_standalone_client_config() {
		local irc_password_line=""
		if [[ -n "$IRC_PASSWORD" ]]; then
			irc_password_line="password = \"$IRC_PASSWORD_TOML\""
		else
			irc_password_line="# password = \"\""
		fi

		cat <<TOML
# Murmur client configuration — generated by setup wizard

[client]
id = "$(toml_escape "$CLIENT_ID")"
hostname = "$(toml_escape "$CLIENT_HOSTNAME")"
autonomy = "$CLIENT_AUTONOMY"

[irc]
server = "$(toml_escape "$IRC_SERVER")"
port = $IRC_PORT
tls = $IRC_TLS
nick = "$CLIENT_NICK_SAFE"
user = "$CLIENT_NICK_SAFE"
realname = "Murmur Client"
$irc_password_line
bus_channel = "#murmur-bus"
max_line_len = 8192

[heartbeat]
interval = "30s"

[security]
bus_key = "$BUS_KEY_TOML"
TOML

		if [[ "$VAULT_ENABLED" == "true" ]]; then
			cat <<TOML

[vault]
enabled = true
db_path = "$DEFAULT_DATA_DIR/vault.db"
passphrase_env = "MURMUR_VAULT_PASS"
TOML
		fi

		# Tools
		if [[ "$CT_SYSTEMINFO" == "true" ]]; then
			cat <<'TOML'

[tools.systeminfo]
enabled = true
TOML
		fi

		if [[ "$CT_SHELL" == "true" ]]; then
			cat <<'TOML'

[tools.shell]
enabled = true
docker_image = "ubuntu:24.04"
network = false
memory_limit = "256m"
cpu_limit = "0.5"
timeout = "30s"
TOML
		fi

		if [[ "$CT_CODE_EXEC" == "true" ]]; then
			cat <<'TOML'

[tools.code_exec]
enabled = true
piston_url = "http://localhost:2000"
default_language = "python"
TOML
		fi

		if [[ "$CT_MAIL_READ" == "true" ]]; then
			cat <<'TOML'

# [tools.mail_read]
# enabled = true
# thunderbird_profile = "~/.thunderbird/abc123.default-release"
# mail_dir = "Mail/pop3.example.com"
TOML
		fi

		if [[ "$CT_MAIL_SEND" == "true" ]]; then
			cat <<'TOML'

# [tools.mail_send]
# enabled = true
# smtp_host = "smtp.example.com"
# smtp_port = 587
# smtp_user = "you@example.com"
# smtp_password = "vault:smtp-password"
# from_address = "you@example.com"
TOML
		fi

		if [[ "$CT_WEB_SEARCH" == "true" ]]; then
			cat <<'TOML'

# [tools.web_search]
# enabled = true
# api_key = "vault:brave-search-key"
# max_results = 5
TOML
		fi

		if [[ "$CT_RSS" == "true" ]]; then
			cat <<'TOML'

[tools.rss]
enabled = true
max_items = 10
TOML
		fi

		if [[ "$CT_DNS" == "true" ]]; then
			cat <<'TOML'

[tools.dns]
enabled = true
TOML
		fi

		if [[ "$CT_GIT" == "true" ]]; then
			cat <<'TOML'

# [tools.git]
# enabled = true
# allowed_repos = [
#     "/home/user/projects/myapp",
# ]
TOML
		fi

		if [[ "$CT_FILE_OPS" == "true" ]]; then
			cat <<'TOML'

# [tools.file_ops]
# enabled = true
# allowed_paths = [
#     "/home/user/documents",
# ]
TOML
		fi

		if [[ "$CT_IMAGE_GEN" == "true" ]]; then
			cat <<'TOML'

# [tools.image_gen]
# enabled = true
# comfyui_host = "http://localhost:8188"
# output_dir = "/home/user/images/murmur"
# checkpoint_name = "sd_xl_base_1.0.safetensors"
TOML
		fi

		if [[ "$CT_SEARXNG" == "true" ]]; then
			cat <<'TOML'

[tools.searxng]
enabled = true
url = "http://localhost:8080"
max_results = 10
TOML
		fi

		if [[ "$API_ENABLED" == "true" ]]; then
			cat <<'TOML'

[api]
enabled = true
listen = "0.0.0.0:8081"
api_key = "vault:api-key"
TOML
		fi
	}

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
# Step 7: Search Provider
# ═══════════════════════════════════════════════════════════════════════════════

step 7 "Web Search"
info "Give Murmur the ability to search the web."

SEARCH_PROVIDER="none"
ask_choice SEARCH_PROVIDER "Search provider" "SearXNG (self-hosted, Docker)|Brave Search (API key)|None" "None"

BRAVE_KEY=""
case "$SEARCH_PROVIDER" in
"SearXNG (self-hosted, Docker)")
	SEARCH_PROVIDER="searxng"
	success "SearXNG will be started alongside Murmur"
	;;
"Brave Search (API key)")
	SEARCH_PROVIDER="brave"
	ask_secret BRAVE_KEY "Brave Search API key"
	if [[ -z "$BRAVE_KEY" ]]; then
		warn "No API key provided — Brave Search disabled."
		SEARCH_PROVIDER="none"
	else
		success "Brave Search configured"
	fi
	;;
*)
	SEARCH_PROVIDER="none"
	success "No search provider (add one later)"
	;;
esac

# ═══════════════════════════════════════════════════════════════════════════════
# Step 8: Server Tools
# ═══════════════════════════════════════════════════════════════════════════════

step 8 "Server Tools"
info "Select which tools to enable on the server."

TOOLS_SELECTED=""
ask_multi TOOLS_SELECTED "Enable server-side tools" \
	"shell|code_exec|rss|dns|http|irc_manage|config_manage" \
	"shell|code_exec|rss|dns|http|irc_manage|config_manage"

# Parse selections into flags
TOOL_SHELL="false"
TOOL_CODE_EXEC="false"
TOOL_RSS="false"
TOOL_DNS="false"
TOOL_HTTP="false"
TOOL_IRC_MANAGE="false"
TOOL_CONFIG_MANAGE="false"

IFS='|' read -ra SELECTED_TOOLS <<<"$TOOLS_SELECTED"
for tool in "${SELECTED_TOOLS[@]}"; do
	case "$tool" in
	shell) TOOL_SHELL="true" ;;
	code_exec) TOOL_CODE_EXEC="true" ;;
	rss) TOOL_RSS="true" ;;
	dns) TOOL_DNS="true" ;;
	http) TOOL_HTTP="true" ;;
	irc_manage) TOOL_IRC_MANAGE="true" ;;
	config_manage) TOOL_CONFIG_MANAGE="true" ;;
	esac
done

success "Tools: ${SELECTED_TOOLS[*]:-none}"

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

# ─── Generate server config ─────────────────────────────────────────────────

generate_server_config() {
	local irc_server irc_port irc_tls data_dir config_path vault_db_path memory_db_path
	local system_prompt_path permissions_path

	if [[ "$INSTALL_MODE" == "docker" ]]; then
		irc_server="ircd"
		irc_port="6667"
		irc_tls="false"
		data_dir="/data"
		config_path="/etc/murmur/server.toml"
		vault_db_path="/data/vault.db"
		memory_db_path="/data/memory.db"
		system_prompt_path="/data/system_prompt.md"
		permissions_path="/data/permissions.toml"
	else
		irc_server="localhost"
		irc_port="6667"
		irc_tls="false"
		data_dir="$DEFAULT_DATA_DIR"
		config_path="$DEFAULT_DATA_DIR/server.toml"
		vault_db_path="$DEFAULT_DATA_DIR/vault.db"
		memory_db_path="$DEFAULT_DATA_DIR/memory.db"
		system_prompt_path="$DEFAULT_DATA_DIR/system_prompt.md"
		permissions_path="$DEFAULT_DATA_DIR/permissions.toml"
	fi

	local irc_password_line=""
	if [[ -n "$IRC_SERVER_PASS" ]]; then
		irc_password_line="password = \"$IRC_SERVER_PASS_TOML\""
	else
		irc_password_line="# password = \"\""
	fi

	cat <<TOML
# Murmur server configuration — generated by setup wizard

[server]
data_dir = "$data_dir"
system_prompt_file = "$system_prompt_path"
name = "server"
verbose = true

[irc]
server = "$irc_server"
port = $irc_port
tls = $irc_tls
nick = "murmur"
user = "murmur"
realname = "Murmur Agent"
$irc_password_line
max_line_len = 8192

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[llm]
default = "$LLM_PROVIDER_ID"

[llm.providers.$LLM_PROVIDER_ID]
api_base = "$LLM_API_BASE"
api_key = "vault:llm-api-key"
model = "$(toml_escape "$LLM_MODEL")"
max_tokens = 8192
temperature = 0.7

[memory]
db_path = "$memory_db_path"
max_history = 100

[scheduler]
enabled = true
heartbeat_interval = "5m"
client_timeout = "2m"
tick_interval = "30s"
max_concurrent = 3

[security]
allowed_users = ["$ADMIN_NICK_TOML"]
require_nickserv = true
bus_key = "$BUS_KEY"
permissions_file = "$permissions_path"

[vault]
enabled = true
db_path = "$vault_db_path"
passphrase_env = "MURMUR_VAULT_PASS"
TOML

	# Server-side tools
	if [[ "$TOOL_SHELL" == "true" ]]; then
		if [[ "$INSTALL_MODE" == "docker" ]]; then
			cat <<'TOML'

[tools.shell]
enabled = true
docker_image = "murmur-shell:latest"
network = true
memory_limit = "256m"
cpu_limit = "1.0"
timeout = "60s"
TOML
		else
			cat <<'TOML'

[tools.shell]
enabled = true
docker_image = "ubuntu:24.04"
network = false
memory_limit = "256m"
cpu_limit = "0.5"
timeout = "30s"
TOML
		fi
	fi

	if [[ "$TOOL_CODE_EXEC" == "true" ]]; then
		local piston_url
		if [[ "$INSTALL_MODE" == "docker" ]]; then
			piston_url="http://piston:2000"
		else
			piston_url="http://localhost:2000"
		fi
		cat <<TOML

[tools.code_exec]
enabled = true
piston_url = "$piston_url"
default_language = "python"
TOML
	fi

	if [[ "$TOOL_RSS" == "true" ]]; then
		cat <<'TOML'

[tools.rss]
enabled = true
max_items = 10
TOML
	fi

	if [[ "$TOOL_DNS" == "true" ]]; then
		cat <<'TOML'

[tools.dns]
enabled = true
TOML
	fi

	if [[ "$TOOL_HTTP" == "true" ]]; then
		cat <<'TOML'

[tools.http]
enabled = true
timeout = "30s"
max_response_bytes = 1048576
block_private_ips = true
TOML
	fi

	if [[ "$SEARCH_PROVIDER" == "searxng" ]]; then
		local searxng_url
		if [[ "$INSTALL_MODE" == "docker" ]]; then
			searxng_url="http://searxng:8080"
		else
			searxng_url="http://localhost:8080"
		fi
		cat <<TOML

[tools.searxng]
enabled = true
url = "$searxng_url"
max_results = 10
TOML
	elif [[ "$SEARCH_PROVIDER" == "brave" ]]; then
		cat <<'TOML'

[tools.web_search]
enabled = true
api_key = "vault:brave-search-key"
max_results = 5
TOML
	fi

	if [[ "$TOOL_CONFIG_MANAGE" == "true" ]]; then
		cat <<TOML

[tools.config_manage]
enabled = true
config_path = "$config_path"
TOML
	fi

	if [[ "$TOOL_IRC_MANAGE" == "true" ]]; then
		cat <<'TOML'

[tools.irc_manage]
enabled = true
TOML
	fi

	# API
	cat <<TOML

[api]
enabled = true
listen = "0.0.0.0:8080"
api_key = "vault:api-key"
event_retention_days = 30
TOML

	# Dashboard
	if [[ "$DASHBOARD_ENABLED" == "true" ]]; then
		cat <<TOML

[dashboard]
enabled = true
listen = "0.0.0.0:$DASHBOARD_PORT"
TOML
	fi

	# Debug (commented out)
	cat <<'TOML'

# [debug]
# enabled = true
# channel = "#murmur-debug"
# log_level = "debug"
# log_tool_calls = true
# log_llm_requests = true
# log_permissions = true
TOML
}

# ─── Generate client config ─────────────────────────────────────────────────

generate_client_config() {
	local irc_server irc_port irc_tls

	if [[ "$INSTALL_MODE" == "docker" ]]; then
		irc_server="ircd"
		irc_port="6667"
		irc_tls="false"
	else
		irc_server="localhost"
		irc_port="6667"
		irc_tls="false"
	fi

	local irc_password_line=""
	if [[ -n "$IRC_SERVER_PASS" ]]; then
		irc_password_line="password = \"$IRC_SERVER_PASS_TOML\""
	else
		irc_password_line="# password = \"\""
	fi

	cat <<TOML
# Murmur client configuration — generated by setup wizard

[client]
id = "local"
hostname = "$(hostname -s 2>/dev/null || echo "murmur-client")"
autonomy = "auto"

[irc]
server = "$irc_server"
port = $irc_port
tls = $irc_tls
nick = "murmur-client"
user = "murmur-client"
realname = "Murmur Client"
$irc_password_line
bus_channel = "#murmur-bus"
max_line_len = 8192

[heartbeat]
interval = "30s"

[security]
bus_key = "$BUS_KEY"

[tools.systeminfo]
enabled = true
TOML

	if [[ "$TOOL_SHELL" == "true" ]]; then
		if [[ "$INSTALL_MODE" == "docker" ]]; then
			cat <<'TOML'

[tools.shell]
enabled = true
docker_image = "murmur-shell:latest"
network = true
memory_limit = "256m"
cpu_limit = "1.0"
timeout = "60s"
TOML
		else
			cat <<'TOML'

[tools.shell]
enabled = true
docker_image = "ubuntu:24.04"
network = false
memory_limit = "256m"
cpu_limit = "0.5"
timeout = "30s"
TOML
		fi
	fi

	# Client API
	if [[ "$INSTALL_MODE" == "docker" ]]; then
		cat <<'TOML'

[api]
enabled = true
listen = "0.0.0.0:8081"
api_key = "vault:api-key"
TOML
	fi
}

# ─── Generate permissions.toml ───────────────────────────────────────────────

generate_permissions_config() {
	cat <<TOML
# Murmur permissions — generated by setup wizard

[users.default]
role = "user"
tools = ["*"]
deny_tools = []
autonomy = "approve"
max_messages_per_hour = 60

[users.$ADMIN_NICK_KEY]
role = "admin"
tools = ["*"]
autonomy = "auto"
max_messages_per_hour = -1
TOML
}

# ─── Generate .env ───────────────────────────────────────────────────────────

generate_env_file() {
	local content="MURMUR_VAULT_PASS=$VAULT_PASS"
	content+=$'\n'
	if [[ "$DASHBOARD_ENABLED" == "true" ]]; then
		content+="MURMUR_DASHBOARD_PORT=$DASHBOARD_PORT"
		content+=$'\n'
	fi
	printf '%s' "$content"
}

# ═══════════════════════════════════════════════════════════════════════════════
# Write Files
# ═══════════════════════════════════════════════════════════════════════════════

if [[ "$INSTALL_MODE" == "docker" ]]; then
	# Docker mode: write configs to project configs/ directory
	SERVER_CONFIG="$(generate_server_config)"
	CLIENT_CONFIG="$(generate_client_config)"
	PERMISSIONS_CONFIG="$(generate_permissions_config)"
	ENV_CONTENT="$(generate_env_file)"

	write_file "$CONFIGS_DIR/server.docker.toml" "$SERVER_CONFIG"
	write_file "$CONFIGS_DIR/client.docker.toml" "$CLIENT_CONFIG"
	write_file "$CONFIGS_DIR/permissions.toml" "$PERMISSIONS_CONFIG"
	write_file_secure "$PROJECT_DIR/.env" "$ENV_CONTENT"

	# Handle IRC server password in ergo config.
	# We copy ergo.yaml to a generated file so we never modify the tracked original.
	if [[ "$DRY_RUN" != "true" ]]; then
		cp "$CONFIGS_DIR/ergo.yaml" "$CONFIGS_DIR/ergo.generated.yaml"
	fi
	if [[ -n "$IRC_SERVER_PASS" ]]; then
		info "Setting IRC server password in Ergo config..."
		if [[ "$DRY_RUN" != "true" ]]; then
			# Generate bcrypt hash via Docker, passing password on stdin
			BCRYPT_HASH="$(printf '%s' "$IRC_SERVER_PASS" | docker run --rm -i alpine:3.21 sh -c \
				'apk add --no-cache apache2-utils >/dev/null 2>&1 && read -r pw && htpasswd -nbBC 4 "" "$pw" | cut -d: -f2')" || true
			if [[ -n "$BCRYPT_HASH" ]]; then
				# Insert password into the generated ergo config under server:
				# Bcrypt hashes contain $ which sed interprets as backrefs; use awk instead.
				if grep -q "^    password:" "$CONFIGS_DIR/ergo.generated.yaml" 2>/dev/null; then
					awk -v hash="$BCRYPT_HASH" '/^    password:/{print "    password: \""hash"\""; next}1' \
						"$CONFIGS_DIR/ergo.generated.yaml" >"$CONFIGS_DIR/ergo.generated.yaml.tmp" &&
						mv "$CONFIGS_DIR/ergo.generated.yaml.tmp" "$CONFIGS_DIR/ergo.generated.yaml"
				else
					awk -v hash="$BCRYPT_HASH" '/^server:/{print; print "    password: \""hash"\""; next}1' \
						"$CONFIGS_DIR/ergo.generated.yaml" >"$CONFIGS_DIR/ergo.generated.yaml.tmp" &&
						mv "$CONFIGS_DIR/ergo.generated.yaml.tmp" "$CONFIGS_DIR/ergo.generated.yaml"
				fi
				success "IRC server password configured in ergo.generated.yaml"
			else
				warn "Could not generate bcrypt hash — set IRC server password manually in ergo.generated.yaml"
			fi
		else
			info "[dry-run] Would generate bcrypt hash and write ergo.generated.yaml"
		fi
	fi

	# Update docker-compose to use the generated ergo config
	# (The user should mount ergo.generated.yaml instead of ergo.yaml)

	# ─── Build ───────────────────────────────────────────────────────────────

	divider
	info "${BOLD}Building Docker images...${RESET}"

	COMPOSE_PROFILES=""
	if [[ "$SEARCH_PROVIDER" == "searxng" ]]; then
		COMPOSE_PROFILES="search"
	fi

	if [[ "$DRY_RUN" != "true" ]]; then
		if [[ -n "$COMPOSE_PROFILES" ]]; then
			COMPOSE_PROFILES="$COMPOSE_PROFILES" docker compose --project-directory "$PROJECT_DIR" build
		else
			docker compose --project-directory "$PROJECT_DIR" build
		fi
		success "Docker images built"
	else
		info "[dry-run] Would run: docker compose build"
	fi

	# ─── Start IRC and register admin ───────────────────────────────────────

	divider
	info "${BOLD}Starting IRC server and registering admin account...${RESET}"

	if [[ "$DRY_RUN" != "true" ]]; then
		# Start only ircd first
		docker compose --project-directory "$PROJECT_DIR" up -d ircd
		info "Waiting for IRC server to be healthy..."

		# Wait for the Docker healthcheck to pass
		retries=0
		while true; do
			health="$(docker compose --project-directory "$PROJECT_DIR" ps ircd --format '{{.Status}}' 2>/dev/null)"
			if [[ "$health" == *"(healthy)"* ]]; then
				break
			fi
			sleep 1
			retries=$((retries + 1))
			if ((retries > 60)); then
				err "IRC server failed to become healthy within 60 seconds."
				exit 1
			fi
		done
		success "IRC server is ready"

		# Register admin account via NickServ.
		# We pipe IRC commands into netcat inside the ircd container.
		info "Registering admin account '$ADMIN_NICK' with NickServ..."
		IRC_OUTPUT="$({
			# If server password is set, send PASS first
			if [[ -n "$IRC_SERVER_PASS" ]]; then
				printf 'PASS %s\r\n' "$IRC_SERVER_PASS"
			fi
			printf 'NICK %s\r\n' "$ADMIN_NICK"
			printf 'USER %s 0 * :Murmur Admin\r\n' "$ADMIN_NICK"
			sleep 2
			printf 'PRIVMSG NickServ :REGISTER %s\r\n' "$ADMIN_PASS"
			sleep 3
			printf 'QUIT :setup complete\r\n'
		} | docker compose --project-directory "$PROJECT_DIR" exec -T ircd \
			sh -c 'nc localhost 6667 2>/dev/null' 2>&1)" || true

		if [[ "$IRC_OUTPUT" == *"900"* || "$IRC_OUTPUT" == *"logged in"* || "$IRC_OUTPUT" == *"Account created"* ]]; then
			success "Admin account registered (nick: $ADMIN_NICK)"
		else
			warn "Could not confirm NickServ registration — you may need to register manually:"
			warn "  /msg NickServ REGISTER <password>"
		fi
	else
		info "[dry-run] Would start ircd and register admin account"
	fi

	# ─── Store vault secrets ─────────────────────────────────────────────────

	divider
	info "${BOLD}Storing secrets in vault...${RESET}"

	if [[ "$DRY_RUN" != "true" ]]; then
		vault_store_docker "llm-api-key" "$LLM_KEY"
		vault_store_docker "api-key" "$API_KEY"

		if [[ "$SEARCH_PROVIDER" == "brave" && -n "$BRAVE_KEY" ]]; then
			vault_store_docker "brave-search-key" "$BRAVE_KEY"
		fi
	else
		info "[dry-run] Would store vault secrets: llm-api-key, api-key"
		if [[ "$SEARCH_PROVIDER" == "brave" ]]; then
			info "[dry-run] Would store vault secret: brave-search-key"
		fi
	fi

	# ─── Start all services ──────────────────────────────────────────────────

	divider
	info "${BOLD}Starting all services...${RESET}"

	if [[ "$DRY_RUN" != "true" ]]; then
		if [[ "$SEARCH_PROVIDER" == "searxng" ]]; then
			COMPOSE_PROFILES=search docker compose --project-directory "$PROJECT_DIR" up -d
		else
			docker compose --project-directory "$PROJECT_DIR" up -d
		fi

		# Wait a moment for services to start
		sleep 3

		# Health check
		info "Checking service health..."
		for svc in ircd murmur-server murmur-client; do
			if docker compose --project-directory "$PROJECT_DIR" ps --format '{{.Service}} {{.Status}}' 2>/dev/null | grep -q "$svc.*Up"; then
				success "$svc is running"
			else
				warn "$svc may not be running — check with: docker compose ps"
			fi
		done
	else
		info "[dry-run] Would run: docker compose up -d"
	fi

else
	# ─── Bare metal mode ─────────────────────────────────────────────────────

	SERVER_CONFIG="$(generate_server_config)"
	CLIENT_CONFIG="$(generate_client_config)"
	PERMISSIONS_CONFIG="$(generate_permissions_config)"

	write_file "$DEFAULT_DATA_DIR/server.toml" "$SERVER_CONFIG"
	write_file "$DEFAULT_DATA_DIR/client.toml" "$CLIENT_CONFIG"
	write_file "$DEFAULT_DATA_DIR/permissions.toml" "$PERMISSIONS_CONFIG"

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

		if [[ "$SEARCH_PROVIDER" == "brave" && -n "$BRAVE_KEY" ]]; then
			vault_store_bare "brave-search-key" "$BRAVE_KEY"
		fi
	else
		info "[dry-run] Would store vault secrets"
	fi
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
	if [[ -n "$IRC_SERVER_PASS" ]]; then
		echo ""
		warn "ergo.generated.yaml was created with the server password."
		warn "Update docker-compose.yml to mount it instead of ergo.yaml:"
		echo "    ${DIM}- ./configs/ergo.generated.yaml:/ircd/ircd.yaml:ro${RESET}"
	fi
	echo ""
	echo "  ${BOLD}Connect your IRC client:${RESET}"
	echo "    Server:   ${CYAN}localhost:6667${RESET}"
	if [[ -n "$IRC_SERVER_PASS" ]]; then
		echo "    Password: ${DIM}(the server password you set)${RESET}"
	fi
	echo "    Nick:     ${CYAN}$ADMIN_NICK${RESET}"
	echo "    Channel:  ${CYAN}#murmur${RESET}"
	echo ""
	echo "  ${BOLD}After connecting, identify with NickServ:${RESET}"
	echo "    ${DIM}/msg NickServ IDENTIFY $ADMIN_NICK <your-password>${RESET}"
	echo ""
	echo "  ${BOLD}Manage services:${RESET}"
	echo "    ${DIM}docker compose ps${RESET}          — check status"
	echo "    ${DIM}docker compose logs -f${RESET}     — view logs"
	echo "    ${DIM}docker compose restart${RESET}     — restart all"
	echo "    ${DIM}docker compose down${RESET}        — stop all"
	echo ""
else
	echo "  ${BOLD}Config files:${RESET}"
	echo "    Server:      ${DIM}$DEFAULT_DATA_DIR/server.toml${RESET}"
	echo "    Client:      ${DIM}$DEFAULT_DATA_DIR/client.toml${RESET}"
	echo "    Permissions: ${DIM}$DEFAULT_DATA_DIR/permissions.toml${RESET}"
	echo ""
	echo "  ${BOLD}Start the server:${RESET}"
	echo "    ${DIM}MURMUR_VAULT_PASS=$VAULT_PASS $PROJECT_DIR/bin/murmur server --config $DEFAULT_DATA_DIR/server.toml${RESET}"
	echo ""
	echo "  ${BOLD}Start the client (separate terminal):${RESET}"
	echo "    ${DIM}MURMUR_VAULT_PASS=$VAULT_PASS $PROJECT_DIR/bin/murmur client --config $DEFAULT_DATA_DIR/client.toml${RESET}"
	echo ""
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
