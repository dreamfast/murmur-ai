package db

import (
	"database/sql"
	"testing"
)

// newTestDB creates an in-memory database with all migrations applied.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestCustomTool_Insert(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	tool := &CustomTool{
		Name:          "my_tool",
		Description:   "A test tool",
		Parameters:    `{"type": "object", "properties": {"input": {"type": "string"}}}`,
		Backend:       "shell",
		BackendConfig: `{"command": "echo {{input}}"}`,
		Enabled:       true,
	}

	if err := db.InsertCustomTool(tool); err != nil {
		t.Fatalf("InsertCustomTool: %v", err)
	}

	// Verify it was inserted.
	got, err := db.GetCustomTool("my_tool")
	if err != nil {
		t.Fatalf("GetCustomTool: %v", err)
	}

	if got.Name != "my_tool" {
		t.Errorf("expected name 'my_tool', got %q", got.Name)
	}
	if got.Description != "A test tool" {
		t.Errorf("expected description 'A test tool', got %q", got.Description)
	}
	if got.Backend != "shell" {
		t.Errorf("expected backend 'shell', got %q", got.Backend)
	}
	if !got.Enabled {
		t.Error("expected enabled=true")
	}
	if got.Created.IsZero() {
		t.Error("expected non-zero created timestamp")
	}
}

func TestCustomTool_InsertDuplicate(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	tool := &CustomTool{
		Name:          "dup_tool",
		Description:   "First",
		Parameters:    "{}",
		Backend:       "shell",
		BackendConfig: "{}",
		Enabled:       true,
	}

	if err := db.InsertCustomTool(tool); err != nil {
		t.Fatalf("first InsertCustomTool: %v", err)
	}

	// Second insert with same name should fail.
	err := db.InsertCustomTool(tool)
	if err == nil {
		t.Fatal("expected error for duplicate insert")
	}
}

func TestCustomTool_GetNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	_, err := db.GetCustomTool("nonexistent")
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestCustomTool_List(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Insert two tools.
	for _, name := range []string{"tool_b", "tool_a"} {
		if err := db.InsertCustomTool(&CustomTool{
			Name:          name,
			Description:   "Tool " + name,
			Parameters:    "{}",
			Backend:       "shell",
			BackendConfig: "{}",
			Enabled:       true,
		}); err != nil {
			t.Fatalf("InsertCustomTool(%s): %v", name, err)
		}
	}

	tools, err := db.ListCustomTools(false)
	if err != nil {
		t.Fatalf("ListCustomTools: %v", err)
	}

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Should be sorted by name.
	if tools[0].Name != "tool_a" {
		t.Errorf("expected first tool 'tool_a', got %q", tools[0].Name)
	}
	if tools[1].Name != "tool_b" {
		t.Errorf("expected second tool 'tool_b', got %q", tools[1].Name)
	}
}

func TestCustomTool_ListEnabledOnly(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Insert one enabled and one disabled.
	if err := db.InsertCustomTool(&CustomTool{
		Name: "enabled_tool", Description: "Enabled", Parameters: "{}",
		Backend: "shell", BackendConfig: "{}", Enabled: true,
	}); err != nil {
		t.Fatalf("InsertCustomTool: %v", err)
	}
	if err := db.InsertCustomTool(&CustomTool{
		Name: "disabled_tool", Description: "Disabled", Parameters: "{}",
		Backend: "shell", BackendConfig: "{}", Enabled: false,
	}); err != nil {
		t.Fatalf("InsertCustomTool: %v", err)
	}

	tools, err := db.ListCustomTools(true)
	if err != nil {
		t.Fatalf("ListCustomTools: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 enabled tool, got %d", len(tools))
	}
	if tools[0].Name != "enabled_tool" {
		t.Errorf("expected 'enabled_tool', got %q", tools[0].Name)
	}
}

