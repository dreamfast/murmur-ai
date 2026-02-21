package tools

import (
	"context"
	"strings"
	"testing"
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
