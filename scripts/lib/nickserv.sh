#!/usr/bin/env bash
# nickserv.sh — NickServ and OPER registration with retry logic.
#
# Sourced by setup.sh; do not execute directly.
# Requires: common.sh (for info, success, warn, err)
# Requires: docker.sh (for compose_cmd)
# Requires: vault.sh (for vault_store_docker, used by docker_store_nickserv_secrets)
# Expects from caller: DRY_RUN, IRC_SERVER_PASS, PROJECT_DIR

# ─── NickServ registration ──────────────────────────────────────────────────

# nickserv_register NICK PASSWORD [REALNAME]
# Registers a nick with NickServ via the ircd container.
# Uses a retry loop: 3 attempts, exponential backoff (2s/4s/8s), 15s nc timeout.
# Returns 0 on success (account created or already registered), 1 on failure.
nickserv_register() {
	local nick="$1" password="$2" realname="${3:-Murmur Agent}"
	local max_attempts=3 attempt=0 backoff=2

	if [[ "$DRY_RUN" == "true" ]]; then
		info "[dry-run] Would register nick '$nick' with NickServ"
		return 0
	fi

	while ((attempt < max_attempts)); do
		attempt=$((attempt + 1))
		if ((attempt > 1)); then
			info "Retry $attempt/$max_attempts for '$nick' (waiting ${backoff}s)..."
			sleep "$backoff"
			backoff=$((backoff * 2))
		fi

		local irc_output
		irc_output="$({
			if [[ -n "$IRC_SERVER_PASS" ]]; then
				printf 'PASS %s\r\n' "$IRC_SERVER_PASS"
			fi
			printf 'NICK %s\r\n' "$nick"
			printf 'USER %s 0 * :%s\r\n' "$nick" "$realname"
			# Wait for MOTD to complete before sending NickServ command
			sleep 3
			printf 'PRIVMSG NickServ :REGISTER %s\r\n' "$password"
			# Wait for NickServ response
			sleep 5
			printf 'QUIT :setup\r\n'
		} | compose_cmd exec -T ircd \
			sh -c 'timeout 15 nc localhost 6667 2>/dev/null' 2>&1)" || true

		# Check for success indicators
		if [[ "$irc_output" == *"Account created"* ]] ||
			[[ "$irc_output" == *"920"* ]] ||
			[[ "$irc_output" == *"900"* ]] ||
			[[ "$irc_output" == *"logged in"* ]]; then
			success "Registered nick '$nick' with NickServ"
			return 0
		fi

		# Check if already registered (idempotent — not an error)
		if [[ "$irc_output" == *"already registered"* ]] ||
			[[ "$irc_output" == *"Account already exists"* ]] ||
			[[ "$irc_output" == *"that account already exists"* ]]; then
			success "Nick '$nick' is already registered (skipping)"
			return 0
		fi

		# Check for nick-in-use (someone else has it)
		if [[ "$irc_output" == *"433"* ]]; then
			warn "Nick '$nick' is already in use by another user"
			return 1
		fi

		warn "Attempt $attempt/$max_attempts: could not confirm registration for '$nick'"
	done

	return 1
}

# ─── OPER credential generation ─────────────────────────────────────────────

# generate_bcrypt_hash PASSWORD
# Generates a bcrypt hash using Docker Alpine. Outputs the hash to stdout.
# Returns 1 on failure.
generate_bcrypt_hash() {
	local password="$1"
	local hash
	hash="$(printf '%s' "$password" | docker run --rm -i alpine:3.21 sh -c \
		'pw=$(cat); apk add --no-cache apache2-utils >/dev/null 2>&1; htpasswd -nbBC 4 "" "$pw" | cut -d: -f2')" || true
	if [[ -z "$hash" ]]; then
		return 1
	fi
	printf '%s' "$hash"
}

# ─── Registration orchestration ─────────────────────────────────────────────

# docker_register_nicks — registers admin, bot, and optionally client nicks with NickServ
# Expects from caller: ADMIN_NICK, ADMIN_PASS, BOT_NICKSERV_PASS,
#   CLIENT_NICKSERV_PASS, SETUP_LOCAL_CLIENT
docker_register_nicks() {
	divider
	info "${BOLD}Registering IRC accounts with NickServ...${RESET}"

	if [[ "$DRY_RUN" == "true" ]]; then
		info "[dry-run] Would register admin nick '$ADMIN_NICK'"
		info "[dry-run] Would register bot nick 'murmur'"
		if [[ "${SETUP_LOCAL_CLIENT:-true}" == "true" ]]; then
			info "[dry-run] Would register client nick 'murmur-client'"
		fi
		return 0
	fi

	# Register admin nick
	if ! nickserv_register "$ADMIN_NICK" "$ADMIN_PASS" "Murmur Admin"; then
		warn "Could not register admin nick '$ADMIN_NICK' — you may need to register manually:"
		warn "  /msg NickServ REGISTER <password>"
	fi

	# Register bot nick (murmur)
	if ! nickserv_register "murmur" "$BOT_NICKSERV_PASS" "Murmur Agent"; then
		warn "Could not register bot nick 'murmur' — the bot may not be able to identify with NickServ."
		warn "  Register manually: /msg NickServ REGISTER <password>"
	fi

	# Register client nick (murmur-client) — only when local client is enabled
	if [[ "${SETUP_LOCAL_CLIENT:-true}" == "true" ]]; then
		if ! nickserv_register "murmur-client" "$CLIENT_NICKSERV_PASS" "Murmur Client"; then
			warn "Could not register client nick 'murmur-client' — the client may not be able to identify."
			warn "  Register manually: /msg NickServ REGISTER <password>"
		fi
	fi
}

# docker_store_nickserv_secrets — stores NickServ and OPER secrets in vault
# Expects from caller: BOT_NICKSERV_PASS, CLIENT_NICKSERV_PASS, OPER_PASS,
#   SETUP_LOCAL_CLIENT
docker_store_nickserv_secrets() {
	if [[ "$DRY_RUN" == "true" ]]; then
		local secrets="nickserv-password, oper-password"
		if [[ "${SETUP_LOCAL_CLIENT:-true}" == "true" ]]; then
			secrets="nickserv-password, client-nickserv-password, oper-password"
		fi
		info "[dry-run] Would store vault secrets: $secrets"
		return 0
	fi

	if [[ -n "$BOT_NICKSERV_PASS" ]]; then
		vault_store_docker "nickserv-password" "$BOT_NICKSERV_PASS"
	fi

	if [[ -n "$CLIENT_NICKSERV_PASS" ]]; then
		vault_store_docker "client-nickserv-password" "$CLIENT_NICKSERV_PASS"
	fi

	if [[ -n "$OPER_PASS" ]]; then
		vault_store_docker "oper-password" "$OPER_PASS"
	fi
}
