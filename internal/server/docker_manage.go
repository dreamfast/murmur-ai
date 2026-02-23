package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"murmur/internal/config"
	"murmur/internal/db"
	"murmur/internal/tools"
)

// blockedDockerFlags are Docker CLI flags that are blocked for security.
// These flags could be used to escape the container sandbox. Both --flag=value
// and --flag value forms are caught by substring matching on the flag name.
var blockedDockerFlags = []string{
	"--privileged",
	"--volume", "-v",
	"--mount",
	"--cap-add",
	"--device",
	"--pid",
	"--userns",
	"--security-opt",
}

// blockedDockerFlagValues are flag+value combinations that are blocked.
// These catch specific dangerous values like --network host and --ipc host.
var blockedDockerFlagValues = []string{
	"--network=host",
	"--network host",
	"--ipc=host",
	"--ipc host",
}

// dockerManageDeps holds the dependencies for the docker_manage tool handler.
// Using a struct avoids long parameter lists and makes testing easier.
type dockerManageDeps struct {
	cfg     *config.DockerManageConfig
	db      *db.DB
	pm      *PermissionManager
	logger  *slog.Logger
	timeout time.Duration
	// runFunc executes a Docker CLI command and returns the output.
	// Injectable for testing.
	runFunc func(ctx context.Context, name string, args ...string) (string, error)
}

// RegisterDockerManageTool registers the docker_manage server-side tool on the
// given ToolRegistry. The tool provides full Docker container lifecycle
// management: create, exec, logs, stop, start, remove, list, inspect, and build.
func RegisterDockerManageTool(registry *ToolRegistry, database *db.DB, cfg *config.DockerManageConfig, pm *PermissionManager, logger *slog.Logger) error {
	timeout, err := cfg.ParseTimeout()
	if err != nil {
		return fmt.Errorf("RegisterDockerManageTool: %w", err)
	}

	deps := &dockerManageDeps{
		cfg:     cfg,
		db:      database,
		pm:      pm,
		logger:  logger,
		timeout: timeout,
		runFunc: tools.RunCommand,
	}

	t := tools.Tool{
		Name:        "docker_manage",
		Description: "Manage Docker containers: create, exec, logs, stop, start, remove, list, inspect, and build images. Containers are tracked and security-hardened with resource limits.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"description": "The Docker action to perform.",
					"enum": ["create", "exec", "logs", "stop", "start", "remove", "list", "inspect", "build"]
				},
				"image": {
					"type": "string",
					"description": "Docker image to use (required for 'create' action, e.g. 'ubuntu:24.04', 'nginx:latest')."
				},
				"name": {
					"type": "string",
					"description": "Container name (required for 'create'; used to identify containers in other actions). Will be prefixed with 'murmur-' automatically."
				},
				"container": {
					"type": "string",
					"description": "Container name or ID to operate on (for exec, logs, stop, start, remove, inspect)."
				},
			"command": {
				"type": "string",
				"description": "Command to run. For 'create': the container entrypoint command (e.g. 'sleep infinity', 'python -m http.server 8080'). For 'exec': the command to execute inside a running container (e.g. 'ls -la /app')."
			},
				"ports": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Port mappings for 'create' (e.g. ['8080:80', '443:443'])."
				},
				"env": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Environment variables for 'create' (e.g. ['FOO=bar', 'DEBUG=1'])."
				},
				"extra_args": {
					"type": "string",
					"description": "Additional Docker arguments for 'create' (e.g. '--restart=unless-stopped'). Some flags are blocked for security."
				},
				"tail": {
					"type": "integer",
					"description": "Number of log lines to show (for 'logs' action). Defaults to 50."
				},
				"dockerfile": {
					"type": "string",
					"description": "Path to Dockerfile or build context (for 'build' action)."
				},
				"tag": {
					"type": "string",
					"description": "Image tag for 'build' action (e.g. 'myapp:latest')."
				}
			},
			"required": ["action"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleDockerManage(ctx, args, deps)
		},
	}

	if err := registry.Register(t); err != nil {
		return fmt.Errorf("RegisterDockerManageTool: %w", err)
	}
	logger.Info("enabled server tool", "name", "docker_manage")
	return nil
}

