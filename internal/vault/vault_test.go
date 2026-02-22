package vault

import (
	"sync"
	"testing"

	"murmur/internal/config"
)

const testPassphrase = "test-passphrase-for-vault"

func openTestVault(t *testing.T) *Vault {
	t.Helper()
	v, err := Open(":memory:", testPassphrase)
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func TestVault_SetAndGet(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	if err := v.Set("api_key", "sk-secret-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := v.Get("api_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk-secret-123" {
		t.Errorf("Get = %q, want %q", got, "sk-secret-123")
	}
}

func TestVault_SetOverwrite(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	if err := v.Set("key", "value1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := v.Set("key", "value2"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}

	got, err := v.Get("key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "value2" {
		t.Errorf("Get = %q, want %q", got, "value2")
	}
}

func TestVault_GetNotFound(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	_, err := v.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	if err != ErrKeyNotFound {
		t.Errorf("error = %v, want ErrKeyNotFound", err)
	}
}

func TestVault_Delete(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	if err := v.Set("key", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := v.Delete("key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := v.Get("key")
	if err != ErrKeyNotFound {
		t.Errorf("Get after Delete: error = %v, want ErrKeyNotFound", err)
	}
}

func TestVault_DeleteNonexistent(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// Should not error.
	if err := v.Delete("nonexistent"); err != nil {
		t.Fatalf("Delete nonexistent: %v", err)
	}
}

func TestVault_List(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// Empty vault.
	keys, err := v.List()
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List empty = %v, want empty", keys)
	}

	// Add some keys.
	for _, k := range []string{"charlie", "alpha", "bravo"} {
		if err := v.Set(k, "val-"+k); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	keys, err = v.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("List len = %d, want 3", len(keys))
	}
	// Should be sorted alphabetically.
	expected := []string{"alpha", "bravo", "charlie"}
	for i, want := range expected {
		if keys[i] != want {
			t.Errorf("List[%d] = %q, want %q", i, keys[i], want)
		}
	}
}

func TestVault_DifferentPassphrase(t *testing.T) {
	t.Parallel()

	// Use a temp file so both vaults share the same salt.
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/vault.db"

	// Open with passphrase A and set a value.
	v1, err := Open(dbPath, "passphrase-A")
	if err != nil {
		t.Fatalf("Open with passphrase A: %v", err)
	}
	if err := v1.Set("secret", "hello"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v1.Close()

	// Open with passphrase B — should derive a different key.
	v2, err := Open(dbPath, "passphrase-B")
	if err != nil {
		t.Fatalf("Open with passphrase B: %v", err)
	}
	defer v2.Close()

	// Get should fail because the decryption key is wrong.
	_, err = v2.Get("secret")
	if err == nil {
		t.Fatal("expected error when decrypting with wrong passphrase")
	}
}

func TestVault_EmptyValue(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	if err := v.Set("empty", ""); err != nil {
		t.Fatalf("Set empty value: %v", err)
	}

	got, err := v.Get("empty")
	if err != nil {
		t.Fatalf("Get empty value: %v", err)
	}
	if got != "" {
		t.Errorf("Get = %q, want empty string", got)
	}
}

func TestVault_LargeValue(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// 10KB value.
	large := make([]byte, 10*1024)
	for i := range large {
		large[i] = byte(i % 256)
	}
	val := string(large)

	if err := v.Set("large", val); err != nil {
		t.Fatalf("Set large: %v", err)
	}

	got, err := v.Get("large")
	if err != nil {
		t.Fatalf("Get large: %v", err)
	}
	if got != val {
		t.Errorf("Get large: length = %d, want %d", len(got), len(val))
	}
}

func TestVault_SetEmptyKey(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	err := v.Set("", "value")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestVault_OpenEmptyPassphrase(t *testing.T) {
	t.Parallel()

	_, err := Open(":memory:", "")
	if err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

func TestVault_ConcurrentSetGet(t *testing.T) {
	t.Parallel()

	// Use a temp file instead of :memory: because in-memory SQLite does not
	// support concurrent access from multiple goroutines reliably.
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/concurrent.db"
	v, err := Open(dbPath, testPassphrase)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { v.Close() })

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n * 2)

	// Concurrent writers.
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			key := "key"
			val := "value"
			if err := v.Set(key, val); err != nil {
				t.Errorf("concurrent Set %d: %v", i, err)
			}
		}(i)
	}

	// Concurrent readers.
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// May get ErrKeyNotFound if writer hasn't run yet — that's OK.
			_, err := v.Get("key")
			if err != nil && err != ErrKeyNotFound {
				t.Errorf("concurrent Get %d: %v", i, err)
			}
		}(i)
	}

	wg.Wait()
}

