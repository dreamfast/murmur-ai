#!/usr/bin/env bash
# murmur.sh — Helper script for managing the Murmur stack.
#
# Wraps Docker Compose for the common case (start/stop/restart/reload services,
# manage vault secrets, view logs, check status). When Docker is not available,
# falls back to managing bare-metal processes via the compiled binary.
#
# Usage:
#   ./murmur.sh <command> [args...]
#
# Commands:
#   start   [service...]   Start all services (or specific ones)
#   stop    [service...]   Stop all services (or specific ones)
#   restart [service...]   Restart all services (or specific ones)
#   reload                 Hot-reload server config (SIGHUP)
#   status                 Show service status
#   logs    [service...]   Tail logs (all or specific services)
#   build                  Build/rebuild Docker images
#   vault   <sub> [args]   Manage vault secrets (set/get/list/delete)
#   send    "message"      Send a message to the agent and print response
#   shell                  Open a shell in the server container
#   piston-setup           Install Piston language runtimes
#   update                 Pull latest code, rebuild, and restart
#   help                   Show this help
#
# Services: ircd, piston, murmur-server, murmur-client, browser, searxng, opencode
#
# Environment:
#   MURMUR_DIR             Project directory (default: script's directory)
#   MURMUR_VAULT_PASS      Vault passphrase (prompted if not set for vault commands)
#   COMPOSE_PROFILES       Docker Compose profiles (read from .env if present)

set -euo pipefail

# ─── Constants ────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="${MURMUR_DIR:-$SCRIPT_DIR}"

# Core services (always started with `start`)
# murmur-client is only included if its config exists (not everyone runs a client)
CORE_SERVICES=(ircd piston murmur-server)
if [[ -f "$PROJECT_DIR/configs/client.docker.toml" ]]; then
	CORE_SERVICES+=(murmur-client)
fi

# Profile-to-service mapping for optional services
# Docker Compose profiles gate these; we resolve them so `start` brings up everything configured.
declare -A PROFILE_SERVICES=(
	[browser]=browser
	[search]=searxng
	[opencode]=opencode
	[full]="browser searxng opencode"
)

# resolve_profile_services — reads COMPOSE_PROFILES from env or .env and
# returns the corresponding service names.
resolve_profile_services() {
	local profiles="${COMPOSE_PROFILES:-}"

	# Fall back to .env file
	if [[ -z "$profiles" && -f "$PROJECT_DIR/.env" ]]; then
		profiles="$(grep -oP '^COMPOSE_PROFILES=\K.*' "$PROJECT_DIR/.env" 2>/dev/null)" || true
	fi

	if [[ -z "$profiles" ]]; then
		return
	fi

	local IFS=','
	local -a profile_list
	read -ra profile_list <<<"$profiles"

	local -A seen=()
	for profile in "${profile_list[@]}"; do
		profile="$(echo "$profile" | tr -d ' ')"
		local svcs="${PROFILE_SERVICES[$profile]:-}"
		for svc in $svcs; do
			if [[ -z "${seen[$svc]:-}" ]]; then
				echo "$svc"
				seen[$svc]=1
			fi
		done
	done
}

# ─── Color helpers ────────────────────────────────────────────────────────────

if [[ -t 1 ]] && command -v tput &>/dev/null; then
	BOLD="$(tput bold)"
	DIM="$(tput dim)"
	RESET="$(tput sgr0)"
	RED="$(tput setaf 1)"
	GREEN="$(tput setaf 2)"
	YELLOW="$(tput setaf 3)"
	BLUE="$(tput setaf 4)"
	CYAN="$(tput setaf 6)"
else
	# shellcheck disable=SC2034
	BOLD="" DIM="" RESET="" RED="" GREEN="" YELLOW="" BLUE="" CYAN=""
fi

info() { echo "  ${BLUE}${BOLD}>>>${RESET} $*"; }
success() { echo "  ${GREEN}${BOLD} ok${RESET} $*"; }
warn() { echo "  ${YELLOW}${BOLD}  !${RESET} $*"; }
err() { echo "  ${RED}${BOLD}err${RESET} $*" >&2; }

# ─── Mode detection ───────────────────────────────────────────────────────────

MODE="" # "docker" or "bare"

