// Package api provides shared HTTP server utilities, JSON response helpers,
// and middleware for the Murmur REST API. Both the server and client APIs
// use this package to ensure consistent behavior.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Response is the standard JSON envelope for all API responses.
// Success responses (status < 400) set ok=true and include the data field.
// Error responses (status >= 400) set ok=false and include the error field.
// The data field is omitted when nil; the error field is omitted when empty.
type Response struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// JSONResponse writes a JSON response with the given status code.
// For status codes < 400, the response has ok=true with the data field.
// For status codes >= 400, data is expected to be a string error message.
// Non-string data for error responses produces a generic "internal server error"
// to prevent accidental information leakage. The logger is used to report
// encoding failures; if nil, the default slog logger is used.
func JSONResponse(w http.ResponseWriter, status int, data any, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	var resp Response
	if status >= 400 {
		if msg, ok := data.(string); ok {
			resp = Response{OK: false, Error: msg}
		} else {
			resp = Response{OK: false, Error: "internal server error"}
		}
	} else {
		resp = Response{OK: true, Data: data}
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("api: failed to encode JSON response", "error", err)
	}
}

// NewHTTPServer creates an *http.Server with sensible timeouts for the API.
// The server binds to the given listen address and uses the provided handler.
func NewHTTPServer(listen string, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:         listen,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
}

// GracefulShutdown shuts down the HTTP server gracefully, waiting up to 5
// seconds for active connections to finish. It logs the shutdown result.
func GracefulShutdown(ctx context.Context, srv *http.Server, logger *slog.Logger) {
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("api server shutdown error", "error", err)
	} else {
		logger.Info("api server stopped gracefully")
	}
}
