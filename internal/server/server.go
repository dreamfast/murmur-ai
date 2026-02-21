// Package server implements the Murmur server — the agent brain that connects
// to IRC, manages the client registry, and coordinates tool execution.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"murmur/internal/api"
	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/db"
	"murmur/internal/irc"
	"murmur/internal/llm"
	"murmur/internal/tools"
	"murmur/internal/vault"
)

// defaultHTTPToolTimeout is the default timeout for the http_request tool.
const defaultHTTPToolTimeout = 30 * time.Second

// Server is the Murmur server that connects to IRC, manages clients via the
// bus protocol, and runs the agent loop for LLM-powered conversations.
type Server struct {
	cfgMu      sync.RWMutex // protects cfg from concurrent read/write during reload
	cfg        *config.ServerConfig
	configPath string // path to the TOML config file, used for hot reload
	conn       *irc.Connection
	handler    *irc.MessageHandler
	sender     *bus.Sender
	receiver   *bus.Receiver
	registry   *Registry
	logger     *slog.Logger

	// Phase 2 components.
	database        *db.DB
	memory          *Memory
	router          *Router
	serverTools     *ToolRegistry
	channelSettings *ChannelSettingsStore
	scheduler       *Scheduler
	commands        *CommandHandler
	agent           *Agent

	// allowedUsers is an atomic copy of the allowed users list, used by
	// isAllowed() for lock-free reads. Updated during Reload().
	allowedUsers atomic.Pointer[[]string]

	// reloadMu serializes concurrent reload attempts from SIGHUP, !reload,
	// and config_manage. Only one reload can run at a time.
	reloadMu sync.Mutex

	// agentWg tracks in-flight agent goroutines so that Run() can wait for
	// them to finish before closing the database.
	agentWg sync.WaitGroup

	// flood provides per-nick rate limiting and per-channel bounded message
	// queuing to prevent abuse from IRC floods.
	flood *floodGuard

	// startTime records when the server was created, used for uptime reporting.
	startTime time.Time

	// httpServer is the REST API HTTP server, nil when API is disabled.
	httpServer *http.Server

	// ircLogHandler is the IRC debug channel log handler, nil when debug
	// channel is not configured. Exposed so commands can toggle it.
	ircLogHandler *irc.IRCLogHandler
}

