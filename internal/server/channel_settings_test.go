package server

import (
	"testing"
)

func newTestChannelSettingsStore(t *testing.T) *ChannelSettingsStore {
	t.Helper()
	database := newTestDB(t)
	return NewChannelSettingsStore(database, newTestLogger())
}

func TestChannelSettingsStore_Get_NotFound(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	cs, err := store.Get("#nonexistent")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if cs != nil {
		t.Errorf("expected nil for nonexistent channel, got %+v", cs)
	}
}

func TestChannelSettingsStore_Upsert_Insert(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	cs := &ChannelSettings{
		Channel:     "#murmur",
		Provider:    "kimi",
		AutoJoin:    true,
		TopicPrefix: "AI Bot",
	}
	if err := store.Upsert(cs); err != nil {
		t.Fatalf("Upsert error: %v", err)
	}

	got, err := store.Get("#murmur")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil settings")
	}
	if got.Channel != "#murmur" {
		t.Errorf("expected channel '#murmur', got %q", got.Channel)
	}
	if got.Provider != "kimi" {
		t.Errorf("expected provider 'kimi', got %q", got.Provider)
	}
	if !got.AutoJoin {
		t.Error("expected auto_join=true")
	}
	if got.TopicPrefix != "AI Bot" {
		t.Errorf("expected topic_prefix 'AI Bot', got %q", got.TopicPrefix)
	}
}

func TestChannelSettingsStore_Upsert_Update(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	// Insert.
	cs := &ChannelSettings{
		Channel:  "#murmur",
		Provider: "kimi",
		AutoJoin: true,
	}
	if err := store.Upsert(cs); err != nil {
		t.Fatalf("first Upsert error: %v", err)
	}

	// Update.
	cs.Provider = "ollama"
	cs.AutoJoin = false
	cs.TopicPrefix = "updated"
	if err := store.Upsert(cs); err != nil {
		t.Fatalf("second Upsert error: %v", err)
	}

	got, err := store.Get("#murmur")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Provider != "ollama" {
		t.Errorf("expected provider 'ollama', got %q", got.Provider)
	}
	if got.AutoJoin {
		t.Error("expected auto_join=false after update")
	}
	if got.TopicPrefix != "updated" {
		t.Errorf("expected topic_prefix 'updated', got %q", got.TopicPrefix)
	}
}

func TestChannelSettingsStore_SetProvider(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	// SetProvider on nonexistent channel creates the row.
	if err := store.SetProvider("#news", "glm"); err != nil {
		t.Fatalf("SetProvider error: %v", err)
	}

	got, err := store.Get("#news")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil settings after SetProvider")
	}
	if got.Provider != "glm" {
		t.Errorf("expected provider 'glm', got %q", got.Provider)
	}
	// Other fields should have defaults.
	if got.AutoJoin {
		t.Error("expected auto_join=false (default)")
	}
	if got.TopicPrefix != "" {
		t.Errorf("expected empty topic_prefix, got %q", got.TopicPrefix)
	}

	// Update provider on existing row.
	if err := store.SetProvider("#news", "kimi"); err != nil {
		t.Fatalf("second SetProvider error: %v", err)
	}
	got, err = store.Get("#news")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Provider != "kimi" {
		t.Errorf("expected provider 'kimi', got %q", got.Provider)
	}

	// Clear provider (reset to global default).
	if err := store.SetProvider("#news", ""); err != nil {
		t.Fatalf("clear SetProvider error: %v", err)
	}
	got, err = store.Get("#news")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Provider != "" {
		t.Errorf("expected empty provider, got %q", got.Provider)
	}
}

func TestChannelSettingsStore_SetAutoJoin(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	// SetAutoJoin on nonexistent channel creates the row.
	if err := store.SetAutoJoin("#news", true); err != nil {
		t.Fatalf("SetAutoJoin error: %v", err)
	}

	got, err := store.Get("#news")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil settings after SetAutoJoin")
	}
	if !got.AutoJoin {
		t.Error("expected auto_join=true")
	}

	// Disable auto-join.
	if err := store.SetAutoJoin("#news", false); err != nil {
		t.Fatalf("second SetAutoJoin error: %v", err)
	}
	got, err = store.Get("#news")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.AutoJoin {
		t.Error("expected auto_join=false after disable")
	}
}

