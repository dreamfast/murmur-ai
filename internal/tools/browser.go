package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BrowserToolConfig holds the configuration for the browser automation tool.
type BrowserToolConfig struct {
	// Endpoint is the base URL of the browser-server HTTP API.
	Endpoint string
	// Timeout is the maximum duration for browser operations.
	Timeout time.Duration
	// MaxContentLength is the maximum characters to return from page content.
	MaxContentLength int
	// HTTPClient allows injection of a custom HTTP client for testing.
	HTTPClient *http.Client
}

// browserAction enumerates the supported browser actions.
var browserActions = map[string]bool{
	"navigate":   true,
	"screenshot": true,
	"click":      true,
	"type":       true,
	"evaluate":   true,
	"content":    true,
	"scroll":     true,
}

// blockedBrowserSchemes are URL schemes that the browser tool refuses to navigate to.
var blockedBrowserSchemes = []string{"file", "javascript", "data"}

// NewBrowserTool creates the browser automation tool that communicates with a
// Dockerized Playwright instance via HTTP. It supports navigation, screenshots,
// clicking, typing, JavaScript evaluation, content extraction, and scrolling.
func NewBrowserTool(cfg BrowserToolConfig) Tool {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxContentLength <= 0 {
		cfg.MaxContentLength = 8000
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}

	return Tool{
		Name:        "browser",
		Description: "Automate a headless browser: navigate to URLs, take screenshots, click elements, type text, run JavaScript, extract page content, and scroll. Backed by a sandboxed Playwright instance.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"description": "The browser action to perform.",
					"enum": ["navigate", "screenshot", "click", "type", "evaluate", "content", "scroll"]
				},
				"url": {
					"type": "string",
					"description": "URL to navigate to (required for 'navigate' action). Must be http:// or https://."
				},
				"selector": {
					"type": "string",
					"description": "CSS selector for 'click' or 'type' actions."
				},
				"text": {
					"type": "string",
					"description": "Text to type (for 'type' action) or text to click (for 'click' action, alternative to selector)."
				},
				"script": {
					"type": "string",
					"description": "JavaScript code to evaluate (for 'evaluate' action)."
				},
				"direction": {
					"type": "string",
					"description": "Scroll direction: 'up' or 'down' (for 'scroll' action). Defaults to 'down'.",
					"enum": ["up", "down"]
				},
				"amount": {
					"type": "integer",
					"description": "Scroll amount in pixels (for 'scroll' action). Defaults to 500."
				},
				"full_page": {
					"type": "boolean",
					"description": "Take a full-page screenshot instead of just the viewport (for 'screenshot' action). Defaults to false."
				},
				"session_id": {
					"type": "string",
					"description": "Optional session ID for maintaining browser state across calls. Defaults to 'default'."
				}
			},
			"required": ["action"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleBrowser(ctx, args, client, cfg)
		},
	}
}

// truncateContent truncates s to maxLen characters, appending a truncation
// notice if the string was shortened.
func truncateContent(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... [truncated]"
}

// handleBrowser dispatches browser actions to the browser-server.
func handleBrowser(ctx context.Context, args map[string]any, client *http.Client, cfg BrowserToolConfig) (string, error) {
	action, err := RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	if !browserActions[action] {
		return "", fmt.Errorf("handleBrowser: unsupported action %q", action)
	}

	sessionID := OptionalStringArg(args, "session_id", "default")

	switch action {
	case "navigate":
		return handleBrowserNavigate(ctx, args, client, cfg, sessionID)
	case "screenshot":
		return handleBrowserScreenshot(ctx, args, client, cfg, sessionID)
	case "click":
		return handleBrowserClick(ctx, args, client, cfg, sessionID)
	case "type":
		return handleBrowserType(ctx, args, client, cfg, sessionID)
	case "evaluate":
		return handleBrowserEvaluate(ctx, args, client, cfg, sessionID)
	case "content":
		return handleBrowserContent(ctx, client, cfg, sessionID)
	case "scroll":
		return handleBrowserScroll(ctx, args, client, cfg, sessionID)
	default:
		return "", fmt.Errorf("handleBrowser: unsupported action %q", action)
	}
}

