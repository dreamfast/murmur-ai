package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VaultOpener is a function that opens a vault database and returns a
// VaultGetter. This indirection allows testing without a real vault.
type VaultOpener func(dbPath, passphrase string) (VaultGetter, error)

// VaultGetter retrieves a decrypted value by key from the vault.
type VaultGetter interface {
	Get(key string) (string, error)
	Close() error
}

// ResolveVaultRef resolves a configuration value that may contain a "vault:"
// prefix. If the value starts with "vault:", the key name is extracted and
// looked up in the vault database at ~/.murmur/vault.db using the passphrase
// from the MURMUR_VAULT_PASS environment variable. If the value does not have
// the prefix, it is returned as-is.
func ResolveVaultRef(value string, opener VaultOpener) (string, error) {
	if !strings.HasPrefix(value, "vault:") {
		return value, nil
	}

	key := strings.TrimPrefix(value, "vault:")
	if key == "" {
		return "", fmt.Errorf("ResolveVaultRef: empty vault key in reference %q", value)
	}

	passphrase := os.Getenv("MURMUR_VAULT_PASS")
	if passphrase == "" {
		return "", fmt.Errorf("ResolveVaultRef: MURMUR_VAULT_PASS environment variable is required for vault: references")
	}

	if opener == nil {
		return "", fmt.Errorf("ResolveVaultRef: vault opener is not configured")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ResolveVaultRef: %w", err)
	}
	dbPath := filepath.Join(home, ".murmur", "vault.db")

	v, err := opener(dbPath, passphrase)
	if err != nil {
		return "", fmt.Errorf("ResolveVaultRef: open vault: %w", err)
	}
	defer v.Close()

	resolved, err := v.Get(key)
	if err != nil {
		return "", fmt.Errorf("ResolveVaultRef: get key %q: %w", key, err)
	}

	return resolved, nil
}