func TestResolveVaultRefs(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// Store secrets in the vault.
	if err := v.Set("openai_key", "sk-openai-secret"); err != nil {
		t.Fatalf("Set openai_key: %v", err)
	}
	if err := v.Set("bus_secret", "my-bus-key"); err != nil {
		t.Fatalf("Set bus_secret: %v", err)
	}
	if err := v.Set("irc_pass", "irc-password"); err != nil {
		t.Fatalf("Set irc_pass: %v", err)
	}
	if err := v.Set("nickserv_pass", "nickserv-password"); err != nil {
		t.Fatalf("Set nickserv_pass: %v", err)
	}
	if err := v.Set("dash_server_pass", "dashboard-irc-pass"); err != nil {
		t.Fatalf("Set dash_server_pass: %v", err)
	}

	cfg := &config.ServerConfig{
		IRC: config.IRCConfig{
			Server:           "irc.example.com",
			Nick:             "murmur",
			Port:             6697,
			Password:         "vault:irc_pass",
			NickServPassword: "vault:nickserv_pass",
			Channels: config.ChannelsConfig{
				Main: "#murmur",
				Bus:  "#murmur-bus",
			},
		},
		LLM: config.LLMConfig{
			Default: "openai",
			Providers: map[string]config.LLMProviderConfig{
				"openai": {
					APIBase: "https://api.openai.com/v1",
					APIKey:  "vault:openai_key",
					Model:   "gpt-4",
				},
			},
		},
		Security: config.SecurityConfig{
			BusKey: "vault:bus_secret",
		},
		Dashboard: config.DashboardConfig{
			ServerPassword: "vault:dash_server_pass",
		},
		Scheduler: config.SchedulerConfig{
			HeartbeatInterval: "5m",
			ClientTimeout:     "2m",
		},
	}

	if err := ResolveVaultRefs(v, cfg); err != nil {
		t.Fatalf("ResolveVaultRefs: %v", err)
	}

	// Verify all references were resolved.
	if cfg.LLM.Providers["openai"].APIKey != "sk-openai-secret" {
		t.Errorf("APIKey = %q, want %q", cfg.LLM.Providers["openai"].APIKey, "sk-openai-secret")
	}
	if cfg.Security.BusKey != "my-bus-key" {
		t.Errorf("BusKey = %q, want %q", cfg.Security.BusKey, "my-bus-key")
	}
	if cfg.IRC.Password != "irc-password" {
		t.Errorf("IRC.Password = %q, want %q", cfg.IRC.Password, "irc-password")
	}
	if cfg.IRC.NickServPassword != "nickserv-password" {
		t.Errorf("IRC.NickServPassword = %q, want %q", cfg.IRC.NickServPassword, "nickserv-password")
	}
	if cfg.Dashboard.ServerPassword != "dashboard-irc-pass" {
		t.Errorf("Dashboard.ServerPassword = %q, want %q", cfg.Dashboard.ServerPassword, "dashboard-irc-pass")
	}
}

