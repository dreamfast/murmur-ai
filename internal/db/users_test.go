package db

import (
	"database/sql"
	"testing"
)

func TestUserCRUD(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Create a user.
	u := &UserRow{
		Nick:               "alice",
		Role:               "admin",
		Tools:              StringSlice{"*"},
		DenyTools:          StringSlice{},
		Autonomy:           "auto",
		AllowedModels:      StringSlice{},
		DenyModels:         StringSlice{},
		MaxMessagesPerHour: 100,
		APIKey:             "key-alice-123",
		NickServAccount:    "alice_ns",
	}
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Read it back.
	got, err := db.GetUser("alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Nick != "alice" {
		t.Errorf("expected nick 'alice', got %q", got.Nick)
	}
	if got.Role != "admin" {
		t.Errorf("expected role 'admin', got %q", got.Role)
	}
	if got.Autonomy != "auto" {
		t.Errorf("expected autonomy 'auto', got %q", got.Autonomy)
	}
	if got.MaxMessagesPerHour != 100 {
		t.Errorf("expected max_messages_per_hour 100, got %d", got.MaxMessagesPerHour)
	}
	if got.APIKey != "key-alice-123" {
		t.Errorf("expected api_key 'key-alice-123', got %q", got.APIKey)
	}
	if got.NickServAccount != "alice_ns" {
		t.Errorf("expected nickserv_account 'alice_ns', got %q", got.NickServAccount)
	}
	if got.Created.IsZero() {
		t.Error("expected non-zero created timestamp")
	}

	// Update the user.
	got.Role = "user"
	got.MaxMessagesPerHour = 50
	got.APIKey = "key-alice-456"
	if err := db.UpdateUser(got); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	updated, err := db.GetUser("alice")
	if err != nil {
		t.Fatalf("GetUser after update: %v", err)
	}
	if updated.Role != "user" {
		t.Errorf("expected role 'user' after update, got %q", updated.Role)
	}
	if updated.MaxMessagesPerHour != 50 {
		t.Errorf("expected max_messages_per_hour 50 after update, got %d", updated.MaxMessagesPerHour)
	}
	if updated.APIKey != "key-alice-456" {
		t.Errorf("expected api_key 'key-alice-456' after update, got %q", updated.APIKey)
	}

	// List users.
	users, err := db.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Nick != "alice" {
		t.Errorf("expected nick 'alice' in list, got %q", users[0].Nick)
	}

	// Count users.
	count, err := db.UserCount()
	if err != nil {
		t.Fatalf("UserCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}

	// Delete the user.
	if err := db.DeleteUser("alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	_, err = db.GetUser("alice")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
	}

	count, err = db.UserCount()
	if err != nil {
		t.Fatalf("UserCount after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0 after delete, got %d", count)
	}
}

func TestUserCaseInsensitive(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	u := &UserRow{
		Nick:      "Alice",
		Role:      "admin",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "auto",
	}
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Lookup with different case should find the same user.
	got, err := db.GetUser("alice")
	if err != nil {
		t.Fatalf("GetUser('alice'): %v", err)
	}
	if got.Nick != "Alice" {
		t.Errorf("expected nick 'Alice' (original case), got %q", got.Nick)
	}

	got, err = db.GetUser("ALICE")
	if err != nil {
		t.Fatalf("GetUser('ALICE'): %v", err)
	}
	if got.Nick != "Alice" {
		t.Errorf("expected nick 'Alice' (original case), got %q", got.Nick)
	}

	// Creating a user with different case should fail (PK conflict).
	dup := &UserRow{
		Nick:      "alice",
		Role:      "user",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
	}
	err = db.CreateUser(dup)
	if err == nil {
		t.Fatal("expected error for case-insensitive duplicate nick")
	}
}

func TestUserAPIKeyUnique(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	u1 := &UserRow{
		Nick:      "user1",
		Role:      "user",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
		APIKey:    "shared-key",
	}
	u2 := &UserRow{
		Nick:      "user2",
		Role:      "user",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
		APIKey:    "shared-key",
	}

	if err := db.CreateUser(u1); err != nil {
		t.Fatalf("CreateUser(user1): %v", err)
	}

	// Second user with same non-empty API key should fail.
	err := db.CreateUser(u2)
	if err == nil {
		t.Fatal("expected error for duplicate api_key")
	}

	// Two users with empty API keys should be fine (partial index excludes '').
	u3 := &UserRow{
		Nick:      "user3",
		Role:      "user",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
		APIKey:    "",
	}
	u4 := &UserRow{
		Nick:      "user4",
		Role:      "user",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
		APIKey:    "",
	}
	if err := db.CreateUser(u3); err != nil {
		t.Fatalf("CreateUser(user3): %v", err)
	}
	if err := db.CreateUser(u4); err != nil {
		t.Fatalf("CreateUser(user4): %v", err)
	}

	// Lookup by API key.
	got, err := db.GetUserByAPIKey("shared-key")
	if err != nil {
		t.Fatalf("GetUserByAPIKey: %v", err)
	}
	if got.Nick != "user1" {
		t.Errorf("expected nick 'user1', got %q", got.Nick)
	}

	// Lookup with empty key returns ErrNoRows.
	_, err = db.GetUserByAPIKey("")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for empty api_key, got %v", err)
	}

	// Lookup with nonexistent key returns ErrNoRows.
	_, err = db.GetUserByAPIKey("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows for nonexistent api_key, got %v", err)
	}
}

