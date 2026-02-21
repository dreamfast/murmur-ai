package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCodeExecTool_Name(t *testing.T) {
	t.Parallel()

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: "http://localhost:2000"})
	if tool.Name != "code_exec" {
		t.Errorf("Name = %q, want %q", tool.Name, "code_exec")
	}
	if tool.Description == "" {
		t.Error("Description should not be empty")
	}
	if tool.Handler == nil {
		t.Error("Handler should not be nil")
	}
}

func TestCodeExecTool_Parameters(t *testing.T) {
	t.Parallel()

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: "http://localhost:2000"})

	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties is not an object")
	}

	for _, key := range []string{"language", "version", "code", "stdin", "args"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing %q property", key)
		}
	}

	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema required is not an array")
	}
	requiredSet := make(map[string]bool)
	for _, r := range required {
		if s, ok := r.(string); ok {
			requiredSet[s] = true
		}
	}
	if !requiredSet["code"] {
		t.Error("schema required should include 'code'")
	}
	if !requiredSet["language"] {
		t.Error("schema required should include 'language'")
	}
}

func newPistonServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestCodeExecTool_SuccessfulExecution(t *testing.T) {
	t.Parallel()

	srv := newPistonServer(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := pistonResponse{
			Language: "python",
			Version:  "3.12.0",
			Run: pistonResult{
				Stdout: "Hello, World!\n",
				Code:   0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: srv.URL})
	result, err := tool.Handler(context.Background(), map[string]any{
		"language": "python",
		"code":     "print('Hello, World!')",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Hello, World!") {
		t.Errorf("result should contain 'Hello, World!', got %q", result)
	}
	if !strings.Contains(result, "Exit code: 0") {
		t.Errorf("result should contain 'Exit code: 0', got %q", result)
	}
	if !strings.Contains(result, "python 3.12.0") {
		t.Errorf("result should contain 'python 3.12.0', got %q", result)
	}
}

func TestCodeExecTool_CompileError(t *testing.T) {
	t.Parallel()

	srv := newPistonServer(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := pistonResponse{
			Language: "c",
			Version:  "10.2.0",
			Compile: &pistonResult{
				Stderr: "error: expected ';' before '}'\n",
				Code:   1,
			},
			Run: pistonResult{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: srv.URL})
	result, err := tool.Handler(context.Background(), map[string]any{
		"language": "c",
		"code":     "int main() { return 0 }",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "compile error") && !strings.Contains(result, "did not run") {
		t.Errorf("result should mention compile error, got %q", result)
	}
	if !strings.Contains(result, "expected ';'") {
		t.Errorf("result should contain compile stderr, got %q", result)
	}
}

func TestCodeExecTool_RuntimeError(t *testing.T) {
	t.Parallel()

	srv := newPistonServer(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := pistonResponse{
			Language: "python",
			Version:  "3.12.0",
			Run: pistonResult{
				Stderr: "Traceback (most recent call last):\n  File \"<stdin>\", line 1\nNameError: name 'x' is not defined\n",
				Code:   1,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: srv.URL})
	result, err := tool.Handler(context.Background(), map[string]any{
		"language": "python",
		"code":     "print(x)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Exit code: 1") {
		t.Errorf("result should contain 'Exit code: 1', got %q", result)
	}
	if !strings.Contains(result, "NameError") {
		t.Errorf("result should contain stderr, got %q", result)
	}
}

func TestCodeExecTool_MissingLanguage(t *testing.T) {
	t.Parallel()

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: "http://localhost:2000"})
	_, err := tool.Handler(context.Background(), map[string]any{
		"code": "print('hello')",
	})
	if err == nil {
		t.Fatal("expected error for missing language, got nil")
	}
	if !strings.Contains(err.Error(), "language") {
		t.Errorf("error = %q, want to mention 'language'", err.Error())
	}
}

func TestCodeExecTool_DefaultLanguage(t *testing.T) {
	t.Parallel()

	var receivedLang string
	srv := newPistonServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req pistonRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedLang = req.Language

		resp := pistonResponse{
			Language: req.Language,
			Version:  "3.12.0",
			Run:      pistonResult{Stdout: "ok\n", Code: 0},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	tool := NewCodeExecTool(CodeExecToolConfig{
		PistonURL:   srv.URL,
		DefaultLang: "python",
	})
	_, err := tool.Handler(context.Background(), map[string]any{
		"code": "print('ok')",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedLang != "python" {
		t.Errorf("received language = %q, want %q", receivedLang, "python")
	}
}

func TestCodeExecTool_MissingCode(t *testing.T) {
	t.Parallel()

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: "http://localhost:2000"})
	_, err := tool.Handler(context.Background(), map[string]any{
		"language": "python",
	})
	if err == nil {
		t.Fatal("expected error for missing code, got nil")
	}
	if !strings.Contains(err.Error(), "code") {
		t.Errorf("error = %q, want to mention 'code'", err.Error())
	}
}

func TestCodeExecTool_PistonUnavailable(t *testing.T) {
	t.Parallel()

	// Use a closed server to simulate unavailability.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: srv.URL})
	_, err := tool.Handler(context.Background(), map[string]any{
		"language": "python",
		"code":     "print('hello')",
	})
	if err == nil {
		t.Fatal("expected error for unavailable Piston, got nil")
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error = %q, want to mention 'request failed'", err.Error())
	}
}

func TestCodeExecTool_PistonBadJSON(t *testing.T) {
	t.Parallel()

	srv := newPistonServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not valid json"))
	})

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: srv.URL})
	_, err := tool.Handler(context.Background(), map[string]any{
		"language": "python",
		"code":     "print('hello')",
	})
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %q, want to mention 'parse response'", err.Error())
	}
}

