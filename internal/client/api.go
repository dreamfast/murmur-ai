package client

import (
	"encoding/json"
	"net/http"
	"time"

	"murmur/internal/api"
)

// newClientAPIMux creates the HTTP handler for the client REST API.
// It registers all endpoints and wraps them with the API key middleware
// and panic recovery.
func newClientAPIMux(c *Client) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/events", c.handlePostEvent)
	mux.HandleFunc("GET /api/v1/status", c.handleGetStatus)
	mux.HandleFunc("GET /api/v1/health", c.handleGetHealth)

	// Apply middleware: recovery first (outermost), then auth.
	var handler http.Handler = mux
	handler = api.APIKeyMiddleware(c.cfg.API.APIKey, c.logger)(handler)
	handler = api.RecoverMiddleware(c.logger, handler)

	return handler
}

// clientEventRequest is the JSON body for POST /api/v1/events.
type clientEventRequest struct {
	Source    string `json:"source"`
	EventType string `json:"event_type"`
	Summary   string `json:"summary"`
	Data      string `json:"data,omitempty"`
	EventID   string `json:"event_id,omitempty"`
}

// maxEventBodyBytes is the maximum size of a POST /api/v1/events request body.
const maxEventBodyBytes = 64 * 1024 // 64 KB

// handlePostEvent accepts an external event and forwards it to the server
// via the IRC bus. Returns 503 if the IRC connection is down.
func (c *Client) handlePostEvent(w http.ResponseWriter, r *http.Request) {
	// Check IRC connectivity before accepting the event.
	if !c.isConnected() {
		api.JSONResponse(w, http.StatusServiceUnavailable, "irc disconnected", c.logger)
		return
	}

	// Limit request body size to prevent abuse.
	r.Body = http.MaxBytesReader(w, r.Body, maxEventBodyBytes)

	var req clientEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.JSONResponse(w, http.StatusBadRequest, "invalid JSON body", c.logger)
		return
	}

	if req.Source == "" || req.EventType == "" || req.Summary == "" {
		api.JSONResponse(w, http.StatusBadRequest, "source, event_type, and summary are required", c.logger)
		return
	}

	// Generate a timestamp for the event.
	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Forward the event to the server via the bus.
	if err := c.sender.SendEvent(
		c.cfg.Client.ID,
		req.Source,
		req.EventType,
		req.Summary,
		req.Data,
		req.EventID,
		timestamp,
	); err != nil {
		c.logger.Error("api: failed to forward event via bus", "error", err)
		api.JSONResponse(w, http.StatusInternalServerError, "failed to forward event", c.logger)
		return
	}

	api.JSONResponse(w, http.StatusAccepted, map[string]any{
		"message":   "event forwarded",
		"client_id": c.cfg.Client.ID,
	}, c.logger)
}

// handleGetStatus returns client status information.
func (c *Client) handleGetStatus(w http.ResponseWriter, _ *http.Request) {
	// Collect tool names.
	toolNames := make([]string, 0, len(c.tools))
	for _, t := range c.tools {
		toolNames = append(toolNames, t.Name)
	}

	// Collect cron job info.
	cronJobs := c.cronRunner.ListJobs()

	status := map[string]any{
		"client_id":   c.cfg.Client.ID,
		"hostname":    c.cfg.Client.Hostname,
		"autonomy":    c.cfg.Client.Autonomy,
		"tools":       toolNames,
		"cron_jobs":   len(cronJobs),
		"uptime":      time.Since(c.startTime).String(),
		"connected":   c.isConnected(),
		"api_version": "v1",
	}

	api.JSONResponse(w, http.StatusOK, status, c.logger)
}

// handleGetHealth returns a simple health check response.
func (c *Client) handleGetHealth(w http.ResponseWriter, _ *http.Request) {
	api.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"}, c.logger)
}