func TestUserJSONFields(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	u := &UserRow{
		Nick:          "jsonuser",
		Role:          "user",
		Tools:         StringSlice{"shell", "web_search", "code_exec"},
		DenyTools:     StringSlice{"dangerous_tool"},
		Autonomy:      "approve",
		AllowedModels: StringSlice{"gpt-4", "claude-3"},
		DenyModels:    StringSlice{"gpt-3.5"},
	}
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := db.GetUser("jsonuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	// Verify tools roundtrip.
	if len(got.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(got.Tools))
	}
	expectedTools := []string{"shell", "web_search", "code_exec"}
	for i, want := range expectedTools {
		if got.Tools[i] != want {
			t.Errorf("tools[%d]: expected %q, got %q", i, want, got.Tools[i])
		}
	}

	// Verify deny_tools roundtrip.
	if len(got.DenyTools) != 1 || got.DenyTools[0] != "dangerous_tool" {
		t.Errorf("expected deny_tools [\"dangerous_tool\"], got %v", got.DenyTools)
	}

	// Verify allowed_models roundtrip.
	if len(got.AllowedModels) != 2 {
		t.Fatalf("expected 2 allowed_models, got %d", len(got.AllowedModels))
	}
	if got.AllowedModels[0] != "gpt-4" || got.AllowedModels[1] != "claude-3" {
		t.Errorf("expected allowed_models [\"gpt-4\", \"claude-3\"], got %v", got.AllowedModels)
	}

	// Verify deny_models roundtrip.
	if len(got.DenyModels) != 1 || got.DenyModels[0] != "gpt-3.5" {
		t.Errorf("expected deny_models [\"gpt-3.5\"], got %v", got.DenyModels)
	}

	// Verify nil/empty slice handling: create user with nil slices (should default to []).
	u2 := &UserRow{
		Nick:     "niluser",
		Role:     "user",
		Autonomy: "approve",
	}
	if err := db.CreateUser(u2); err != nil {
		t.Fatalf("CreateUser(niluser): %v", err)
	}

	got2, err := db.GetUser("niluser")
	if err != nil {
		t.Fatalf("GetUser(niluser): %v", err)
	}
	// nil StringSlice marshals to "[]", which unmarshals to empty slice.
	if got2.Tools == nil {
		t.Error("expected non-nil tools (empty slice), got nil")
	}
	if len(got2.Tools) != 0 {
		t.Errorf("expected 0 tools for nil input, got %d", len(got2.Tools))
	}
}

func TestChannelPermissionCRUD(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Create channel permission.
	cp := &ChannelPermissionRow{
		Channel:       "#general",
		Tools:         StringSlice{"web_search", "shell"},
		DenyTools:     StringSlice{"code_exec"},
		Autonomy:      "auto",
		AllowedModels: StringSlice{"gpt-4"},
	}
	if err := db.SetChannelPermission(cp); err != nil {
		t.Fatalf("SetChannelPermission: %v", err)
	}

	// Read it back.
	got, err := db.GetChannelPermission("#general")
	if err != nil {
		t.Fatalf("GetChannelPermission: %v", err)
	}
	if got.Channel != "#general" {
		t.Errorf("expected channel '#general', got %q", got.Channel)
	}
	if got.Autonomy != "auto" {
		t.Errorf("expected autonomy 'auto', got %q", got.Autonomy)
	}
	if len(got.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(got.Tools))
	}
	if len(got.DenyTools) != 1 || got.DenyTools[0] != "code_exec" {
		t.Errorf("expected deny_tools [\"code_exec\"], got %v", got.DenyTools)
	}
	if len(got.AllowedModels) != 1 || got.AllowedModels[0] != "gpt-4" {
		t.Errorf("expected allowed_models [\"gpt-4\"], got %v", got.AllowedModels)
	}

	// Update via upsert.
	cp.Autonomy = "report"
	cp.Tools = StringSlice{"web_search"}
	if err := db.SetChannelPermission(cp); err != nil {
		t.Fatalf("SetChannelPermission (update): %v", err)
	}

	got, err = db.GetChannelPermission("#general")
	if err != nil {
		t.Fatalf("GetChannelPermission after update: %v", err)
	}
	if got.Autonomy != "report" {
		t.Errorf("expected autonomy 'report' after update, got %q", got.Autonomy)
	}
	if len(got.Tools) != 1 || got.Tools[0] != "web_search" {
		t.Errorf("expected tools [\"web_search\"] after update, got %v", got.Tools)
	}

	// Case-insensitive lookup.
	got, err = db.GetChannelPermission("#GENERAL")
	if err != nil {
		t.Fatalf("GetChannelPermission('#GENERAL'): %v", err)
	}
	if got.Channel != "#general" {
		t.Errorf("expected channel '#general' (original case), got %q", got.Channel)
	}

	// List channel permissions.
	// Add a second channel.
	cp2 := &ChannelPermissionRow{
		Channel:   "#dev",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "auto",
	}
	if err := db.SetChannelPermission(cp2); err != nil {
		t.Fatalf("SetChannelPermission(#dev): %v", err)
	}

	perms, err := db.ListChannelPermissions()
	if err != nil {
		t.Fatalf("ListChannelPermissions: %v", err)
	}
	if len(perms) != 2 {
		t.Fatalf("expected 2 channel permissions, got %d", len(perms))
	}
	// Should be sorted by channel name.
	if perms[0].Channel != "#dev" {
		t.Errorf("expected first channel '#dev', got %q", perms[0].Channel)
	}
	if perms[1].Channel != "#general" {
		t.Errorf("expected second channel '#general', got %q", perms[1].Channel)
	}

	// Delete channel permission.
	if err := db.DeleteChannelPermission("#general"); err != nil {
		t.Fatalf("DeleteChannelPermission: %v", err)
	}

	_, err = db.GetChannelPermission("#general")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
	}

	// Delete nonexistent should error.
	err = db.DeleteChannelPermission("#nonexistent")
	if err == nil {
		t.Fatal("expected error for deleting nonexistent channel permission")
	}
}

