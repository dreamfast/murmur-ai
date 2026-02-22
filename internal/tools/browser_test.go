package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTruncateContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"over limit", "hello world", 5, "hello... [truncated]"},
		{"empty string", "", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateContent(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateContent(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestBrowserTool_Navigate(t *testing.T) {
	t.Parallel()

	var receivedPath string
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"title":"Example","url":"https://example.com","content":"Hello World"}`)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:         server.URL,
		MaxContentLength: 8000,
		HTTPClient:       server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "navigate",
		"url":    "https://example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedPath != "/navigate" {
		t.Errorf("path = %q, want /navigate", receivedPath)
	}
	if receivedBody["url"] != "https://example.com" {
		t.Errorf("url = %v, want https://example.com", receivedBody["url"])
	}
	if !strings.Contains(result, "Example") {
		t.Errorf("result should contain title, got: %s", result)
	}
	if !strings.Contains(result, "Hello World") {
		t.Errorf("result should contain content, got: %s", result)
	}
}

func TestBrowserTool_URLValidation(t *testing.T) {
	t.Parallel()

	// The server should never be called for blocked URLs.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be called for blocked URLs")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"file scheme", "file:///etc/passwd", "blocked URL scheme"},
		{"javascript scheme", "javascript:alert(1)", "blocked URL scheme"},
		{"data scheme", "data:text/html,<h1>hi</h1>", "blocked URL scheme"},
		{"ftp scheme", "ftp://example.com/file", "unsupported URL scheme"},
		{"no scheme", "example.com", "unsupported URL scheme"},
		{"empty url", "", "unsupported URL scheme"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"action": "navigate",
				"url":    tt.url,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestBrowserTool_ContentTruncation(t *testing.T) {
	t.Parallel()

	longContent := strings.Repeat("x", 10000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]string{
			"title":   "Long Page",
			"url":     "https://example.com/long",
			"content": longContent,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:         server.URL,
		MaxContentLength: 100,
		HTTPClient:       server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "navigate",
		"url":    "https://example.com/long",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "[truncated]") {
		t.Errorf("result should contain truncation notice, got: %s", result)
	}
	// The content portion should be at most MaxContentLength + truncation notice.
	// The full result includes "Navigated to:" header, so just check it's reasonable.
	if len(result) > 500 {
		t.Errorf("result too long (%d chars), expected truncation", len(result))
	}
}

func TestBrowserTool_Timeout(t *testing.T) {
	t.Parallel()

	// Server that delays long enough to trigger the client timeout.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		Timeout:    50 * time.Millisecond,
		HTTPClient: &http.Client{Timeout: 50 * time.Millisecond},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "navigate",
		"url":    "https://example.com",
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestBrowserTool_PrivateIPBlocked(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be called for private IPs")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	tests := []struct {
		name string
		url  string
	}{
		{"loopback", "http://127.0.0.1/secret"},
		{"private 10.x", "http://10.0.0.1/internal"},
		{"private 192.168.x", "http://192.168.1.1/admin"},
		{"private 172.16.x", "http://172.16.0.1/api"},
		{"ipv6 loopback", "http://[::1]/secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"action": "navigate",
				"url":    tt.url,
			})
			if err == nil {
				t.Fatal("expected error for private IP, got nil")
			}
			if !strings.Contains(err.Error(), "private/reserved IP") {
				t.Errorf("error = %q, want to contain 'private/reserved IP'", err.Error())
			}
		})
	}
}

func TestBrowserTool_Screenshot(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/screenshot" {
			t.Errorf("path = %q, want /screenshot", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"title":"Test Page","width":1280,"height":720,"size_bytes":45678}`)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "screenshot",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "1280x720") {
		t.Errorf("result should contain dimensions, got: %s", result)
	}
	if !strings.Contains(result, "Test Page") {
		t.Errorf("result should contain title, got: %s", result)
	}
	if !strings.Contains(result, "45678 bytes") {
		t.Errorf("result should contain size, got: %s", result)
	}
}

