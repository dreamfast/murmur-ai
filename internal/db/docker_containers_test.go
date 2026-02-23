package db

import (
	"errors"
	"testing"
)

func TestCreateContainer(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	c := &ContainerRow{
		ContainerID: "abc123",
		Name:        "murmur-test",
		Image:       "ubuntu:24.04",
		Status:      "running",
		Channel:     "#murmur",
		Nick:        "bird",
		Ports:       StringSlice{"8080:80"},
	}

	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer error: %v", err)
	}
	if c.ID <= 0 {
		t.Errorf("expected positive ID after create, got %d", c.ID)
	}

	// Verify it was inserted.
	got, err := db.GetContainer("abc123")
	if err != nil {
		t.Fatalf("GetContainer error: %v", err)
	}
	if got.Name != "murmur-test" {
		t.Errorf("expected name 'murmur-test', got %q", got.Name)
	}
	if got.Image != "ubuntu:24.04" {
		t.Errorf("expected image 'ubuntu:24.04', got %q", got.Image)
	}
	if got.Status != "running" {
		t.Errorf("expected status 'running', got %q", got.Status)
	}
	if got.Nick != "bird" {
		t.Errorf("expected nick 'bird', got %q", got.Nick)
	}
	if len(got.Ports) != 1 || got.Ports[0] != "8080:80" {
		t.Errorf("expected ports [8080:80], got %v", got.Ports)
	}
}

func TestCreateContainer_DuplicateID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	c := &ContainerRow{
		ContainerID: "dup-id",
		Name:        "murmur-first",
		Image:       "alpine",
		Status:      "running",
		Channel:     "#test",
		Nick:        "user1",
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("first CreateContainer error: %v", err)
	}

	c2 := &ContainerRow{
		ContainerID: "dup-id",
		Name:        "murmur-second",
		Image:       "alpine",
		Status:      "running",
		Channel:     "#test",
		Nick:        "user2",
	}
	if err := db.CreateContainer(c2); err == nil {
		t.Fatal("expected error for duplicate container_id")
	}
}

func TestCreateContainer_DuplicateName(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	c := &ContainerRow{
		ContainerID: "id-1",
		Name:        "murmur-samename",
		Image:       "alpine",
		Status:      "running",
		Channel:     "#test",
		Nick:        "user1",
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("first CreateContainer error: %v", err)
	}

	c2 := &ContainerRow{
		ContainerID: "id-2",
		Name:        "murmur-samename",
		Image:       "alpine",
		Status:      "running",
		Channel:     "#test",
		Nick:        "user2",
	}
	if err := db.CreateContainer(c2); err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestUpdateContainerStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	c := &ContainerRow{
		ContainerID: "upd-1",
		Name:        "murmur-update",
		Image:       "alpine",
		Status:      "running",
		Channel:     "#test",
		Nick:        "bird",
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer error: %v", err)
	}

	if err := db.UpdateContainerStatus("upd-1", "stopped"); err != nil {
		t.Fatalf("UpdateContainerStatus error: %v", err)
	}

	got, err := db.GetContainer("upd-1")
	if err != nil {
		t.Fatalf("GetContainer error: %v", err)
	}
	if got.Status != "stopped" {
		t.Errorf("expected status 'stopped', got %q", got.Status)
	}
}

func TestUpdateContainerStatus_NotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.UpdateContainerStatus("nonexistent", "stopped")
	if err == nil {
		t.Fatal("expected error for nonexistent container")
	}
	if !errors.Is(err, ErrContainerNotFound) {
		t.Errorf("expected ErrContainerNotFound, got: %v", err)
	}
}

func TestGetContainer_NotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	_, err := db.GetContainer("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent container")
	}
	if !errors.Is(err, ErrContainerNotFound) {
		t.Errorf("expected ErrContainerNotFound, got: %v", err)
	}
}

func TestGetContainerByName(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	c := &ContainerRow{
		ContainerID: "byname-1",
		Name:        "murmur-byname",
		Image:       "nginx",
		Status:      "running",
		Channel:     "#test",
		Nick:        "bird",
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer error: %v", err)
	}

	got, err := db.GetContainerByName("murmur-byname")
	if err != nil {
		t.Fatalf("GetContainerByName error: %v", err)
	}
	if got.ContainerID != "byname-1" {
		t.Errorf("expected container_id 'byname-1', got %q", got.ContainerID)
	}
}

func TestGetContainerByName_NotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	_, err := db.GetContainerByName("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent name")
	}
	if !errors.Is(err, ErrContainerNotFound) {
		t.Errorf("expected ErrContainerNotFound, got: %v", err)
	}
}

