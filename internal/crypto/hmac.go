package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SignHMAC computes HMAC-SHA256(key, payload) and returns the hex-encoded
// signature string.
func SignHMAC(key string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHMAC checks whether the hex-encoded signature matches
// HMAC-SHA256(key, payload). It uses constant-time comparison to prevent
// timing attacks.
func VerifyHMAC(key, signature string, payload []byte) bool {
	expected := SignHMAC(key, payload)
	return hmac.Equal([]byte(signature), []byte(expected))
}
