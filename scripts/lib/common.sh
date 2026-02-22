#!/usr/bin/env bash
# common.sh — Constants, color helpers, output helpers, input helpers, and
# utility functions shared by all setup wizard modules.
#
# Sourced by setup.sh; do not execute directly.
# Expects from caller: SCRIPT_DIR, PROJECT_DIR, TOTAL_STEPS (for step()),
#   DRY_RUN (for write_file/write_file_secure)

# ─── Constants ───────────────────────────────────────────────────────────────
# SCRIPT_DIR and PROJECT_DIR are set by setup.sh before sourcing this file.
# This avoids fragile BASH_SOURCE index assumptions.

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
