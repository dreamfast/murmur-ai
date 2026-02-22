package server

import (
	"sort"
	"time"

	"murmur/internal/dashboard"
	"murmur/internal/db"
)

// buildAdminDeps constructs the AdminDeps for the dashboard admin API by
// wrapping server-internal types with adapter implementations that satisfy
// the dashboard interfaces. Returns nil if the database is not available.
func (s *Server) buildAdminDeps() *dashboard.AdminDeps {
	if s.database == nil {
		return nil
	}

	deps := &dashboard.AdminDeps{
		DB:       s.database,
		Reloader: s,
	}

	// Admin checker — wraps PermissionManager.
	if s.permissions != nil {
		deps.Admin = s.permissions
	}

	// Task manager — wraps Scheduler.
	if s.scheduler != nil {
		deps.Tasks = &schedulerAdapter{s: s.scheduler}
	}

	// Tool lister — combines server tools, client tools, and custom tools.
	deps.Tools = &toolListerAdapter{
		serverTools: s.serverTools,
		registry:    s.registry,
		database:    s.database,
	}

	// Channel lister — wraps ChannelSettingsStore.
	if s.channelSettings != nil {
		deps.Channels = &channelListerAdapter{store: s.channelSettings}
	}

	// Provider lister — reads from config.
	deps.Providers = &providerListerAdapter{s: s}

	return deps
}

// schedulerAdapter adapts *Scheduler to dashboard.TaskManager by converting
// ScheduledTask to dashboard.TaskInfo.
type schedulerAdapter struct {
	s *Scheduler
}

// ListTasks returns all scheduled tasks as dashboard.TaskInfo values.
func (a *schedulerAdapter) ListTasks() ([]dashboard.TaskInfo, error) {
	tasks, err := a.s.ListTasks()
	if err != nil {
		return nil, err
	}
	result := make([]dashboard.TaskInfo, len(tasks))
	for i, t := range tasks {
		result[i] = scheduledTaskToInfo(t)
	}
	return result, nil
}

// AddTask creates a new cron task.
func (a *schedulerAdapter) AddTask(name, schedule, action, channel, createdBy, provider string) (int64, error) {
	return a.s.AddTask(name, schedule, action, channel, createdBy, provider)
}

// AddOneShotTask creates a new one-shot task.
func (a *schedulerAdapter) AddOneShotTask(name string, runAt time.Time, action, channel, createdBy, provider string) (int64, error) {
	return a.s.AddOneShotTask(name, runAt, action, channel, createdBy, provider)
}

// RemoveTask deletes a task by ID.
func (a *schedulerAdapter) RemoveTask(id int64) error {
	return a.s.RemoveTask(id)
}

// EnableTask enables a task by ID.
func (a *schedulerAdapter) EnableTask(id int64) error {
	return a.s.EnableTask(id)
}

// DisableTask disables a task by ID.
func (a *schedulerAdapter) DisableTask(id int64) error {
	return a.s.DisableTask(id)
}

// scheduledTaskToInfo converts a ScheduledTask to a dashboard.TaskInfo.
func scheduledTaskToInfo(t ScheduledTask) dashboard.TaskInfo {
	info := dashboard.TaskInfo{
		ID:        t.ID,
		Name:      t.Name,
		Schedule:  t.Schedule,
		Action:    t.Action,
		Channel:   t.Channel,
		Enabled:   t.Enabled,
		Type:      t.Type,
		CreatedBy: t.CreatedBy,
		Provider:  t.Provider,
	}
	if t.LastRun.Valid {
		lr := t.LastRun.Time
		info.LastRun = &lr
	}
	if t.NextRun.Valid {
		nr := t.NextRun.Time
		info.NextRun = &nr
	}
	if t.RunAt.Valid {
		ra := t.RunAt.Time
		info.RunAt = &ra
	}
	return info
}

// customToolLister is the interface needed by toolListerAdapter for listing
// custom tools. Satisfied by *db.DB.
type customToolLister interface {
	ListCustomTools(enabledOnly bool) ([]db.CustomTool, error)
}

// toolListerAdapter combines server tools, client tools, and custom tools
// into a single list for the dashboard.
type toolListerAdapter struct {
	serverTools *ToolRegistry
	registry    *Registry
	database    customToolLister
}

// ListAllTools returns all tools with their source.
func (a *toolListerAdapter) ListAllTools() []dashboard.ToolInfo {
	var result []dashboard.ToolInfo

	// Server tools.
	if a.serverTools != nil {
		for _, td := range a.serverTools.AllToolDefs() {
			result = append(result, dashboard.ToolInfo{
				Name:        td.Name,
				Description: td.Description,
				Source:      "server",
			})
		}
	}

	// Client tools.
	if a.registry != nil {
		for _, td := range a.registry.AllTools() {
			result = append(result, dashboard.ToolInfo{
				Name:        td.Name,
				Description: td.Description,
				Source:      "client",
			})
		}
	}

	// Custom tools.
	if a.database != nil {
		tools, err := a.database.ListCustomTools(false)
		if err == nil {
			for _, t := range tools {
				result = append(result, dashboard.ToolInfo{
					Name:        t.Name,
					Description: t.Description,
					Source:      "custom",
				})
			}
		}
	}

	return result
}

// channelListerAdapter wraps ChannelSettingsStore for the dashboard.
type channelListerAdapter struct {
	store *ChannelSettingsStore
}

// ListChannels returns all channel settings.
func (a *channelListerAdapter) ListChannels() ([]dashboard.ChannelSettingsInfo, error) {
	settings, err := a.store.ListAll()
	if err != nil {
		return nil, err
	}
	result := make([]dashboard.ChannelSettingsInfo, len(settings))
	for i, cs := range settings {
		result[i] = dashboard.ChannelSettingsInfo{
			Channel:     cs.Channel,
			Provider:    cs.Provider,
			AutoJoin:    cs.AutoJoin,
			TopicPrefix: cs.TopicPrefix,
		}
	}
	return result, nil
}

// UpdateChannel updates channel settings via the store.
func (a *channelListerAdapter) UpdateChannel(cs *dashboard.ChannelSettingsInfo) error {
	return a.store.Upsert(&ChannelSettings{
		Channel:     cs.Channel,
		Provider:    cs.Provider,
		AutoJoin:    cs.AutoJoin,
		TopicPrefix: cs.TopicPrefix,
	})
}

// providerListerAdapter reads LLM provider config from the server.
type providerListerAdapter struct {
	s *Server
}

// ListProviders returns all configured LLM providers sorted by name for
// deterministic ordering.
func (a *providerListerAdapter) ListProviders() []dashboard.ProviderInfo {
	cfg := a.s.loadCfg()
	var result []dashboard.ProviderInfo
	for name, p := range cfg.LLM.Providers {
		result = append(result, dashboard.ProviderInfo{
			Name:      name,
			Model:     p.Model,
			APIBase:   p.APIBase,
			IsDefault: name == cfg.LLM.Default,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}
