package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"
	"time"
)

// dnsTimeout is the maximum time allowed for DNS and TLS operations.
const dnsTimeout = 10 * time.Second

// domainRe validates domain names to prevent injection in shell commands
// and ensure well-formed DNS queries.
var domainRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// NewDNSCheckTool creates the dns_check tool for DNS lookups, SSL certificate
// inspection, and whois expiry checks.
func NewDNSCheckTool() Tool {
	return Tool{
		Name:        "dns_check",
		Description: "DNS and SSL inspection tool. Actions: lookup (DNS records: A/AAAA, MX, TXT), ssl (certificate details, expiry, SANs), whois_expiry (domain registration expiry date).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["lookup", "ssl", "whois_expiry"],
					"description": "The check to perform"
				},
				"domain": {
					"type": "string",
					"description": "The domain name to check (e.g., 'example.com')"
				}
			},
			"required": ["action", "domain"]
		}`),
		Handler: handleDNSCheck,
	}
}

// handleDNSCheck dispatches to the appropriate DNS/SSL check action.
func handleDNSCheck(ctx context.Context, args map[string]any) (string, error) {
	action, err := RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	domain, err := RequireStringArg(args, "domain")
	if err != nil {
		return "", err
	}

	// Validate domain.
	if !domainRe.MatchString(domain) {
		return "", fmt.Errorf("invalid domain name %q: must contain only alphanumeric characters, hyphens, and dots", domain)
	}

	switch action {
	case "lookup":
		return dnsLookup(ctx, domain)
	case "ssl":
		return sslCheck(ctx, domain)
	case "whois_expiry":
		return whoisExpiry(ctx, domain)
	default:
		return "", fmt.Errorf("unknown dns_check action %q", action)
	}
}

// dnsLookup performs DNS lookups for A/AAAA, MX, and TXT records.
func dnsLookup(ctx context.Context, domain string) (string, error) {
	resolver := &net.Resolver{}
	lookupCtx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("DNS records for %s:\n\n", domain))

	// A/AAAA records.
	addrs, err := resolver.LookupHost(lookupCtx, domain)
	if err != nil {
		sb.WriteString(fmt.Sprintf("A/AAAA: error — %v\n", err))
	} else if len(addrs) == 0 {
		sb.WriteString("A/AAAA: no records\n")
	} else {
		sb.WriteString("A/AAAA:\n")
		for _, addr := range addrs {
			sb.WriteString(fmt.Sprintf("  %s\n", addr))
		}
	}

	// MX records.
	mxRecords, err := resolver.LookupMX(lookupCtx, domain)
	if err != nil {
		sb.WriteString(fmt.Sprintf("\nMX: error — %v\n", err))
	} else if len(mxRecords) == 0 {
		sb.WriteString("\nMX: no records\n")
	} else {
		sb.WriteString("\nMX:\n")
		for _, mx := range mxRecords {
			sb.WriteString(fmt.Sprintf("  %s (priority %d)\n", mx.Host, mx.Pref))
		}
	}

	// TXT records.
	txtRecords, err := resolver.LookupTXT(lookupCtx, domain)
	if err != nil {
		sb.WriteString(fmt.Sprintf("\nTXT: error — %v\n", err))
	} else if len(txtRecords) == 0 {
		sb.WriteString("\nTXT: no records\n")
	} else {
		sb.WriteString("\nTXT:\n")
		for _, txt := range txtRecords {
			sb.WriteString(fmt.Sprintf("  %s\n", txt))
		}
	}

	return TruncateOutput(sb.String()), nil
}

// sslCheck connects to the domain on port 443 and inspects the TLS certificate.
func sslCheck(ctx context.Context, domain string) (string, error) {
	dialer := &tls.Dialer{
		Config: &tls.Config{
			ServerName: domain,
			MinVersion: tls.VersionTLS12,
		},
	}

	dialCtx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	conn, err := dialer.DialContext(dialCtx, "tcp", domain+":443")
	if err != nil {
		return "", fmt.Errorf("ssl: failed to connect to %s:443: %w", domain, err)
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", fmt.Errorf("ssl: connection is not TLS")
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("ssl: no certificates presented by %s", domain)
	}

	cert := state.PeerCertificates[0]
	now := time.Now()
	daysUntilExpiry := math.Floor(time.Until(cert.NotAfter).Hours() / 24)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("SSL certificate for %s:\n\n", domain))
	sb.WriteString(fmt.Sprintf("Subject:    %s\n", cert.Subject.CommonName))
	sb.WriteString(fmt.Sprintf("Issuer:     %s\n", cert.Issuer.CommonName))
	sb.WriteString(fmt.Sprintf("Not Before: %s\n", cert.NotBefore.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Not After:  %s\n", cert.NotAfter.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Days Until Expiry: %.0f\n", daysUntilExpiry))

	if cert.NotAfter.Before(now) {
		sb.WriteString("Status: EXPIRED\n")
	} else if daysUntilExpiry < 30 {
		sb.WriteString("Status: EXPIRING SOON\n")
	} else {
		sb.WriteString("Status: Valid\n")
	}

	sb.WriteString(fmt.Sprintf("TLS Version: %s\n", tlsVersionString(state.Version)))

	if len(cert.DNSNames) > 0 {
		sb.WriteString(fmt.Sprintf("\nSANs (%d):\n", len(cert.DNSNames)))
		for _, san := range cert.DNSNames {
			sb.WriteString(fmt.Sprintf("  %s\n", san))
		}
	}

	return TruncateOutput(sb.String()), nil
}

// tlsVersionString returns a human-readable TLS version string.
func tlsVersionString(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("unknown (0x%04x)", version)
	}
}

// whoisExpiry shells out to the whois command and parses the expiry date.
// This is best-effort — whois output format varies by registrar.
func whoisExpiry(ctx context.Context, domain string) (string, error) {
	whoisCtx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	output, err := RunCommand(whoisCtx, "whois", domain)
	if err != nil {
		return "", fmt.Errorf("whois_expiry: %w", err)
	}

	expiry := parseWhoisExpiry(output)
	if expiry == "" {
		return fmt.Sprintf("Whois for %s: could not parse expiry date from whois output.\n\nRaw output (first 2000 chars):\n%s",
			domain, truncateString(output, 2000)), nil
	}

	return fmt.Sprintf("Domain: %s\nExpiry: %s", domain, expiry), nil
}

// parseWhoisExpiry attempts to extract the expiry date from whois output.
// It tries several common field names used by different registrars.
var whoisExpiryPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:Registry Expiry Date|Expiration Date|Expiry Date|paid-till|Registrar Registration Expiration Date):\s*(.+)`),
	regexp.MustCompile(`(?i)(?:expire|expires):\s*(.+)`),
}

// parseWhoisExpiry extracts the expiry date from whois output using common patterns.
func parseWhoisExpiry(output string) string {
	for _, re := range whoisExpiryPatterns {
		if matches := re.FindStringSubmatch(output); len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}
	return ""
}

// truncateString truncates a string to maxLen characters.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