detect_mode() {
	if command -v docker &>/dev/null && [[ -f "$PROJECT_DIR/docker-compose.yml" ]]; then
		# Check if Docker daemon is reachable
		if docker info &>/dev/null 2>&1; then
			MODE="docker"
			return
		fi
	fi

	# Fall back to bare metal
	if [[ -x "$PROJECT_DIR/bin/murmur" ]]; then
		MODE="bare"
		return
	fi

	# Try to build it
	if command -v go &>/dev/null; then
		MODE="bare"
		return
	fi

	err "Neither Docker nor a compiled murmur binary found."
	err "Install Docker or run 'make build' first."
	exit 1
}

# ─── Docker Compose wrapper ──────────────────────────────────────────────────

compose() {
	docker compose --project-directory "$PROJECT_DIR" "$@"
}

# ─── Commands: Docker mode ────────────────────────────────────────────────────

docker_start() {
	local services=("$@")
	if [[ ${#services[@]} -eq 0 ]]; then
		services=("${CORE_SERVICES[@]}")
		# Add profile-gated services (browser, searxng, opencode) if configured
		local profile_svc
		while IFS= read -r profile_svc; do
			[[ -n "$profile_svc" ]] && services+=("$profile_svc")
		done < <(resolve_profile_services)
	fi

	info "Starting: ${services[*]}"
	compose up -d "${services[@]}"

	# Brief pause then health check
	sleep 2
	info "Checking health..."
	for svc in "${services[@]}"; do
		local status
		status="$(compose ps "$svc" --format '{{.Status}}' 2>/dev/null)" || true
		if [[ "$status" == *"Up"* ]]; then
			success "$svc is running"
		elif [[ -z "$status" ]]; then
			warn "$svc — not found (may be profile-gated)"
		else
			warn "$svc — $status"
		fi
	done
}

docker_stop() {
	local services=("$@")
	if [[ ${#services[@]} -eq 0 ]]; then
		info "Stopping all services..."
		compose down
	else
		info "Stopping: ${services[*]}"
		compose stop "${services[@]}"
	fi
	success "Done"
}

docker_restart() {
	local services=("$@")
	if [[ ${#services[@]} -eq 0 ]]; then
		info "Restarting all services..."
		compose down
		# Rebuild full service list including profile services
		local all_services=("${CORE_SERVICES[@]}")
		local profile_svc
		while IFS= read -r profile_svc; do
			[[ -n "$profile_svc" ]] && all_services+=("$profile_svc")
		done < <(resolve_profile_services)
		compose up -d "${all_services[@]}"
	else
		info "Restarting: ${services[*]}"
		compose restart "${services[@]}"
	fi
	success "Done"
}

docker_reload() {
	info "Sending SIGHUP to murmur-server for config reload..."
	local cid
	cid="$(compose ps -q murmur-server 2>/dev/null)" || true
	if [[ -z "$cid" ]]; then
		err "murmur-server is not running"
		exit 1
	fi
	docker kill --signal=SIGHUP "$cid"
	success "Config reload signal sent"
}

docker_status() {
	compose ps --format "table {{.Service}}\t{{.Status}}\t{{.Ports}}"
}

docker_logs() {
	local services=("$@")
	if [[ ${#services[@]} -eq 0 ]]; then
		compose logs -f --tail=100
	else
		compose logs -f --tail=100 "${services[@]}"
	fi
}

docker_build() {
	info "Building Docker images..."
	compose build
	success "Build complete"
}

docker_vault() {
	local subcmd="${1:-}"
	shift || true
	local args=("$@")

	if [[ -z "$subcmd" ]]; then
		err "Usage: murmur.sh vault <set|get|list|delete> [args]"
		exit 1
	fi

	# Ensure vault passphrase is available
	local vault_pass="${MURMUR_VAULT_PASS:-}"
	if [[ -z "$vault_pass" ]]; then
		# Try to read from .env
		if [[ -f "$PROJECT_DIR/.env" ]]; then
			vault_pass="$(grep -oP '^MURMUR_VAULT_PASS=\K.*' "$PROJECT_DIR/.env" 2>/dev/null)" || true
		fi
	fi

	if [[ -z "$vault_pass" ]]; then
		printf "Vault passphrase: " >&2
		read -rs vault_pass
		echo "" >&2
	fi

	if [[ -z "$vault_pass" ]]; then
		err "Vault passphrase required (set MURMUR_VAULT_PASS or add to .env)"
		exit 1
	fi

	case "$subcmd" in
	set)
		local key="${args[0]:-}"
		if [[ -z "$key" ]]; then
			printf "Key: " >&2
			read -r key
		fi
		if [[ -z "$key" ]]; then
			err "Key must not be empty"
			exit 1
		fi

		local value=""
		if [[ ! -t 0 ]]; then
			# Piped input
			read -r value
		else
			printf "Value: " >&2
			read -rs value
			echo "" >&2
		fi

		if [[ -z "$value" ]]; then
			err "Value must not be empty"
			exit 1
		fi

		printf '%s' "$value" | compose run --rm --no-deps -T \
			-e MURMUR_VAULT_PASS="$vault_pass" \
			murmur-server \
			vault set "$key" --db /data/vault.db
		success "Stored '$key'"
		;;
	get)
		local key="${args[0]:-}"
		if [[ -z "$key" ]]; then
			err "Usage: murmur.sh vault get <key>"
			exit 1
		fi
		compose run --rm --no-deps -T \
			-e MURMUR_VAULT_PASS="$vault_pass" \
			murmur-server \
			vault get "$key" --db /data/vault.db
		;;
	list)
		compose run --rm --no-deps -T \
			-e MURMUR_VAULT_PASS="$vault_pass" \
			murmur-server \
			vault list --db /data/vault.db
		;;
	delete)
		local key="${args[0]:-}"
		if [[ -z "$key" ]]; then
			err "Usage: murmur.sh vault delete <key>"
			exit 1
		fi
		compose run --rm --no-deps -T \
			-e MURMUR_VAULT_PASS="$vault_pass" \
			murmur-server \
			vault delete "$key" --db /data/vault.db
		success "Deleted '$key'"
		;;
	*)
		err "Unknown vault subcommand: $subcmd"
		err "Usage: murmur.sh vault <set|get|list|delete> [args]"
		exit 1
		;;
	esac
}

docker_send() {
	local message="$*"
	if [[ -z "$message" ]]; then
		err "Usage: murmur.sh send \"message\""
		exit 1
	fi
	compose exec murmur-server murmur send --config /etc/murmur/server.toml "$message"
}

docker_shell() {
	info "Opening shell in murmur-server container..."
	compose exec murmur-server /bin/sh
}

docker_piston_setup() {
	info "Installing Piston language runtimes..."
	compose run --rm piston-setup
	success "Piston setup complete"
}

docker_update() {
	info "Pulling latest code..."
	git -C "$PROJECT_DIR" pull --ff-only || {
		warn "git pull failed — continuing with rebuild"
	}

	info "Rebuilding images..."
	compose build

	info "Restarting services..."
	compose down
	local all_services=("${CORE_SERVICES[@]}")
	local profile_svc
	while IFS= read -r profile_svc; do
		[[ -n "$profile_svc" ]] && all_services+=("$profile_svc")
	done < <(resolve_profile_services)
	compose up -d "${all_services[@]}"

	success "Update complete"
}

# ─── Commands: Bare-metal mode ────────────────────────────────────────────────

MURMUR_BIN="$PROJECT_DIR/bin/murmur"
BARE_PID_DIR="${XDG_RUNTIME_DIR:-/tmp}/murmur"

ensure_binary() {
	if [[ ! -x "$MURMUR_BIN" ]]; then
		info "Binary not found, building..."
		make -C "$PROJECT_DIR" build-go-only
		if [[ ! -x "$MURMUR_BIN" ]]; then
			err "Build failed — cannot find $MURMUR_BIN"
			exit 1
		fi
	fi
}

bare_pid_file() {
	echo "$BARE_PID_DIR/$1.pid"
}

bare_start_service() {
	local name="$1"
	shift
	local pidfile
	pidfile="$(bare_pid_file "$name")"

	if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
		warn "$name is already running (PID $(cat "$pidfile"))"
		return
	fi

	mkdir -p "$BARE_PID_DIR"
	local logfile="$BARE_PID_DIR/$name.log"

	nohup "$MURMUR_BIN" "$@" >>"$logfile" 2>&1 &
	local pid=$!
	echo "$pid" >"$pidfile"
	success "$name started (PID $pid, log: $logfile)"
}

bare_stop_service() {
	local name="$1"
	local pidfile
	pidfile="$(bare_pid_file "$name")"

	if [[ ! -f "$pidfile" ]]; then
		warn "$name is not running (no PID file)"
		return
	fi

	local pid
	pid="$(cat "$pidfile")"
	if kill -0 "$pid" 2>/dev/null; then
		kill "$pid"
		# Wait up to 10s for graceful shutdown
		local i=0
		while kill -0 "$pid" 2>/dev/null && ((i < 10)); do
			sleep 1
			((i++))
		done
		if kill -0 "$pid" 2>/dev/null; then
			warn "$name didn't stop gracefully, sending SIGKILL"
			kill -9 "$pid" 2>/dev/null || true
		fi
		success "$name stopped"
	else
		warn "$name was not running (stale PID file)"
	fi
	rm -f "$pidfile"
}

bare_start() {
	ensure_binary
	local services=("$@")

	if [[ ${#services[@]} -eq 0 ]]; then
		services=(server client)
	fi

	for svc in "${services[@]}"; do
		case "$svc" in
		server | murmur-server)
			bare_start_service "server" "server"
			;;
		client | murmur-client)
			bare_start_service "client" "client"
			;;
		*)
			warn "Bare-metal mode only supports 'server' and 'client' (skipping '$svc')"
			;;
		esac
	done
}

bare_stop() {
	local services=("$@")

	if [[ ${#services[@]} -eq 0 ]]; then
		services=(server client)
	fi

	for svc in "${services[@]}"; do
		case "$svc" in
		server | murmur-server) bare_stop_service "server" ;;
		client | murmur-client) bare_stop_service "client" ;;
		*) warn "Bare-metal mode only supports 'server' and 'client' (skipping '$svc')" ;;
		esac
	done
}

bare_restart() {
	bare_stop "$@"
	sleep 1
	bare_start "$@"
}

bare_reload() {
	local pidfile
	pidfile="$(bare_pid_file "server")"
	if [[ ! -f "$pidfile" ]]; then
		err "Server is not running (no PID file)"
		exit 1
	fi

	local pid
	pid="$(cat "$pidfile")"
	if kill -0 "$pid" 2>/dev/null; then
		kill -HUP "$pid"
		success "Config reload signal sent to server (PID $pid)"
	else
		err "Server process $pid is not running"
		rm -f "$pidfile"
		exit 1
	fi
}

bare_status() {
	local found=false
	for name in server client; do
		local pidfile
		pidfile="$(bare_pid_file "$name")"
		if [[ -f "$pidfile" ]]; then
			local pid
			pid="$(cat "$pidfile")"
			if kill -0 "$pid" 2>/dev/null; then
				success "$name is running (PID $pid)"
			else
				warn "$name has a stale PID file ($pid not running)"
			fi
			found=true
		fi
	done

	if [[ "$found" == "false" ]]; then
		info "No murmur processes found"
	fi
}

bare_logs() {
	local services=("$@")
	if [[ ${#services[@]} -eq 0 ]]; then
		services=(server client)
	fi

	local files=()
	for svc in "${services[@]}"; do
		case "$svc" in
		server | murmur-server) files+=("$BARE_PID_DIR/server.log") ;;
		client | murmur-client) files+=("$BARE_PID_DIR/client.log") ;;
		esac
	done

	local existing=()
	for f in "${files[@]}"; do
		if [[ -f "$f" ]]; then
			existing+=("$f")
		fi
	done

	if [[ ${#existing[@]} -eq 0 ]]; then
		warn "No log files found"
		return
	fi

	tail -f "${existing[@]}"
}

bare_vault() {
	ensure_binary
	local subcmd="${1:-}"
	shift || true

	if [[ -z "$subcmd" ]]; then
		err "Usage: murmur.sh vault <set|get|list|delete> [args]"
		exit 1
	fi

	"$MURMUR_BIN" vault "$subcmd" "$@"
}

bare_send() {
	ensure_binary
	local message="$*"
	if [[ -z "$message" ]]; then
		err "Usage: murmur.sh send \"message\""
		exit 1
	fi
	"$MURMUR_BIN" send "$message"
}

bare_build() {
	info "Building murmur binary..."
	make -C "$PROJECT_DIR" build
	success "Build complete: $MURMUR_BIN"
}

bare_update() {
	info "Pulling latest code..."
	git -C "$PROJECT_DIR" pull --ff-only || {
		warn "git pull failed — continuing with rebuild"
	}

	bare_stop
	bare_build
	bare_start

	success "Update complete"
}

# ─── Help ─────────────────────────────────────────────────────────────────────

print_help() {
	cat <<EOF
${BOLD}murmur.sh${RESET} — Murmur stack management

${BOLD}Usage:${RESET}
  ./murmur.sh <command> [args...]

${BOLD}Commands:${RESET}
  ${CYAN}start${RESET}   [service...]   Start all services (or specific ones)
  ${CYAN}stop${RESET}    [service...]   Stop all services (or specific ones)
  ${CYAN}restart${RESET} [service...]   Restart all services (or specific ones)
  ${CYAN}reload${RESET}                 Hot-reload server config (SIGHUP)
  ${CYAN}status${RESET}                 Show service status
  ${CYAN}logs${RESET}    [service...]   Tail logs (all or specific services)
  ${CYAN}build${RESET}                  Build/rebuild images or binary
  ${CYAN}vault${RESET}   <sub> [args]   Manage secrets (set/get/list/delete)
  ${CYAN}send${RESET}    "message"      Send a message to the agent
  ${CYAN}update${RESET}                 Pull, rebuild, and restart
  ${CYAN}help${RESET}                   Show this help

${BOLD}Docker-only commands:${RESET}
  ${CYAN}shell${RESET}                  Open a shell in the server container
  ${CYAN}piston-setup${RESET}           Install Piston language runtimes

${BOLD}Services:${RESET}
  ircd, piston, murmur-server, murmur-client, browser, searxng, opencode

${BOLD}Vault examples:${RESET}
  ./murmur.sh vault set llm-api-key          # prompts for value
  ./murmur.sh vault list                     # list all keys
  ./murmur.sh vault get llm-api-key          # print decrypted value
  echo "sk-..." | ./murmur.sh vault set key  # piped input

${BOLD}Environment:${RESET}
  MURMUR_DIR           Project directory (default: script location)
  MURMUR_VAULT_PASS    Vault passphrase (prompted if not set)
  COMPOSE_PROFILES     Docker Compose profiles (from .env)

${BOLD}Mode:${RESET} $(detect_mode_label)
EOF
}

detect_mode_label() {
	detect_mode
	if [[ "$MODE" == "docker" ]]; then
		echo "${GREEN}Docker${RESET} (docker-compose.yml found, daemon reachable)"
	else
		echo "${YELLOW}Bare metal${RESET} (using local binary)"
	fi
}

# ─── Main dispatch ────────────────────────────────────────────────────────────

main() {
	local cmd="${1:-help}"
	shift || true

	# Help doesn't need mode detection
	if [[ "$cmd" == "help" || "$cmd" == "-h" || "$cmd" == "--help" ]]; then
		print_help
		exit 0
	fi

	detect_mode

	case "$cmd" in
	start)
		if [[ "$MODE" == "docker" ]]; then
			docker_start "$@"
		else bare_start "$@"; fi
		;;
	stop)
		if [[ "$MODE" == "docker" ]]; then
			docker_stop "$@"
		else bare_stop "$@"; fi
		;;
	restart)
		if [[ "$MODE" == "docker" ]]; then
			docker_restart "$@"
		else bare_restart "$@"; fi
		;;
	reload)
		if [[ "$MODE" == "docker" ]]; then
			docker_reload
		else bare_reload; fi
		;;
	status)
		if [[ "$MODE" == "docker" ]]; then
			docker_status
		else bare_status; fi
		;;
	logs)
		if [[ "$MODE" == "docker" ]]; then
			docker_logs "$@"
		else bare_logs "$@"; fi
		;;
	build)
		if [[ "$MODE" == "docker" ]]; then
			docker_build
		else bare_build; fi
		;;
	vault)
		if [[ "$MODE" == "docker" ]]; then
			docker_vault "$@"
		else bare_vault "$@"; fi
		;;
	send)
		if [[ "$MODE" == "docker" ]]; then
			docker_send "$@"
		else bare_send "$@"; fi
		;;
	update)
		if [[ "$MODE" == "docker" ]]; then
			docker_update
		else bare_update; fi
		;;
	shell)
		if [[ "$MODE" != "docker" ]]; then
			err "'shell' command is only available in Docker mode"
			exit 1
		fi
		docker_shell
		;;
	piston-setup)
		if [[ "$MODE" != "docker" ]]; then
			err "'piston-setup' command is only available in Docker mode"
			exit 1
		fi
		docker_piston_setup
		;;
	*)
		err "Unknown command: $cmd"
		echo ""
		print_help
		exit 1
		;;
	esac
}

main "$@"
