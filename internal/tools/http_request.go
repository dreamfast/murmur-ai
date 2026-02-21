package tools

import (
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

// defaultHTTPTimeout is the default timeout for HTTP requests.
const defaultHTTPTimeout = 30 * time.Second

// defaultMaxHTTPResponseBytes is the default maximum response body size (1 MB).
const defaultMaxHTTPResponseBytes = 1024 * 1024

// allowedHTTPMethods is the set of HTTP methods the tool accepts.
var allowedHTTPMethods = map[string]bool{
	"GET":    true,
	"POST":   true,
	"PUT":    true,
	"PATCH":  true,
	"DELETE": true,
	"HEAD":   true,
}

// privateRanges is the precomputed list of private/reserved CIDR ranges
// checked by isPrivateIP. Parsed once at package init to avoid repeated
// allocations and to fail fast on invalid constants.
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",  // Link-local
		"127.0.0.0/8",     // Loopback
		"100.64.0.0/10",   // Shared address space (CGNAT)
		"192.0.0.0/24",    // IETF Protocol Assignments
		"192.0.2.0/24",    // Documentation (TEST-NET-1)
		"198.51.100.0/24", // Documentation (TEST-NET-2)
		"203.0.113.0/24",  // Documentation (TEST-NET-3)
		"fc00::/7",        // IPv6 unique local
		"fe80::/10",       // IPv6 link-local
		"::1/128",         // IPv6 loopback
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			// This is a programming error — all CIDRs above are constants.
			panic(fmt.Sprintf("invalid private CIDR %q: %v", cidr, err))
		}
		privateRanges = append(privateRanges, network)
	}
}

// HTTPRequestToolConfig holds the configuration for the http_request tool.
type HTTPRequestToolConfig struct {
	// Timeout is the request timeout duration.
	Timeout time.Duration
	// MaxResponseBytes is the maximum response body size to read.
	MaxResponseBytes int
	// AllowedDomains is an optional list of allowed domain patterns.
	// Exact matches and wildcard prefixes (e.g., "*.example.com") are
	// supported. Wildcards match exactly one subdomain level.
	// When empty, all domains are allowed.
	AllowedDomains []string
	// BlockPrivateIPs blocks requests to private/reserved IP ranges.
	BlockPrivateIPs bool
	// HTTPClient allows injection of a custom HTTP client for testing.
	// When nil, a client with SSRF-safe transport is created.
	// Note: injected clients bypass SSRF protections (dialer, redirect blocking).
	HTTPClient *http.Client
}

// NewHTTPRequestTool creates the http_request tool for making outbound HTTP
// requests. It includes SSRF protection: URL scheme validation, optional
// domain allowlist, private IP blocking at the dial level to prevent DNS
// rebinding attacks, and redirect blocking.
func NewHTTPRequestTool(cfg HTTPRequestToolConfig) Tool {
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultHTTPTimeout
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxHTTPResponseBytes
	}

	client := cfg.HTTPClient
	if client == nil {
		transport := &http.Transport{
			DialContext: ssrfSafeDialer(cfg.BlockPrivateIPs),
		}
		client = &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
			// Do not follow redirects — return the redirect response directly.
			// This prevents open redirect abuse and SSRF via redirect chains.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	return Tool{
		Name:        "http_request",
		Description: "Make an HTTP request to a URL. Supports GET, POST, PUT, PATCH, DELETE, and HEAD methods. Returns the response status, headers, and body.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"method": {
					"type": "string",
					"description": "HTTP method (GET, POST, PUT, PATCH, DELETE, HEAD). Defaults to GET.",
					"enum": ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"]
				},
				"url": {
					"type": "string",
					"description": "The URL to request (required). Must be http:// or https://."
				},
				"headers": {
					"type": "object",
					"description": "Optional HTTP headers as key-value pairs.",
					"additionalProperties": { "type": "string" }
				},
				"body": {
					"type": "string",
					"description": "Optional request body (for POST, PUT, PATCH)."
				}
			},
			"required": ["url"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleHTTPRequest(ctx, args, client, cfg)
		},
	}
}

// handleHTTPRequest executes the HTTP request with SSRF validation.
func handleHTTPRequest(ctx context.Context, args map[string]any, client *http.Client, cfg HTTPRequestToolConfig) (string, error) {
	rawURL, err := RequireStringArg(args, "url")
	if err != nil {
		return "", err
	}

	method := OptionalStringArg(args, "method", "GET")
	method = strings.ToUpper(method)

	// Validate HTTP method server-side (schema validation alone is insufficient
	// since bus payloads can bypass JSON schema constraints).
	if !allowedHTTPMethods[method] {
		return "", fmt.Errorf("handleHTTPRequest: unsupported HTTP method %q", method)
	}

	bodyStr := OptionalStringArg(args, "body", "")

	// Parse and validate the URL.
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("handleHTTPRequest: invalid URL: %w", err)
	}

	// Validate scheme.
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("handleHTTPRequest: unsupported URL scheme %q (only http and https are allowed)", parsedURL.Scheme)
	}

	// Validate hostname is present.
	hostname := parsedURL.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("handleHTTPRequest: URL has no hostname")
	}

	// Check domain allowlist.
	if len(cfg.AllowedDomains) > 0 {
		if !isDomainAllowed(hostname, cfg.AllowedDomains) {
			return "", fmt.Errorf("handleHTTPRequest: domain %q is not in the allowed domains list", hostname)
		}
	}

	// Build the request.
	var bodyReader io.Reader
	if bodyStr != "" {
		bodyReader = strings.NewReader(bodyStr)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("handleHTTPRequest: create request: %w", err)
	}

	// Set headers from args.
	if headersRaw, ok := args["headers"]; ok {
		if headersMap, ok := headersRaw.(map[string]any); ok {
			for k, v := range headersMap {
				if s, ok := v.(string); ok {
					req.Header.Set(k, s)
				}
			}
		}
	}

	// Execute the request.
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("handleHTTPRequest: %w", err)
	}
	defer resp.Body.Close()

	// Read response body up to the configured limit.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(cfg.MaxResponseBytes)+1))
	if err != nil {
		return "", fmt.Errorf("handleHTTPRequest: read response: %w", err)
	}

	truncated := false
	if len(body) > cfg.MaxResponseBytes {
		body = body[:cfg.MaxResponseBytes]
		truncated = true
	}

	// Format the output.
	return formatHTTPResponse(resp, body, truncated), nil
}

