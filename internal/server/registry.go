package server

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"murmur/internal/bus"
)

// ClientInfo holds the state of a connected client as tracked by the server.
type ClientInfo struct {
	ClientID      string
	Hostname      string
	Tools         []bus.ToolDef
	LastHeartbeat time.Time
	Status        string // "online" or "offline"
	RegisteredAt  time.Time
	Load          bus.LoadInfo
	Autonomy      string // "auto", "approve", or "report"; default "auto"
}

// copyClientInfo returns a deep copy of a ClientInfo, including its Tools slice.
func copyClientInfo(c *ClientInfo) ClientInfo {
	info := *c
	if c.Tools != nil {
		info.Tools = make([]bus.ToolDef, len(c.Tools))
		copy(info.Tools, c.Tools)
	}
	return info
}

// Registry tracks connected clients and their tools. It is safe for
// concurrent use. The registry is in-memory only — state is lost on restart
// but self-heals as clients re-register.
type Registry struct {
	mu            sync.RWMutex
	clients       map[string]*ClientInfo
	clientTimeout time.Duration
	logger        *slog.Logger
}

// NewRegistry creates a new client registry with the given heartbeat timeout.
func NewRegistry(clientTimeout time.Duration, logger *slog.Logger) *Registry {
	return &Registry{
		clients:       make(map[string]*ClientInfo),
		clientTimeout: clientTimeout,
		logger:        logger,
	}
}

// Register adds or updates a client in the registry. If the client already
// exists, its tools and status are updated.
func (r *Registry) Register(msg *bus.RegisterMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	tools := make([]bus.ToolDef, len(msg.Tools))
	copy(tools, msg.Tools)

	// Default autonomy to "auto" for backward compatibility with clients
	// that don't send an autonomy field. Invalid values are coerced to
	// "approve" (the most restrictive mode) to prevent fail-open on
	// misconfiguration or malformed client payloads.
	autonomy := msg.Autonomy
	switch autonomy {
	case "", "auto":
		autonomy = "auto"
	case "approve", "report":
		// Valid — keep as-is.
	default:
		r.logger.Warn("invalid autonomy value, defaulting to approve",
			"client_id", msg.ClientID,
			"autonomy", autonomy,
		)
		autonomy = "approve"
	}

	existing, ok := r.clients[msg.ClientID]
	if ok {
		existing.Hostname = msg.Hostname
		existing.Tools = tools
		existing.Status = "online"
		existing.LastHeartbeat = now
		existing.Autonomy = autonomy
		r.logger.Info("client re-registered",
			"client_id", msg.ClientID,
			"tools", len(msg.Tools),
			"autonomy", autonomy,
		)
	} else {
		r.clients[msg.ClientID] = &ClientInfo{
			ClientID:      msg.ClientID,
			Hostname:      msg.Hostname,
			Tools:         tools,
			LastHeartbeat: now,
			Status:        "online",
			RegisteredAt:  now,
			Autonomy:      autonomy,
		}
		r.logger.Info("client registered",
			"client_id", msg.ClientID,
			"hostname", msg.Hostname,
			"tools", len(msg.Tools),
			"autonomy", autonomy,
		)
	}
}

// Deregister marks a client as offline.
func (r *Registry) Deregister(clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if client, ok := r.clients[clientID]; ok {
		client.Status = "offline"
		r.logger.Info("client deregistered", "client_id", clientID)
	}
}

// Heartbeat updates a client's last heartbeat time and load metrics.
func (r *Registry) Heartbeat(msg *bus.HeartbeatMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()

	client, ok := r.clients[msg.ClientID]
	if !ok {
		r.logger.Warn("heartbeat from unknown client", "client_id", msg.ClientID)
		return
	}

	client.LastHeartbeat = time.Now()
	client.Load = msg.Load
	if client.Status == "offline" {
		client.Status = "online"
		r.logger.Info("client came back online", "client_id", msg.ClientID)
	}
}

