package tools

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestDNSCheck_Lookup(t *testing.T) {
	t.Parallel()

	tool := NewDNSCheckTool()
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "lookup",
		"domain": "localhost",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "DNS records for localhost") {
		t.Errorf("expected header, got: %s", result)
	}
	// localhost should resolve to 127.0.0.1 or ::1.
	if !strings.Contains(result, "127.0.0.1") && !strings.Contains(result, "::1") {
		t.Errorf("expected localhost to resolve, got: %s", result)
	}
}

func TestDNSCheck_SSL_InvalidDomain(t *testing.T) {
	t.Parallel()

	// Test the handler rejects domain+port format (colon not allowed in domain regex).
	tool := NewDNSCheckTool()
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "ssl",
		"domain": "127.0.0.1:12345",
	})
	if err == nil {
		t.Fatal("expected error for domain with port")
	}
	if !strings.Contains(err.Error(), "invalid domain") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDNSCheck_SSL_SelfSigned(t *testing.T) {
	t.Parallel()

	// Create a self-signed TLS server and test sslCheck directly.
	cert, key := generateTestCert(t, "ssl-test.example.com", time.Now().Add(90*24*time.Hour))
	tlsCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			tlsConn, ok := conn.(*tls.Conn)
			if ok {
				_ = tlsConn.Handshake()
			}
			conn.Close()
		}
	}()

	// sslCheck hardcodes port 443, so we can't use it directly with the
	// test listener. Instead, verify the TLS connection and cert parsing
	// logic by connecting directly.
	addr := listener.Addr().String()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(cert)

	conn, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    pool,
		ServerName: "ssl-test.example.com",
	})
	if err != nil {
		t.Fatalf("TLS dial failed: %v", err)
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no peer certificates")
	}

	peerCert := state.PeerCertificates[0]
	if peerCert.Subject.CommonName != "ssl-test.example.com" {
		t.Errorf("expected CN 'ssl-test.example.com', got %q", peerCert.Subject.CommonName)
	}
	if len(peerCert.DNSNames) == 0 || peerCert.DNSNames[0] != "ssl-test.example.com" {
		t.Errorf("expected SAN 'ssl-test.example.com', got %v", peerCert.DNSNames)
	}
}

func TestDNSCheck_SSLExpiry(t *testing.T) {
	t.Parallel()

	// Test the days-until-expiry calculation by creating a cert that
	// expires in exactly 30 days.
	now := time.Now()
	expiry := now.Add(30 * 24 * time.Hour)

	cert, key := generateTestCert(t, "expiry-test.example.com", expiry)
	tlsCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Complete the TLS handshake before closing.
			tlsConn, ok := conn.(*tls.Conn)
			if ok {
				_ = tlsConn.Handshake()
			}
			conn.Close()
		}
	}()

	// Connect directly to verify cert expiry parsing.
	addr := listener.Addr().String()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(cert)

	conn, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    pool,
		ServerName: "expiry-test.example.com",
	})
	if err != nil {
		t.Fatalf("TLS dial failed: %v", err)
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no peer certificates")
	}

	peerCert := state.PeerCertificates[0]
	daysUntil := time.Until(peerCert.NotAfter).Hours() / 24
	if daysUntil < 29 || daysUntil > 31 {
		t.Errorf("expected ~30 days until expiry, got %.1f", daysUntil)
	}
}

func TestDNSCheck_InvalidDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		domain string
	}{
		{name: "special chars", domain: "example.com; rm -rf /"},
		{name: "spaces", domain: "example .com"},
		{name: "backtick", domain: "example`com"},
		{name: "pipe", domain: "example|com"},
		{name: "empty", domain: ""},
		{name: "starts with hyphen", domain: "-example.com"},
		{name: "ends with hyphen", domain: "example-.com"},
	}

	tool := NewDNSCheckTool()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tool.Handler(context.Background(), map[string]any{
				"action": "lookup",
				"domain": tt.domain,
			})
			if err == nil {
				t.Errorf("expected error for domain %q", tt.domain)
			}
			if err != nil && !strings.Contains(err.Error(), "invalid domain") {
				t.Errorf("expected 'invalid domain' error, got: %v", err)
			}
		})
	}
}

func TestDNSCheck_ValidDomains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		domain string
	}{
		{name: "simple", domain: "example.com"},
		{name: "subdomain", domain: "www.example.com"},
		{name: "hyphenated", domain: "my-site.example.com"},
		{name: "single label", domain: "localhost"},
		{name: "numeric", domain: "123.456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !domainRe.MatchString(tt.domain) {
				t.Errorf("expected domain %q to be valid", tt.domain)
			}
		})
	}
}

func TestDNSCheck_InvalidAction(t *testing.T) {
	t.Parallel()

	tool := NewDNSCheckTool()
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "nslookup",
		"domain": "example.com",
	})
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
	if !strings.Contains(err.Error(), "unknown dns_check action") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDNSCheck_MissingAction(t *testing.T) {
	t.Parallel()

	tool := NewDNSCheckTool()
	_, err := tool.Handler(context.Background(), map[string]any{
		"domain": "example.com",
	})
	if err == nil {
		t.Fatal("expected error for missing action")
	}
}

func TestDNSCheck_MissingDomain(t *testing.T) {
	t.Parallel()

	tool := NewDNSCheckTool()
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "lookup",
	})
	if err == nil {
		t.Fatal("expected error for missing domain")
	}
}

func TestParseWhoisExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "registry expiry date",
			input: "Registry Expiry Date: 2025-08-13T04:00:00Z",
			want:  "2025-08-13T04:00:00Z",
		},
		{
			name:  "expiration date",
			input: "Expiration Date: 2025-08-13",
			want:  "2025-08-13",
		},
		{
			name:  "registrar expiration",
			input: "Registrar Registration Expiration Date: 2025-08-13T04:00:00Z",
			want:  "2025-08-13T04:00:00Z",
		},
		{
			name:  "expires field",
			input: "expires: 2025-08-13",
			want:  "2025-08-13",
		},
		{
			name:  "paid-till",
			input: "paid-till: 2025-08-13",
			want:  "2025-08-13",
		},
		{
			name:  "no match",
			input: "Domain Name: example.com\nCreated: 2020-01-01",
			want:  "",
		},
		{
			name: "multiline with expiry",
			input: `Domain Name: example.com
Registrar: Example Registrar
Registry Expiry Date: 2025-12-31T23:59:59Z
Updated Date: 2024-01-01`,
			want: "2025-12-31T23:59:59Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseWhoisExpiry(tt.input)
			if got != tt.want {
				t.Errorf("parseWhoisExpiry() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTLSVersionString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version uint16
		want    string
	}{
		{tls.VersionTLS10, "TLS 1.0"},
		{tls.VersionTLS11, "TLS 1.1"},
		{tls.VersionTLS12, "TLS 1.2"},
		{tls.VersionTLS13, "TLS 1.3"},
		{0x0000, "unknown (0x0000)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := tlsVersionString(tt.version)
			if got != tt.want {
				t.Errorf("tlsVersionString(0x%04x) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

// generateTestCert creates a self-signed certificate for testing.
func generateTestCert(t *testing.T, commonName string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{commonName},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return
}