func TestResolveVaultRefs_ToolSecrets(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// Store tool secrets in the vault.
	if err := v.Set("brave_key", "sk-brave-secret"); err != nil {
		t.Fatalf("Set brave_key: %v", err)
	}
	if err := v.Set("smtp_pass", "smtp-secret-password"); err != nil {
		t.Fatalf("Set smtp_pass: %v", err)
	}
	if err := v.Set("opencode_pass", "opencode-secret"); err != nil {
		t.Fatalf("Set opencode_pass: %v", err)
	}

	cfg := &config.ServerConfig{
		IRC: config.IRCConfig{
			Server: "irc.example.com",
			Nick:   "murmur",
			Port:   6697,
			Channels: config.ChannelsConfig{
				Main: "#murmur",
				Bus:  "#murmur-bus",
			},
		},
		Scheduler: config.SchedulerConfig{
			HeartbeatInterval: "5m",
			ClientTimeout:     "2m",
		},
		Tools: config.ToolsConfig{
			WebSearch: &config.WebSearchConfig{
				Enabled: true,
				APIKey:  "vault:brave_key",
			},
			MailSend: &config.MailSendConfig{
				Enabled:     true,
				SMTPHost:    "mail.example.com",
				SMTPPass:    "vault:smtp_pass",
				FromAddress: "test@example.com",
			},
			OpenCode: &config.OpenCodeConfig{
				Enabled:  true,
				URL:      "http://localhost:3000",
				Password: "vault:opencode_pass",
			},
		},
	}

	if err := ResolveVaultRefs(v, cfg); err != nil {
		t.Fatalf("ResolveVaultRefs: %v", err)
	}

	if cfg.Tools.WebSearch.APIKey != "sk-brave-secret" {
		t.Errorf("WebSearch.APIKey = %q, want %q", cfg.Tools.WebSearch.APIKey, "sk-brave-secret")
	}
	if cfg.Tools.MailSend.SMTPPass != "smtp-secret-password" {
		t.Errorf("MailSend.SMTPPass = %q, want %q", cfg.Tools.MailSend.SMTPPass, "smtp-secret-password")
	}
	if cfg.Tools.OpenCode.Password != "opencode-secret" {
		t.Errorf("OpenCode.Password = %q, want %q", cfg.Tools.OpenCode.Password, "opencode-secret")
	}
}

func TestResolveVaultRefs_NilToolSections(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// Config with nil tool sections should not panic.
	cfg := &config.ServerConfig{
		IRC: config.IRCConfig{
			Server: "irc.example.com",
			Nick:   "murmur",
			Port:   6697,
			Channels: config.ChannelsConfig{
				Main: "#murmur",
				Bus:  "#murmur-bus",
			},
		},
		Scheduler: config.SchedulerConfig{
			HeartbeatInterval: "5m",
			ClientTimeout:     "2m",
		},
	}

	if err := ResolveVaultRefs(v, cfg); err != nil {
		t.Fatalf("ResolveVaultRefs with nil tools: %v", err)
	}
}

func TestResolveVaultRefs_NonVaultValues(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	cfg := &config.ServerConfig{
		IRC: config.IRCConfig{
			Server:           "irc.example.com",
			Nick:             "murmur",
			Port:             6697,
			Password:         "plain-password",
			NickServPassword: "",
			Channels: config.ChannelsConfig{
				Main: "#murmur",
				Bus:  "#murmur-bus",
			},
		},
		LLM: config.LLMConfig{
			Default: "openai",
			Providers: map[string]config.LLMProviderConfig{
				"openai": {
					APIBase: "https://api.openai.com/v1",
					APIKey:  "sk-plain-key",
					Model:   "gpt-4",
				},
			},
		},
		Security: config.SecurityConfig{
			BusKey: "plain-bus-key",
		},
		Scheduler: config.SchedulerConfig{
			HeartbeatInterval: "5m",
			ClientTimeout:     "2m",
		},
	}

	if err := ResolveVaultRefs(v, cfg); err != nil {
		t.Fatalf("ResolveVaultRefs: %v", err)
	}

	// Values without vault: prefix should be unchanged.
	if cfg.LLM.Providers["openai"].APIKey != "sk-plain-key" {
		t.Errorf("APIKey = %q, want %q", cfg.LLM.Providers["openai"].APIKey, "sk-plain-key")
	}
	if cfg.Security.BusKey != "plain-bus-key" {
		t.Errorf("BusKey = %q, want %q", cfg.Security.BusKey, "plain-bus-key")
	}
	if cfg.IRC.Password != "plain-password" {
		t.Errorf("IRC.Password = %q, want %q", cfg.IRC.Password, "plain-password")
	}
}

