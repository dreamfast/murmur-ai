package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// serviceNameRe validates systemctl service names to prevent injection.
// Allows alphanumeric, underscore, at-sign, dot, and hyphen.
var serviceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_@.\-]+$`)

// NewSystemInfoTool creates the system_info tool with all 8 actions:
// uptime, disk, memory, cpu, os_info, docker_ps, apt_upgradable,
// systemctl_status. These are safe, read-only queries that run directly
// on the host (no Docker needed).
func NewSystemInfoTool() Tool {
	return Tool{
		Name:        "system_info",
		Description: "Query system information. Actions: uptime, disk, memory, cpu, os_info, docker_ps, apt_upgradable, systemctl_status.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["uptime", "disk", "memory", "cpu", "os_info", "docker_ps", "apt_upgradable", "systemctl_status"],
					"description": "The system info query to run"
				},
				"service": {
					"type": "string",
					"description": "Service name for systemctl_status action"
				}
			},
			"required": ["action"]
		}`),
		Handler: handleSystemInfo,
	}
}

// handleSystemInfo dispatches to the appropriate system info action.
func handleSystemInfo(ctx context.Context, args map[string]any) (string, error) {
	action, err := RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	switch action {
	case "uptime":
		return getUptime()
	case "disk":
		return RunCommand(ctx, "df", "-h")
	case "memory":
		return getMemoryInfo()
	case "cpu":
		return getCPUInfo()
	case "os_info":
		return getOSInfo()
	case "docker_ps":
		return RunCommand(ctx, "docker", "ps", "--format", "table {{.Names}}\t{{.Status}}\t{{.Image}}")
	case "apt_upgradable":
		return RunCommand(ctx, "apt", "list", "--upgradable")
	case "systemctl_status":
		return getSystemctlStatus(ctx, args)
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// getUptime reads /proc/uptime and formats it as a human-readable string.
func getUptime() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("system_info: uptime is only supported on Linux")
	}

	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", fmt.Errorf("read /proc/uptime: %w", err)
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return "", fmt.Errorf("unexpected /proc/uptime format")
	}

	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", fmt.Errorf("parse uptime: %w", err)
	}

	return formatUptime(seconds), nil
}

// formatUptime converts seconds to a human-readable duration string.
func formatUptime(totalSeconds float64) string {
	total := int(math.Round(totalSeconds))
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d day%s", days, plural(days)))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d hour%s", hours, plural(hours)))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d minute%s", minutes, plural(minutes)))
	}
	return "Uptime: " + strings.Join(parts, ", ")
}

// plural returns "s" if n != 1, empty string otherwise.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// getMemoryInfo reads /proc/meminfo and formats memory statistics.
func getMemoryInfo() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("system_info: memory is only supported on Linux")
	}

	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "", fmt.Errorf("read /proc/meminfo: %w", err)
	}

	info := parseMeminfo(string(data))

	var sb strings.Builder
	sb.WriteString("Memory:\n")
	if v, ok := info["MemTotal"]; ok {
		sb.WriteString(fmt.Sprintf("  Total:     %s\n", formatKB(v)))
	}
	if v, ok := info["MemAvailable"]; ok {
		sb.WriteString(fmt.Sprintf("  Available: %s\n", formatKB(v)))
	}
	if total, ok := info["MemTotal"]; ok {
		if avail, ok2 := info["MemAvailable"]; ok2 {
			used := total - avail
			pct := 0.0
			if total > 0 {
				pct = float64(used) / float64(total) * 100
			}
			sb.WriteString(fmt.Sprintf("  Used:      %s (%.1f%%)\n", formatKB(used), pct))
		}
	}
	if v, ok := info["SwapTotal"]; ok && v > 0 {
		sb.WriteString(fmt.Sprintf("  Swap:      %s total", formatKB(v)))
		if free, ok2 := info["SwapFree"]; ok2 {
			sb.WriteString(fmt.Sprintf(", %s free", formatKB(free)))
		}
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

// parseMeminfo parses /proc/meminfo content into a map of key -> value in kB.
func parseMeminfo(content string) map[string]int64 {
	result := make(map[string]int64)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		result[key] = val
	}
	return result
}

// formatKB formats a value in kB to a human-readable string.
func formatKB(kb int64) string {
	switch {
	case kb >= 1048576: // 1 GB in kB
		return fmt.Sprintf("%.1f GB", float64(kb)/1048576)
	case kb >= 1024:
		return fmt.Sprintf("%.1f MB", float64(kb)/1024)
	default:
		return fmt.Sprintf("%d kB", kb)
	}
}

// getCPUInfo reads /proc/loadavg and /proc/cpuinfo to report load and core count.
func getCPUInfo() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("system_info: cpu is only supported on Linux")
	}

	var sb strings.Builder

	// Load averages.
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			sb.WriteString(fmt.Sprintf("Load average: %s %s %s (1m 5m 15m)\n", fields[0], fields[1], fields[2]))
		}
	}

	// Core count from /proc/cpuinfo.
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		cores := 0
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "processor") {
				cores++
			}
		}
		if cores > 0 {
			sb.WriteString(fmt.Sprintf("CPU cores: %d", cores))
		}
	}

	result := strings.TrimRight(sb.String(), "\n")
	if result == "" {
		return "", fmt.Errorf("could not read CPU information")
	}
	return result, nil
}

// getOSInfo reads /etc/os-release and formats distribution information.
func getOSInfo() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("system_info: os_info is only supported on Linux")
	}

	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", fmt.Errorf("read /etc/os-release: %w", err)
	}

	info := parseOSRelease(string(data))

	var sb strings.Builder
	sb.WriteString("OS: ")
	if name, ok := info["PRETTY_NAME"]; ok {
		sb.WriteString(name)
	} else {
		if id, ok := info["ID"]; ok {
			sb.WriteString(id)
		}
		if ver, ok := info["VERSION_ID"]; ok {
			sb.WriteString(" " + ver)
		}
	}

	// Add kernel version.
	if kdata, err := os.ReadFile("/proc/version"); err == nil {
		fields := strings.Fields(string(kdata))
		if len(fields) >= 3 {
			sb.WriteString(fmt.Sprintf("\nKernel: %s", fields[2]))
		}
	}

	return sb.String(), nil
}

// parseOSRelease parses /etc/os-release content into a key-value map.
// Values are unquoted if surrounded by double quotes.
func parseOSRelease(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		// Unquote value.
		val = strings.Trim(val, `"`)
		result[key] = val
	}
	return result
}

// getSystemctlStatus validates the service name and runs systemctl status.
func getSystemctlStatus(ctx context.Context, args map[string]any) (string, error) {
	service, err := RequireStringArg(args, "service")
	if err != nil {
		return "", fmt.Errorf("systemctl_status requires a 'service' argument: %w", err)
	}

	if !serviceNameRe.MatchString(service) {
		return "", fmt.Errorf("invalid service name %q: must match [a-zA-Z0-9_@.-]", service)
	}

	// systemctl status returns exit code 3 for inactive services, which is
	// not an error — we want the output regardless. However, context
	// cancellation/timeout errors must always propagate.
	output, err := RunCommand(ctx, "systemctl", "status", service, "--no-pager", "-l")
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("systemctl_status: %w", ctx.Err())
		}
		// Non-zero exit with output (e.g., inactive service) — return output.
		if output != "" {
			return output, nil
		}
		return "", err
	}
	return output, nil
}
