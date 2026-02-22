package crypto

import (
	"encoding/hex"
	"testing"
)

func TestRandomHex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		n       int
		wantLen int // hex string length = 2*n
	}{
		{"zero bytes", 0, 0},
		{"one byte", 1, 2},
		{"four bytes", 4, 8},
		{"sixteen bytes", 16, 32},
		{"thirty-two bytes", 32, 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RandomHex(tt.n)
			if err != nil {
				t.Fatalf("RandomHex(%d) error: %v", tt.n, err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("RandomHex(%d) length = %d, want %d", tt.n, len(got), tt.wantLen)
			}
			// Verify it's valid hex.
			if _, err := hex.DecodeString(got); err != nil {
				t.Errorf("RandomHex(%d) produced invalid hex: %q", tt.n, got)
			}
		})
	}
}

func TestRandomHex_Unique(t *testing.T) {
	t.Parallel()

	a, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex(16) error: %v", err)
	}
	b, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex(16) error: %v", err)
	}
	if a == b {
		t.Error("RandomHex(16) produced identical values on consecutive calls")
	}
}

func TestRandomBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		n       int
		wantLen int
	}{
		{"zero bytes", 0, 0},
		{"one byte", 1, 1},
		{"twelve bytes", 12, 12},
		{"thirty-two bytes", 32, 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RandomBytes(tt.n)
			if err != nil {
				t.Fatalf("RandomBytes(%d) error: %v", tt.n, err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("RandomBytes(%d) length = %d, want %d", tt.n, len(got), tt.wantLen)
			}
		})
	}
}

func TestRandomBytes_Unique(t *testing.T) {
	t.Parallel()

	a, err := RandomBytes(32)
	if err != nil {
		t.Fatalf("RandomBytes(32) error: %v", err)
	}
	b, err := RandomBytes(32)
	if err != nil {
		t.Fatalf("RandomBytes(32) error: %v", err)
	}

	same := true
	for i := range a {
		if a[i] != b[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("RandomBytes(32) produced identical values on consecutive calls")
	}
}
