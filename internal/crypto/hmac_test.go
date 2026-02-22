package crypto

import (
	"testing"
)

func TestSignHMAC(t *testing.T) {
	t.Parallel()

	key := "test-secret-key"
	payload := []byte(`{"type":"register","client_id":"test"}`)

	sig := SignHMAC(key, payload)

	if sig == "" {
		t.Fatal("SignHMAC returned empty string")
	}
	if len(sig) != 64 { // SHA-256 = 32 bytes = 64 hex chars
		t.Errorf("SignHMAC signature length = %d, want 64", len(sig))
	}
}

func TestSignHMAC_Deterministic(t *testing.T) {
	t.Parallel()

	key := "test-key"
	payload := []byte("hello world")

	sig1 := SignHMAC(key, payload)
	sig2 := SignHMAC(key, payload)

	if sig1 != sig2 {
		t.Errorf("SignHMAC not deterministic: %q != %q", sig1, sig2)
	}
}

func TestVerifyHMAC(t *testing.T) {
	t.Parallel()

	key := "test-secret-key"
	payload := []byte(`{"type":"register","client_id":"test"}`)

	sig := SignHMAC(key, payload)

	if !VerifyHMAC(key, sig, payload) {
		t.Error("VerifyHMAC returned false for valid signature")
	}
}

func TestVerifyHMAC_WrongKey(t *testing.T) {
	t.Parallel()

	payload := []byte("test payload")
	sig := SignHMAC("correct-key", payload)

	if VerifyHMAC("wrong-key", sig, payload) {
		t.Error("VerifyHMAC returned true for wrong key")
	}
}

func TestVerifyHMAC_TamperedPayload(t *testing.T) {
	t.Parallel()

	key := "test-key"
	payload := []byte("original payload")
	sig := SignHMAC(key, payload)

	tampered := []byte("tampered payload")
	if VerifyHMAC(key, sig, tampered) {
		t.Error("VerifyHMAC returned true for tampered payload")
	}
}

func TestVerifyHMAC_InvalidSignature(t *testing.T) {
	t.Parallel()

	key := "test-key"
	payload := []byte("test payload")

	if VerifyHMAC(key, "not-a-valid-hex-signature", payload) {
		t.Error("VerifyHMAC returned true for invalid signature")
	}
}

func TestVerifyHMAC_EmptyKey(t *testing.T) {
	t.Parallel()

	payload := []byte("test payload")
	sig := SignHMAC("", payload)

	// Empty key should still work consistently.
	if !VerifyHMAC("", sig, payload) {
		t.Error("VerifyHMAC returned false for empty key with matching signature")
	}
}
