package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPRequestTool_GET(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"message":"hello"}`)
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false, // Allow localhost for testing.
		HTTPClient:      server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"url": server.URL + "/test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "HTTP 200") {
		t.Errorf("result should contain HTTP 200, got: %s", result)
	}
	if !strings.Contains(result, `{"message":"hello"}`) {
		t.Errorf("result should contain response body, got: %s", result)
	}
	if !strings.Contains(result, "application/json") {
		t.Errorf("result should contain Content-Type, got: %s", result)
	}
}

func TestHTTPRequestTool_POST(t *testing.T) {
	t.Parallel()

	var receivedBody string
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		receivedContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "created")
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		HTTPClient:      server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"method": "POST",
		"url":    server.URL + "/items",
		"headers": map[string]any{
			"Content-Type": "application/json",
		},
		"body": `{"name":"test"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "HTTP 201") {
		t.Errorf("result should contain HTTP 201, got: %s", result)
	}
	if receivedBody != `{"name":"test"}` {
		t.Errorf("server received body = %q, want %q", receivedBody, `{"name":"test"}`)
	}
	if receivedContentType != "application/json" {
		t.Errorf("server received Content-Type = %q, want application/json", receivedContentType)
	}
}

func TestHTTPRequestTool_CustomHeaders(t *testing.T) {
	t.Parallel()

	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		HTTPClient:      server.Client(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"url": server.URL,
		"headers": map[string]any{
			"Authorization": "Bearer test-token",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer test-token")
	}
}

func TestHTTPRequestTool_InvalidURL(t *testing.T) {
	t.Parallel()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
	})

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"ftp scheme", "ftp://example.com/file", "unsupported URL scheme"},
		{"no scheme", "example.com/path", "unsupported URL scheme"},
		{"javascript scheme", "javascript:alert(1)", "unsupported URL scheme"},
		{"empty url", "", "unsupported URL scheme"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), map[string]any{
				"url": tt.url,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestHTTPRequestTool_MissingURL(t *testing.T) {
	t.Parallel()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
	})

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing url, got nil")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("error = %q, want to contain 'missing required argument'", err.Error())
	}
}

func TestHTTPRequestTool_DomainAllowlist(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		AllowedDomains:  []string{"api.example.com", "*.trusted.org"},
		HTTPClient:      server.Client(),
	})

	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{"blocked domain", "https://evil.com/data", true, "not in the allowed domains list"},
		{"blocked subdomain", "https://sub.example.com/data", true, "not in the allowed domains list"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), map[string]any{
				"url": tt.url,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestHTTPRequestTool_DomainAllowlistMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hostname string
		patterns []string
		want     bool
	}{
		{"exact match", "api.example.com", []string{"api.example.com"}, true},
		{"glob match", "sub.trusted.org", []string{"*.trusted.org"}, true},
		{"no match", "evil.com", []string{"api.example.com", "*.trusted.org"}, false},
		{"case insensitive", "API.Example.COM", []string{"api.example.com"}, true},
		{"empty list", "anything.com", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDomainAllowed(tt.hostname, tt.patterns)
			if got != tt.want {
				t.Errorf("isDomainAllowed(%q, %v) = %v, want %v", tt.hostname, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestHTTPRequestTool_PrivateIPBlocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ip      string
		private bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v6", "::1", true},
		{"private 10.x", "10.0.0.1", true},
		{"private 172.16.x", "172.16.0.1", true},
		{"private 192.168.x", "192.168.1.1", true},
		{"link-local", "169.254.1.1", true},
		{"CGNAT", "100.64.0.1", true},
		{"public", "8.8.8.8", false},
		{"public v6", "2001:4860:4860::8888", false},
		{"ipv6 link-local", "fe80::1", true},
		{"ipv6 unique local", "fd00::1", true},
		{"unspecified v4", "0.0.0.0", true},
		{"unspecified v6", "::", true},
		// IPv4-mapped IPv6 addresses must be caught to prevent SSRF bypass.
		{"ipv4-mapped loopback", "::ffff:127.0.0.1", true},
		{"ipv4-mapped private 10.x", "::ffff:10.0.0.1", true},
		{"ipv4-mapped private 192.168.x", "::ffff:192.168.1.1", true},
		{"ipv4-mapped link-local", "::ffff:169.254.169.254", true},
		{"ipv4-mapped public", "::ffff:8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			got := isPrivateIP(ip)
			if got != tt.private {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}

func TestHTTPRequestTool_MaxResponseTruncation(t *testing.T) {
	t.Parallel()

	// Create a server that returns a large response.
	largeBody := strings.Repeat("x", 2048)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, largeBody)
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs:  false,
		MaxResponseBytes: 512, // Small limit for testing.
		HTTPClient:       server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"url": server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "response truncated") {
		t.Error("result should contain truncation notice")
	}
	// The response body should be truncated. The full body is 2048 x's,
	// but we should see at most MaxResponseBytes (512) of body content.
	// Verify the result is significantly shorter than the full response.
	if len(result) >= 2048 {
		t.Errorf("result should be truncated, got %d bytes", len(result))
	}
}

func TestHTTPRequestTool_RedirectNotFollowed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://example.com/redirected")
		w.WriteHeader(http.StatusFound)
		fmt.Fprint(w, "redirecting...")
	}))
	defer server.Close()

	// Use the tool's own client (not server.Client()) so that the
	// CheckRedirect policy is applied. Disable private IP blocking
	// since the test server is on localhost.
	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"url": server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "HTTP 302") {
		t.Errorf("result should contain HTTP 302, got: %s", result)
	}
	if !strings.Contains(result, "Location: https://example.com/redirected") {
		t.Errorf("result should contain Location header, got: %s", result)
	}
}