// formatHTTPResponse formats the HTTP response for the LLM.
func formatHTTPResponse(resp *http.Response, body []byte, truncated bool) string {
	var b strings.Builder

	// Extract the reason phrase safely. resp.Status is typically "200 OK"
	// but the reason phrase is optional per RFC 7230, so we fall back to
	// http.StatusText if the status string is too short.
	statusText := http.StatusText(resp.StatusCode)
	if len(resp.Status) > 4 {
		statusText = resp.Status[4:]
	}

	fmt.Fprintf(&b, "HTTP %d %s\n", resp.StatusCode, statusText)
	fmt.Fprintf(&b, "Content-Type: %s\n", resp.Header.Get("Content-Type"))

	if loc := resp.Header.Get("Location"); loc != "" {
		fmt.Fprintf(&b, "Location: %s\n", loc)
	}

	b.WriteString("\n")
	b.Write(body)

	if truncated {
		fmt.Fprintf(&b, "\n... [response truncated at %d bytes]", len(body))
	}

	return TruncateOutput(b.String())
}

// isDomainAllowed checks if a hostname matches any of the allowed domain
// patterns. Supports exact matches and wildcard prefixes ("*.example.com").
// Wildcards match exactly one subdomain level — "*.example.com" matches
// "api.example.com" but not "deep.sub.example.com".
func isDomainAllowed(hostname string, allowedDomains []string) bool {
	hostname = strings.ToLower(hostname)
	for _, pattern := range allowedDomains {
		pattern = strings.ToLower(pattern)
		// Exact match.
		if pattern == hostname {
			return true
		}
		// Wildcard match: "*.example.com" matches "sub.example.com"
		// but not "deep.sub.example.com" (one level only).
		if strings.HasPrefix(pattern, "*.") {
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(hostname, suffix) &&
				strings.Count(hostname, ".") == strings.Count(suffix, ".") {
				return true
			}
		}
	}
	return false
}

// ssrfSafeDialer returns a DialContext function that validates resolved IP
// addresses before connecting. This prevents SSRF via DNS rebinding — the IP
// is checked at dial time, not at URL parse time. When blockPrivate is false,
// the standard dialer is used without IP validation.
//
// Note: only the first resolved IP is dialed. Unlike the standard library
// which tries multiple IPs on failure, this dialer prioritizes security over
// availability to prevent DNS rebinding attacks.
func ssrfSafeDialer(blockPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !blockPrivate {
			return dialer.DialContext(ctx, network, addr)
		}

		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("ssrfSafeDialer: invalid address %q: %w", addr, err)
		}

		// Resolve the hostname to IP addresses.
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("ssrfSafeDialer: DNS lookup failed: %w", err)
		}

		if len(ips) == 0 {
			return nil, fmt.Errorf("ssrfSafeDialer: no IP addresses found for host")
		}

		// Check all resolved IPs — reject if any is private.
		for _, ip := range ips {
			if isPrivateIP(ip.IP) {
				return nil, fmt.Errorf("ssrfSafeDialer: blocked request to private/reserved IP address")
			}
		}

		// Connect to the first resolved IP to prevent DNS rebinding.
		// By dialing the resolved IP directly, a second DNS lookup cannot
		// return a different (private) IP.
		target := net.JoinHostPort(ips[0].IP.String(), port)
		return dialer.DialContext(ctx, network, target)
	}
}

// isPrivateIP returns true if the IP address is in a private, loopback,
// link-local, or otherwise reserved range that should not be accessed by
// the HTTP request tool. IPv4-mapped IPv6 addresses (e.g., ::ffff:10.0.0.1)
// are normalized to IPv4 before checking.
func isPrivateIP(ip net.IP) bool {
	// Normalize IPv4-mapped IPv6 addresses to IPv4 to prevent bypass
	// via addresses like ::ffff:10.0.0.1 or ::ffff:127.0.0.1.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Check standard library methods first (Go 1.17+).
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}

	// Check against precomputed private CIDR ranges.
	for _, network := range privateRanges {
		if network.Contains(ip) {
			return true
		}
	}

	return false
}
