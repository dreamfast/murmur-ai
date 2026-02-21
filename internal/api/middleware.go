package api

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
)

// authNickKey is the context key used to store the authenticated user's nick
// resolved from a per-user API key. Handlers can retrieve it with AuthNick().
type authNickKey struct{}

// AuthNick returns the authenticated user's nick from the request context,
// or empty string if the request was authenticated with the global API key
// (or no per-user key resolution was configured).
func AuthNick(ctx context.Context) string {
	nick, _ := ctx.Value(authNickKey{}).(string)
	return nick
}

// UserKeyResolver is a function that looks up a nick by API key. It returns
// the nick associated with the key, or empty string if no match is found.
// Implementations should use constant-time comparison internally.
type UserKeyResolver func(apiKey string) string

// APIKeyMiddleware returns middleware that validates the Authorization header
// against the expected API key using constant-time comparison. If the key is
// empty, the middleware is a no-op (all requests pass through). The expected
// header format is "Bearer <key>" (scheme matching is case-insensitive per
// RFC 7235 Section 2.1).
func APIKeyMiddleware(key string) func(http.Handler) http.Handler {
	return APIKeyMiddlewareWithUserKeys(key, nil)
}

// APIKeyMiddlewareWithUserKeys returns middleware that validates the
// Authorization header against both per-user API keys and a global API key.
// Per-user keys are checked first via the resolver function. If a per-user
// key matches, the resolved nick is stored in the request context (retrievable
// via AuthNick). If no per-user key matches, the global key is checked.
// If the global key is empty and no resolver is provided, all requests pass.
func APIKeyMiddlewareWithUserKeys(globalKey string, resolver UserKeyResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth if no key is configured and no resolver is set.
			if globalKey == "" && resolver == nil {
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

			// Try per-user key resolution first.
			if resolver != nil {
				if nick := resolver(token); nick != "" {
					ctx := context.WithValue(r.Context(), authNickKey{}, nick)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// Fall back to global key.
			if globalKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(globalKey)) == 1 {
				next.ServeHTTP(w, r)
				return
			}

			JSONResponse(w, http.StatusUnauthorized, "invalid or missing api key")
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