// handleDockerManage dispatches docker_manage tool calls to the appropriate
// action handler based on the "action" parameter.
func handleDockerManage(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	action, err := tools.RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	switch action {
	case "create":
		return dockerCreate(ctx, args, deps)
	case "exec":
		return dockerExec(ctx, args, deps)
	case "logs":
		return dockerLogs(ctx, args, deps)
	case "stop":
		return dockerStop(ctx, args, deps)
	case "start":
		return dockerStart(ctx, args, deps)
	case "remove":
		return dockerRemove(ctx, args, deps)
	case "list":
		return dockerList(deps)
	case "inspect":
		return dockerInspect(ctx, args, deps)
	case "build":
		return dockerBuild(ctx, args, deps)
	default:
		return "", fmt.Errorf("handleDockerManage: unsupported action %q", action)
	}
}

// dockerCreate creates a new Docker container with security hardening.
func dockerCreate(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	image, err := tools.RequireStringArg(args, "image")
	if err != nil {
		return "", fmt.Errorf("dockerCreate: %w", err)
	}

	name, err := tools.RequireStringArg(args, "name")
	if err != nil {
		return "", fmt.Errorf("dockerCreate: %w", err)
	}

	// Validate image against allowlist.
	if err := validateImage(image, deps.cfg.AllowedImages); err != nil {
		return "", fmt.Errorf("dockerCreate: %w", err)
	}

	// Check container limit.
	active, err := deps.db.CountActiveContainers()
	if err != nil {
		return "", fmt.Errorf("dockerCreate: count active containers: %w", err)
	}
	if active >= deps.cfg.MaxContainers {
		return "", fmt.Errorf("dockerCreate: container limit reached (%d/%d active containers)", active, deps.cfg.MaxContainers)
	}

	// Validate extra_args for blocked flags.
	extraArgs := tools.OptionalStringArg(args, "extra_args", "")
	if extraArgs != "" {
		if err := validateExtraArgs(extraArgs); err != nil {
			return "", fmt.Errorf("dockerCreate: %w", err)
		}
	}

	// Extract requesting nick and channel from context.
	nick, _ := ctx.Value(requestNickKey{}).(string)
	if nick == "" {
		nick = "_system"
	}

	// Prefix the container name.
	fullName := "murmur-" + name

	// Build docker run command.
	dockerArgs := []string{
		"run", "-d",
		"--name=" + fullName,
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		fmt.Sprintf("--pids-limit=%d", deps.cfg.PidsLimit),
		fmt.Sprintf("--memory=%s", deps.cfg.MemoryLimit),
		fmt.Sprintf("--cpus=%s", deps.cfg.CPULimit),
		"--label=murmur.managed=true",
		"--label=murmur.creator=" + nick,
	}

	// Network configuration.
	if !deps.cfg.GetAllowNetwork() {
		dockerArgs = append(dockerArgs, "--network=none")
	} else if deps.cfg.Network != "" {
		dockerArgs = append(dockerArgs, "--network="+deps.cfg.Network)
	}

	// Read-only filesystem.
	if deps.cfg.ReadOnly {
		dockerArgs = append(dockerArgs, "--read-only")
	}

	// Port mappings.
	ports := tools.OptionalStringSliceArg(args, "ports")
	for _, p := range ports {
		dockerArgs = append(dockerArgs, "-p", p)
	}

	// Environment variables.
	envVars := tools.OptionalStringSliceArg(args, "env")
	for _, e := range envVars {
		dockerArgs = append(dockerArgs, "-e", e)
	}

	// Extra args (already validated).
	if extraArgs != "" {
		parts := strings.Fields(extraArgs)
		dockerArgs = append(dockerArgs, parts...)
	}

	// Image and optional command.
	dockerArgs = append(dockerArgs, image)

	// Append the container command if provided (e.g. "sleep infinity",
	// "python -m http.server 8080"). Without this, Docker uses the image's
	// default CMD/ENTRYPOINT which may exit immediately for interactive images.
	command := tools.OptionalStringArg(args, "command", "")
	if command != "" {
		dockerArgs = append(dockerArgs, strings.Fields(command)...)
	}

	// Execute with timeout.
	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	output, err := deps.runFunc(execCtx, "docker", dockerArgs...)
	if err != nil {
		return "", fmt.Errorf("dockerCreate: %w\n%s", err, output)
	}

	// Extract container ID from Docker output. Docker prints the full container
	// ID as the last line of stdout. We take the last non-empty line and
	// truncate to 12 chars (short ID format).
	containerID := extractContainerID(output)

	// Store in database.
	row := &db.ContainerRow{
		ContainerID: containerID,
		Name:        fullName,
		Image:       image,
		Status:      "running",
		Channel:     "", // Channel not available in context; will be empty for now.
		Nick:        nick,
		Ports:       db.StringSlice(ports),
	}
	if err := deps.db.CreateContainer(row); err != nil {
		deps.logger.Error("dockerCreate: failed to store container in DB",
			"container_id", containerID,
			"error", err,
		)
		// Container was created but DB insert failed — still return success.
		return fmt.Sprintf("Container %s created (ID: %s) but failed to track in DB: %v", fullName, containerID, err), nil
	}

	deps.logger.Info("docker container created",
		"name", fullName,
		"image", image,
		"container_id", containerID,
		"creator", nick,
	)

	result := fmt.Sprintf("Container %s created successfully.\nID: %s\nImage: %s", fullName, containerID, image)
	if len(ports) > 0 {
		result += fmt.Sprintf("\nPorts: %s", strings.Join(ports, ", "))
	}
	return result, nil
}