func TestCodeExecTool_OutputTruncation(t *testing.T) {
	t.Parallel()

	// Generate output larger than MaxOutputBytes.
	largeOutput := strings.Repeat("x", MaxOutputBytes+1000)

	srv := newPistonServer(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := pistonResponse{
			Language: "python",
			Version:  "3.12.0",
			Run: pistonResult{
				Stdout: largeOutput,
				Code:   0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: srv.URL})
	result, err := tool.Handler(context.Background(), map[string]any{
		"language": "python",
		"code":     "print('x' * 30000)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "truncated") {
		t.Error("result should contain truncation notice")
	}
}

func TestCodeExecTool_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Server that blocks until context is done.
	srv := newPistonServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	tool := NewCodeExecTool(CodeExecToolConfig{PistonURL: srv.URL})
	_, err := tool.Handler(ctx, map[string]any{
		"language": "python",
		"code":     "print('hello')",
	})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestFormatPistonResult_Success(t *testing.T) {
	t.Parallel()

	resp := &pistonResponse{
		Language: "python",
		Version:  "3.12.0",
		Run: pistonResult{
			Stdout: "42\n",
			Code:   0,
		},
	}

	result := formatPistonResult(resp)
	if !strings.Contains(result, "python 3.12.0") {
		t.Errorf("result should contain language/version, got %q", result)
	}
	if !strings.Contains(result, "Exit code: 0") {
		t.Errorf("result should contain exit code, got %q", result)
	}
	if !strings.Contains(result, "42") {
		t.Errorf("result should contain stdout, got %q", result)
	}
}

func TestFormatPistonResult_CompileError(t *testing.T) {
	t.Parallel()

	resp := &pistonResponse{
		Language: "c",
		Version:  "10.2.0",
		Compile: &pistonResult{
			Stderr: "error: missing semicolon\n",
			Code:   1,
		},
		Run: pistonResult{},
	}

	result := formatPistonResult(resp)
	if !strings.Contains(result, "Compile exit code: 1") {
		t.Errorf("result should contain compile exit code, got %q", result)
	}
	if !strings.Contains(result, "missing semicolon") {
		t.Errorf("result should contain compile stderr, got %q", result)
	}
	if !strings.Contains(result, "did not run") {
		t.Errorf("result should mention program did not run, got %q", result)
	}
}
