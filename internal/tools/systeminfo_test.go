package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestSystemInfoTool_Name(t *testing.T) {
	t.Parallel()

	tool := NewSystemInfoTool()
	if tool.Name != "system_info" {
		t.Errorf("Name = %q, want %q", tool.Name, "system_info")
	}
	if tool.Description == "" {
		t.Error("Description should not be empty")
	}
	if tool.Handler == nil {
		t.Error("Handler should not be nil")
	}
}

func TestSystemInfoTool_Parameters(t *testing.T) {
	t.Parallel()

	tool := NewSystemInfoTool()

	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}

	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want %q", schema["type"], "object")
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties is not an object")
	}

	if _, ok := props["action"]; !ok {
		t.Error("schema missing 'action' property")
	}
	if _, ok := props["service"]; !ok {
		t.Error("schema missing 'service' property")
	}

	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema required is not an array")
	}
	found := false
	for _, r := range required {
		if r == "action" {
			found = true
			break
		}
	}
	if !found {
		t.Error("schema required should include 'action'")
	}
}

func TestSystemInfoTool_Uptime(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	tool := NewSystemInfoTool()
	result, err := tool.Handler(context.Background(), map[string]any{"action": "uptime"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Uptime: ") {
		t.Errorf("result = %q, want prefix %q", result, "Uptime: ")
	}
}

func TestSystemInfoTool_Memory(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	tool := NewSystemInfoTool()
	result, err := tool.Handler(context.Background(), map[string]any{"action": "memory"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Memory:") {
		t.Errorf("result should start with 'Memory:', got %q", result)
	}
	if !strings.Contains(result, "Total:") {
		t.Error("result should contain 'Total:'")
	}
	if !strings.Contains(result, "Available:") {
		t.Error("result should contain 'Available:'")
	}
}

func TestSystemInfoTool_CPU(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	tool := NewSystemInfoTool()
	result, err := tool.Handler(context.Background(), map[string]any{"action": "cpu"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Load average:") {
		t.Errorf("result should contain 'Load average:', got %q", result)
	}
	if !strings.Contains(result, "CPU cores:") {
		t.Errorf("result should contain 'CPU cores:', got %q", result)
	}
}

func TestSystemInfoTool_OSInfo(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}

	tool := NewSystemInfoTool()
	result, err := tool.Handler(context.Background(), map[string]any{"action": "os_info"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "OS: ") {
		t.Errorf("result should start with 'OS: ', got %q", result)
	}
}

func TestSystemInfoTool_Disk(t *testing.T) {
	t.Parallel()

	tool := NewSystemInfoTool()
	result, err := tool.Handler(context.Background(), map[string]any{"action": "disk"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// df -h output should contain "Filesystem" header.
	if !strings.Contains(result, "Filesystem") {
		t.Errorf("disk result should contain 'Filesystem', got %q", result)
	}
}

func TestSystemInfoTool_InvalidAction(t *testing.T) {
	t.Parallel()

	tool := NewSystemInfoTool()
	_, err := tool.Handler(context.Background(), map[string]any{"action": "nonexistent"})
	if err == nil {
		t.Fatal("expected error for invalid action, got nil")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "unknown action")
	}
}

func TestSystemInfoTool_SystemctlMissingService(t *testing.T) {
	t.Parallel()

	tool := NewSystemInfoTool()
	_, err := tool.Handler(context.Background(), map[string]any{"action": "systemctl_status"})
	if err == nil {
		t.Fatal("expected error for missing service, got nil")
	}
	if !strings.Contains(err.Error(), "service") {
		t.Errorf("error = %q, want to mention 'service'", err.Error())
	}
}

func TestSystemInfoTool_SystemctlInvalidServiceName(t *testing.T) {
	t.Parallel()

	tool := NewSystemInfoTool()

	cases := []struct {
		name    string
		service string
	}{
		{"semicolon injection", "nginx; rm -rf /"},
		{"pipe injection", "nginx | cat"},
		{"backtick injection", "nginx`whoami`"},
		{"space injection", "nginx foo"},
		{"empty string", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"action":  "systemctl_status",
				"service": tc.service,
			})
			if err == nil {
				t.Errorf("expected error for service name %q, got nil", tc.service)
			}
		})
	}
}

func TestSystemInfoTool_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	tool := NewSystemInfoTool()
	// disk uses RunCommand which respects context.
	_, err := tool.Handler(ctx, map[string]any{"action": "disk"})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// --- Unit tests for internal formatting functions ---

func TestFormatUptime(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		seconds float64
		want    string
	}{
		{"zero", 0, "Uptime: 0 minutes"},
		{"minutes only", 300, "Uptime: 5 minutes"},
		{"one minute", 60, "Uptime: 1 minute"},
		{"hours and minutes", 3661, "Uptime: 1 hour, 1 minute"},
		{"days hours minutes", 90061, "Uptime: 1 day, 1 hour, 1 minute"},
		{"multiple days", 259200, "Uptime: 3 days"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatUptime(tc.seconds)
			if got != tc.want {
				t.Errorf("formatUptime(%v) = %q, want %q", tc.seconds, got, tc.want)
			}
		})
	}
}

func TestParseMeminfo(t *testing.T) {
	t.Parallel()

	content := `MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    8192000 kB
SwapTotal:       4096000 kB
SwapFree:        4096000 kB
`
	info := parseMeminfo(content)

	if info["MemTotal"] != 16384000 {
		t.Errorf("MemTotal = %d, want 16384000", info["MemTotal"])
	}
	if info["MemAvailable"] != 8192000 {
		t.Errorf("MemAvailable = %d, want 8192000", info["MemAvailable"])
	}
	if info["SwapTotal"] != 4096000 {
		t.Errorf("SwapTotal = %d, want 4096000", info["SwapTotal"])
	}
}

func TestFormatKB(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		kb   int64
		want string
	}{
		{"bytes range", 512, "512 kB"},
		{"megabytes", 2048, "2.0 MB"},
		{"gigabytes", 2097152, "2.0 GB"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatKB(tc.kb)
			if got != tc.want {
				t.Errorf("formatKB(%d) = %q, want %q", tc.kb, got, tc.want)
			}
		})
	}
}

func TestParseOSRelease(t *testing.T) {
	t.Parallel()

	content := `NAME="Ubuntu"
VERSION="24.04 LTS (Noble Numbat)"
ID=ubuntu
PRETTY_NAME="Ubuntu 24.04 LTS"
# This is a comment
VERSION_ID="24.04"
`
	info := parseOSRelease(content)

	if info["NAME"] != "Ubuntu" {
		t.Errorf("NAME = %q, want %q", info["NAME"], "Ubuntu")
	}
	if info["PRETTY_NAME"] != "Ubuntu 24.04 LTS" {
		t.Errorf("PRETTY_NAME = %q, want %q", info["PRETTY_NAME"], "Ubuntu 24.04 LTS")
	}
	if info["ID"] != "ubuntu" {
		t.Errorf("ID = %q, want %q", info["ID"], "ubuntu")
	}
	if info["VERSION_ID"] != "24.04" {
		t.Errorf("VERSION_ID = %q, want %q", info["VERSION_ID"], "24.04")
	}
}
