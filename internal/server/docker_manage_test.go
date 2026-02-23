package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"murmur/internal/config"
	"murmur/internal/db"
)

// newTestDockerDeps creates a dockerManageDeps with an in-memory DB and a
// mock runFunc for testing. The runFunc returns the given output and error.
func newTestDockerDeps(t *testing.T, runOutput string, runErr error) *dockerManageDeps {
	t.Helper()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.DockerManageConfig{
		Enabled:       true,
		MaxContainers: 10,
		MemoryLimit:   "512m",
		CPULimit:      "1.0",
		PidsLimit:     256,
		AllowNetwork:  boolPtr(true),
		Timeout:       "5m",
	}

	return &dockerManageDeps{
		cfg:     cfg,
		db:      database,
		pm:      nil,
		logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		timeout: 5 * 60 * 1e9, // 5 minutes in nanoseconds
		runFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			return runOutput, runErr
		},
	}
}

func boolPtr(b bool) *bool { return &b }

// ctxWithNick returns a context with the given nick set.
func ctxWithNick(nick string) context.Context {
	return context.WithValue(context.Background(), requestNickKey{}, nick)
}

func TestDockerCreate_Success(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "abc123def456\n", nil)

	args := map[string]any{
		"action": "create",
		"image":  "ubuntu:24.04",
		"name":   "test",
	}

	result, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "murmur-test") {
		t.Errorf("expected result to contain 'murmur-test', got: %s", result)
	}
	if !strings.Contains(result, "abc123def456") {
		t.Errorf("expected result to contain container ID, got: %s", result)
	}

	// Verify DB record.
	row, err := deps.db.GetContainerByName("murmur-test")
	if err != nil {
		t.Fatalf("GetContainerByName error: %v", err)
	}
	if row.Image != "ubuntu:24.04" {
		t.Errorf("expected image 'ubuntu:24.04', got %q", row.Image)
	}
	if row.Nick != "bird" {
		t.Errorf("expected nick 'bird', got %q", row.Nick)
	}
}

func TestDockerCreate_WithPorts(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "abc123\n", nil)

	args := map[string]any{
		"action": "create",
		"image":  "nginx",
		"name":   "web",
		"ports":  []any{"8080:80", "443:443"},
	}

	result, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "8080:80") {
		t.Errorf("expected ports in result, got: %s", result)
	}
}

func TestDockerCreate_ImageNotAllowed(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "", nil)
	deps.cfg.AllowedImages = []string{"ubuntu:*", "alpine"}

	args := map[string]any{
		"action": "create",
		"image":  "nginx:latest",
		"name":   "blocked",
	}

	_, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err == nil {
		t.Fatal("expected error for blocked image")
	}
	if !strings.Contains(err.Error(), "not in the allowed images list") {
		t.Errorf("expected allowlist error, got: %v", err)
	}
}

func TestDockerCreate_ContainerLimitReached(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "abc123\n", nil)
	deps.cfg.MaxContainers = 1

	// Create first container.
	args := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "first",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), args, deps); err != nil {
		t.Fatalf("first create error: %v", err)
	}

	// Second should fail.
	args["name"] = "second"
	_, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err == nil {
		t.Fatal("expected error for container limit")
	}
	if !strings.Contains(err.Error(), "container limit reached") {
		t.Errorf("expected limit error, got: %v", err)
	}
}

func TestDockerCreate_BlockedExtraArgs(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "", nil)

	tests := []struct {
		name      string
		extraArgs string
	}{
		{"privileged", "--privileged"},
		{"volume", "--volume /host:/container"},
		{"volume short", "-v /host:/container"},
		{"mount", "--mount type=bind,source=/,target=/host"},
		{"cap-add", "--cap-add SYS_ADMIN"},
		{"device", "--device /dev/sda"},
		{"host network equals", "--network=host"},
		{"host network space", "--network host"},
		{"host ipc equals", "--ipc=host"},
		{"host ipc space", "--ipc host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{
				"action":     "create",
				"image":      "alpine",
				"name":       "blocked-" + tt.name,
				"extra_args": tt.extraArgs,
			}
			_, err := handleDockerManage(ctxWithNick("bird"), args, deps)
			if err == nil {
				t.Fatal("expected error for blocked flag")
			}
			if !strings.Contains(err.Error(), "blocked Docker flag") {
				t.Errorf("expected blocked flag error, got: %v", err)
			}
		})
	}
}

