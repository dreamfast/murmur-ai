package config

import (
	"errors"
	"testing"
)

// mockVaultGetter is a test double for VaultGetter.
type mockVaultGetter struct {
	store map[string]string
}

func (m *mockVaultGetter) Get(key string) (string, error) {
	v, ok := m.store[key]
	if !ok {
		return "", errors.New("vault key not found")
	}
	return v, nil
}

func (m *mockVaultGetter) Close() error { return nil }

func mockOpener(store map[string]string) VaultOpener {
	return func(dbPath, passphrase string) (VaultGetter, error) {
		return &mockVaultGetter{store: store}, nil
	}
}

func TestVaultResolve_PlainValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{"plain_string", "my-password"},
		{"empty", ""},
		{"no_prefix", "not-a-vault-ref"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveVaultRef(tt.value, nil) // opener should not be called
			if err != nil {
				t.Fatalf("ResolveVaultRef(%q): %v", tt.value, err)
			}
			if got != tt.value {
				t.Errorf("ResolveVaultRef(%q) = %q, want %q", tt.value, got, tt.value)
			}
		})
	}
}

func TestVaultResolve_VaultPrefix(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().
	store := map[string]string{
		"smtp-password": "s3cret",
		"api-key":       "key123",
	}

	t.Setenv("MURMUR_VAULT_PASS", "test-passphrase")

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"smtp_password", "vault:smtp-password", "s3cret"},
		{"api_key", "vault:api-key", "key123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveVaultRef(tt.value, mockOpener(store))
			if err != nil {
				t.Fatalf("ResolveVaultRef(%q): %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("ResolveVaultRef(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestVaultResolve_EmptyKey(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().
	t.Setenv("MURMUR_VAULT_PASS", "test-passphrase")

	_, err := ResolveVaultRef("vault:", mockOpener(nil))
	if err == nil {
		t.Fatal("expected error for empty vault key")
	}
}

func TestVaultResolve_MissingEnvVar(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().
	t.Setenv("MURMUR_VAULT_PASS", "")

	_, err := ResolveVaultRef("vault:some-key", mockOpener(nil))
	if err == nil {
		t.Fatal("expected error when MURMUR_VAULT_PASS is not set")
	}
}

func TestVaultResolve_KeyNotFound(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv().
	t.Setenv("MURMUR_VAULT_PASS", "test-passphrase")

	_, err := ResolveVaultRef("vault:nonexistent", mockOpener(map[string]string{}))
	if err == nil {
		t.Fatal("expected error for nonexistent vault key")
	}
}
