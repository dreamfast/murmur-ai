package bus

import "errors"

// Sentinel errors for bus protocol operations.
var (
	// ErrUnknownMessageType is returned when a bus message has an
	// unrecognized type field.
	ErrUnknownMessageType = errors.New("unknown bus message type")

	// ErrInvalidJSON is returned when a bus message cannot be parsed as JSON.
	ErrInvalidJSON = errors.New("invalid JSON in bus message")

	// ErrInvalidSignature is returned when a bus message's HMAC-SHA256
	// signature does not match the expected value.
	ErrInvalidSignature = errors.New("bus message signature verification failed")
)
