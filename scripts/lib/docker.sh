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

# docker_start_all — starts services (respects --dry-run and SETUP_LOCAL_CLIENT)
# When SETUP_LOCAL_CLIENT is false, murmur-client is excluded from startup.
# Expects from caller: SETUP_LOCAL_CLIENT
docker_start_all() {
	divider
	info "${BOLD}Starting all services...${RESET}"

	# Build the list of services to start and health-check
	local services=("ircd" "murmur-server")
	if [[ "${SETUP_LOCAL_CLIENT:-true}" == "true" ]]; then
		services+=("murmur-client")
	fi

	if [[ "$DRY_RUN" != "true" ]]; then
		compose_cmd up -d "${services[@]}"

		# Wait a moment for services to start
		sleep 3

		# Health check
		info "Checking service health..."
		for svc in "${services[@]}"; do
			if compose_cmd ps --format '{{.Service}} {{.Status}}' 2>/dev/null | grep -q "$svc.*Up"; then
				success "$svc is running"
			else
				warn "$svc may not be running — check with: docker compose ps"
			fi
		done
	else
		info "[dry-run] Would run: docker compose up -d ${services[*]}"
	fi
}

# ─── Ergo config management ─────────────────────────────────────────────────

# docker_setup_ergo_config — copies ergo.yaml to ergo.generated.yaml and
# applies IRC server password and OPER credentials if set.
# Expects: IRC_SERVER_PASS, OPER_BCRYPT_HASH (from nickserv.sh)
docker_setup_ergo_config() {
	if [[ "$DRY_RUN" != "true" ]]; then
		cp "$CONFIGS_DIR/ergo.yaml" "$CONFIGS_DIR/ergo.generated.yaml"
	fi

	if [[ -n "$IRC_SERVER_PASS" ]]; then
		info "Setting IRC server password in Ergo config..."
		if [[ "$DRY_RUN" != "true" ]]; then
			local bcrypt_hash
			bcrypt_hash="$(generate_bcrypt_hash "$IRC_SERVER_PASS")"
			if [[ -n "$bcrypt_hash" ]]; then
				# Bcrypt hashes contain $ which sed interprets as backrefs; use awk.
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

	# Update OPER password hash in ergo config (context-aware: only under opers.admin)
	if [[ -n "${OPER_BCRYPT_HASH:-}" && "$DRY_RUN" != "true" ]]; then
		info "Setting OPER credentials in Ergo config..."
		# Use context-aware awk: track opers → admin sections, only replace password there.
		# Bcrypt hashes contain $ so we pass the hash via -v to avoid shell interpolation.
		awk -v hash="$OPER_BCRYPT_HASH" '
			/^[a-zA-Z]/ && !/^opers:/ { in_opers=0; in_admin=0 }
			/^opers:/ { in_opers=1 }
			in_opers && /^    [a-zA-Z]/ { in_admin=0 }
			in_opers && /^    admin:/ { in_admin=1 }
			in_admin && /^        password:/ {
				print "        password: \""hash"\""
				next
			}
			{ print }
		' "$CONFIGS_DIR/ergo.generated.yaml" >"$CONFIGS_DIR/ergo.generated.yaml.tmp" &&
			mv "$CONFIGS_DIR/ergo.generated.yaml.tmp" "$CONFIGS_DIR/ergo.generated.yaml"
		success "OPER credentials configured in ergo.generated.yaml"
	elif [[ -n "${OPER_BCRYPT_HASH:-}" && "$DRY_RUN" == "true" ]]; then
		info "[dry-run] Would set OPER credentials in ergo.generated.yaml"
	fi

	# Update docker-compose.yml to mount ergo.generated.yaml instead of ergo.yaml.
	# This ensures the IRC server picks up server password and OPER credentials.
	local compose_file="$PROJECT_DIR/docker-compose.yml"
	if [[ "$DRY_RUN" != "true" ]]; then
		if grep -q 'ergo\.yaml:/ircd/ircd\.yaml' "$compose_file" 2>/dev/null; then
			# Use temp file for portability (BSD sed -i requires different syntax)
			sed 's|ergo\.yaml:/ircd/ircd\.yaml|ergo.generated.yaml:/ircd/ircd.yaml|' \
				"$compose_file" >"$compose_file.tmp" && mv "$compose_file.tmp" "$compose_file"
			success "docker-compose.yml updated to mount ergo.generated.yaml"
		fi
	else
		info "[dry-run] Would update docker-compose.yml to mount ergo.generated.yaml"
	fi
}

# ─── NickServ registration ───────────────────────────────────────────────────
# Registration logic lives in nickserv.sh: docker_register_nicks.

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

		if [[ -n "$IRC_SERVER_PASS" ]]; then
			vault_store_docker "irc-server-password" "$IRC_SERVER_PASS"
		fi
	else
		info "[dry-run] Would store vault secrets: llm-api-key, api-key"
		if [[ "$SEARCH_PROVIDER" == "brave" ]]; then
			info "[dry-run] Would store vault secret: brave-search-key"
		fi
		if [[ -n "$IRC_SERVER_PASS" ]]; then
			info "[dry-run] Would store vault secret: irc-server-password"
		fi
	fi
}