// New creates a new server from the given configuration. It opens the SQLite
// database, creates the LLM providers, and wires all components together.
// It does not connect to IRC — call Run to start the server. The configPath
// is stored for hot config reload via SIGHUP or !reload.
func New(cfg *config.ServerConfig, configPath string, logger *slog.Logger) (*Server, error) {
	// Resolve vault: references in the config before using any values.
	if cfg.Vault.Enabled && cfg.Vault.DBPath != "" {
		passphrase := os.Getenv(cfg.Vault.PassphraseEnv)
		if passphrase == "" {
			logger.Warn("vault enabled but passphrase env var is empty, vault: references will not be resolved",
				"env_var", cfg.Vault.PassphraseEnv)
		} else {
			v, err := vault.Open(cfg.Vault.DBPath, passphrase)
			if err != nil {
				logger.Warn("vault enabled but could not open, vault: references will not be resolved", "error", err)
			} else {
				if err := vault.ResolveVaultRefs(v, cfg); err != nil {
					v.Close()
					return nil, fmt.Errorf("server.New: resolve vault refs: %w", err)
				}
				v.Close()
				logger.Info("vault references resolved")
			}
		}
	}

	// Set up the IRC debug log handler if a debug channel is configured.
	// This wraps the existing logger with a MultiHandler that fans out to
	// both stderr and the IRC channel. The IRC handler starts disabled and
	// is activated after the IRC connection is established in Run().
	var ircLogHandler *irc.IRCLogHandler
	if cfg.Server.DebugChannel != "" {
		ircLogHandler = irc.NewIRCLogHandler(cfg.Server.DebugChannel, slog.LevelDebug)
		multiHandler := irc.NewMultiHandler(logger.Handler(), ircLogHandler)
		logger = slog.New(multiHandler)
		logger.Info("debug channel configured", "channel", cfg.Server.DebugChannel)
	}

	channels := []string{cfg.IRC.Channels.Main, cfg.IRC.Channels.Bus}

	conn, err := irc.NewConnection(cfg.IRC, channels, logger)
	if err != nil {
		return nil, fmt.Errorf("server.New: %w", err)
	}

	handler := irc.NewMessageHandler(conn, cfg.IRC.Channels.Bus)
	sender := bus.NewSender(conn, cfg.IRC.Channels.Bus, cfg.Security.BusKey, cfg.IRC.MaxLineLen, logger)
	receiver := bus.NewReceiver(cfg.Security.BusKey, logger)

	parsed, err := cfg.ParseScheduler()
	if err != nil {
		return nil, fmt.Errorf("server.New: %w", err)
	}

	registry := NewRegistry(parsed.ClientTimeout, logger)

	// Open the SQLite database and run migrations.
	database, err := db.Open(cfg.Memory.DBPath)
	if err != nil {
		return nil, fmt.Errorf("server.New: open database: %w", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: migrate database: %w", err)
	}

	// Create the channel settings store for per-channel state.
	channelSettings := NewChannelSettingsStore(database, logger)

	// Create LLM providers from config.
	providers := make(map[string]llm.Provider)
	for name, provCfg := range cfg.LLM.Providers {
		providers[name] = llm.NewOpenAICompatProvider(name, provCfg, logger)
	}

	// Validate that the default provider exists in the providers map.
	if len(providers) > 0 && cfg.LLM.Default != "" {
		if _, ok := providers[cfg.LLM.Default]; !ok {
			database.Close()
			return nil, fmt.Errorf("server.New: default LLM provider %q not found in configured providers", cfg.LLM.Default)
		}
	}

	// Resolve the summary provider (may be nil if not configured).
	var summaryProvider llm.Provider
	if cfg.Memory.SummaryModel != "" {
		sp, ok := providers[cfg.Memory.SummaryModel]
		if !ok {
			database.Close()
			return nil, fmt.Errorf("server.New: summary_model %q not found in configured providers", cfg.Memory.SummaryModel)
		}
		summaryProvider = sp
	}

	// Create conversation memory with optional summarization.
	memory := NewMemory(database, cfg.Memory.MaxHistory, cfg.Memory.SummaryThreshold, summaryProvider, logger)

	// Create the tool router.
	router := NewRouter(registry, sender, logger)

	// Create the server-side tool registry for tools that execute locally.
	serverTools := NewToolRegistry()

	// Create the notes store and register note tools.
	notesStore := NewNotesStore(database, logger)
	if err := RegisterNoteTools(serverTools, notesStore); err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: register note tools: %w", err)
	}

	// reloadPtr is a late-binding pointer used by the config_manage tool's
	// reload callback. It is set to the server after construction, breaking
	// the circular dependency (tools need server, server needs tools).
	var reloadPtr *Server

	// Build and register server-side tools from the unified config. Vault
	// references are already resolved upfront, so we pass nil for the
	// resolver. The IRC connection satisfies tools.IRCManager, and the
	// MemoryAdapter bridges Memory to tools.MemoryReader.
	builtTools, err := tools.BuildTools(tools.BuildToolsOpts{
		Config:           &cfg.Tools,
		Logger:           logger,
		IRCManager:       conn,
		Memory:           NewMemoryAdapter(memory),
		BusChannel:       cfg.IRC.Channels.Bus,
		ChannelPersister: channelSettings,
		ReloadFunc: func() error {
			if reloadPtr == nil {
				return fmt.Errorf("server not initialized yet")
			}
			return reloadPtr.Reload()
		},
	})
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: build tools: %w", err)
	}
	for _, t := range builtTools {
		if err := serverTools.Register(t); err != nil {
			database.Close()
			return nil, fmt.Errorf("server.New: register tool %s: %w", t.Name, err)
		}
	}
	if cfg.Tools.HTTP != nil && cfg.Tools.HTTP.Enabled {
		timeout := defaultHTTPToolTimeout
		if cfg.Tools.HTTP.Timeout != "" {
			parsed, err := time.ParseDuration(cfg.Tools.HTTP.Timeout)
			if err != nil {
				database.Close()
				return nil, fmt.Errorf("server.New: http tool timeout: %w", err)
			}
			timeout = parsed
		}
		httpCfg := tools.HTTPRequestToolConfig{
			Timeout:          timeout,
			MaxResponseBytes: cfg.Tools.HTTP.MaxResponseBytes,
			AllowedDomains:   cfg.Tools.HTTP.AllowedDomains,
			BlockPrivateIPs:  cfg.Tools.HTTP.GetBlockPrivateIPs(),
		}
		if httpCfg.MaxResponseBytes <= 0 {
			httpCfg.MaxResponseBytes = 1024 * 1024 // 1 MB default
		}
		if err := serverTools.Register(tools.NewHTTPRequestTool(httpCfg)); err != nil {
			database.Close()
			return nil, fmt.Errorf("server.New: %w", err)
		}
		logger.Info("enabled server tool", "name", "http_request")
	}

	// Load the system prompt.
	systemPrompt, err := LoadSystemPrompt(cfg.Server.SystemPromptFile)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: %w", err)
	}

	// Parse the approval timeout.
	approvalTimeout, err := cfg.ParseApprovalTimeout()
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: %w", err)
	}

	// Create the approval manager for the tool call approval flow.
	approvals := NewApprovalManager(logger)

	// Create the agent (may have zero providers — commands still work).
	agent := NewAgent(
		providers,
		cfg.LLM.Default,
		serverTools,
		registry,
		memory,
		router,
		approvals,
		conn,
		systemPrompt,
		cfg.Server.Name,
		cfg.IRC.Channels.Bus,
		cfg.Memory.MaxHistory,
		cfg.Memory.CrossChannelContext,
		channelSettings,
		2*time.Minute,
		approvalTimeout,
		cfg.Server.Verbose,
		logger,
	)

	// Create the task scheduler (may be nil if not enabled).
	var scheduler *Scheduler
	if cfg.Scheduler.Enabled {
		scheduler = NewScheduler(database, agent, parsed.TickInterval, parsed.MaxConcurrent, logger)
	}

	// Register scheduler tools so the agent can create/manage tasks.
	if err := RegisterSchedulerTools(serverTools, scheduler, cfg.IRC.Channels.Main); err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: register scheduler tools: %w", err)
	}

	// Create the custom tool manager and load persisted tools from the DB.
	// The Piston URL is extracted from the code_exec config if available.
	var pistonURL string
	if cfg.Tools.CodeExec != nil && cfg.Tools.CodeExec.PistonURL != "" {
		pistonURL = cfg.Tools.CodeExec.PistonURL
	}
	customToolMgr := NewCustomToolManager(database, serverTools, pistonURL, logger)
	if err := customToolMgr.RegisterMetaTools(serverTools); err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: register custom tool meta-tools: %w", err)
	}
	if err := customToolMgr.LoadFromDB(); err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: load custom tools from DB: %w", err)
	}

	// Wire the pipeline executor: the agent implements ToolExecutor, which
	// the CustomToolManager needs for pipeline steps. This breaks the circular
	// dependency (agent → serverTools → customToolMgr → agent) via late binding.
	customToolMgr.SetExecutor(agent)

	// Create the command handler.
	var modelSwitcher ModelSwitcher
	if len(providers) > 0 {
		modelSwitcher = agent
	}
	flood := newFloodGuard(logger)
	var debugToggler DebugToggler
	if ircLogHandler != nil {
		debugToggler = ircLogHandler
	}
	commands := NewCommandHandler(registry, memory, notesStore, scheduler, approvals, conn, modelSwitcher, flood, debugToggler, nil, cfg.Security.AllowedUsers, time.Now(), logger)

	s := &Server{
		cfg:             cfg,
		configPath:      configPath,
		conn:            conn,
		handler:         handler,
		sender:          sender,
		receiver:        receiver,
		registry:        registry,
		logger:          logger,
		database:        database,
		memory:          memory,
		router:          router,
		serverTools:     serverTools,
		channelSettings: channelSettings,
		scheduler:       scheduler,
		commands:        commands,
		agent:           agent,
		flood:           flood,
		startTime:       time.Now(),
		ircLogHandler:   ircLogHandler,
	}
	s.allowedUsers.Store(&cfg.Security.AllowedUsers)

	// Wire the reloader to the command handler now that the server exists.
	// This breaks the circular dependency: commands needs a Reloader, but
	// the server needs commands to be created first.
	commands.reloader = s

	// Wire the late-binding reload pointer for the config_manage tool.
	reloadPtr = s

	s.registerBusHandlers()

	// Mark the main channel as auto-join so it's always rejoined on reconnect.
	if err := channelSettings.SetAutoJoin(cfg.IRC.Channels.Main, true); err != nil {
		logger.Warn("failed to mark main channel as auto-join", "error", err)
	}

	// Register an OnConnect callback that rejoins all auto-join channels
	// after a reconnect. Static channels (main + bus) are joined by the IRC
	// connection itself, so we skip them to avoid redundant JOIN commands.
	conn.OnConnect(func() {
		autoJoinChannels, err := channelSettings.GetAutoJoinChannels()
		if err != nil {
			logger.Error("failed to get auto-join channels on reconnect", "error", err)
			return
		}

		staticChannels := map[string]struct{}{
			strings.ToLower(cfg.IRC.Channels.Main): {},
			strings.ToLower(cfg.IRC.Channels.Bus):  {},
		}

		for _, ch := range autoJoinChannels {
			if _, isStatic := staticChannels[strings.ToLower(ch)]; isStatic {
				continue
			}
			if err := conn.Join(ch); err != nil {
				logger.Warn("auto-join failed", "channel", ch, "error", err)
			} else {
				logger.Info("auto-joined channel on reconnect", "channel", ch)
			}
		}
	})

	// Sync channel topics when OPER status is granted. Topics require operator
	// privileges, so we wait for RPL_YOUREOPER before attempting to set them.
	conn.OnOper(func() {
		agent.SyncAllTopics()
	})

	// Join the debug channel and activate the IRC log handler on connect.
	if ircLogHandler != nil {
		conn.OnConnect(func() {
			if err := conn.Join(cfg.Server.DebugChannel); err != nil {
				logger.Warn("failed to join debug channel", "channel", cfg.Server.DebugChannel, "error", err)
			}
			ircLogHandler.SetConnection(conn)
			logger.Info("debug channel active", "channel", cfg.Server.DebugChannel)
		})
	}

	return s, nil
}