// GetClient returns a copy of the client info for the given ID.
func (r *Registry) GetClient(clientID string) (ClientInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	client, ok := r.clients[clientID]
	if !ok {
		return ClientInfo{}, false
	}
	return copyClientInfo(client), true
}

// GetOnlineClients returns a copy of all online clients.
func (r *Registry) GetOnlineClients() []ClientInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []ClientInfo
	for _, c := range r.clients {
		if c.Status == "online" {
			result = append(result, copyClientInfo(c))
		}
	}
	return result
}

// OnlineClients returns a sorted list of client IDs for all currently online clients.
func (r *Registry) OnlineClients() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var ids []string
	for _, c := range r.clients {
		if c.Status == "online" {
			ids = append(ids, c.ClientID)
		}
	}
	sort.Strings(ids)
	return ids
}

// GetToolProvider finds the online client that provides the given tool.
// If multiple clients provide the same tool, the one with the most recent
// heartbeat is preferred.
func (r *Registry) GetToolProvider(toolName string) (ClientInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var best *ClientInfo
	for _, c := range r.clients {
		if c.Status != "online" {
			continue
		}
		for _, t := range c.Tools {
			if t.Name == toolName {
				if best == nil || c.LastHeartbeat.After(best.LastHeartbeat) ||
					(c.LastHeartbeat.Equal(best.LastHeartbeat) && c.ClientID < best.ClientID) {
					best = c
				}
				break
			}
		}
	}

	if best == nil {
		return ClientInfo{}, false
	}
	return copyClientInfo(best), true
}

// GetClientAutonomy returns the autonomy level for the given client. If the
// client is not found, it returns "auto" as a safe default.
func (r *Registry) GetClientAutonomy(clientID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	client, ok := r.clients[clientID]
	if !ok {
		return "auto"
	}
	if client.Autonomy == "" {
		return "auto"
	}
	return client.Autonomy
}

// ShellTargets returns the client IDs of all online clients that provide the
// shell tool. Used to populate the available targets in the shell tool description.
func (r *Registry) ShellTargets() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var targets []string
	for _, c := range r.clients {
		if c.Status != "online" {
			continue
		}
		for _, t := range c.Tools {
			if t.Name == "shell" {
				targets = append(targets, c.ClientID)
				break
			}
		}
	}
	return targets
}

// AllTools returns all tools from all online clients. If multiple clients
// provide the same tool, only the one from the preferred provider is included.
func (r *Registry) AllTools() []bus.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]time.Time)
	seenClient := make(map[string]string)
	toolMap := make(map[string]bus.ToolDef)

	for _, c := range r.clients {
		if c.Status != "online" {
			continue
		}
		for _, t := range c.Tools {
			lastHB, exists := seen[t.Name]
			if !exists || c.LastHeartbeat.After(lastHB) ||
				(c.LastHeartbeat.Equal(lastHB) && c.ClientID < seenClient[t.Name]) {
				seen[t.Name] = c.LastHeartbeat
				seenClient[t.Name] = c.ClientID
				toolMap[t.Name] = t
			}
		}
	}

	result := make([]bus.ToolDef, 0, len(toolMap))
	for _, t := range toolMap {
		result = append(result, t)
	}
	return result
}

// StartMonitor runs a background goroutine that periodically checks for
// clients that have missed their heartbeat deadline and marks them offline.
// It stops when the context is cancelled.
func (r *Registry) StartMonitor(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.checkTimeouts()
		}
	}
}

// checkTimeouts marks clients as offline if they haven't sent a heartbeat
// within the configured timeout.
func (r *Registry) checkTimeouts() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for _, c := range r.clients {
		if c.Status == "online" && now.Sub(c.LastHeartbeat) > r.clientTimeout {
			c.Status = "offline"
			r.logger.Warn("client timed out",
				"client_id", c.ClientID,
				"last_heartbeat", c.LastHeartbeat,
			)
		}
	}
}
