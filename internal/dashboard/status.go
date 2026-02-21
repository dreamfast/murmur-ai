package dashboard

import "time"

// StatusInfo holds server status data for the dashboard overview page.
// It is returned by StatusProvider implementations and serialized to JSON
// for the /dashboard/status endpoint.
type StatusInfo struct {
	// ServerName is the configured server display name.
	ServerName string `json:"server_name"`
	// Provider is the active LLM provider identifier.
	Provider string `json:"provider"`
	// Clients is the number of currently connected bus clients.
	Clients int `json:"clients"`
	// Tools is the total number of available tools (client + server).
	Tools int `json:"tools"`
	// Uptime is how long the server has been running.
	Uptime time.Duration `json:"uptime_ns"`
	// UptimeHuman is a human-readable uptime string (e.g. "2h15m30s").
	UptimeHuman string `json:"uptime"`
}

// StatusProvider supplies server status information to the dashboard.
// It is implemented by the server package and injected into the dashboard
// handler to avoid circular imports between internal/dashboard and
// internal/server.
type StatusProvider interface {
	// GetStatus returns the current server status snapshot.
	GetStatus() StatusInfo
}