func TestResolveVaultRefs_MissingKey(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	cfg := &config.ServerConfig{
		IRC: config.IRCConfig{
			Server: "irc.example.com",
			Nick:   "murmur",
			Port:   6697,
			Channels: config.ChannelsConfig{
				Main: "#murmur",
				Bus:  "#murmur-bus",
			},
		},
		LLM: config.LLMConfig{
			Default: "openai",
			Providers: map[string]config.LLMProviderConfig{
				"openai": {
					APIBase: "https://api.openai.com/v1",
					APIKey:  "vault:nonexistent_key",
					Model:   "gpt-4",
				},
			},
		},
		Scheduler: config.SchedulerConfig{
			HeartbeatInterval: "5m",
			ClientTimeout:     "2m",
		},
	}

	err := ResolveVaultRefs(v, cfg)
	if err == nil {
		t.Fatal("expected error for missing vault key")
	}
}

func TestResolveClientVaultRefs(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// Store secrets in the vault.
	if err := v.Set("brave_key", "sk-brave-secret"); err != nil {
		t.Fatalf("Set brave_key: %v", err)
	}
	if err := v.Set("smtp_pass", "smtp-secret-password"); err != nil {
		t.Fatalf("Set smtp_pass: %v", err)
	}
	if err := v.Set("opencode_pass", "opencode-secret"); err != nil {
		t.Fatalf("Set opencode_pass: %v", err)
	}
	if err := v.Set("bus_secret", "my-bus-key"); err != nil {
		t.Fatalf("Set bus_secret: %v", err)
	}
	if err := v.Set("irc_pass", "irc-password"); err != nil {
		t.Fatalf("Set irc_pass: %v", err)
	}
	if err := v.Set("nickserv_pass", "nickserv-secret"); err != nil {
		t.Fatalf("Set nickserv_pass: %v", err)
	}

	cfg := &config.ClientConfig{
		Client: config.ClientSection{
			ID:       "test-client",
			Autonomy: "report",
		},
		IRC: config.IRCConfig{
			Server:           "irc.example.com",
			Nick:             "murmur-test",
			Port:             6697,
			BusChannel:       "#murmur-bus",
			Password:         "vault:irc_pass",
			NickServPassword: "vault:nickserv_pass",
		},
		Security: config.ClientSecurityConfig{
			BusKey: "vault:bus_secret",
		},
		Tools: config.ToolsConfig{
			WebSearch: &config.WebSearchConfig{
				Enabled: true,
				APIKey:  "vault:brave_key",
			},
			MailSend: &config.MailSendConfig{
				Enabled:     true,
				SMTPHost:    "mail.example.com",
				SMTPPass:    "vault:smtp_pass",
				FromAddress: "test@example.com",
			},
			OpenCode: &config.OpenCodeConfig{
				Enabled:  true,
				URL:      "http://localhost:3000",
				Password: "vault:opencode_pass",
			},
		},
	}

	if err := ResolveClientVaultRefs(v, cfg); err != nil {
		t.Fatalf("ResolveClientVaultRefs: %v", err)
	}

	// Verify all references were resolved.
	if cfg.Tools.WebSearch.APIKey != "sk-brave-secret" {
		t.Errorf("WebSearch.APIKey = %q, want %q", cfg.Tools.WebSearch.APIKey, "sk-brave-secret")
	}
	if cfg.Tools.MailSend.SMTPPass != "smtp-secret-password" {
		t.Errorf("MailSend.SMTPPass = %q, want %q", cfg.Tools.MailSend.SMTPPass, "smtp-secret-password")
	}
	if cfg.Tools.OpenCode.Password != "opencode-secret" {
		t.Errorf("OpenCode.Password = %q, want %q", cfg.Tools.OpenCode.Password, "opencode-secret")
	}
	if cfg.Security.BusKey != "my-bus-key" {
		t.Errorf("Security.BusKey = %q, want %q", cfg.Security.BusKey, "my-bus-key")
	}
	if cfg.IRC.Password != "irc-password" {
		t.Errorf("IRC.Password = %q, want %q", cfg.IRC.Password, "irc-password")
	}
	if cfg.IRC.NickServPassword != "nickserv-secret" {
		t.Errorf("IRC.NickServPassword = %q, want %q", cfg.IRC.NickServPassword, "nickserv-secret")
	}
}

