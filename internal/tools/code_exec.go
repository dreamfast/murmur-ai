package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxPistonResponseBytes limits the size of Piston API responses to prevent
// memory exhaustion from unexpectedly large payloads.
const maxPistonResponseBytes = 10 * 1024 * 1024 // 10 MB

// CodeExecToolConfig holds runtime configuration for the Piston-based
// code execution tool.
type CodeExecToolConfig struct {
	// PistonURL is the base URL of the Piston API.
	PistonURL string
	// DefaultLang is the default programming language when none is specified.
	DefaultLang string
	// RunTimeout is the maximum execution time in milliseconds.
	RunTimeout int
	// RunMemoryLimit is the maximum memory in bytes.
	RunMemoryLimit int
}

// pistonRuntime represents a language runtime from the Piston /api/v2/runtimes endpoint.
type pistonRuntime struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

// fetchPistonRuntimes queries Piston for installed language runtimes.
// Returns a formatted string listing available languages and versions.
func fetchPistonRuntimes(pistonURL string) string {
	url := strings.TrimRight(pistonURL, "/") + "/api/v2/runtimes"
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return ""
	}

	var runtimes []pistonRuntime
	if err := json.Unmarshal(body, &runtimes); err != nil {
		return ""
	}

	if len(runtimes) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Available: ")
	for i, rt := range runtimes {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(rt.Language)
		sb.WriteString(" ")
		sb.WriteString(rt.Version)
	}
	return sb.String()
}

// pistonRequest is the request body for the Piston /api/v2/execute endpoint.
type pistonRequest struct {
	Language       string       `json:"language"`
	Version        string       `json:"version,omitempty"`
	Files          []pistonFile `json:"files"`
	Stdin          string       `json:"stdin,omitempty"`
	Args           []string     `json:"args,omitempty"`
	RunTimeout     int          `json:"run_timeout,omitempty"`
	RunMemoryLimit int          `json:"run_memory_limit,omitempty"`
}

// pistonFile represents a source file in a Piston execution request.
type pistonFile struct {
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
}

// pistonResponse is the response body from the Piston /api/v2/execute endpoint.
type pistonResponse struct {
	Language string        `json:"language"`
	Version  string        `json:"version"`
	Run      pistonResult  `json:"run"`
	Compile  *pistonResult `json:"compile,omitempty"`
}

// pistonResult holds the output of a compile or run stage.
type pistonResult struct {
	Stdout string  `json:"stdout"`
	Stderr string  `json:"stderr"`
	Code   int     `json:"code"`
	Signal *string `json:"signal"`
	Output string  `json:"output"`
}

// NewCodeExecTool creates a Piston-based code execution tool that supports
// 60+ programming languages in a sandboxed environment. It queries Piston
// at creation time to discover installed runtimes and includes them in the
// tool description so the LLM knows exactly which languages and versions
// are available.
func NewCodeExecTool(cfg CodeExecToolConfig) Tool {
	client := &http.Client{
		Timeout: 2 * time.Minute,
	}

	// Discover installed runtimes to include in the tool description.
	runtimeList := fetchPistonRuntimes(cfg.PistonURL)
	description := "Execute code in a sandboxed environment. Use the 'language' parameter with the exact language name and 'version' with the exact version string from the available list. Always specify both language and version."
	if runtimeList != "" {
		description += " " + runtimeList
	}

	return Tool{
		Name:        "code_exec",
		Description: description,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"language": {
					"type": "string",
					"description": "Programming language name exactly as listed (e.g., 'python', 'javascript', 'go', 'rust', 'bash')"
				},
				"version": {
					"type": "string",
					"description": "Exact version string from the available runtimes list (e.g., '3.9.4', '20.11.1'). Use '*' for latest."
				},
				"code": {
					"type": "string",
					"description": "The source code to execute"
				},
				"stdin": {
					"type": "string",
					"description": "Optional: stdin input for the program"
				},
				"args": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Optional: command-line arguments"
				}
			},
			"required": ["language", "code"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleCodeExec(ctx, cfg, client, args)
		},
	}
}

// handleCodeExec extracts arguments, builds a Piston request, sends it,
// and formats the response.
func handleCodeExec(ctx context.Context, cfg CodeExecToolConfig, client *http.Client, args map[string]any) (string, error) {
	// Extract language — fall back to default if missing.
	language := OptionalStringArg(args, "language", cfg.DefaultLang)
	if language == "" {
		return "", fmt.Errorf("missing required argument \"language\"")
	}

	code, err := RequireStringArg(args, "code")
	if err != nil {
		return "", err
	}

	version := OptionalStringArg(args, "version", "*")
	stdin := OptionalStringArg(args, "stdin", "")
	execArgs := OptionalStringSliceArg(args, "args")

	req := pistonRequest{
		Language: language,
		Version:  version,
		Files: []pistonFile{
			{Content: code},
		},
		Stdin:          stdin,
		Args:           execArgs,
		RunTimeout:     cfg.RunTimeout,
		RunMemoryLimit: cfg.RunMemoryLimit,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("code_exec: marshal request: %w", err)
	}

	url := strings.TrimRight(cfg.PistonURL, "/") + "/api/v2/execute"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("code_exec: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("code_exec: request cancelled: %w", ctx.Err())
		}
		return "", fmt.Errorf("code_exec: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxPistonResponseBytes))
	if err != nil {
		return "", fmt.Errorf("code_exec: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("code_exec: piston returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var pistonResp pistonResponse
	if err := json.Unmarshal(respBody, &pistonResp); err != nil {
		return "", fmt.Errorf("code_exec: parse response: %w", err)
	}

	result := formatPistonResult(&pistonResp)
	return TruncateOutput(result), nil
}

// formatPistonResult formats a Piston execution response into a human-readable
// string. Compile errors are shown separately from run results.
func formatPistonResult(resp *pistonResponse) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Language: %s %s\n", resp.Language, resp.Version))

	// Check for compile errors first.
	if resp.Compile != nil && resp.Compile.Code != 0 {
		sb.WriteString(fmt.Sprintf("Compile exit code: %d\n", resp.Compile.Code))
		if resp.Compile.Stderr != "" {
			sb.WriteString("Compile stderr:\n")
			sb.WriteString(resp.Compile.Stderr)
			if !strings.HasSuffix(resp.Compile.Stderr, "\n") {
				sb.WriteString("\n")
			}
		}
		sb.WriteString("Note: program did not run due to compile error")
		return sb.String()
	}

	// Run results.
	sb.WriteString(fmt.Sprintf("Exit code: %d\n", resp.Run.Code))

	if resp.Run.Signal != nil {
		sb.WriteString(fmt.Sprintf("Signal: %s\n", *resp.Run.Signal))
	}

	if resp.Run.Stdout != "" {
		sb.WriteString("Stdout:\n")
		sb.WriteString(resp.Run.Stdout)
		if !strings.HasSuffix(resp.Run.Stdout, "\n") {
			sb.WriteString("\n")
		}
	}

	if resp.Run.Stderr != "" {
		sb.WriteString("Stderr:\n")
		sb.WriteString(resp.Run.Stderr)
		if !strings.HasSuffix(resp.Run.Stderr, "\n") {
			sb.WriteString("\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