// handleBrowserNavigate navigates to a URL and returns the page title and content.
func handleBrowserNavigate(ctx context.Context, args map[string]any, client *http.Client, cfg BrowserToolConfig, sessionID string) (string, error) {
	rawURL, err := RequireStringArg(args, "url")
	if err != nil {
		return "", fmt.Errorf("handleBrowser: navigate requires 'url': %w", err)
	}

	if err := validateBrowserURL(rawURL); err != nil {
		return "", fmt.Errorf("handleBrowser: %w", err)
	}

	payload := map[string]any{
		"session_id": sessionID,
		"url":        rawURL,
	}

	var result struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		Error   string `json:"error"`
	}

	if err := browserRequest(ctx, client, cfg.Endpoint, "/navigate", payload, &result); err != nil {
		return "", fmt.Errorf("handleBrowser: navigate: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("handleBrowser: navigate: %s", result.Error)
	}

	return fmt.Sprintf("Navigated to: %s\nTitle: %s\n\n%s", result.URL, result.Title, truncateContent(result.Content, cfg.MaxContentLength)), nil
}

// handleBrowserScreenshot takes a screenshot and returns metadata.
func handleBrowserScreenshot(ctx context.Context, args map[string]any, client *http.Client, cfg BrowserToolConfig, sessionID string) (string, error) {
	payload := map[string]any{
		"session_id": sessionID,
	}

	// Check for full_page option (must be boolean).
	if fp, ok := args["full_page"]; ok {
		if b, isBool := fp.(bool); isBool {
			payload["full_page"] = b
		} else {
			return "", fmt.Errorf("handleBrowser: screenshot: 'full_page' must be a boolean")
		}
	}

	var result struct {
		Title     string `json:"title"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		SizeBytes int    `json:"size_bytes"`
		Error     string `json:"error"`
	}

	if err := browserRequest(ctx, client, cfg.Endpoint, "/screenshot", payload, &result); err != nil {
		return "", fmt.Errorf("handleBrowser: screenshot: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("handleBrowser: screenshot: %s", result.Error)
	}

	return fmt.Sprintf("[Screenshot taken: %dx%d, %d bytes, page: %s]", result.Width, result.Height, result.SizeBytes, result.Title), nil
}

// handleBrowserClick clicks an element by selector or text.
func handleBrowserClick(ctx context.Context, args map[string]any, client *http.Client, cfg BrowserToolConfig, sessionID string) (string, error) {
	selector := OptionalStringArg(args, "selector", "")
	text := OptionalStringArg(args, "text", "")

	if selector == "" && text == "" {
		return "", fmt.Errorf("handleBrowser: click requires 'selector' or 'text'")
	}

	payload := map[string]any{
		"session_id": sessionID,
	}
	if selector != "" {
		payload["selector"] = selector
	}
	if text != "" {
		payload["text"] = text
	}

	var result struct {
		Success bool   `json:"success"`
		URL     string `json:"url"`
		Error   string `json:"error"`
	}

	if err := browserRequest(ctx, client, cfg.Endpoint, "/click", payload, &result); err != nil {
		return "", fmt.Errorf("handleBrowser: click: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("handleBrowser: click: %s", result.Error)
	}

	return fmt.Sprintf("Clicked successfully. Current URL: %s", result.URL), nil
}

// handleBrowserType types text into an element.
func handleBrowserType(ctx context.Context, args map[string]any, client *http.Client, cfg BrowserToolConfig, sessionID string) (string, error) {
	selector, err := RequireStringArg(args, "selector")
	if err != nil {
		return "", fmt.Errorf("handleBrowser: type requires 'selector': %w", err)
	}
	text, err := RequireStringArg(args, "text")
	if err != nil {
		return "", fmt.Errorf("handleBrowser: type requires 'text': %w", err)
	}

	payload := map[string]any{
		"session_id": sessionID,
		"selector":   selector,
		"text":       text,
	}

	var result struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}

	if err := browserRequest(ctx, client, cfg.Endpoint, "/type", payload, &result); err != nil {
		return "", fmt.Errorf("handleBrowser: type: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("handleBrowser: type: %s", result.Error)
	}

	return "Typed text successfully.", nil
}

// handleBrowserEvaluate runs JavaScript on the page.
func handleBrowserEvaluate(ctx context.Context, args map[string]any, client *http.Client, cfg BrowserToolConfig, sessionID string) (string, error) {
	script, err := RequireStringArg(args, "script")
	if err != nil {
		return "", fmt.Errorf("handleBrowser: evaluate requires 'script': %w", err)
	}

	payload := map[string]any{
		"session_id": sessionID,
		"script":     script,
	}

	var result struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}

	if err := browserRequest(ctx, client, cfg.Endpoint, "/evaluate", payload, &result); err != nil {
		return "", fmt.Errorf("handleBrowser: evaluate: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("handleBrowser: evaluate: %s", result.Error)
	}

	return fmt.Sprintf("Result: %s", truncateContent(result.Result, cfg.MaxContentLength)), nil
}

// handleBrowserContent extracts the page text content.
func handleBrowserContent(ctx context.Context, client *http.Client, cfg BrowserToolConfig, sessionID string) (string, error) {
	payload := map[string]any{
		"session_id": sessionID,
	}

	var result struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		Error   string `json:"error"`
	}

	if err := browserRequest(ctx, client, cfg.Endpoint, "/content", payload, &result); err != nil {
		return "", fmt.Errorf("handleBrowser: content: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("handleBrowser: content: %s", result.Error)
	}

	return fmt.Sprintf("Page: %s (%s)\n\n%s", result.Title, result.URL, truncateContent(result.Content, cfg.MaxContentLength)), nil
}

// handleBrowserScroll scrolls the page up or down.
func handleBrowserScroll(ctx context.Context, args map[string]any, client *http.Client, cfg BrowserToolConfig, sessionID string) (string, error) {
	direction := OptionalStringArg(args, "direction", "down")
	if direction != "up" && direction != "down" {
		return "", fmt.Errorf("handleBrowser: scroll: 'direction' must be 'up' or 'down', got %q", direction)
	}

	payload := map[string]any{
		"session_id": sessionID,
		"direction":  direction,
	}
	if amount, ok := args["amount"]; ok {
		// JSON numbers decode as float64; accept both float64 and int.
		switch v := amount.(type) {
		case float64:
			payload["amount"] = int(v)
		case int:
			payload["amount"] = v
		default:
			return "", fmt.Errorf("handleBrowser: scroll: 'amount' must be a number")
		}
	}

	var result struct {
		ScrollY      int    `json:"scroll_y"`
		ScrollHeight int    `json:"scroll_height"`
		Error        string `json:"error"`
	}

	if err := browserRequest(ctx, client, cfg.Endpoint, "/scroll", payload, &result); err != nil {
		return "", fmt.Errorf("handleBrowser: scroll: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("handleBrowser: scroll: %s", result.Error)
	}

	return fmt.Sprintf("Scrolled %s. Position: %d / %d", direction, result.ScrollY, result.ScrollHeight), nil
}

// browserRequest sends a JSON POST request to the browser-server and decodes
// the response into result.
func browserRequest(ctx context.Context, client *http.Client, endpoint, path string, payload map[string]any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("browserRequest: marshal: %w", err)
	}

	reqURL := strings.TrimRight(endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("browserRequest: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("browserRequest: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return fmt.Errorf("browserRequest: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("browserRequest: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("browserRequest: decode response: %w", err)
	}

	return nil
}

// validateBrowserURL validates a URL for browser navigation. It blocks
// dangerous schemes (file://, javascript://, data://) and private IP addresses
// to prevent SSRF attacks. When the hostname is not a literal IP, it resolves
// DNS to check that all resulting addresses are public (blocks Docker-internal
// service names like "murmur-server" or external hostnames that resolve to
// private IPs).
func validateBrowserURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Check for blocked schemes.
	scheme := strings.ToLower(parsed.Scheme)
	for _, blocked := range blockedBrowserSchemes {
		if scheme == blocked {
			return fmt.Errorf("blocked URL scheme %q", scheme)
		}
	}

	// Only allow http and https.
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q (only http and https are allowed)", scheme)
	}

	// Validate hostname is present.
	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("URL has no hostname")
	}

	// Block private IP addresses (SSRF protection).
	if ip := net.ParseIP(hostname); ip != nil {
		// Literal IP address — check directly.
		if isPrivateIP(ip) {
			return fmt.Errorf("blocked request to private/reserved IP address")
		}
		return nil
	}

	// Hostname is not a literal IP — resolve DNS and check all addresses.
	// This blocks Docker-internal service names (e.g., "murmur-server",
	// "piston", "ircd") and external hostnames that resolve to private IPs.
	addrs, err := net.LookupIP(hostname)
	if err != nil {
		return fmt.Errorf("dns resolution failed for %q: %w", hostname, err)
	}
	for _, addr := range addrs {
		if isPrivateIP(addr) {
			return fmt.Errorf("blocked request to private/reserved IP address (resolved from %q)", hostname)
		}
	}

	return nil
}