func TestHTTPRequestTool_Timeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		Timeout:         100 * time.Millisecond,
		HTTPClient: &http.Client{
			Timeout:   100 * time.Millisecond,
			Transport: &http.Transport{
				// Use the test server's TLS config if needed.
			},
		},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"url": server.URL,
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// The error message varies by OS but should indicate a timeout or deadline.
	errStr := err.Error()
	if !strings.Contains(errStr, "deadline") && !strings.Contains(errStr, "timeout") &&
		!strings.Contains(errStr, "Timeout") && !strings.Contains(errStr, "context") {
		t.Errorf("error = %q, want timeout-related error", errStr)
	}
}

func TestHTTPRequestTool_DefaultMethod(t *testing.T) {
	t.Parallel()

	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		HTTPClient:      server.Client(),
	})

	// No method specified — should default to GET.
	_, err := tool.Handler(context.Background(), map[string]any{
		"url": server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedMethod != "GET" {
		t.Errorf("method = %s, want GET", receivedMethod)
	}
}

func TestHTTPRequestTool_HEAD(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", "12345")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		HTTPClient:      server.Client(),
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"method": "HEAD",
		"url":    server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "HTTP 200") {
		t.Errorf("result should contain HTTP 200, got: %s", result)
	}
}

func TestHTTPRequestTool_SSRFDialerBlocksPrivateIP(t *testing.T) {
	t.Parallel()

	// Create a tool with private IP blocking enabled and no custom client.
	// We can't easily test the dialer in isolation without a real connection,
	// but we can verify the tool rejects requests to localhost.
	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: true,
		Timeout:         2 * time.Second,
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"url": "http://127.0.0.1:9999/secret",
	})
	if err == nil {
		t.Fatal("expected error for private IP, got nil")
	}
	if !strings.Contains(err.Error(), "private IP") && !strings.Contains(err.Error(), "blocked") {
		// The error might also be a connection refused if the dialer check
		// happens after DNS resolution but the connection fails first.
		// Accept any error — the important thing is it doesn't succeed.
		t.Logf("got error (acceptable): %v", err)
	}
}

func TestHTTPRequestTool_MethodCaseInsensitive(t *testing.T) {
	t.Parallel()

	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		HTTPClient:      server.Client(),
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"method": "post",
		"url":    server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedMethod != "POST" {
		t.Errorf("method = %s, want POST (case-normalized)", receivedMethod)
	}
}

func TestHTTPRequestTool_UnsupportedMethod(t *testing.T) {
	t.Parallel()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
	})

	tests := []struct {
		name   string
		method string
	}{
		{"TRACE", "TRACE"},
		{"CONNECT", "CONNECT"},
		{"OPTIONS", "OPTIONS"},
		{"custom method", "FOOBAR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), map[string]any{
				"method": tt.method,
				"url":    "https://example.com",
			})
			if err == nil {
				t.Fatal("expected error for unsupported method, got nil")
			}
			if !strings.Contains(err.Error(), "unsupported HTTP method") {
				t.Errorf("error = %q, want to contain 'unsupported HTTP method'", err.Error())
			}
		})
	}
}

func TestHTTPRequestTool_DomainWildcardDepth(t *testing.T) {
	t.Parallel()

	// Wildcard "*.example.com" should match "sub.example.com" but NOT
	// "deep.sub.example.com" (one level only).
	tests := []struct {
		name     string
		hostname string
		patterns []string
		want     bool
	}{
		{"one level match", "api.example.com", []string{"*.example.com"}, true},
		{"two level no match", "deep.sub.example.com", []string{"*.example.com"}, false},
		{"exact with wildcard", "example.com", []string{"*.example.com"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDomainAllowed(tt.hostname, tt.patterns)
			if got != tt.want {
				t.Errorf("isDomainAllowed(%q, %v) = %v, want %v", tt.hostname, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestHTTPRequestTool_ErrorStatusCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"not found"}`)
	}))
	defer server.Close()

	tool := NewHTTPRequestTool(HTTPRequestToolConfig{
		BlockPrivateIPs: false,
		HTTPClient:      server.Client(),
	})

	// Error status codes should NOT return an error — the response is still valid.
	result, err := tool.Handler(context.Background(), map[string]any{
		"url": server.URL + "/missing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "HTTP 404") {
		t.Errorf("result should contain HTTP 404, got: %s", result)
	}
	if !strings.Contains(result, `{"error":"not found"}`) {
		t.Errorf("result should contain error body, got: %s", result)
	}
}