// dockerExec executes a command inside a running managed container.
func dockerExec(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	container, err := tools.RequireStringArg(args, "container")
	if err != nil {
		return "", fmt.Errorf("dockerExec: %w", err)
	}

	command, err := tools.RequireStringArg(args, "command")
	if err != nil {
		return "", fmt.Errorf("dockerExec: %w", err)
	}

	// Resolve container and check ownership.
	row, err := resolveContainer(deps, container)
	if err != nil {
		return "", fmt.Errorf("dockerExec: %w", err)
	}

	if err := checkOwnership(ctx, deps, row); err != nil {
		return "", fmt.Errorf("dockerExec: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	dockerArgs := []string{"exec", row.ContainerID, "sh", "-c", command}
	output, err := deps.runFunc(execCtx, "docker", dockerArgs...)
	if err != nil {
		// Return output alongside error for non-zero exit codes.
		if output != "" {
			return tools.TruncateOutput(output), nil
		}
		return "", fmt.Errorf("dockerExec: %w", err)
	}

	return tools.TruncateOutput(output), nil
}

// dockerLogs retrieves logs from a managed container.
func dockerLogs(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	container, err := tools.RequireStringArg(args, "container")
	if err != nil {
		return "", fmt.Errorf("dockerLogs: %w", err)
	}

	row, err := resolveContainer(deps, container)
	if err != nil {
		return "", fmt.Errorf("dockerLogs: %w", err)
	}

	if err := checkOwnership(ctx, deps, row); err != nil {
		return "", fmt.Errorf("dockerLogs: %w", err)
	}

	tail := tools.OptionalIntArg(args, "tail", 50)
	if tail <= 0 {
		tail = 50
	}
	if tail > 500 {
		tail = 500
	}

	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	dockerArgs := []string{"logs", "--tail", fmt.Sprintf("%d", tail), row.ContainerID}
	output, err := deps.runFunc(execCtx, "docker", dockerArgs...)
	if err != nil {
		if output != "" {
			return tools.TruncateOutput(output), nil
		}
		return "", fmt.Errorf("dockerLogs: %w", err)
	}

	if output == "" {
		return "(no logs)", nil
	}
	return tools.TruncateOutput(output), nil
}

// dockerStop stops a running managed container.
func dockerStop(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	container, err := tools.RequireStringArg(args, "container")
	if err != nil {
		return "", fmt.Errorf("dockerStop: %w", err)
	}

	row, err := resolveContainer(deps, container)
	if err != nil {
		return "", fmt.Errorf("dockerStop: %w", err)
	}

	if err := checkOwnership(ctx, deps, row); err != nil {
		return "", fmt.Errorf("dockerStop: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	output, err := deps.runFunc(execCtx, "docker", "stop", row.ContainerID)
	if err != nil {
		return "", fmt.Errorf("dockerStop: %w\n%s", err, output)
	}

	if err := deps.db.UpdateContainerStatus(row.ContainerID, "stopped"); err != nil {
		deps.logger.Error("dockerStop: failed to update status in DB",
			"container_id", row.ContainerID,
			"error", err,
		)
	}

	return fmt.Sprintf("Container %s stopped.", row.Name), nil
}

// dockerStart starts a stopped managed container.
func dockerStart(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	container, err := tools.RequireStringArg(args, "container")
	if err != nil {
		return "", fmt.Errorf("dockerStart: %w", err)
	}

	row, err := resolveContainer(deps, container)
	if err != nil {
		return "", fmt.Errorf("dockerStart: %w", err)
	}

	if err := checkOwnership(ctx, deps, row); err != nil {
		return "", fmt.Errorf("dockerStart: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	output, err := deps.runFunc(execCtx, "docker", "start", row.ContainerID)
	if err != nil {
		return "", fmt.Errorf("dockerStart: %w\n%s", err, output)
	}

	if err := deps.db.UpdateContainerStatus(row.ContainerID, "running"); err != nil {
		deps.logger.Error("dockerStart: failed to update status in DB",
			"container_id", row.ContainerID,
			"error", err,
		)
	}

	return fmt.Sprintf("Container %s started.", row.Name), nil
}

// dockerRemove force-removes a managed container and deletes it from the DB.
func dockerRemove(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	container, err := tools.RequireStringArg(args, "container")
	if err != nil {
		return "", fmt.Errorf("dockerRemove: %w", err)
	}

	row, err := resolveContainer(deps, container)
	if err != nil {
		return "", fmt.Errorf("dockerRemove: %w", err)
	}

	if err := checkOwnership(ctx, deps, row); err != nil {
		return "", fmt.Errorf("dockerRemove: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	output, err := deps.runFunc(execCtx, "docker", "rm", "-f", row.ContainerID)
	if err != nil {
		return "", fmt.Errorf("dockerRemove: %w\n%s", err, output)
	}

	if err := deps.db.RemoveContainer(row.ContainerID); err != nil {
		deps.logger.Error("dockerRemove: failed to remove from DB",
			"container_id", row.ContainerID,
			"error", err,
		)
	}

	return fmt.Sprintf("Container %s removed.", row.Name), nil
}

// dockerList lists all managed containers from the database.
func dockerList(deps *dockerManageDeps) (string, error) {
	containers, err := deps.db.ListContainers()
	if err != nil {
		return "", fmt.Errorf("dockerList: %w", err)
	}

	if len(containers) == 0 {
		return "No managed containers.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Managed containers (%d):\n", len(containers)))
	for _, c := range containers {
		sb.WriteString(fmt.Sprintf("  %s (ID: %s) — image: %s, status: %s, creator: %s",
			c.Name, c.ContainerID, c.Image, c.Status, c.Nick))
		if len(c.Ports) > 0 {
			sb.WriteString(fmt.Sprintf(", ports: %s", strings.Join(c.Ports, ", ")))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// dockerInspect returns detailed information about a managed container.
func dockerInspect(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	container, err := tools.RequireStringArg(args, "container")
	if err != nil {
		return "", fmt.Errorf("dockerInspect: %w", err)
	}

	row, err := resolveContainer(deps, container)
	if err != nil {
		return "", fmt.Errorf("dockerInspect: %w", err)
	}

	if err := checkOwnership(ctx, deps, row); err != nil {
		return "", fmt.Errorf("dockerInspect: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	// Use a compact format to keep output manageable.
	dockerArgs := []string{
		"inspect",
		"--format", `ID: {{.Id}}
Name: {{.Name}}
Image: {{.Config.Image}}
Status: {{.State.Status}}
Running: {{.State.Running}}
StartedAt: {{.State.StartedAt}}
Ports: {{range $k, $v := .NetworkSettings.Ports}}{{$k}}->{{range $v}}{{.HostIp}}:{{.HostPort}}{{end}} {{end}}
Mounts: {{range .Mounts}}{{.Source}}->{{.Destination}} {{end}}
Env: {{range .Config.Env}}{{.}} {{end}}`,
		row.ContainerID,
	}

	output, err := deps.runFunc(execCtx, "docker", dockerArgs...)
	if err != nil {
		return "", fmt.Errorf("dockerInspect: %w\n%s", err, output)
	}

	return tools.TruncateOutput(output), nil
}

// dockerBuild builds a Docker image from a Dockerfile. Requires admin
// privileges because builds can access the host filesystem.
func dockerBuild(ctx context.Context, args map[string]any, deps *dockerManageDeps) (string, error) {
	if !deps.cfg.AllowBuild {
		return "", fmt.Errorf("dockerBuild: build action is disabled (set allow_build = true in config)")
	}

	// Require admin for builds — they can access the host filesystem.
	nick, _ := ctx.Value(requestNickKey{}).(string)
	if nick != "_system" && (deps.pm == nil || !deps.pm.IsAdmin(nick)) {
		return "", fmt.Errorf("dockerBuild: permission denied: admin role required for build operations")
	}

	dockerfile, err := tools.RequireStringArg(args, "dockerfile")
	if err != nil {
		return "", fmt.Errorf("dockerBuild: %w", err)
	}

	// Validate dockerfile path doesn't contain path traversal.
	if strings.Contains(dockerfile, "..") {
		return "", fmt.Errorf("dockerBuild: path traversal not allowed in dockerfile path")
	}

	tag, err := tools.RequireStringArg(args, "tag")
	if err != nil {
		return "", fmt.Errorf("dockerBuild: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()

	dockerArgs := []string{"build", "-t", tag, "-f", dockerfile, "."}
	output, err := deps.runFunc(execCtx, "docker", dockerArgs...)
	if err != nil {
		return "", fmt.Errorf("dockerBuild: %w\n%s", err, tools.TruncateOutput(output))
	}

	return fmt.Sprintf("Image %s built successfully.\n%s", tag, tools.TruncateOutput(output)), nil
}

// extractContainerID extracts the Docker container ID from command output.
// Docker prints the full container ID as the last non-empty line of stdout.
// We take the last non-empty line and truncate to 12 chars (short ID format).
func extractContainerID(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			if len(line) > 12 {
				return line[:12]
			}
			return line
		}
	}
	return strings.TrimSpace(output)
}

// resolveContainer looks up a container by name or container ID.
// It first tries to find by name (with "murmur-" prefix if not already present),
// then falls back to container ID lookup.
func resolveContainer(deps *dockerManageDeps, nameOrID string) (*db.ContainerRow, error) {
	// Try by name first (add prefix if missing).
	lookupName := nameOrID
	if !strings.HasPrefix(lookupName, "murmur-") {
		lookupName = "murmur-" + lookupName
	}

	row, err := deps.db.GetContainerByName(lookupName)
	if err == nil {
		return row, nil
	}

	// Fall back to container ID lookup.
	row, err = deps.db.GetContainer(nameOrID)
	if err == nil {
		return row, nil
	}

	return nil, fmt.Errorf("container %q not found (tried name %q and ID %q)", nameOrID, lookupName, nameOrID)
}

// checkOwnership verifies that the requesting user is the container's creator
// or an admin. Returns an error if the user is not authorized.
func checkOwnership(ctx context.Context, deps *dockerManageDeps, row *db.ContainerRow) error {
	nick, _ := ctx.Value(requestNickKey{}).(string)
	if nick == "" {
		nick = "_system"
	}

	// System user always has access.
	if nick == "_system" {
		return nil
	}

	// Creator has access.
	if strings.EqualFold(nick, row.Nick) {
		return nil
	}

	// Admins have access.
	if deps.pm != nil && deps.pm.IsAdmin(nick) {
		return nil
	}

	return fmt.Errorf("permission denied: container %q was created by %q (you are %q)", row.Name, row.Nick, nick)
}

// validateImage checks whether an image is allowed by the allowlist.
// If the allowlist is empty, all images are allowed. Patterns use
// filepath.Match glob syntax.
func validateImage(image string, allowedImages []string) error {
	if len(allowedImages) == 0 {
		return nil
	}

	for _, pattern := range allowedImages {
		matched, err := filepath.Match(pattern, image)
		if err != nil {
			continue // Invalid pattern — skip.
		}
		if matched {
			return nil
		}
	}

	return fmt.Errorf("image %q is not in the allowed images list", image)
}

// validateExtraArgs checks that extra Docker arguments don't contain
// blocked security-sensitive flags. It checks both individual flag names
// and specific flag+value combinations (e.g., --network host).
func validateExtraArgs(extraArgs string) error {
	lower := strings.ToLower(extraArgs)

	// Check blocked flag names (substring match catches --flag=value too).
	for _, blocked := range blockedDockerFlags {
		if strings.Contains(lower, strings.ToLower(blocked)) {
			return fmt.Errorf("blocked Docker flag %q in extra_args", blocked)
		}
	}

	// Check blocked flag+value combinations.
	for _, blocked := range blockedDockerFlagValues {
		if strings.Contains(lower, strings.ToLower(blocked)) {
			return fmt.Errorf("blocked Docker flag %q in extra_args", blocked)
		}
	}

	return nil
}

// ReconcileContainers synchronizes the database with Docker's actual state.
// It queries Docker for all containers with the murmur.managed=true label and
// updates the database accordingly. Containers in the DB that no longer exist
// in Docker are marked as "removed". Called on server startup.
func ReconcileContainers(ctx context.Context, database *db.DB, logger *slog.Logger, runFunc func(ctx context.Context, name string, args ...string) (string, error)) error {
	if runFunc == nil {
		runFunc = tools.RunCommand
	}

	// Query Docker for all managed containers.
	output, err := runFunc(ctx, "docker", "ps", "-a",
		"--filter", "label=murmur.managed=true",
		"--format", "{{.ID}}\t{{.Names}}\t{{.State}}")
	if err != nil {
		return fmt.Errorf("ReconcileContainers: docker ps: %w", err)
	}

	// Parse Docker output into a map of container name -> state.
	dockerState := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			logger.Warn("ReconcileContainers: unexpected docker ps line", "line", line)
			continue
		}
		_, name, state := parts[0], parts[1], parts[2]
		dockerState[name] = state
	}

	// Get all containers from the database.
	dbContainers, err := database.ListContainers()
	if err != nil {
		return fmt.Errorf("ReconcileContainers: list containers: %w", err)
	}

	// Reconcile each DB record with Docker reality.
	for _, c := range dbContainers {
		if c.Status == "removed" {
			continue // Already marked as removed.
		}

		state, exists := dockerState[c.Name]
		if !exists {
			// Container no longer exists in Docker — mark as removed.
			if err := database.UpdateContainerStatus(c.ContainerID, "removed"); err != nil {
				logger.Error("ReconcileContainers: failed to mark container as removed",
					"name", c.Name,
					"container_id", c.ContainerID,
					"error", err,
				)
			} else {
				logger.Info("ReconcileContainers: marked missing container as removed",
					"name", c.Name,
					"container_id", c.ContainerID,
				)
			}
			continue
		}

		// Update status if it changed.
		if c.Status != state {
			if err := database.UpdateContainerStatus(c.ContainerID, state); err != nil {
				logger.Error("ReconcileContainers: failed to update container status",
					"name", c.Name,
					"container_id", c.ContainerID,
					"old_status", c.Status,
					"new_status", state,
					"error", err,
				)
			} else {
				logger.Info("ReconcileContainers: updated container status",
					"name", c.Name,
					"old_status", c.Status,
					"new_status", state,
				)
			}
		}
	}

	// Clean up removed containers from the DB.
	deleted, err := database.DeleteRemovedContainers()
	if err != nil {
		logger.Error("ReconcileContainers: failed to clean up removed containers", "error", err)
	} else if deleted > 0 {
		logger.Info("ReconcileContainers: cleaned up removed containers", "count", deleted)
	}

	logger.Info("ReconcileContainers: reconciliation complete",
		"docker_containers", len(dockerState),
		"db_containers", len(dbContainers),
	)
	return nil
}