func TestDockerExec_Success(t *testing.T) {
	t.Parallel()

	callCount := 0
	deps := newTestDockerDeps(t, "", nil)
	deps.runFunc = func(ctx context.Context, name string, args ...string) (string, error) {
		callCount++
		if callCount == 1 {
			return "abc123\n", nil // create
		}
		return "hello world\n", nil // exec
	}

	// Create a container first.
	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "exectest",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Exec into it.
	execArgs := map[string]any{
		"action":    "exec",
		"container": "exectest",
		"command":   "echo hello",
	}
	result, err := handleDockerManage(ctxWithNick("bird"), execArgs, deps)
	if err != nil {
		t.Fatalf("exec error: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected 'hello world' in output, got: %s", result)
	}
}

func TestDockerExec_PermissionDenied(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "abc123\n", nil)

	// Create as bird.
	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "owned",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Exec as different user.
	execArgs := map[string]any{
		"action":    "exec",
		"container": "owned",
		"command":   "whoami",
	}
	_, err := handleDockerManage(ctxWithNick("hacker"), execArgs, deps)
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected permission denied, got: %v", err)
	}
}

func TestDockerStop_Success(t *testing.T) {
	t.Parallel()

	callCount := 0
	deps := newTestDockerDeps(t, "", nil)
	deps.runFunc = func(ctx context.Context, name string, args ...string) (string, error) {
		callCount++
		if callCount == 1 {
			return "abc123\n", nil // create
		}
		return "abc123\n", nil // stop
	}

	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "stopme",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	stopArgs := map[string]any{
		"action":    "stop",
		"container": "stopme",
	}
	result, err := handleDockerManage(ctxWithNick("bird"), stopArgs, deps)
	if err != nil {
		t.Fatalf("stop error: %v", err)
	}
	if !strings.Contains(result, "stopped") {
		t.Errorf("expected 'stopped' in result, got: %s", result)
	}

	// Verify DB status updated.
	row, err := deps.db.GetContainerByName("murmur-stopme")
	if err != nil {
		t.Fatalf("GetContainerByName error: %v", err)
	}
	if row.Status != "stopped" {
		t.Errorf("expected status 'stopped', got %q", row.Status)
	}
}

func TestDockerRemove_Success(t *testing.T) {
	t.Parallel()

	callCount := 0
	deps := newTestDockerDeps(t, "", nil)
	deps.runFunc = func(ctx context.Context, name string, args ...string) (string, error) {
		callCount++
		if callCount == 1 {
			return "abc123\n", nil // create
		}
		return "abc123\n", nil // rm
	}

	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "removeme",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	removeArgs := map[string]any{
		"action":    "remove",
		"container": "removeme",
	}
	result, err := handleDockerManage(ctxWithNick("bird"), removeArgs, deps)
	if err != nil {
		t.Fatalf("remove error: %v", err)
	}
	if !strings.Contains(result, "removed") {
		t.Errorf("expected 'removed' in result, got: %s", result)
	}

	// Verify container is gone from DB.
	_, err = deps.db.GetContainerByName("murmur-removeme")
	if !errors.Is(err, db.ErrContainerNotFound) {
		t.Errorf("expected ErrContainerNotFound, got: %v", err)
	}
}

func TestDockerList_Empty(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "", nil)

	args := map[string]any{"action": "list"}
	result, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if !strings.Contains(result, "No managed containers") {
		t.Errorf("expected 'No managed containers', got: %s", result)
	}
}