// Run starts the server: connects to IRC, starts the registry monitor, and
// blocks until the context is cancelled or a fatal IRC error occurs. On
// shutdown, it disconnects from IRC, waits for in-flight agent goroutines
// to finish, and then closes the database.
func (s *Server) Run(ctx context.Context) error {
	s.logger.Info("starting murmur server")

	// Wire the bus receiver to the message handler.
	s.handler.RegisterBusHandler(s.receiver.HandleRaw)

	// Wire the user message handler. The closure captures ctx so that agent
	// goroutines receive the server's run context without storing it as a
	// field (which would be a data race).
	s.handler.RegisterUserHandler(func(channel, nick, message string) {
		s.handleUserMessage(ctx, channel, nick, message)
	})

	// Create a derived context so we can cancel the monitor goroutine
	// when Connect returns (either from context cancellation or fatal error).
	monitorCtx, monitorCancel := context.WithCancel(ctx)
	defer monitorCancel()

	// Start the registry heartbeat monitor.
	var monitorWg sync.WaitGroup
	monitorWg.Add(1)
	go func() {
		defer monitorWg.Done()
		s.registry.StartMonitor(monitorCtx)
	}()

	// Start the task scheduler if enabled.
	if s.scheduler != nil {
		monitorWg.Add(1)
		go func() {
			defer monitorWg.Done()
			s.scheduler.Run(monitorCtx)
		}()
	}

	// Start the REST API server if enabled.
	// Snapshot config for API startup — API config is not reloadable.
	startCfg := s.loadCfg()
	if startCfg.API.Enabled {
		mux := newServerAPIMux(s)
		s.httpServer = api.NewHTTPServer(startCfg.API.Listen, mux, s.logger)

		monitorWg.Add(1)
		go func() {
			defer monitorWg.Done()
			s.logger.Info("starting REST API server", "listen", startCfg.API.Listen)
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error("REST API server error", "error", err)
			}
		}()

		// Graceful shutdown goroutine: waits for monitorCtx cancellation.
		monitorWg.Add(1)
		go func() {
			defer monitorWg.Done()
			<-monitorCtx.Done()
			api.GracefulShutdown(context.Background(), s.httpServer, s.logger)
		}()
	}

	// Connect to IRC (blocks until context cancelled or fatal error).
	err := s.conn.Connect(ctx)

	// Cancel the monitor and wait for it to finish.
	monitorCancel()
	monitorWg.Wait()

	// Wait for all in-flight agent goroutines to finish before closing the
	// database. This prevents goroutines from hitting a closed DB.
	s.logger.Info("waiting for in-flight agent goroutines to finish")
	s.agentWg.Wait()

	// Close the IRC log handler before closing the database.
	if s.ircLogHandler != nil {
		s.ircLogHandler.Close()
	}

	// Close the database after all goroutines are done.
	if err := s.database.Close(); err != nil {
		s.logger.Error("failed to close database", "error", err)
	}

	s.logger.Info("murmur server stopped")
	return err
}