func TestListContainers(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Empty list.
	containers, err := db.ListContainers()
	if err != nil {
		t.Fatalf("ListContainers error: %v", err)
	}
	if len(containers) != 0 {
		t.Errorf("expected 0 containers, got %d", len(containers))
	}

	// Insert two containers.
	for i, name := range []string{"murmur-a", "murmur-b"} {
		c := &ContainerRow{
			ContainerID: name,
			Name:        name,
			Image:       "alpine",
			Status:      "running",
			Channel:     "#test",
			Nick:        "bird",
			Ports:       StringSlice{},
		}
		if err := db.CreateContainer(c); err != nil {
			t.Fatalf("CreateContainer %d error: %v", i, err)
		}
	}

	containers, err = db.ListContainers()
	if err != nil {
		t.Fatalf("ListContainers error: %v", err)
	}
	if len(containers) != 2 {
		t.Errorf("expected 2 containers, got %d", len(containers))
	}
}

func TestRemoveContainer(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	c := &ContainerRow{
		ContainerID: "rm-1",
		Name:        "murmur-remove",
		Image:       "alpine",
		Status:      "running",
		Channel:     "#test",
		Nick:        "bird",
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer error: %v", err)
	}

	if err := db.RemoveContainer("rm-1"); err != nil {
		t.Fatalf("RemoveContainer error: %v", err)
	}

	// Verify it's gone.
	_, err := db.GetContainer("rm-1")
	if !errors.Is(err, ErrContainerNotFound) {
		t.Errorf("expected ErrContainerNotFound after removal, got: %v", err)
	}
}

func TestRemoveContainer_NotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	err := db.RemoveContainer("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent container")
	}
	if !errors.Is(err, ErrContainerNotFound) {
		t.Errorf("expected ErrContainerNotFound, got: %v", err)
	}
}

func TestDeleteRemovedContainers(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Insert containers with various statuses.
	statuses := map[string]string{
		"c-running":  "running",
		"c-stopped":  "stopped",
		"c-removed":  "removed",
		"c-removed2": "removed",
	}
	for id, status := range statuses {
		c := &ContainerRow{
			ContainerID: id,
			Name:        "murmur-" + id,
			Image:       "alpine",
			Status:      status,
			Channel:     "#test",
			Nick:        "bird",
		}
		if err := db.CreateContainer(c); err != nil {
			t.Fatalf("CreateContainer %s error: %v", id, err)
		}
	}

	deleted, err := db.DeleteRemovedContainers()
	if err != nil {
		t.Fatalf("DeleteRemovedContainers error: %v", err)
	}
	if deleted != 2 {
		t.Errorf("expected 2 deleted, got %d", deleted)
	}

	// Verify only non-removed containers remain.
	containers, err := db.ListContainers()
	if err != nil {
		t.Fatalf("ListContainers error: %v", err)
	}
	if len(containers) != 2 {
		t.Errorf("expected 2 remaining containers, got %d", len(containers))
	}
}

func TestCountActiveContainers(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Empty DB.
	count, err := db.CountActiveContainers()
	if err != nil {
		t.Fatalf("CountActiveContainers error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Insert containers with various statuses.
	entries := []struct {
		id     string
		status string
	}{
		{"active-1", "running"},
		{"active-2", "created"},
		{"active-3", "stopped"},
		{"inactive-1", "removed"},
		{"inactive-2", "exited"},
	}
	for _, e := range entries {
		c := &ContainerRow{
			ContainerID: e.id,
			Name:        "murmur-" + e.id,
			Image:       "alpine",
			Status:      e.status,
			Channel:     "#test",
			Nick:        "bird",
		}
		if err := db.CreateContainer(c); err != nil {
			t.Fatalf("CreateContainer %s error: %v", e.id, err)
		}
	}

	count, err = db.CountActiveContainers()
	if err != nil {
		t.Fatalf("CountActiveContainers error: %v", err)
	}
	// running, created, stopped are active; removed, exited are not.
	if count != 3 {
		t.Errorf("expected 3 active containers, got %d", count)
	}
}

func TestContainerRow_NilPorts(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	// Create with nil ports (should store as "[]").
	c := &ContainerRow{
		ContainerID: "nil-ports",
		Name:        "murmur-nilports",
		Image:       "alpine",
		Status:      "running",
		Channel:     "#test",
		Nick:        "bird",
		Ports:       nil,
	}
	if err := db.CreateContainer(c); err != nil {
		t.Fatalf("CreateContainer error: %v", err)
	}

	got, err := db.GetContainer("nil-ports")
	if err != nil {
		t.Fatalf("GetContainer error: %v", err)
	}
	if got.Ports == nil {
		t.Error("expected non-nil ports after round-trip (should be empty slice)")
	}
	if len(got.Ports) != 0 {
		t.Errorf("expected empty ports, got %v", got.Ports)
	}
}