func TestResolveClientVaultRefs_NonVaultValues(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	cfg := &config.ClientConfig{
		Client: config.ClientSection{
			ID:       "test-client",
			Autonomy: "report",
		},
		IRC: config.IRCConfig{
			Server:     "irc.example.com",
			Nick:       "murmur-test",
			Port:       6697,
			BusChannel: "#murmur-bus",
			Password:   "plain-password",
		},
		Security: config.ClientSecurityConfig{
			BusKey: "plain-bus-key",
		},
		Tools: config.ToolsConfig{
			WebSearch: &config.WebSearchConfig{
				Enabled: true,
				APIKey:  "sk-plain-key",
			},
		},
	}

	if err := ResolveClientVaultRefs(v, cfg); err != nil {
		t.Fatalf("ResolveClientVaultRefs: %v", err)
	}

	// Values without vault: prefix should be unchanged.
	if cfg.Tools.WebSearch.APIKey != "sk-plain-key" {
		t.Errorf("WebSearch.APIKey = %q, want %q", cfg.Tools.WebSearch.APIKey, "sk-plain-key")
	}
	if cfg.Security.BusKey != "plain-bus-key" {
		t.Errorf("Security.BusKey = %q, want %q", cfg.Security.BusKey, "plain-bus-key")
	}
	if cfg.IRC.Password != "plain-password" {
		t.Errorf("IRC.Password = %q, want %q", cfg.IRC.Password, "plain-password")
	}
}

func TestResolveClientVaultRefs_MissingKey(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	cfg := &config.ClientConfig{
		Client: config.ClientSection{
			ID:       "test-client",
			Autonomy: "report",
		},
		IRC: config.IRCConfig{
			Server:     "irc.example.com",
			Nick:       "murmur-test",
			Port:       6697,
			BusChannel: "#murmur-bus",
		},
		Security: config.ClientSecurityConfig{
			BusKey: "vault:nonexistent_key",
		},
	}

	err := ResolveClientVaultRefs(v, cfg)
	if err == nil {
		t.Fatal("expected error for missing vault key")
	}
}

func TestResolveClientVaultRefs_NilToolSections(t *testing.T) {
	t.Parallel()
	v := openTestVault(t)

	// Config with nil tool sections should not panic.
	cfg := &config.ClientConfig{
		Client: config.ClientSection{
			ID:       "test-client",
			Autonomy: "report",
		},
		IRC: config.IRCConfig{
			Server:     "irc.example.com",
			Nick:       "murmur-test",
			Port:       6697,
			BusChannel: "#murmur-bus",
		},
	}

	if err := ResolveClientVaultRefs(v, cfg); err != nil {
		t.Fatalf("ResolveClientVaultRefs with nil tools: %v", err)
	}
}

func TestVault_SaltPersistence(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := tmpDir + "/vault.db"

	// Open, set a value, close.
	v1, err := Open(dbPath, testPassphrase)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := v1.Set("key", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v1.Close()

	// Reopen with the same passphrase — should derive the same key.
	v2, err := Open(dbPath, testPassphrase)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer v2.Close()

	got, err := v2.Get("key")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got != "value" {
		t.Errorf("Get = %q, want %q", got, "value")
	}
}