func TestDockerList_WithContainers(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "abc123\n", nil)

	// Create a container.
	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "listed",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	args := map[string]any{"action": "list"}
	result, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if !strings.Contains(result, "murmur-listed") {
		t.Errorf("expected 'murmur-listed' in list, got: %s", result)
	}
	if !strings.Contains(result, "alpine") {
		t.Errorf("expected 'alpine' in list, got: %s", result)
	}
}

func TestDockerBuild_Disabled(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "", nil)
	deps.cfg.AllowBuild = false

	args := map[string]any{
		"action":     "build",
		"dockerfile": "Dockerfile",
		"tag":        "myapp:latest",
	}
	_, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err == nil {
		t.Fatal("expected error when build is disabled")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected disabled error, got: %v", err)
	}
}

func TestDockerBuild_Enabled_AdminOnly(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "Successfully built abc123\n", nil)
	deps.cfg.AllowBuild = true

	// Non-admin should be denied.
	args := map[string]any{
		"action":     "build",
		"dockerfile": "Dockerfile",
		"tag":        "myapp:latest",
	}
	_, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err == nil {
		t.Fatal("expected permission denied for non-admin build")
	}
	if !strings.Contains(err.Error(), "admin role required") {
		t.Errorf("expected admin required error, got: %v", err)
	}

	// System user should succeed.
	result, err := handleDockerManage(ctxWithNick("_system"), args, deps)
	if err != nil {
		t.Fatalf("build error for _system: %v", err)
	}
	if !strings.Contains(result, "myapp:latest") {
		t.Errorf("expected tag in result, got: %s", result)
	}
}

func TestDockerBuild_PathTraversal(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "", nil)
	deps.cfg.AllowBuild = true

	args := map[string]any{
		"action":     "build",
		"dockerfile": "../../../etc/passwd",
		"tag":        "evil:latest",
	}
	_, err := handleDockerManage(ctxWithNick("_system"), args, deps)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("expected path traversal error, got: %v", err)
	}
}

func TestDockerInspect_Success(t *testing.T) {
	t.Parallel()

	callCount := 0
	deps := newTestDockerDeps(t, "", nil)
	deps.runFunc = func(ctx context.Context, name string, args ...string) (string, error) {
		callCount++
		if callCount == 1 {
			return "abc123\n", nil // create
		}
		return "ID: abc123\nName: murmur-inspected\nImage: alpine\nStatus: running\n", nil // inspect
	}

	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "inspected",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	inspectArgs := map[string]any{
		"action":    "inspect",
		"container": "inspected",
	}
	result, err := handleDockerManage(ctxWithNick("bird"), inspectArgs, deps)
	if err != nil {
		t.Fatalf("inspect error: %v", err)
	}
	if !strings.Contains(result, "alpine") {
		t.Errorf("expected 'alpine' in inspect output, got: %s", result)
	}
}

func TestDockerLogs_Success(t *testing.T) {
	t.Parallel()

	callCount := 0
	deps := newTestDockerDeps(t, "", nil)
	deps.runFunc = func(ctx context.Context, name string, args ...string) (string, error) {
		callCount++
		if callCount == 1 {
			return "abc123\n", nil // create
		}
		return "log line 1\nlog line 2\n", nil // logs
	}

	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "logtest",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	logsArgs := map[string]any{
		"action":    "logs",
		"container": "logtest",
		"tail":      float64(10),
	}
	result, err := handleDockerManage(ctxWithNick("bird"), logsArgs, deps)
	if err != nil {
		t.Fatalf("logs error: %v", err)
	}
	if !strings.Contains(result, "log line 1") {
		t.Errorf("expected log output, got: %s", result)
	}
}

