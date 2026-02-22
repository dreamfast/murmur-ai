// Package vault provides encrypted key-value storage for secrets using
// AES-256-GCM encryption with Argon2id key derivation. Secrets are stored
// in a SQLite database with unique nonces per entry.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // Pure Go SQLite driver

	"golang.org/x/crypto/argon2"

	"murmur/internal/config"
	mcrypto "murmur/internal/crypto"
)

// ErrKeyNotFound is returned when a vault key does not exist.
var ErrKeyNotFound = errors.New("vault key not found")

// Argon2id parameters for key derivation.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MB
	argonThreads = 4
	argonKeyLen  = 32 // AES-256
	saltLen      = 16
	nonceLen     = 12 // AES-GCM standard nonce size
)

// Vault provides encrypted key-value storage using AES-256-GCM.
type Vault struct {
	db  *sql.DB
	key []byte // derived encryption key (32 bytes for AES-256)
}

// Open opens or creates a vault at the given database path. It creates the
// schema if needed, reads or generates the Argon2id salt, and derives the
// encryption key from the passphrase. The passphrase is not stored.
func Open(dbPath, passphrase string) (*Vault, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("vault.Open: passphrase must not be empty")
	}

	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("vault.Open: create directory %s: %w", dir, err)
		}
	}

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("vault.Open: %w", err)
	}

	// Limit to a single connection to avoid SQLITE_BUSY errors under
	// concurrent access. SQLite only supports one writer at a time, and
	// modernc.org/sqlite doesn't always handle busy retries gracefully.
	sqlDB.SetMaxOpenConns(1)

	// Enable WAL mode for better concurrent read performance.
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("vault.Open: enable WAL mode: %w", err)
	}

	// Set a busy timeout so concurrent access retries instead of failing
	// immediately with SQLITE_BUSY.
	if _, err := sqlDB.Exec("PRAGMA busy_timeout=5000"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("vault.Open: set busy timeout: %w", err)
	}

	// Create tables if they don't exist.
	if err := createSchema(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("vault.Open: create schema: %w", err)
	}

	// Read or generate the salt.
	salt, err := getOrCreateSalt(sqlDB)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("vault.Open: %w", err)
	}

	// Derive the encryption key using Argon2id.
	key := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return &Vault{db: sqlDB, key: key}, nil
}

// Close closes the vault's database connection and zeros the encryption key
// to reduce the window of memory exposure.
func (v *Vault) Close() error {
	// Zero the key material.
	for i := range v.key {
		v.key[i] = 0
	}

	if v.db == nil {
		return nil
	}
	return v.db.Close()
}

// Set encrypts and stores a value under the given key. If the key already
// exists, its value is replaced.
func (v *Vault) Set(key, value string) error {
	if key == "" {
		return fmt.Errorf("vault.Set: key must not be empty")
	}

	nonce, err := mcrypto.RandomBytes(nonceLen)
	if err != nil {
		return fmt.Errorf("vault.Set: generate nonce: %w", err)
	}

	encrypted, err := v.encrypt([]byte(value), nonce)
	if err != nil {
		return fmt.Errorf("vault.Set: %w", err)
	}

	_, err = v.db.Exec(
		`INSERT INTO vault (key, encrypted_value, nonce, updated)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET
		   encrypted_value = excluded.encrypted_value,
		   nonce = excluded.nonce,
		   updated = CURRENT_TIMESTAMP`,
		key, encrypted, nonce,
	)
	if err != nil {
		return fmt.Errorf("vault.Set: %w", err)
	}
	return nil
}

// Get decrypts and returns the value for the given key. Returns
// ErrKeyNotFound if the key does not exist.
func (v *Vault) Get(key string) (string, error) {
	var encrypted, nonce []byte
	err := v.db.QueryRow(
		`SELECT encrypted_value, nonce FROM vault WHERE key = ?`, key,
	).Scan(&encrypted, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrKeyNotFound
	}
	if err != nil {
		return "", fmt.Errorf("vault.Get: %w", err)
	}

	plaintext, err := v.decrypt(encrypted, nonce)
	if err != nil {
		return "", fmt.Errorf("vault.Get: %w", err)
	}
	return string(plaintext), nil
}

