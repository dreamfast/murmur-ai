package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"murmur/internal/api"
	"murmur/internal/db"
)

// newServerAPIMux creates the HTTP handler for the server REST API.
// It registers all endpoints and wraps them with the API key middleware
// and panic recovery. When a PermissionManager is available, per-user API
// keys are supported — requests authenticated with a per-user key have the
// resolved nick stored in the request context.
func newServerAPIMux(s *Server) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/events", s.handlePostEvent)
	mux.HandleFunc("GET /api/v1/events", s.handleGetEvents)
	mux.HandleFunc("GET /api/v1/status", s.handleGetStatus)
	mux.HandleFunc("GET /api/v1/clients", s.handleGetClients)
	mux.HandleFunc("GET /api/v1/health", s.handleGetHealth)

	// Build the per-user key resolver if a PermissionManager is available.
	var resolver api.UserKeyResolver
	if pm := s.permissions; pm != nil {
		resolver = pm.GetUserByAPIKey
	}

	// Apply middleware: recovery first (outermost), then auth.
	var handler http.Handler = mux
	handler = api.APIKeyMiddlewareWithUserKeys(s.cfg.API.APIKey, resolver, s.logger)(handler)
	handler = api.RecoverMiddleware(s.logger, handler)

	return handler
}

// eventRequest is the JSON body for POST /api/v1/events.
type eventRequest struct {
	Source    string `json:"source"`
	EventType string `json:"event_type"`
	Summary   string `json:"summary"`
	Data      string `json:"data,omitempty"`
	Channel   string `json:"channel,omitempty"`
	EventID   string `json:"event_id,omitempty"`
}

// handlePostEvent accepts an external event, stores it in the database, and
// triggers the agent loop. Returns 202 Accepted on success.
func (s *Server) handlePostEvent(w http.ResponseWriter, r *http.Request) {
	var req eventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.JSONResponse(w, http.StatusBadRequest, "invalid JSON body", s.logger)
		return
	}

	if req.Source == "" || req.EventType == "" || req.Summary == "" {
		api.JSONResponse(w, http.StatusBadRequest, "source, event_type, and summary are required", s.logger)
		return
	}

	channel := req.Channel
	if channel == "" {
		channel = s.cfg.IRC.Channels.Main
	}

	// Store the event in the database.
	event := &db.Event{
		EventID:   req.EventID,
		Source:    req.Source,
		EventType: req.EventType,
		Summary:   req.Summary,
		Data:      req.Data,
		Channel:   channel,
	}

	id, inserted, err := s.database.InsertEvent(r.Context(), event)
	if err != nil {
		s.logger.Error("api: failed to insert event", "error", err)
		api.JSONResponse(w, http.StatusInternalServerError, "failed to store event", s.logger)
		return
	}

	if !inserted {
		// Duplicate event_id — return the existing event's ID.
		api.JSONResponse(w, http.StatusOK, map[string]any{
			"id":        id,
			"duplicate": true,
		}, s.logger)
		return
	}

	// Resolve the nick for permission filtering. Per-user API keys resolve
	// to the associated nick; the global API key uses "_system" (admin-equivalent,
	// bypasses permission filtering).
	nick := api.AuthNick(r.Context())
	if nick == "" {
		nick = "_system"
	}

	// Trigger agent processing in a goroutine (non-blocking for the API caller).
	// Use context.Background() because the agent goroutine must outlive the
	// HTTP request — r.Context() is cancelled when the response is sent.
	// MarkEventProcessed is called AFTER HandleEvent succeeds to avoid marking
	// events as processed that were never actually handled (crash recovery).
	s.agentWg.Add(1)
	go func() {
		defer s.agentWg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("api: agent event goroutine panicked", "recover", rec)
			}
		}()
		if err := s.agent.HandleEvent(context.Background(), channel, nick, req.Source, req.EventType, req.Summary, req.Data); err != nil {
			s.logger.Error("api: agent HandleEvent failed", "error", err)
			return
		}
		if err := s.database.MarkEventProcessed(context.Background(), id); err != nil {
			s.logger.Error("api: failed to mark event processed", "error", err)
		}
	}()

	api.JSONResponse(w, http.StatusAccepted, map[string]any{
		"id":      id,
		"message": "event accepted",
	}, s.logger)
}

// handleGetEvents returns a paginated list of events.
func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	q := db.EventsQuery{
		Source: r.URL.Query().Get("source"),
	}

	if afterStr := r.URL.Query().Get("after_id"); afterStr != "" {
		afterID, err := strconv.ParseInt(afterStr, 10, 64)
		if err != nil {
			api.JSONResponse(w, http.StatusBadRequest, "invalid after_id parameter", s.logger)
			return
		}
		q.AfterID = afterID
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil {
			api.JSONResponse(w, http.StatusBadRequest, "invalid limit parameter", s.logger)
			return
		}
		q.Limit = limit
	}

	events, err := s.database.ListEvents(r.Context(), q)
	if err != nil {
		s.logger.Error("api: failed to list events", "error", err)
		api.JSONResponse(w, http.StatusInternalServerError, "failed to list events", s.logger)
		return
	}

	api.JSONResponse(w, http.StatusOK, events, s.logger)
}

// handleGetStatus returns server status information.
func (s *Server) handleGetStatus(w http.ResponseWriter, _ *http.Request) {
	clients := s.registry.GetOnlineClients()
	toolCount := len(s.registry.AllTools()) + len(s.serverTools.AllToolDefs())

	status := map[string]any{
		"server":      s.cfg.Server.Name,
		"provider":    s.agent.GetProvider(),
		"clients":     len(clients),
		"tools":       toolCount,
		"uptime":      time.Since(s.startTime).String(),
		"api_version": "v1",
	}

	api.JSONResponse(w, http.StatusOK, status, s.logger)
}

// handleGetClients returns a list of connected clients with their tools.
func (s *Server) handleGetClients(w http.ResponseWriter, _ *http.Request) {
	clients := s.registry.GetOnlineClients()

	type clientInfo struct {
		ClientID string   `json:"client_id"`
		Hostname string   `json:"hostname"`
		Autonomy string   `json:"autonomy"`
		Tools    []string `json:"tools"`
		Status   string   `json:"status"`
	}

	result := make([]clientInfo, 0, len(clients))
	for _, c := range clients {
		toolNames := make([]string, 0, len(c.Tools))
		for _, t := range c.Tools {
			toolNames = append(toolNames, t.Name)
		}
		result = append(result, clientInfo{
			ClientID: c.ClientID,
			Hostname: c.Hostname,
			Autonomy: c.Autonomy,
			Tools:    toolNames,
			Status:   "online",
		})
	}

	api.JSONResponse(w, http.StatusOK, result, s.logger)
}

// handleGetHealth returns a simple health check response.
func (s *Server) handleGetHealth(w http.ResponseWriter, _ *http.Request) {
	api.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"}, s.logger)
}