func TestCustomTool_ListEmpty(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	tools, err := db.ListCustomTools(false)
	if err != nil {
		t.Fatalf("ListCustomTools: %v", err)
	}

	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestCustomTool_Update(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	if err := db.InsertCustomTool(&CustomTool{
		Name: "update_me", Description: "Original", Parameters: "{}",
		Backend: "shell", BackendConfig: `{"command": "echo old"}`, Enabled: true,
	}); err != nil {
		t.Fatalf("InsertCustomTool: %v", err)
	}

	// Update the tool.
	updated := &CustomTool{
		Name:          "update_me",
		Description:   "Updated description",
		Parameters:    `{"type": "object"}`,
		Backend:       "http",
		BackendConfig: `{"url": "https://example.com"}`,
		Enabled:       false,
	}
	if err := db.UpdateCustomTool(updated); err != nil {
		t.Fatalf("UpdateCustomTool: %v", err)
	}

	// Verify the update.
	got, err := db.GetCustomTool("update_me")
	if err != nil {
		t.Fatalf("GetCustomTool: %v", err)
	}

	if got.Description != "Updated description" {
		t.Errorf("expected description 'Updated description', got %q", got.Description)
	}
	if got.Backend != "http" {
		t.Errorf("expected backend 'http', got %q", got.Backend)
	}
	if got.Enabled {
		t.Error("expected enabled=false")
	}
	// Updated should be >= Created. In fast tests they may be equal,
	// so we only check that Updated is not before Created.
	if got.Updated.Before(got.Created) {
		t.Errorf("expected updated >= created, got updated=%v created=%v", got.Updated, got.Created)
	}
}

func TestCustomTool_UpdateNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.UpdateCustomTool(&CustomTool{
		Name: "nonexistent", Description: "X", Parameters: "{}",
		Backend: "shell", BackendConfig: "{}",
	})
	if err == nil {
		t.Fatal("expected error for updating nonexistent tool")
	}
}

func TestCustomTool_Delete(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	if err := db.InsertCustomTool(&CustomTool{
		Name: "delete_me", Description: "To be deleted", Parameters: "{}",
		Backend: "shell", BackendConfig: "{}", Enabled: true,
	}); err != nil {
		t.Fatalf("InsertCustomTool: %v", err)
	}

	if err := db.DeleteCustomTool("delete_me"); err != nil {
		t.Fatalf("DeleteCustomTool: %v", err)
	}

	// Verify it's gone.
	_, err := db.GetCustomTool("delete_me")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
	}
}

func TestCustomTool_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.DeleteCustomTool("nonexistent")
	if err == nil {
		t.Fatal("expected error for deleting nonexistent tool")
	}
}

func TestCustomTool_EnableDisable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	if err := db.InsertCustomTool(&CustomTool{
		Name: "toggle_me", Description: "Toggle", Parameters: "{}",
		Backend: "shell", BackendConfig: "{}", Enabled: true,
	}); err != nil {
		t.Fatalf("InsertCustomTool: %v", err)
	}

	// Disable.
	if err := db.SetCustomToolEnabled("toggle_me", false); err != nil {
		t.Fatalf("SetCustomToolEnabled(false): %v", err)
	}

	got, err := db.GetCustomTool("toggle_me")
	if err != nil {
		t.Fatalf("GetCustomTool: %v", err)
	}
	if got.Enabled {
		t.Error("expected enabled=false after disable")
	}

	// Re-enable.
	if err := db.SetCustomToolEnabled("toggle_me", true); err != nil {
		t.Fatalf("SetCustomToolEnabled(true): %v", err)
	}

	got, err = db.GetCustomTool("toggle_me")
	if err != nil {
		t.Fatalf("GetCustomTool: %v", err)
	}
	if !got.Enabled {
		t.Error("expected enabled=true after re-enable")
	}
}

func TestCustomTool_EnableNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.SetCustomToolEnabled("nonexistent", true)
	if err == nil {
		t.Fatal("expected error for enabling nonexistent tool")
	}
}

func TestCustomTool_InvalidBackend(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.InsertCustomTool(&CustomTool{
		Name: "bad_backend", Description: "Bad", Parameters: "{}",
		Backend: "invalid", BackendConfig: "{}", Enabled: true,
	})
	if err == nil {
		t.Fatal("expected error for invalid backend")
	}
}

func TestMigrate_CustomToolsTable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Verify custom_tools table exists.
	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='custom_tools'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("custom_tools table not found: %v", err)
	}

	// Verify the index exists.
	err = db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_custom_tools_enabled'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("idx_custom_tools_enabled index not found: %v", err)
	}
}