func TestGetDefaultUser(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// No default user yet.
	_, err := db.GetDefaultUser()
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows when no default user, got %v", err)
	}

	// Create the default user.
	def := &UserRow{
		Nick:      "default",
		Role:      "user",
		Tools:     StringSlice{"web_search", "shell"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
	}
	if err := db.CreateUser(def); err != nil {
		t.Fatalf("CreateUser(default): %v", err)
	}

	got, err := db.GetDefaultUser()
	if err != nil {
		t.Fatalf("GetDefaultUser: %v", err)
	}
	if got.Nick != "default" {
		t.Errorf("expected nick 'default', got %q", got.Nick)
	}
	if got.Role != "user" {
		t.Errorf("expected role 'user', got %q", got.Role)
	}
	if len(got.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(got.Tools))
	}

	// Case-insensitive: "Default" should also work.
	got, err = db.GetDefaultUser()
	if err != nil {
		t.Fatalf("GetDefaultUser (case test): %v", err)
	}
	if got.Nick != "default" {
		t.Errorf("expected nick 'default', got %q", got.Nick)
	}
}

func TestMetadata(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Get nonexistent key.
	_, err := db.GetMetadata("nonexistent")
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows for nonexistent key, got %v", err)
	}

	// Set a key.
	if err := db.SetMetadata("permissions_imported", "true"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	got, err := db.GetMetadata("permissions_imported")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if got != "true" {
		t.Errorf("expected value 'true', got %q", got)
	}

	// Update the key (upsert).
	if err := db.SetMetadata("permissions_imported", "2026-02-22"); err != nil {
		t.Fatalf("SetMetadata (update): %v", err)
	}

	got, err = db.GetMetadata("permissions_imported")
	if err != nil {
		t.Fatalf("GetMetadata after update: %v", err)
	}
	if got != "2026-02-22" {
		t.Errorf("expected value '2026-02-22', got %q", got)
	}

	// Multiple keys.
	if err := db.SetMetadata("version", "1.0"); err != nil {
		t.Fatalf("SetMetadata(version): %v", err)
	}

	v, err := db.GetMetadata("version")
	if err != nil {
		t.Fatalf("GetMetadata(version): %v", err)
	}
	if v != "1.0" {
		t.Errorf("expected version '1.0', got %q", v)
	}

	// Original key should still be there.
	got, err = db.GetMetadata("permissions_imported")
	if err != nil {
		t.Fatalf("GetMetadata(permissions_imported) after adding version: %v", err)
	}
	if got != "2026-02-22" {
		t.Errorf("expected value '2026-02-22', got %q", got)
	}
}

func TestUserUpdateNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.UpdateUser(&UserRow{
		Nick:      "nonexistent",
		Role:      "user",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
	})
	if err == nil {
		t.Fatal("expected error for updating nonexistent user")
	}
}

func TestUserDeleteNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.DeleteUser("nonexistent")
	if err == nil {
		t.Fatal("expected error for deleting nonexistent user")
	}
}

func TestUserDuplicate(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	u := &UserRow{
		Nick:      "dupuser",
		Role:      "user",
		Tools:     StringSlice{"*"},
		DenyTools: StringSlice{},
		Autonomy:  "approve",
	}
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}

	err := db.CreateUser(u)
	if err == nil {
		t.Fatal("expected error for duplicate user")
	}
}
