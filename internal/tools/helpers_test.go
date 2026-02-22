package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTruncateOutput_Short(t *testing.T) {
	t.Parallel()

	input := "hello world"
	got := TruncateOutput(input)
	if got != input {
		t.Errorf("TruncateOutput(%q) = %q, want unchanged", input, got)
	}
}

func TestTruncateOutput_ExactLimit(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("a", MaxOutputBytes)
	got := TruncateOutput(input)
	if got != input {
		t.Errorf("TruncateOutput at exact limit should return unchanged, got len=%d", len(got))
	}
}

func TestTruncateOutput_Long(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("x", MaxOutputBytes+1000)
	got := TruncateOutput(input)

	if !strings.HasSuffix(got, truncationNotice) {
		t.Error("TruncateOutput should append truncation notice")
	}
	// The truncated content should be MaxOutputBytes + notice length.
	expectedLen := MaxOutputBytes + len(truncationNotice)
	if len(got) != expectedLen {
		t.Errorf("TruncateOutput length = %d, want %d", len(got), expectedLen)
	}
}

func TestRequireStringArg_Present(t *testing.T) {
	t.Parallel()

	args := map[string]any{"name": "hello"}
	got, err := RequireStringArg(args, "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("RequireStringArg = %q, want %q", got, "hello")
	}
}

func TestRequireStringArg_Missing(t *testing.T) {
	t.Parallel()

	args := map[string]any{}
	_, err := RequireStringArg(args, "name")
	if err == nil {
		t.Fatal("expected error for missing arg, got nil")
	}
}

func TestRequireStringArg_WrongType(t *testing.T) {
	t.Parallel()

	args := map[string]any{"name": 42}
	_, err := RequireStringArg(args, "name")
	if err == nil {
		t.Fatal("expected error for wrong type, got nil")
	}
}

func TestOptionalStringArg_Present(t *testing.T) {
	t.Parallel()

	args := map[string]any{"lang": "python"}
	got := OptionalStringArg(args, "lang", "bash")
	if got != "python" {
		t.Errorf("OptionalStringArg = %q, want %q", got, "python")
	}
}

func TestOptionalStringArg_Missing(t *testing.T) {
	t.Parallel()

	args := map[string]any{}
	got := OptionalStringArg(args, "lang", "bash")
	if got != "bash" {
		t.Errorf("OptionalStringArg = %q, want default %q", got, "bash")
	}
}

func TestOptionalStringArg_WrongType(t *testing.T) {
	t.Parallel()

	args := map[string]any{"lang": 123}
	got := OptionalStringArg(args, "lang", "bash")
	if got != "bash" {
		t.Errorf("OptionalStringArg = %q, want default %q for wrong type", got, "bash")
	}
}

func TestOptionalStringSliceArg_Present(t *testing.T) {
	t.Parallel()

	args := map[string]any{"items": []any{"a", "b", "c"}}
	got := OptionalStringSliceArg(args, "items")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("OptionalStringSliceArg = %v, want [a b c]", got)
	}
}

func TestOptionalStringSliceArg_Missing(t *testing.T) {
	t.Parallel()

	args := map[string]any{}
	got := OptionalStringSliceArg(args, "items")
	if got != nil {
		t.Errorf("OptionalStringSliceArg = %v, want nil", got)
	}
}

func TestOptionalStringSliceArg_WrongType(t *testing.T) {
	t.Parallel()

	args := map[string]any{"items": "not-a-slice"}
	got := OptionalStringSliceArg(args, "items")
	if got != nil {
		t.Errorf("OptionalStringSliceArg = %v, want nil for wrong type", got)
	}
}

func TestRunCommand_Success(t *testing.T) {
	t.Parallel()

	output, err := RunCommand(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(output) != "hello" {
		t.Errorf("RunCommand output = %q, want %q", strings.TrimSpace(output), "hello")
	}
}

func TestRunCommand_Failure(t *testing.T) {
	t.Parallel()

	_, err := RunCommand(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error for failing command, got nil")
	}
}

func TestRunCommand_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := RunCommand(ctx, "sleep", "10")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestOptionalIntArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       map[string]any
		key        string
		defaultVal int
		want       int
	}{
		{
			name:       "float64 value",
			args:       map[string]any{"count": float64(42)},
			key:        "count",
			defaultVal: 10,
			want:       42,
		},
		{
			name:       "int value",
			args:       map[string]any{"count": 7},
			key:        "count",
			defaultVal: 10,
			want:       7,
		},
		{
			name:       "json.Number value",
			args:       map[string]any{"count": json.Number("99")},
			key:        "count",
			defaultVal: 10,
			want:       99,
		},
		{
			name:       "json.Number invalid",
			args:       map[string]any{"count": json.Number("abc")},
			key:        "count",
			defaultVal: 10,
			want:       10,
		},
		{
			name:       "missing key",
			args:       map[string]any{},
			key:        "count",
			defaultVal: 5,
			want:       5,
		},
		{
			name:       "wrong type",
			args:       map[string]any{"count": "not-a-number"},
			key:        "count",
			defaultVal: 5,
			want:       5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := OptionalIntArg(tt.args, tt.key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("OptionalIntArg() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewHTTPClient_Injected(t *testing.T) {
	t.Parallel()

	injected := &http.Client{Timeout: 99 * time.Second}
	got := NewHTTPClient(5*time.Second, injected)
	if got != injected {
		t.Error("NewHTTPClient should return injected client when non-nil")
	}
}

func TestNewHTTPClient_Default(t *testing.T) {
	t.Parallel()

	got := NewHTTPClient(15*time.Second, nil)
	if got == nil {
		t.Fatal("NewHTTPClient should return non-nil client")
	}
	if got.Timeout != 15*time.Second {
		t.Errorf("NewHTTPClient timeout = %v, want %v", got.Timeout, 15*time.Second)
	}
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string unchanged",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length unchanged",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "long string truncated",
			input:  "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}