// Reload re-reads the TOML config file and applies safe changes without
// restarting. It resolves vault references, rebuilds LLM providers, and
// updates the agent, command handler, and memory with new values.
//
// Safe to reload: LLM providers, allowed_users, verbose, system_prompt,
// max_history, cross_channel_context, approval_timeout, summary_threshold,
// debug channel toggle.
//
// NOT reloadable (requires restart): IRC connection, DB path, vault config,
// API listen address, bus key, tool configs (shell, code_exec, etc.).
func (s *Server) Reload() error {
	// Serialize concurrent reload attempts.
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	s.logger.Info("reloading configuration", "path", s.configPath)

	cfg, err := config.LoadServerConfig(s.configPath)
	if err != nil {
		return fmt.Errorf("Reload: load config: %w", err)
	}

	// Resolve vault references in the new config.
	if cfg.Vault.Enabled && cfg.Vault.DBPath != "" {
		passphrase := os.Getenv(cfg.Vault.PassphraseEnv)
		if passphrase != "" {
			v, err := vault.Open(cfg.Vault.DBPath, passphrase)
			if err != nil {
				s.logger.Warn("Reload: could not open vault, vault: references will not be resolved", "error", err)
			} else {
				if err := vault.ResolveVaultRefs(v, cfg); err != nil {
					v.Close()
					return fmt.Errorf("Reload: resolve vault refs: %w", err)
				}
				v.Close()
			}
		}
	}

	// Rebuild LLM providers from the new config.
	providers := make(map[string]llm.Provider)
	for name, provCfg := range cfg.LLM.Providers {
		providers[name] = llm.NewOpenAICompatProvider(name, provCfg, s.logger)
	}

	// Validate that the default provider exists.
	if len(providers) > 0 && cfg.LLM.Default != "" {
		if _, ok := providers[cfg.LLM.Default]; !ok {
			return fmt.Errorf("Reload: default LLM provider %q not found in configured providers", cfg.LLM.Default)
		}
	}

	// Load the system prompt.
	systemPrompt, err := LoadSystemPrompt(cfg.Server.SystemPromptFile)
	if err != nil {
		return fmt.Errorf("Reload: %w", err)
	}

	// Parse the approval timeout.
	approvalTimeout, err := cfg.ParseApprovalTimeout()
	if err != nil {
		return fmt.Errorf("Reload: %w", err)
	}

	// Apply changes atomically to each component.
	s.agent.UpdateProviders(providers, cfg.LLM.Default)
	s.agent.UpdateConfig(cfg.Server.Verbose, cfg.Memory.MaxHistory, cfg.Memory.CrossChannelContext, approvalTimeout, systemPrompt)
	s.commands.UpdateAllowedUsers(cfg.Security.AllowedUsers)
	s.allowedUsers.Store(&cfg.Security.AllowedUsers)
	s.memory.UpdateConfig(cfg.Memory.MaxHistory, cfg.Memory.SummaryThreshold)

	// Toggle debug channel handler.
	if s.ircLogHandler != nil {
		oldDebugCh := s.loadCfg().Server.DebugChannel
		if cfg.Server.DebugChannel == "" {
			s.ircLogHandler.SetEnabled(false)
			s.logger.Info("debug channel disabled by reload")
		} else {
			s.ircLogHandler.SetEnabled(true)
			if oldDebugCh != cfg.Server.DebugChannel {
				s.logger.Warn("debug_channel name changed; new channel requires restart to take effect",
					"old", oldDebugCh,
					"new", cfg.Server.DebugChannel,
				)
			}
			s.logger.Info("debug channel enabled by reload", "channel", cfg.Server.DebugChannel)
		}
	}

	// Update the stored config reference under write lock.
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()

	s.logger.Info("configuration reloaded successfully")
	return nil
}

