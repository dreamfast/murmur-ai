#!/usr/bin/env bash
# docker.sh — Docker Compose operations, profile management, build, and
# health checks.
#
# Sourced by setup.sh; do not execute directly.
# Requires: common.sh (for info, success, warn, err, PROJECT_DIR, CONFIGS_DIR)
# Requires: vault.sh (for vault_store_docker)
# Expects from caller: DRY_RUN, IRC_SERVER_PASS, ADMIN_NICK, ADMIN_PASS,
#   LLM_KEY, API_KEY, SEARCH_PROVIDER, BRAVE_KEY, VAULT_PASS

# ─── Profile management ─────────────────────────────────────────────────────

# COMPOSE_PROFILES is built up during setup and exported before compose calls.
DOCKER_PROFILES=""

# add_docker_profile PROFILE — appends a profile to the comma-separated list
# (deduplicates by checking comma-delimited boundaries)
add_docker_profile() {
	local profile="$1"
	if [[ -z "$DOCKER_PROFILES" ]]; then
		DOCKER_PROFILES="$profile"
	elif [[ ",$DOCKER_PROFILES," != *",$profile,"* ]]; then
		DOCKER_PROFILES="$DOCKER_PROFILES,$profile"
	fi
}

# compose_cmd ARGS... — runs docker compose with project dir and profiles
compose_cmd() {
	if [[ -n "$DOCKER_PROFILES" ]]; then
		COMPOSE_PROFILES="$DOCKER_PROFILES" docker compose --project-directory "$PROJECT_DIR" "$@"
	else
		docker compose --project-directory "$PROJECT_DIR" "$@"
	fi
}

# ─── Build ───────────────────────────────────────────────────────────────────

# docker_build — builds all Docker images (respects --dry-run)
docker_build() {
	divider
	info "${BOLD}Building Docker images...${RESET}"

	if [[ "$DRY_RUN" != "true" ]]; then
		compose_cmd build
		success "Docker images built"
	else
		info "[dry-run] Would run: docker compose build"
	fi
}

# ─── IRC server lifecycle ───────────────────────────────────────────────────

# docker_start_ircd — starts only the ircd service and waits for healthy
docker_start_ircd() {
	divider
	info "${BOLD}Starting IRC server...${RESET}"

	if [[ "$DRY_RUN" != "true" ]]; then
		compose_cmd up -d ircd
		info "Waiting for IRC server to be healthy..."

		local retries=0
		while true; do
			local health
			health="$(compose_cmd ps ircd --format '{{.Status}}' 2>/dev/null)"
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
	else
		info "[dry-run] Would start ircd and wait for healthy"
	fi
}

# ─── Start all services ─────────────────────────────────────────────────────

# docker_start_all — starts all services (respects --dry-run)
docker_start_all() {
	divider
	info "${BOLD}Starting all services...${RESET}"

	if [[ "$DRY_RUN" != "true" ]]; then
		compose_cmd up -d

		# Wait a moment for services to start
		sleep 3

		# Health check
		info "Checking service health..."
		for svc in ircd murmur-server murmur-client; do
			if compose_cmd ps --format '{{.Service}} {{.Status}}' 2>/dev/null | grep -q "$svc.*Up"; then
				success "$svc is running"
			else
				warn "$svc may not be running — check with: docker compose ps"
			fi
		done
	else
		info "[dry-run] Would run: docker compose up -d"
	fi
}

# ─── Ergo config management ─────────────────────────────────────────────────

# docker_setup_ergo_config — copies ergo.yaml to ergo.generated.yaml and
# applies IRC server password if set.
docker_setup_ergo_config() {
	if [[ "$DRY_RUN" != "true" ]]; then
		cp "$CONFIGS_DIR/ergo.yaml" "$CONFIGS_DIR/ergo.generated.yaml"
	fi

	if [[ -n "$IRC_SERVER_PASS" ]]; then
		info "Setting IRC server password in Ergo config..."
		if [[ "$DRY_RUN" != "true" ]]; then
			# Generate bcrypt hash via Docker, passing password on stdin.
			# Read stdin into a variable first — apk consumes stdin otherwise.
			local bcrypt_hash
			bcrypt_hash="$(printf '%s' "$IRC_SERVER_PASS" | docker run --rm -i alpine:3.21 sh -c \
				'pw=$(cat); apk add --no-cache apache2-utils >/dev/null 2>&1; htpasswd -nbBC 4 "" "$pw" | cut -d: -f2')" || true
			if [[ -n "$bcrypt_hash" ]]; then
				# Insert password into the generated ergo config under server:
				# Bcrypt hashes contain $ which sed interprets as backrefs; use awk instead.
				if grep -q "^    password:" "$CONFIGS_DIR/ergo.generated.yaml" 2>/dev/null; then
					awk -v hash="$bcrypt_hash" '/^    password:/{print "    password: \""hash"\""; next}1' \
						"$CONFIGS_DIR/ergo.generated.yaml" >"$CONFIGS_DIR/ergo.generated.yaml.tmp" &&
						mv "$CONFIGS_DIR/ergo.generated.yaml.tmp" "$CONFIGS_DIR/ergo.generated.yaml"
				else
					awk -v hash="$bcrypt_hash" '/^server:/{print; print "    password: \""hash"\""; next}1' \
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
}

# ─── NickServ registration (basic — replaced by nickserv.sh in Task 2) ──────

# docker_register_admin — registers the admin nick with NickServ
docker_register_admin() {
	info "${BOLD}Registering admin account with NickServ...${RESET}"

	if [[ "$DRY_RUN" != "true" ]]; then
		info "Registering admin account '$ADMIN_NICK' with NickServ..."
		local irc_output
		irc_output="$({
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
		} | compose_cmd exec -T ircd \
			sh -c 'nc localhost 6667 2>/dev/null' 2>&1)" || true

		if [[ "$irc_output" == *"900"* || "$irc_output" == *"logged in"* || "$irc_output" == *"Account created"* ]]; then
			success "Admin account registered (nick: $ADMIN_NICK)"
		else
			warn "Could not confirm NickServ registration — you may need to register manually:"
			warn "  /msg NickServ REGISTER <password>"
		fi
	else
		info "[dry-run] Would register admin account with NickServ"
	fi
}

# ─── Vault secrets ──────────────────────────────────────────────────────────

# docker_store_secrets — stores all vault secrets for Docker mode
docker_store_secrets() {
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
}
