package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// MaxOutputBytes is the maximum tool output size in bytes before truncation.
// Set to 25KB to leave headroom for JSON escaping, bus envelope, HMAC
// signature, and multi-part overhead within the ~33KB bus message limit.
const MaxOutputBytes = 25 * 1024

// truncationNotice is appended to output that exceeds MaxOutputBytes.
const truncationNotice = "\n... [output truncated, showing first 25600 bytes]"

// TruncateOutput truncates output to MaxOutputBytes if it exceeds the limit,
// appending a truncation notice. If the output fits, it is returned unchanged.
func TruncateOutput(output string) string {
	if len(output) <= MaxOutputBytes {
		return output
	}
	return output[:MaxOutputBytes] + truncationNotice
}

// RequireStringArg extracts a required string argument from the args map.
// Returns an error if the key is missing or the value is not a string.
func RequireStringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string, got %T", key, v)
	}
	return s, nil
}

// OptionalStringArg extracts an optional string argument from the args map.
// Returns defaultVal if the key is missing or the value is not a string.
func OptionalStringArg(args map[string]any, key, defaultVal string) string {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	s, ok := v.(string)
	if !ok {
		return defaultVal
	}
	return s
}

// OptionalStringSliceArg extracts an optional string slice argument from the
// args map. JSON arrays of strings are unmarshaled by encoding/json as
// []interface{}, so this function handles the conversion. Returns nil if the
// key is missing or the value is not a slice.
func OptionalStringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	slice, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// RunCommand executes a command with the given context and returns the
// combined stdout+stderr output. The output is truncated to MaxOutputBytes.
// If the command exits with a non-zero status, the output is still returned
// along with the error.
func RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	output := TruncateOutput(buf.String())

	if err != nil {
		// Return the output alongside the error — the caller may want both.
		// For example, a command that exits non-zero but produces useful output.
		if ctx.Err() != nil {
			return output, fmt.Errorf("command timed out or cancelled: %w", ctx.Err())
		}
		return output, fmt.Errorf("command failed: %w\n%s", err, output)
	}

	return output, nil
}