func TestDockerStart_Success(t *testing.T) {
	t.Parallel()

	callCount := 0
	deps := newTestDockerDeps(t, "", nil)
	deps.runFunc = func(ctx context.Context, name string, args ...string) (string, error) {
		callCount++
		switch callCount {
		case 1:
			return "abc123\n", nil // create
		case 2:
			return "abc123\n", nil // stop
		case 3:
			return "abc123\n", nil // start
		}
		return "", nil
	}

	// Create.
	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "startme",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Stop.
	stopArgs := map[string]any{
		"action":    "stop",
		"container": "startme",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), stopArgs, deps); err != nil {
		t.Fatalf("stop error: %v", err)
	}

	// Start.
	startArgs := map[string]any{
		"action":    "start",
		"container": "startme",
	}
	result, err := handleDockerManage(ctxWithNick("bird"), startArgs, deps)
	if err != nil {
		t.Fatalf("start error: %v", err)
	}
	if !strings.Contains(result, "started") {
		t.Errorf("expected 'started' in result, got: %s", result)
	}

	// Verify DB status.
	row, err := deps.db.GetContainerByName("murmur-startme")
	if err != nil {
		t.Fatalf("GetContainerByName error: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("expected status 'running', got %q", row.Status)
	}
}

func TestValidateImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		image   string
		allowed []string
		wantErr bool
	}{
		{"empty allowlist allows all", "anything:latest", nil, false},
		{"exact match", "alpine", []string{"alpine", "ubuntu:*"}, false},
		{"glob match", "ubuntu:24.04", []string{"alpine", "ubuntu:*"}, false},
		{"no match", "nginx:latest", []string{"alpine", "ubuntu:*"}, true},
		{"wildcard all", "anything", []string{"*"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImage(tt.image, tt.allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateImage(%q, %v) error = %v, wantErr %v", tt.image, tt.allowed, err, tt.wantErr)
			}
		})
	}
}

func TestValidateExtraArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"safe args", "--restart=unless-stopped", false},
		{"privileged", "--privileged", true},
		{"volume", "--volume /host:/container", true},
		{"volume short", "-v /host:/container", true},
		{"mount", "--mount type=bind", true},
		{"cap-add", "--cap-add SYS_ADMIN", true},
		{"device", "--device /dev/sda", true},
		{"host network equals", "--network=host", true},
		{"host network space", "--network host", true},
		{"host ipc equals", "--ipc=host", true},
		{"host ipc space", "--ipc host", true},
		{"case insensitive", "--PRIVILEGED", true},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExtraArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateExtraArgs(%q) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestResolveContainer_ByName(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "abc123\n", nil)

	// Create a container.
	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "resolve",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Resolve by short name (without prefix).
	row, err := resolveContainer(deps, "resolve")
	if err != nil {
		t.Fatalf("resolveContainer error: %v", err)
	}
	if row.Name != "murmur-resolve" {
		t.Errorf("expected name 'murmur-resolve', got %q", row.Name)
	}
}

func TestResolveContainer_ByFullName(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "abc123\n", nil)

	createArgs := map[string]any{
		"action": "create",
		"image":  "alpine",
		"name":   "fullname",
	}
	if _, err := handleDockerManage(ctxWithNick("bird"), createArgs, deps); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Resolve by full name (with prefix).
	row, err := resolveContainer(deps, "murmur-fullname")
	if err != nil {
		t.Fatalf("resolveContainer error: %v", err)
	}
	if row.Name != "murmur-fullname" {
		t.Errorf("expected name 'murmur-fullname', got %q", row.Name)
	}
}

func TestResolveContainer_NotFound(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "", nil)

	_, err := resolveContainer(deps, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent container")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestCheckOwnership_Creator(t *testing.T) {
	t.Parallel()

	row := &db.ContainerRow{Nick: "bird"}
	deps := newTestDockerDeps(t, "", nil)

	err := checkOwnership(ctxWithNick("bird"), deps, row)
	if err != nil {
		t.Errorf("expected no error for creator, got: %v", err)
	}
}

