package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
)

// APIKeyMiddleware returns middleware that validates the Authorization header
// against the expected API key using constant-time comparison. If the key is
// empty, the middleware is a no-op (all requests pass through). The expected
// header format is "Bearer <key>" (scheme matching is case-insensitive per
// RFC 7235 Section 2.1).
func APIKeyMiddleware(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth if no key is configured.
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			provided := r.Header.Get("Authorization")

			// Parse scheme and token (case-insensitive scheme per RFC 7235).
			scheme, token, found := strings.Cut(provided, " ")
			if !found || !strings.EqualFold(scheme, "Bearer") {
				JSONResponse(w, http.StatusUnauthorized, "invalid or missing api key")
				return
			}

			if subtle.ConstantTimeCompare([]byte(token), []byte(key)) != 1 {
				JSONResponse(w, http.StatusUnauthorized, "invalid or missing api key")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RecoverMiddleware returns middleware that recovers from panics in HTTP
// handlers, logs the stack trace, and returns a 500 Internal Server Error.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("api: panic recovered",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				JSONResponse(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
