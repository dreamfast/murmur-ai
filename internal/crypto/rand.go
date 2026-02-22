// Package crypto provides shared cryptographic utilities for Murmur,
// including random byte/hex generation and HMAC-SHA256 signing/verification.
package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RandomHex returns n cryptographically random bytes encoded as a hex string
// (2n characters). It returns an error if the system's CSPRNG fails.
func RandomHex(n int) (string, error) {
	b, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// RandomBytes generates n cryptographically random bytes.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto.RandomBytes: %w", err)
	}
	return b, nil
}
