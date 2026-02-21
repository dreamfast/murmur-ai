#!/bin/sh
# Murmur first-time setup script.
# Stores your LLM API key in the encrypted vault so the server can use it.
#
# Usage:
#   ./scripts/setup.sh
#
# Or with environment variables:
#   MURMUR_VAULT_PASS=mypass LLM_API_KEY=sk-or-... ./scripts/setup.sh

set -e

# Copy example configs if the live ones don't exist yet.
if [ ! -f configs/server.docker.toml ]; then
	cp configs/server.docker.toml.example configs/server.docker.toml
	echo "Created configs/server.docker.toml from example (edit to customize)."
fi
if [ ! -f configs/client.docker.toml ]; then
	cp configs/client.docker.toml.example configs/client.docker.toml
	echo "Created configs/client.docker.toml from example (edit to customize)."
fi

VAULT_PASS="${MURMUR_VAULT_PASS:-}"
API_KEY="${LLM_API_KEY:-}"

# Prompt for vault passphrase if not set.
if [ -z "$VAULT_PASS" ]; then
	printf "Vault passphrase (used to encrypt secrets): "
	read -r VAULT_PASS
	if [ -z "$VAULT_PASS" ]; then
		echo "Error: passphrase cannot be empty." >&2
		exit 1
	fi
fi

# Prompt for LLM API key if not set.
if [ -z "$API_KEY" ]; then
	printf "LLM API key (OpenRouter, OpenAI, etc.): "
	read -r API_KEY
	if [ -z "$API_KEY" ]; then
		echo "Error: API key cannot be empty." >&2
		exit 1
	fi
fi

export MURMUR_VAULT_PASS="$VAULT_PASS"

echo "Building murmur..."
docker compose build murmur-server

echo ""
echo "Storing API key in vault..."

# Run the vault set command inside the server container, using the server-data
# volume so the vault.db ends up in the right place.
docker compose run --rm \
	-e MURMUR_VAULT_PASS="$VAULT_PASS" \
	murmur-server \
	vault set llm-api-key --db /data/vault.db --value "$API_KEY"

echo ""
echo "Done! Save these for your .env file:"
echo ""
echo "  MURMUR_VAULT_PASS=$VAULT_PASS"
echo ""
echo "Start murmur with:"
echo "  MURMUR_VAULT_PASS=$VAULT_PASS docker compose up -d"
echo ""
echo "Then connect your IRC client to localhost:6667 and join #murmur"
