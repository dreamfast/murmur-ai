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
	"murmur/internal/dashboard"
	"murmur/internal/db"
	"murmur/internal/irc"
	"murmur/internal/llm"
	"murmur/internal/rag"
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
	permissions     *PermissionManager
	nickserv        atomic.Pointer[NickServVerifier] // NickServ identity verification; nil if disabled
	permCleanupStop context.CancelFunc               // stops the PM cleanup goroutine; nil if PM not active
	monitorCtx      context.Context                  // lifecycle context for background goroutines; set in Run()

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

	// debouncer collects consecutive IRC messages from the same nick+channel
	// and concatenates them into a single message after a quiet period. This
	// allows multi-line pastes to be processed as one LLM call.
	debouncer *messageDebouncer

	// startTime records when the server was created, used for uptime reporting.
	startTime time.Time

	// httpServer is the REST API HTTP server, nil when API is disabled.
	httpServer *http.Server

	// dashboardServer is the dashboard HTTP server, nil when dashboard is disabled.
	dashboardServer *http.Server

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
	if cfg.Debug.Channel != "" {
		debugLevel := cfg.Debug.ParseDebugLevel()
		ircLogHandler = irc.NewIRCLogHandler(cfg.Debug.Channel, debugLevel)
		if !cfg.Debug.Enabled {
			ircLogHandler.SetEnabled(false)
		}
		multiHandler := irc.NewMultiHandler(logger.Handler(), ircLogHandler)
		logger = slog.New(multiHandler)
		logger.Info("debug channel configured",
			"channel", cfg.Debug.Channel,
			"level", debugLevel.String(),
			"enabled", cfg.Debug.Enabled,
		)
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

	// Wire RAG memory search if enabled.
	if cfg.Memory.RAG.Enabled {
		// Optionally create an embedding provider for semantic search.
		var embedder rag.EmbeddingProvider
		if embCfg := cfg.Memory.RAG.Embeddings; embCfg != nil && embCfg.APIBase != "" {
			embedder = rag.NewOpenAIEmbeddingProvider(rag.OpenAIEmbeddingConfig{
				APIBase: embCfg.APIBase,
				APIKey:  embCfg.APIKey,
				Model:   embCfg.Model,
				Dims:    embCfg.Dimensions,
			})
			logger.Info("RAG embedding provider configured",
				"api_base", embCfg.APIBase,
				"model", embCfg.Model,
			)
		}

		ragStore := rag.NewRAGStore(database, embedder, logger, rag.RAGStoreConfig{})
		if err := RegisterRAGTools(serverTools, ragStore); err != nil {
			database.Close()
			return nil, fmt.Errorf("server.New: register RAG tools: %w", err)
		}

		// Auto-ingest summaries into RAG when summarization completes.
		if cfg.Memory.RAG.GetAutoIngestSummaries() {
			memory.OnSummary = func(channel, summary string) {
				if err := ragStore.Ingest("summary:"+channel, summary); err != nil {
					logger.Error("RAG auto-ingest summary failed",
						"channel", channel,
						"error", err,
					)
				}
			}
		}

		// Index startup files synchronously. This is a one-time cost during
		// server initialization (local file reads + SQLite inserts). Running
		// synchronously avoids lifecycle issues with untracked goroutines
		// racing the database close on shutdown.
		for _, path := range cfg.Memory.RAG.Files {
			if err := ragStore.IngestFile(path); err != nil {
				logger.Warn("RAG startup file ingest failed",
					"path", path,
					"error", err,
				)
			} else {
				logger.Info("RAG indexed startup file", "path", path)
			}
		}

		logger.Info("RAG memory search enabled",
			"embeddings", embedder != nil,
			"auto_ingest_summaries", cfg.Memory.RAG.GetAutoIngestSummaries(),
			"startup_files", len(cfg.Memory.RAG.Files),
		)
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

	// Auto-import permissions from TOML if this is the first run with the DB.
	// Check: metadata marker "permissions_imported" doesn't exist AND
	// permissions.toml file exists AND users table is empty.
	userCount, err := database.UserCount()
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: count users: %w", err)
	}
	importedMarker, _ := database.GetMetadata("permissions_imported")
	if importedMarker == "" && userCount == 0 && cfg.Security.PermissionsFile != "" {
		tomlCfg, err := config.LoadPermissionsConfig(cfg.Security.PermissionsFile)
		if err != nil {
			logger.Warn("failed to load permissions.toml for import", "error", err)
		} else if len(tomlCfg.Users) > 0 || len(tomlCfg.Channels) > 0 {
			usersN, channelsN, err := config.ImportPermissionsToDB(database, tomlCfg)
			if err != nil {
				database.Close()
				return nil, fmt.Errorf("server.New: import permissions from TOML: %w", err)
			}
			if err := database.SetMetadata("permissions_imported", "true"); err != nil {
				database.Close()
				return nil, fmt.Errorf("server.New: set import marker: %w", err)
			}
			logger.Info("imported permissions from TOML to SQLite",
				"users", usersN,
				"channels", channelsN,
				"file", cfg.Security.PermissionsFile,
			)
		}
	} else if importedMarker != "" && cfg.Security.PermissionsFile != "" {
		// Already imported — warn if the TOML file still exists.
		if _, statErr := os.Stat(cfg.Security.PermissionsFile); statErr == nil {
			logger.Warn("permissions.toml exists but has already been imported to SQLite; the file is no longer used",
				"file", cfg.Security.PermissionsFile,
			)
		}
	}

	// Load permissions from DB and create the permission manager.
	permCfg, err := config.LoadPermissionsFromDB(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: load permissions from DB: %w", err)
	}
	var pm *PermissionManager
	if len(permCfg.Users) > 0 || len(permCfg.Channels) > 0 {
		pm = NewPermissionManager(permCfg, logger)
		pm.SetLogPermissions(cfg.Debug.LogPermissions)
		logger.Info("permissions loaded from DB",
			"users", len(permCfg.Users),
			"channels", len(permCfg.Channels),
		)
	}

	// Create the agent (may have zero providers — commands still work).
	agent := NewAgent(AgentParams{
		Providers:           providers,
		ProviderFallbacks:   buildProviderFallbacks(cfg),
		DefaultProvider:     cfg.LLM.Default,
		ServerTools:         serverTools,
		Registry:            registry,
		Memory:              memory,
		Router:              router,
		Approvals:           approvals,
		Conn:                conn,
		SystemPrompt:        systemPrompt,
		ServerName:          cfg.Server.Name,
		BusChannel:          cfg.IRC.Channels.Bus,
		MaxHistory:          cfg.Memory.MaxHistory,
		CrossChannelContext: cfg.Memory.CrossChannelContext,
		ChannelSettings:     channelSettings,
		ToolTimeout:         2 * time.Minute,
		ApprovalTimeout:     approvalTimeout,
		Verbose:             cfg.Server.Verbose,
		Debug:               cfg.Debug,
		Logger:              logger,
	})

	// Wire the permission manager into the agent.
	agent.SetPermissions(pm)

	// Create NickServ verifier if permissions require identity verification.
	// Default: enabled when users exist in the DB. Re-query the count because
	// the TOML import above may have added users since the initial check.
	var nickserv *NickServVerifier
	currentUserCount, err := database.UserCount()
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: recount users: %w", err)
	}
	requireNickServ := cfg.Security.RequireNickServ || currentUserCount > 0
	if requireNickServ {
		cacheTTL := defaultNickServCacheTTL
		if cfg.Security.NickServCacheTTL != "" {
			ttl, err := time.ParseDuration(cfg.Security.NickServCacheTTL)
			if err != nil {
				database.Close()
				return nil, fmt.Errorf("server.New: parse nickserv_cache_ttl: %w", err)
			}
			if ttl < 0 {
				database.Close()
				return nil, fmt.Errorf("server.New: nickserv_cache_ttl must be non-negative")
			}
			cacheTTL = ttl
		}
		whoisFn := func(nick string) (string, error) {
			result, err := conn.Whois(nick)
			if err != nil {
				return "", err
			}
			return result.Account, nil
		}
		nickserv = NewNickServVerifier(whoisFn, cacheTTL, logger)
		logger.Info("NickServ verification enabled", "cache_ttl", cacheTTL)
	}

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

	// Parse the debounce window for multi-line paste support.
	debounceWindow, err := cfg.ParseDebounceWindow()
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("server.New: %w", err)
	}

	var debugToggler DebugToggler
	if ircLogHandler != nil {
		debugToggler = ircLogHandler
	}
	commands := NewCommandHandler(registry, serverTools, memory, notesStore, scheduler, approvals, conn, modelSwitcher, flood, debugToggler, nil, cfg.Security.AllowedUsers, time.Now(), logger)

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
		permissions:     pm,
		flood:           flood,
		startTime:       time.Now(),
		ircLogHandler:   ircLogHandler,
	}
	s.allowedUsers.Store(&cfg.Security.AllowedUsers)
	if nickserv != nil {
		s.nickserv.Store(nickserv)
	}

	// Create the message debouncer for multi-line paste support. The flush
	// callback is set later in Run() when the run context is available.
	// We pass a no-op here; Run() replaces it before any messages arrive.
	s.debouncer = newMessageDebouncer(debounceWindow, func(_, _, _ string) {}, logger)
	if debounceWindow > 0 {
		logger.Info("message debouncing enabled", "window", debounceWindow)
	}

	// Wire the reloader to the command handler now that the server exists.
	// This breaks the circular dependency: commands needs a Reloader, but
	// the server needs commands to be created first.
	commands.reloader = s

	// Wire permissions into the command handler for admin commands (!user, !channel).
	// Also register the permissions_manage LLM tool with the DB-backed store.
	if pm != nil {
		commands.permissions.Store(pm)
		ps := NewPermissionsStore(database, pm, logger)
		commands.permStore.Store(ps)
		if err := RegisterPermissionsTool(serverTools, ps, pm, logger); err != nil {
			database.Close()
			return nil, fmt.Errorf("server.New: %w", err)
		}
	}

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

	// Invalidate NickServ cache when users quit or change nicks. A QUIT
	// means the user's session ended — their identity must be re-verified
	// on reconnect. A NICK change invalidates both old and new nicks.
	conn.OnQuit(func(nick string) {
		if nv := s.nickserv.Load(); nv != nil {
			nv.InvalidateCache(nick)
			logger.Debug("invalidated NickServ cache on QUIT", "nick", nick)
		}
	})
	conn.OnNick(func(oldNick, newNick string) {
		if nv := s.nickserv.Load(); nv != nil {
			nv.InvalidateCache(oldNick)
			nv.InvalidateCache(newNick)
			logger.Debug("invalidated NickServ cache on NICK change",
				"old_nick", oldNick, "new_nick", newNick)
		}
	})

	// Join the debug channel and activate the IRC log handler on connect.
	if ircLogHandler != nil {
		conn.OnConnect(func() {
			if err := conn.Join(cfg.Debug.Channel); err != nil {
				logger.Warn("failed to join debug channel", "channel", cfg.Debug.Channel, "error", err)
			}
			ircLogHandler.SetConnection(conn)
			logger.Info("debug channel active", "channel", cfg.Debug.Channel)
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

	// Propagate the lifecycle context to Memory so summarization timeout
	// contexts are cancelled on shutdown.
	s.memory.SetLifecycleContext(ctx)

	// Wire the bus receiver to the message handler.
	s.handler.RegisterBusHandler(s.receiver.HandleRaw)

	// Wire the debouncer's flush callback now that we have the run context.
	// The flush callback routes debounced messages through flood protection
	// and into the agent loop. This must be set before RegisterUserHandler
	// so that debounced messages have a valid context.
	s.debouncer.SetFlush(func(channel, nick, message string) {
		s.processDebounced(ctx, channel, nick, message)
	})

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
	s.monitorCtx = monitorCtx

	// Start the registry heartbeat monitor.
	var monitorWg sync.WaitGroup
	monitorWg.Add(1)
	go func() {
		defer monitorWg.Done()
		s.registry.StartMonitor(monitorCtx)
	}()

	// Start the permission manager cleanup goroutine.
	if s.permissions != nil {
		cleanupCtx, cleanupCancel := context.WithCancel(monitorCtx)
		s.permCleanupStop = cleanupCancel
		s.permissions.StartCleanup(cleanupCtx)
	}

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

	// Start the dashboard server if enabled.
	if startCfg.Dashboard.Enabled {
		sessionTimeout, parseErr := time.ParseDuration(startCfg.Dashboard.SessionTimeout)
		if parseErr != nil {
			s.logger.Warn("invalid dashboard.session_timeout, using 24h", "error", parseErr)
			sessionTimeout = 24 * time.Hour
		}

		sessions := dashboard.NewSessionStore(sessionTimeout, s.logger)
		adminDeps := s.buildAdminDeps()
		dashHandler := dashboard.NewHandler(sessions, startCfg.Dashboard, startCfg.IRC, s.statusProvider(), adminDeps, s.logger)
		s.dashboardServer = api.NewHTTPServer(startCfg.Dashboard.Listen, dashHandler, s.logger)

		done := make(chan struct{})
		sessions.StartCleanup(done)

		monitorWg.Add(1)
		go func() {
			defer monitorWg.Done()
			s.logger.Info("starting dashboard server", "listen", startCfg.Dashboard.Listen)
			if err := s.dashboardServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error("dashboard server error", "error", err)
			}
		}()

		monitorWg.Add(1)
		go func() {
			defer monitorWg.Done()
			<-monitorCtx.Done()
			close(done) // stop session cleanup
			api.GracefulShutdown(context.Background(), s.dashboardServer, s.logger)
		}()
	}

	// Connect to IRC (blocks until context cancelled or fatal error).
	err := s.conn.Connect(ctx)

	// Cancel the monitor and wait for it to finish.
	monitorCancel()
	monitorWg.Wait()

	// Close the debouncer first to flush any pending multi-line batches.
	// This ensures buffered messages are delivered before we shut down
	// the flood guard.
	s.debouncer.Close()

	// Close the flood guard to stop per-channel worker goroutines from
	// picking up new messages. This also prevents enqueue from sending on
	// closed channels. Workers that are mid-handler will finish naturally.
	s.flood.Close()

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
// buildProviderFallbacks extracts the fallback chains from the config into
// a map keyed by provider name. Only providers with non-empty Fallbacks are
// included.
func buildProviderFallbacks(cfg *config.ServerConfig) map[string][]string {
	fallbacks := make(map[string][]string)
	for name, provCfg := range cfg.LLM.Providers {
		if len(provCfg.Fallbacks) > 0 {
			fallbacks[name] = provCfg.Fallbacks
		}
	}
	return fallbacks
}

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

	// Reload permissions from DB.
	permCfg, err := config.LoadPermissionsFromDB(s.database)
	if err != nil {
		return fmt.Errorf("Reload: load permissions from DB: %w", err)
	}
	if s.permissions != nil {
		s.permissions.Update(permCfg)
		s.permissions.SetLogPermissions(cfg.Debug.LogPermissions)
		s.logger.Info("permissions reloaded from DB",
			"users", len(permCfg.Users),
			"channels", len(permCfg.Channels),
		)
	} else if len(permCfg.Users) > 0 || len(permCfg.Channels) > 0 {
		// Permissions were added after initial startup — create a new manager
		// and start its cleanup goroutine.
		pm := NewPermissionManager(permCfg, s.logger)
		pm.SetLogPermissions(cfg.Debug.LogPermissions)
		s.agent.SetPermissions(pm)
		s.permissions = pm
		// Wire into command handler for admin commands.
		s.commands.permissions.Store(pm)
		if s.commands.permStore.Load() == nil {
			ps := NewPermissionsStore(s.database, pm, s.logger)
			s.commands.permStore.Store(ps)
			// Register the permissions_manage LLM tool if not already present.
			if !s.serverTools.HasTool("permissions_manage") {
				if err := RegisterPermissionsTool(s.serverTools, ps, pm, s.logger); err != nil {
					s.logger.Error("Reload: failed to register permissions_manage tool", "error", err)
				}
			}
		}
		// Derive cleanup context from the server's lifecycle context. If Reload()
		// is called before Run() (monitorCtx not yet set), fall back to
		// context.Background() — the goroutine will be cleaned up when Run()
		// eventually sets up proper lifecycle management.
		parentCtx := s.monitorCtx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		cleanupCtx, cleanupCancel := context.WithCancel(parentCtx)
		s.permCleanupStop = cleanupCancel
		pm.StartCleanup(cleanupCtx)
		s.logger.Info("permissions enabled via reload",
			"users", len(permCfg.Users),
			"channels", len(permCfg.Channels),
		)
	}

	// Update NickServ verifier: recreate on enable or TTL change, disable if
	// no longer required. Always recreating when enabled is cheap (just a
	// struct with empty maps) and ensures TTL changes take effect.
	reloadUserCount, _ := s.database.UserCount()
	requireNickServ := cfg.Security.RequireNickServ || reloadUserCount > 0
	if requireNickServ {
		cacheTTL := defaultNickServCacheTTL
		if cfg.Security.NickServCacheTTL != "" {
			ttl, err := time.ParseDuration(cfg.Security.NickServCacheTTL)
			if err != nil {
				return fmt.Errorf("Reload: parse nickserv_cache_ttl: %w", err)
			}
			if ttl < 0 {
				return fmt.Errorf("Reload: nickserv_cache_ttl must be non-negative")
			}
			cacheTTL = ttl
		}
		whoisFn := func(nick string) (string, error) {
			result, err := s.conn.Whois(nick)
			if err != nil {
				return "", err
			}
			return result.Account, nil
		}
		s.nickserv.Store(NewNickServVerifier(whoisFn, cacheTTL, s.logger))
		s.logger.Info("NickServ verification reloaded", "cache_ttl", cacheTTL)
	} else if !requireNickServ && s.nickserv.Load() != nil {
		s.nickserv.Store(nil)
		s.logger.Info("NickServ verification disabled via reload")
	}

	// Apply changes atomically to each component.
	s.agent.UpdateProviders(providers, cfg.LLM.Default, buildProviderFallbacks(cfg))
	s.agent.UpdateConfig(cfg.Server.Verbose, cfg.Memory.MaxHistory, cfg.Memory.CrossChannelContext, approvalTimeout, systemPrompt, cfg.Debug)
	s.commands.UpdateAllowedUsers(cfg.Security.AllowedUsers)
	s.allowedUsers.Store(&cfg.Security.AllowedUsers)
	s.memory.UpdateConfig(cfg.Memory.MaxHistory, cfg.Memory.SummaryThreshold)

	// Toggle debug channel handler and update level.
	if s.ircLogHandler != nil {
		oldDebugCh := s.loadCfg().Debug.Channel
		if cfg.Debug.Channel == "" || !cfg.Debug.Enabled {
			s.ircLogHandler.SetEnabled(false)
			s.logger.Info("debug channel disabled by reload")
		} else {
			s.ircLogHandler.SetEnabled(true)
			s.ircLogHandler.SetLevel(cfg.Debug.ParseDebugLevel())
			if oldDebugCh != cfg.Debug.Channel {
				s.logger.Warn("debug channel name changed; new channel requires restart to take effect",
					"old", oldDebugCh,
					"new", cfg.Debug.Channel,
				)
			}
			s.logger.Info("debug channel updated by reload",
				"channel", cfg.Debug.Channel,
				"enabled", cfg.Debug.Enabled,
				"level", cfg.Debug.LogLevel,
				"log_tool_calls", cfg.Debug.LogToolCalls,
				"log_llm_requests", cfg.Debug.LogLLMRequests,
				"log_bus_protocol", cfg.Debug.LogBusProtocol,
				"log_permissions", cfg.Debug.LogPermissions,
			)
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
			if err := s.agent.HandleEvent(context.Background(), channel, "_system", m.Source, m.EventType, m.Summary, m.Data); err != nil {
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
func (s *Server) handleUserMessage(ctx context.Context, channel, nick, message string) {
	s.logger.Debug("user message received",
		"channel", channel,
		"nick", nick,
		"message", message,
	)

	// Check authorization: if AllowedUsers is configured, only listed nicks
	// can interact. Unauthorized users are silently ignored for agent messages
	// (commands handle their own authorization with a rejection message).
	if users := s.loadAllowedUsers(); len(users) > 0 && !IsNickAllowed(nick, users) {
		// Still let commands handle authorization (they send rejection messages).
		if strings.HasPrefix(message, "!") {
			s.commands.HandleCommand(channel, nick, message)
		}
		return
	}

	// Try command handler first. Commands bypass flood protection and
	// NickServ verification so that !help, !status, etc. always work.
	if s.commands.HandleCommand(channel, nick, message) {
		return
	}

	// NickServ identity verification: if enabled, require users to be
	// identified before their messages reach the agent. Commands (above)
	// still work without identification.
	if nv := s.nickserv.Load(); nv != nil && !nv.IsIdentified(nick) {
		s.conn.Send(channel, nick+": you must be identified with NickServ to use this bot")
		return
	}

	// No LLM providers configured — can't run the agent loop.
	if len(s.loadCfg().LLM.Providers) == 0 {
		s.conn.Send(channel, "no LLM configured")
		return
	}

	// Route through the debouncer. Multi-line pastes are collected and
	// concatenated before being processed. The debouncer's flush callback
	// (set in Run) handles flood protection and agent dispatch.
	s.debouncer.Add(channel, nick, message)
}

// processDebounced is the debouncer's flush callback. It receives the
// concatenated message after the quiet period expires (or immediately if
// debouncing is disabled). It applies flood protection, permissions rate
// limiting, and enqueues the message for the agent loop.
func (s *Server) processDebounced(ctx context.Context, channel, nick, message string) {
	// Per-nick rate limiting: drop messages from nicks that exceed the
	// flood threshold. This prevents a single user from queuing dozens
	// of LLM calls. Counts debounced messages, not raw IRC lines.
	if !s.flood.allow(nick) {
		s.logger.Debug("debounced message dropped by flood guard",
			"channel", channel,
			"nick", nick,
		)
		return
	}

	// Per-user rate limiting from permissions config. This is a longer-term
	// hourly rate limit (vs the flood guard's short burst protection).
	// Admins with max_messages_per_hour = -1 bypass this check.
	if pm := s.permissions; pm != nil && !pm.CheckRateLimit(nick) {
		s.logger.Info("debounced message dropped by permissions rate limit",
			"channel", channel,
			"nick", nick,
		)
		s.conn.Send(channel, nick+": rate limit exceeded, please try again later")
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

// statusProvider returns a dashboard.StatusProvider backed by this server's
// live state. The returned adapter captures a pointer to the server and reads
// current values on each call, so it always reflects the latest state.
func (s *Server) statusProvider() dashboard.StatusProvider {
	return &serverStatusAdapter{s: s}
}

// serverStatusAdapter implements dashboard.StatusProvider by reading live
// state from a *Server. It is a thin wrapper to avoid a circular import
// between internal/dashboard and internal/server.
type serverStatusAdapter struct {
	s *Server
}

// GetStatus returns a snapshot of the server's current status.
func (a *serverStatusAdapter) GetStatus() dashboard.StatusInfo {
	uptime := time.Since(a.s.startTime)
	clients := a.s.registry.GetOnlineClients()
	toolCount := len(a.s.registry.AllTools()) + len(a.s.serverTools.AllToolDefs())

	details := make([]dashboard.ClientDetail, 0, len(clients))
	for _, c := range clients {
		toolNames := make([]string, 0, len(c.Tools))
		for _, t := range c.Tools {
			toolNames = append(toolNames, t.Name)
		}
		details = append(details, dashboard.ClientDetail{
			ClientID: c.ClientID,
			Hostname: c.Hostname,
			Autonomy: c.Autonomy,
			Tools:    toolNames,
		})
	}

	return dashboard.StatusInfo{
		ServerName:    a.s.loadCfg().Server.Name,
		Provider:      a.s.agent.GetProvider(),
		Clients:       len(clients),
		Tools:         toolCount,
		Uptime:        uptime,
		UptimeHuman:   uptime.Truncate(time.Second).String(),
		ClientDetails: details,
	}
}