func TestBrowserTool_Click(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/click" {
			t.Errorf("path = %q, want /click", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"url":"https://example.com/clicked"}`)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":   "click",
		"selector": "#submit-btn",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Clicked successfully") {
		t.Errorf("result should contain success message, got: %s", result)
	}
}

func TestBrowserTool_ClickRequiresSelectorOrText(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be called without selector or text")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "click",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "selector") {
		t.Errorf("error = %q, want to contain 'selector'", err.Error())
	}
}

func TestBrowserTool_UnsupportedAction(t *testing.T) {
	t.Parallel()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint: "http://localhost:3001",
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "destroy",
	})
	if err == nil {
		t.Fatal("expected error for unsupported action, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported action") {
		t.Errorf("error = %q, want to contain 'unsupported action'", err.Error())
	}
}

func TestBrowserTool_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"browser crashed"}`)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "navigate",
		"url":    "https://example.com",
	})
	if err == nil {
		t.Fatal("expected error for server error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want to contain 'HTTP 500'", err.Error())
	}
}

func TestBrowserTool_InvalidDirection(t *testing.T) {
	t.Parallel()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint: "http://localhost:3001",
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":    "scroll",
		"direction": "left",
	})
	if err == nil {
		t.Fatal("expected error for invalid direction, got nil")
	}
	if !strings.Contains(err.Error(), "must be 'up' or 'down'") {
		t.Errorf("error = %q, want to contain direction validation message", err.Error())
	}
}

func TestBrowserTool_InvalidFullPage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be called with invalid full_page type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action":    "screenshot",
		"full_page": "yes",
	})
	if err == nil {
		t.Fatal("expected error for non-boolean full_page, got nil")
	}
	if !strings.Contains(err.Error(), "must be a boolean") {
		t.Errorf("error = %q, want to contain type validation message", err.Error())
	}
}

func TestBrowserTool_InvalidAmount(t *testing.T) {
	t.Parallel()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint: "http://localhost:3001",
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "scroll",
		"amount": "lots",
	})
	if err == nil {
		t.Fatal("expected error for non-numeric amount, got nil")
	}
	if !strings.Contains(err.Error(), "must be a number") {
		t.Errorf("error = %q, want to contain type validation message", err.Error())
	}
}

func TestBrowserTool_ClientError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "navigate",
		"url":    "https://example.com",
	})
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("error = %q, want to contain 'HTTP 400'", err.Error())
	}
}

func TestBrowserTool_ScrollAmount(t *testing.T) {
	t.Parallel()

	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scroll" {
			t.Errorf("path = %q, want /scroll", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"scroll_y":1000,"scroll_height":5000}`)
	}))
	defer server.Close()

	tool := NewBrowserTool(BrowserToolConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action":    "scroll",
		"direction": "down",
		"amount":    float64(1000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Scrolled down") {
		t.Errorf("result should contain direction, got: %s", result)
	}
	// Verify the amount was passed through to the server.
	if amt, ok := receivedBody["amount"]; !ok {
		t.Error("amount not sent to server")
	} else if amt != float64(1000) {
		t.Errorf("amount = %v, want 1000", amt)
	}
}

func TestValidateBrowserURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{"valid https", "https://example.com", false, ""},
		{"valid http", "http://example.com/page", false, ""},
		{"file scheme", "file:///etc/passwd", true, "blocked URL scheme"},
		{"javascript scheme", "javascript:void(0)", true, "blocked URL scheme"},
		{"data scheme", "data:text/html,test", true, "blocked URL scheme"},
		{"ftp scheme", "ftp://example.com", true, "unsupported URL scheme"},
		{"private 127.0.0.1", "http://127.0.0.1", true, "private/reserved IP"},
		{"private 10.x", "http://10.0.0.1", true, "private/reserved IP"},
		{"private 192.168.x", "http://192.168.0.1", true, "private/reserved IP"},
		{"ipv6 loopback", "http://[::1]", true, "private/reserved IP"},
		{"public IP", "http://8.8.8.8", false, ""},
		{"domain name", "https://google.com", false, ""},
		{"localhost hostname", "http://localhost/secret", true, "private/reserved IP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateBrowserURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