// Delete removes the entry for the given key. It is not an error if the key
// does not exist.
func (v *Vault) Delete(key string) error {
	_, err := v.db.Exec(`DELETE FROM vault WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("vault.Delete: %w", err)
	}
	return nil
}

// List returns all keys in the vault, sorted alphabetically. Values are not
// returned.
func (v *Vault) List() ([]string, error) {
	rows, err := v.db.Query(`SELECT key FROM vault ORDER BY key ASC`)
	if err != nil {
		return nil, fmt.Errorf("vault.List: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("vault.List: scan: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vault.List: %w", err)
	}
	return keys, nil
}

// ResolveVaultRefs walks specific config fields and replaces "vault:keyname"
// references with the decrypted value from the vault. Fields resolved:
// LLM.Providers[*].APIKey, Security.BusKey, IRC.Password, IRC.NickServPassword,
// Tools.WebSearch.APIKey, Tools.MailSend.SMTPPass, Tools.OpenCode.Password.
func ResolveVaultRefs(v *Vault, cfg *config.ServerConfig) error {
	if v == nil {
		return fmt.Errorf("ResolveVaultRefs: vault is nil")
	}
	if cfg == nil {
		return fmt.Errorf("ResolveVaultRefs: config is nil")
	}

	// Resolve LLM provider API keys.
	for name, prov := range cfg.LLM.Providers {
		if ref, ok := parseVaultRef(prov.APIKey); ok {
			val, err := v.Get(ref)
			if err != nil {
				return fmt.Errorf("ResolveVaultRefs: provider %q api_key: %w", name, err)
			}
			prov.APIKey = val
			cfg.LLM.Providers[name] = prov
		}
	}

	// Resolve Security.BusKey.
	if ref, ok := parseVaultRef(cfg.Security.BusKey); ok {
		val, err := v.Get(ref)
		if err != nil {
			return fmt.Errorf("ResolveVaultRefs: security.bus_key: %w", err)
		}
		cfg.Security.BusKey = val
	}

	// Resolve IRC.Password.
	if ref, ok := parseVaultRef(cfg.IRC.Password); ok {
		val, err := v.Get(ref)
		if err != nil {
			return fmt.Errorf("ResolveVaultRefs: irc.password: %w", err)
		}
		cfg.IRC.Password = val
	}

	// Resolve IRC.NickServPassword.
	if ref, ok := parseVaultRef(cfg.IRC.NickServPassword); ok {
		val, err := v.Get(ref)
		if err != nil {
			return fmt.Errorf("ResolveVaultRefs: irc.nickserv_password: %w", err)
		}
		cfg.IRC.NickServPassword = val
	}

	// Resolve IRC.OperPassword.
	if ref, ok := parseVaultRef(cfg.IRC.OperPassword); ok {
		val, err := v.Get(ref)
		if err != nil {
			return fmt.Errorf("ResolveVaultRefs: irc.oper_password: %w", err)
		}
		cfg.IRC.OperPassword = val
	}

	// Resolve tool secrets. With the unified ToolsConfig, the server may
	// have any tool configured, so we resolve all known secret fields.
	if cfg.Tools.WebSearch != nil && cfg.Tools.WebSearch.APIKey != "" {
		if ref, ok := parseVaultRef(cfg.Tools.WebSearch.APIKey); ok {
			val, err := v.Get(ref)
			if err != nil {
				return fmt.Errorf("ResolveVaultRefs: tools.web_search.api_key: %w", err)
			}
			cfg.Tools.WebSearch.APIKey = val
		}
	}

	if cfg.Tools.MailSend != nil && cfg.Tools.MailSend.SMTPPass != "" {
		if ref, ok := parseVaultRef(cfg.Tools.MailSend.SMTPPass); ok {
			val, err := v.Get(ref)
			if err != nil {
				return fmt.Errorf("ResolveVaultRefs: tools.mail_send.smtp_password: %w", err)
			}
			cfg.Tools.MailSend.SMTPPass = val
		}
	}

	if cfg.Tools.OpenCode != nil && cfg.Tools.OpenCode.Password != "" {
		if ref, ok := parseVaultRef(cfg.Tools.OpenCode.Password); ok {
			val, err := v.Get(ref)
			if err != nil {
				return fmt.Errorf("ResolveVaultRefs: tools.opencode.password: %w", err)
			}
			cfg.Tools.OpenCode.Password = val
		}
	}

	return nil
}

// ResolveClientVaultRefs walks specific client config fields and replaces
// "vault:keyname" references with the decrypted value from the vault. Fields
// resolved: Tools.WebSearch.APIKey, Tools.MailSend.SMTPPass,
// Tools.OpenCode.Password, Security.BusKey, IRC.Password, IRC.NickServPassword.
func ResolveClientVaultRefs(v *Vault, cfg *config.ClientConfig) error {
	if v == nil {
		return fmt.Errorf("ResolveClientVaultRefs: vault is nil")
	}
	if cfg == nil {
		return fmt.Errorf("ResolveClientVaultRefs: config is nil")
	}

	// Resolve Security.BusKey.
	if ref, ok := parseVaultRef(cfg.Security.BusKey); ok {
		val, err := v.Get(ref)
		if err != nil {
			return fmt.Errorf("ResolveClientVaultRefs: security.bus_key: %w", err)
		}
		cfg.Security.BusKey = val
	}

	// Resolve IRC.Password.
	if ref, ok := parseVaultRef(cfg.IRC.Password); ok {
		val, err := v.Get(ref)
		if err != nil {
			return fmt.Errorf("ResolveClientVaultRefs: irc.password: %w", err)
		}
		cfg.IRC.Password = val
	}

	// Resolve IRC.NickServPassword.
	if ref, ok := parseVaultRef(cfg.IRC.NickServPassword); ok {
		val, err := v.Get(ref)
		if err != nil {
			return fmt.Errorf("ResolveClientVaultRefs: irc.nickserv_password: %w", err)
		}
		cfg.IRC.NickServPassword = val
	}

	// Resolve tool secrets.
	if cfg.Tools.WebSearch != nil && cfg.Tools.WebSearch.APIKey != "" {
		if ref, ok := parseVaultRef(cfg.Tools.WebSearch.APIKey); ok {
			val, err := v.Get(ref)
			if err != nil {
				return fmt.Errorf("ResolveClientVaultRefs: tools.web_search.api_key: %w", err)
			}
			cfg.Tools.WebSearch.APIKey = val
		}
	}

	if cfg.Tools.MailSend != nil && cfg.Tools.MailSend.SMTPPass != "" {
		if ref, ok := parseVaultRef(cfg.Tools.MailSend.SMTPPass); ok {
			val, err := v.Get(ref)
			if err != nil {
				return fmt.Errorf("ResolveClientVaultRefs: tools.mail_send.smtp_password: %w", err)
			}
			cfg.Tools.MailSend.SMTPPass = val
		}
	}

	if cfg.Tools.OpenCode != nil && cfg.Tools.OpenCode.Password != "" {
		if ref, ok := parseVaultRef(cfg.Tools.OpenCode.Password); ok {
			val, err := v.Get(ref)
			if err != nil {
				return fmt.Errorf("ResolveClientVaultRefs: tools.opencode.password: %w", err)
			}
			cfg.Tools.OpenCode.Password = val
		}
	}

	return nil
}

// parseVaultRef checks if a string has the "vault:" prefix and returns the
// key name. Returns ("", false) if the string is not a vault reference.
func parseVaultRef(s string) (string, bool) {
	if strings.HasPrefix(s, "vault:") {
		return strings.TrimPrefix(s, "vault:"), true
	}
	return "", false
}

// encrypt encrypts plaintext using AES-256-GCM with the given nonce.
func (v *Vault) encrypt(plaintext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	if len(nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("encrypt: nonce length %d, want %d", len(nonce), aead.NonceSize())
	}
	return aead.Seal(nil, nonce, plaintext, nil), nil
}

// decrypt decrypts ciphertext using AES-256-GCM with the given nonce.
func (v *Vault) decrypt(ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	if len(nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("decrypt: nonce length %d, want %d", len(nonce), aead.NonceSize())
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// createSchema creates the vault tables if they don't exist.
func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vault_meta (
			key TEXT PRIMARY KEY,
			value BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS vault (
			key TEXT PRIMARY KEY,
			encrypted_value BLOB NOT NULL,
			nonce BLOB NOT NULL,
			created DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}

// getOrCreateSalt reads the salt from vault_meta, or generates and stores a
// new one if it doesn't exist. Uses INSERT OR IGNORE to avoid TOCTOU races
// when two processes open the vault simultaneously.
func getOrCreateSalt(db *sql.DB) ([]byte, error) {
	var salt []byte
	err := db.QueryRow(`SELECT value FROM vault_meta WHERE key = 'salt'`).Scan(&salt)
	if err == nil {
		return salt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getOrCreateSalt: read: %w", err)
	}

	// Generate a new salt.
	salt, err = mcrypto.RandomBytes(saltLen)
	if err != nil {
		return nil, fmt.Errorf("getOrCreateSalt: generate: %w", err)
	}

	// Use INSERT OR IGNORE to handle the race where another process inserted
	// a salt between our SELECT and this INSERT.
	_, err = db.Exec(`INSERT OR IGNORE INTO vault_meta (key, value) VALUES ('salt', ?)`, salt)
	if err != nil {
		return nil, fmt.Errorf("getOrCreateSalt: store: %w", err)
	}

	// Re-read to get the actual salt (ours or the other process's).
	err = db.QueryRow(`SELECT value FROM vault_meta WHERE key = 'salt'`).Scan(&salt)
	if err != nil {
		return nil, fmt.Errorf("getOrCreateSalt: re-read: %w", err)
	}
	return salt, nil
}