func TestChannelSettingsStore_SetAutoJoin_PreservesOtherFields(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	// Insert with provider set.
	cs := &ChannelSettings{
		Channel:  "#murmur",
		Provider: "kimi",
		AutoJoin: false,
	}
	if err := store.Upsert(cs); err != nil {
		t.Fatalf("Upsert error: %v", err)
	}

	// SetAutoJoin should not overwrite provider.
	if err := store.SetAutoJoin("#murmur", true); err != nil {
		t.Fatalf("SetAutoJoin error: %v", err)
	}

	got, err := store.Get("#murmur")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Provider != "kimi" {
		t.Errorf("expected provider 'kimi' preserved, got %q", got.Provider)
	}
	if !got.AutoJoin {
		t.Error("expected auto_join=true")
	}
}

func TestChannelSettingsStore_SetProvider_PreservesOtherFields(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	// Insert with auto_join set.
	cs := &ChannelSettings{
		Channel:  "#murmur",
		AutoJoin: true,
	}
	if err := store.Upsert(cs); err != nil {
		t.Fatalf("Upsert error: %v", err)
	}

	// SetProvider should not overwrite auto_join.
	if err := store.SetProvider("#murmur", "ollama"); err != nil {
		t.Fatalf("SetProvider error: %v", err)
	}

	got, err := store.Get("#murmur")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if !got.AutoJoin {
		t.Error("expected auto_join=true preserved")
	}
	if got.Provider != "ollama" {
		t.Errorf("expected provider 'ollama', got %q", got.Provider)
	}
}

func TestChannelSettingsStore_GetAutoJoinChannels(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	// Empty initially.
	channels, err := store.GetAutoJoinChannels()
	if err != nil {
		t.Fatalf("GetAutoJoinChannels error: %v", err)
	}
	if len(channels) != 0 {
		t.Errorf("expected 0 channels, got %d", len(channels))
	}

	// Add some channels.
	if err := store.SetAutoJoin("#murmur", true); err != nil {
		t.Fatalf("SetAutoJoin error: %v", err)
	}
	if err := store.SetAutoJoin("#news", true); err != nil {
		t.Fatalf("SetAutoJoin error: %v", err)
	}
	if err := store.SetAutoJoin("#temp", false); err != nil {
		t.Fatalf("SetAutoJoin error: %v", err)
	}

	channels, err = store.GetAutoJoinChannels()
	if err != nil {
		t.Fatalf("GetAutoJoinChannels error: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 auto-join channels, got %d", len(channels))
	}
	// Should be sorted.
	if channels[0] != "#murmur" {
		t.Errorf("expected first channel '#murmur', got %q", channels[0])
	}
	if channels[1] != "#news" {
		t.Errorf("expected second channel '#news', got %q", channels[1])
	}
}

func TestChannelSettingsStore_GetProvider_NotFound(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	provider, err := store.GetProvider("#nonexistent")
	if err != nil {
		t.Fatalf("GetProvider error: %v", err)
	}
	if provider != "" {
		t.Errorf("expected empty provider for nonexistent channel, got %q", provider)
	}
}

func TestChannelSettingsStore_CaseInsensitive(t *testing.T) {
	t.Parallel()
	store := newTestChannelSettingsStore(t)

	// Insert with mixed case.
	cs := &ChannelSettings{
		Channel:  "#Murmur",
		Provider: "kimi",
		AutoJoin: true,
	}
	if err := store.Upsert(cs); err != nil {
		t.Fatalf("Upsert error: %v", err)
	}

	// Lookup with different case should find it.
	tests := []string{"#murmur", "#MURMUR", "#Murmur", "#mUrMuR"}
	for _, ch := range tests {
		got, err := store.Get(ch)
		if err != nil {
			t.Fatalf("Get(%q) error: %v", ch, err)
		}
		if got == nil {
			t.Errorf("Get(%q) returned nil, expected settings", ch)
			continue
		}
		if got.Provider != "kimi" {
			t.Errorf("Get(%q) provider = %q, want 'kimi'", ch, got.Provider)
		}
	}

	// SetProvider with different case should update the same row.
	if err := store.SetProvider("#MURMUR", "ollama"); err != nil {
		t.Fatalf("SetProvider error: %v", err)
	}
	got, err := store.Get("#murmur")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Provider != "ollama" {
		t.Errorf("expected provider 'ollama' after case-insensitive update, got %q", got.Provider)
	}

	// Should still be only one row.
	channels, err := store.GetAutoJoinChannels()
	if err != nil {
		t.Fatalf("GetAutoJoinChannels error: %v", err)
	}
	if len(channels) != 1 {
		t.Errorf("expected 1 auto-join channel (no duplicates), got %d", len(channels))
	}
}