// registerBusHandlers sets up handlers for bus protocol messages.
func (s *Server) registerBusHandlers() {
	s.receiver.On(bus.TypeRegister, func(nick string, msg any) {
		if m, ok := msg.(*bus.RegisterMessage); ok {
			s.registry.Register(m)
		}
	})

	s.receiver.On(bus.TypeDeregister, func(nick string, msg any) {
		if m, ok := msg.(*bus.DeregisterMessage); ok {
			s.registry.Deregister(m.ClientID)
		}
	})

	s.receiver.On(bus.TypeHeartbeat, func(nick string, msg any) {
		if m, ok := msg.(*bus.HeartbeatMessage); ok {
			s.registry.Heartbeat(m)
		}
	})

	// TODO: Validate that the tool response sender matches the client that
	// was assigned the request. Currently any bus participant can spoof a
	// response by sending a matching request_id. This is a known limitation
	// of the bus protocol — address in Phase 3 with sender binding. (Phase 3)
	s.receiver.On(bus.TypeToolResponse, func(nick string, msg any) {
		if m, ok := msg.(*bus.ToolResponseMessage); ok {
			s.router.HandleToolResponse(nick, m)
		}
	})

	// Handle event messages forwarded from clients via the bus. The event is
	// stored in the database for audit/replay, then injected into the agent
	// loop. Uses context.Background() because bus handlers don't receive a
	// context and the agent goroutine must outlive the callback.
	s.receiver.On(bus.TypeEvent, func(nick string, msg any) {
		m, ok := msg.(*bus.EventMessage)
		if !ok {
			return
		}

		channel := s.loadCfg().IRC.Channels.Main

		// Store the event in the database.
		event := &db.Event{
			EventID:   m.EventID,
			Source:    m.Source,
			EventType: m.EventType,
			Summary:   m.Summary,
			Data:      m.Data,
			Channel:   channel,
		}

		id, inserted, err := s.database.InsertEvent(context.Background(), event)
		if err != nil {
			s.logger.Error("bus: failed to insert event", "error", err, "client_id", m.ClientID)
			return
		}
		if !inserted {
			s.logger.Info("bus: duplicate event ignored", "event_id", m.EventID, "existing_id", id)
			return
		}

		// Trigger agent processing in a tracked goroutine.
		s.agentWg.Add(1)
		go func() {
			defer s.agentWg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					s.logger.Error("bus: agent event goroutine panicked", "recover", rec)
				}
			}()
			if err := s.agent.HandleEvent(context.Background(), channel, m.Source, m.EventType, m.Summary, m.Data); err != nil {
				s.logger.Error("bus: agent HandleEvent failed", "error", err, "event_id", m.EventID)
				return
			}
			if err := s.database.MarkEventProcessed(context.Background(), id); err != nil {
				s.logger.Error("bus: failed to mark event processed", "error", err, "id", id)
			}
		}()
	})

	// Handle cron result notifications from clients. Output is truncated
	// to maxCronOutputDisplay bytes for IRC readability — the full output
	// was already truncated to tools.MaxOutputBytes on the client side.
	// TODO: Verify that nick matches the registered client for m.ClientID
	// to prevent spoofed cron results. Same sender-binding issue as
	// tool_response above — address in a future phase.
	s.receiver.On(bus.TypeCronResult, func(nick string, msg any) {
		if m, ok := msg.(*bus.CronResultMessage); ok {
			const maxCronOutputDisplay = 300
			output := m.Output
			if len(output) > maxCronOutputDisplay {
				output = output[:maxCronOutputDisplay] + "..."
			}
			s.conn.Send(s.loadCfg().IRC.Channels.Main, fmt.Sprintf("[cron:%s@%s] %s: %s", m.JobName, m.ClientID, m.Status, output))
		}
	})
}

