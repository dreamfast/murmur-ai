#!/usr/bin/env bash
# vault.sh — Vault secret storage operations.
#
# Sourced by setup.sh; do not execute directly.
# Requires: common.sh (for success, PROJECT_DIR, DEFAULT_DATA_DIR)
# Expects from caller: VAULT_PASS

# vault_store_docker KEY VALUE — stores a secret via stdin (no argv exposure)
# Note: uses docker compose directly instead of compose_cmd because vault
# operations need -T and -e flags on `run`, not profile-aware `up`.
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
