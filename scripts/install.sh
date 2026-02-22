#!/usr/bin/env bash
# Murmur curl-pipe installer.
# Downloads (or updates) the Murmur repository and runs the setup wizard.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dreamfast/murmur-ai/main/scripts/install.sh | bash
#   curl -fsSL ... | bash -s -- client          # standalone client setup
#   curl -fsSL ... | bash -s -- --dry-run       # preview mode
#
# Environment variables:
#   MURMUR_INSTALL_DIR  — where to clone the repo (default: ~/murmur)
#   MURMUR_BRANCH       — git branch to checkout (default: main)
#   MURMUR_REPO_URL     — git clone URL (default: https://github.com/dreamfast/murmur-ai.git)

set -euo pipefail

# ─── Configuration ───────────────────────────────────────────────────────────

INSTALL_DIR="${MURMUR_INSTALL_DIR:-$HOME/murmur}"
BRANCH="${MURMUR_BRANCH:-main}"
REPO_URL="${MURMUR_REPO_URL:-https://github.com/dreamfast/murmur-ai.git}"

# ─── Colors ──────────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
RESET='\033[0m'

info() { printf "${CYAN}  >${RESET}  %b\n" "$*"; }
success() { printf "${GREEN}  +${RESET}  %b\n" "$*"; }
warn() { printf "${YELLOW}  !${RESET}  %b\n" "$*"; }
err() { printf "${RED}  x${RESET}  %b\n" "$*" >&2; }

# ─── Prerequisite checks ────────────────────────────────────────────────────

check_prereqs() {
	local missing=()

	if ! command -v git &>/dev/null; then
		missing+=("git")
	fi

	if ! command -v docker &>/dev/null; then
		missing+=("docker")
	fi

	if ! docker compose version &>/dev/null 2>&1; then
		missing+=("docker compose v2")
	fi

	if ((${#missing[@]} > 0)); then
		err "Missing prerequisites: ${missing[*]}"
		echo ""
		echo "  Install the following before running this script:"
		for dep in "${missing[@]}"; do
			case "$dep" in
			git) echo "    ${DIM}git:               https://git-scm.com/downloads${RESET}" ;;
			docker) echo "    ${DIM}docker:            https://docs.docker.com/get-docker/${RESET}" ;;
			"docker compose v2") echo "    ${DIM}docker compose v2: https://docs.docker.com/compose/install/${RESET}" ;;
			esac
		done
		echo ""
		exit 1
	fi

	success "Prerequisites found: git, docker, docker compose"
}

# ─── Clone or update ─────────────────────────────────────────────────────────

clone_or_update() {
	if [[ -d "$INSTALL_DIR/.git" ]]; then
		info "Updating existing installation in ${BOLD}$INSTALL_DIR${RESET}..."
		if ! git -C "$INSTALL_DIR" fetch origin "$BRANCH" --quiet 2>/dev/null; then
			warn "Could not fetch from remote — using existing checkout."
			return 0
		fi
		git -C "$INSTALL_DIR" checkout "$BRANCH" --quiet 2>/dev/null || {
			# Branch doesn't exist locally — create tracking branch
			git -C "$INSTALL_DIR" checkout -b "$BRANCH" "origin/$BRANCH" --quiet 2>/dev/null || {
				warn "Could not checkout branch '$BRANCH' — using current branch."
				return 0
			}
		}
		git -C "$INSTALL_DIR" pull --ff-only --quiet 2>/dev/null || {
			warn "Could not fast-forward — local changes may exist. Using existing checkout."
			return 0
		}
		success "Repository updated"
	elif [[ -d "$INSTALL_DIR" ]]; then
		err "$INSTALL_DIR exists but is not a git repository."
		err "Remove it or set MURMUR_INSTALL_DIR to a different path."
		exit 1
	else
		info "Cloning Murmur to ${BOLD}$INSTALL_DIR${RESET}..."
		git clone --branch "$BRANCH" "$REPO_URL" "$INSTALL_DIR" --quiet
		success "Repository cloned"
	fi
}

# ─── Main ────────────────────────────────────────────────────────────────────

echo ""
echo "${BOLD}  Murmur Installer${RESET}"
echo ""

check_prereqs
clone_or_update

echo ""
info "Running setup wizard..."
echo ""

exec bash "$INSTALL_DIR/scripts/setup.sh" "$@"