// handleUserMessage processes messages from user-facing IRC channels.
// It checks authorization, dispatches ! commands, and spawns agent loops
// for regular messages. The ctx parameter is the server's run context,
// passed via closure from Run() to avoid storing it as a struct field.
//
// TODO: Enforce SecurityConfig.RequireNickServ — verify NickServ identification
// before processing messages from allowed users. (Phase 3)
func (s *Server) handleUserMessage(ctx context.Context, channel, nick, message string) {
	s.logger.Debug("user message received",
		"channel", channel,
		"nick", nick,
		"message", message,
	)

	// Check authorization: if AllowedUsers is configured, only listed nicks
	// can interact. Unauthorized users are silently ignored for agent messages
	// (commands handle their own authorization with a rejection message).
	//
	// TODO: Deduplicate allowlist check with commands.go — both use
	// case-insensitive EqualFold loops. Consider extracting to a shared
	// helper. (Low priority)
	if users := s.loadAllowedUsers(); len(users) > 0 && !s.isAllowed(nick) {
		// Still let commands handle authorization (they send rejection messages).
		if strings.HasPrefix(message, "!") {
			s.commands.HandleCommand(channel, nick, message)
		}
		return
	}

	// Try command handler first. Commands bypass flood protection so that
	// !flush and !forget always work even during a flood.
	if s.commands.HandleCommand(channel, nick, message) {
		return
	}

	// No LLM providers configured — can't run the agent loop.
	if len(s.loadCfg().LLM.Providers) == 0 {
		s.conn.Send(channel, "no LLM configured")
		return
	}

	// Per-nick rate limiting: drop messages from nicks that exceed the
	// flood threshold. This prevents a single user from queuing dozens
	// of LLM calls.
	if !s.flood.allow(nick) {
		s.logger.Debug("message dropped by flood guard",
			"channel", channel,
			"nick", nick,
		)
		return
	}

	// Enqueue the message into the channel's bounded queue. A single
	// worker goroutine per channel processes messages sequentially.
	// If the queue is full, the message is dropped (flood protection).
	msg := pendingMsg{channel: channel, nick: nick, message: message}
	s.flood.enqueue(msg, func(m pendingMsg) {
		s.agentWg.Add(1)
		defer s.agentWg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("agent goroutine panicked",
					"recover", r,
					"channel", m.channel,
					"nick", m.nick,
				)
			}
		}()
		s.agent.HandleMessage(ctx, m.channel, m.nick, m.message)
	})
}

// loadAllowedUsers returns the current allowed users list from the atomic pointer.
// Returns nil if no users have been stored (zero-value atomic pointer).
func (s *Server) loadAllowedUsers() []string {
	p := s.allowedUsers.Load()
	if p == nil {
		return nil
	}
	return *p
}

// isAllowed checks if a nick is in the allowed users list (case-insensitive).
func (s *Server) isAllowed(nick string) bool {
	for _, u := range s.loadAllowedUsers() {
		if strings.EqualFold(u, nick) {
			return true
		}
	}
	return false
}

// loadCfg returns the current server config under read lock. This is used by
// runtime paths that read s.cfg concurrently with Reload() writes.
func (s *Server) loadCfg() *config.ServerConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// Registry returns the server's client registry for external access.
func (s *Server) Registry() *Registry {
	return s.registry
}