func TestCheckOwnership_System(t *testing.T) {
	t.Parallel()

	row := &db.ContainerRow{Nick: "bird"}
	deps := newTestDockerDeps(t, "", nil)

	err := checkOwnership(ctxWithNick("_system"), deps, row)
	if err != nil {
		t.Errorf("expected no error for _system, got: %v", err)
	}
}

func TestCheckOwnership_Denied(t *testing.T) {
	t.Parallel()

	row := &db.ContainerRow{Nick: "bird", Name: "murmur-test"}
	deps := newTestDockerDeps(t, "", nil)

	err := checkOwnership(ctxWithNick("hacker"), deps, row)
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected 'permission denied', got: %v", err)
	}
}

func TestReconcileContainers(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Insert containers into DB.
	containers := []struct {
		id     string
		name   string
		status string
	}{
		{"abc123", "murmur-running", "running"},
		{"def456", "murmur-missing", "running"},
		{"ghi789", "murmur-changed", "running"},
	}
	for _, c := range containers {
		row := &db.ContainerRow{
			ContainerID: c.id,
			Name:        c.name,
			Image:       "alpine",
			Status:      c.status,
			Channel:     "#test",
			Nick:        "bird",
		}
		if err := database.CreateContainer(row); err != nil {
			t.Fatalf("CreateContainer %s error: %v", c.name, err)
		}
	}

	// Mock Docker output: murmur-running is still running, murmur-changed is now exited,
	// murmur-missing is gone.
	mockOutput := "abc123\tmurmur-running\trunning\nghi789\tmurmur-changed\texited"
	runFunc := func(ctx context.Context, name string, args ...string) (string, error) {
		return mockOutput, nil
	}

	if err := ReconcileContainers(context.Background(), database, logger, runFunc); err != nil {
		t.Fatalf("ReconcileContainers error: %v", err)
	}

	// murmur-running should still be running.
	row, err := database.GetContainerByName("murmur-running")
	if err != nil {
		t.Fatalf("GetContainerByName murmur-running error: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("expected murmur-running status 'running', got %q", row.Status)
	}

	// murmur-changed should be exited.
	row, err = database.GetContainerByName("murmur-changed")
	if err != nil {
		t.Fatalf("GetContainerByName murmur-changed error: %v", err)
	}
	if row.Status != "exited" {
		t.Errorf("expected murmur-changed status 'exited', got %q", row.Status)
	}

	// murmur-missing should have been marked removed and then cleaned up.
	_, err = database.GetContainerByName("murmur-missing")
	if !errors.Is(err, db.ErrContainerNotFound) {
		t.Errorf("expected murmur-missing to be cleaned up, got: %v", err)
	}
}

func TestExtractContainerID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"clean ID", "abc123def456789\n", "abc123def456"},
		{"with warning", "WARNING: something\nabc123def456789\n", "abc123def456"},
		{"short ID", "abc123\n", "abc123"},
		{"empty", "", ""},
		{"whitespace only", "  \n  \n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractContainerID(tt.output)
			if got != tt.want {
				t.Errorf("extractContainerID(%q) = %q, want %q", tt.output, got, tt.want)
			}
		})
	}
}

func TestDockerCreate_DockerFailure(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "error: image not found", fmt.Errorf("exit status 1"))

	args := map[string]any{
		"action": "create",
		"image":  "nonexistent:latest",
		"name":   "fail",
	}

	_, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err == nil {
		t.Fatal("expected error for Docker failure")
	}
}

func TestDockerManage_UnknownAction(t *testing.T) {
	t.Parallel()
	deps := newTestDockerDeps(t, "", nil)

	args := map[string]any{"action": "destroy"}
	_, err := handleDockerManage(ctxWithNick("bird"), args, deps)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unsupported action") {
		t.Errorf("expected 'unsupported action' error, got: %v", err)
	}
}
