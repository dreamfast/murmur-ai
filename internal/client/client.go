// Package client implements the Murmur client — a tool provider that connects
// to IRC, registers its capabilities with the server, and executes tool
// requests.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"murmur/internal/api"
	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/irc"
	"murmur/internal/tools"
	"murmur/internal/vault"
)

// maxConcurrentTools is the maximum number of tool executions that can run
// concurrently on a single client. Excess requests are rejected immediately.
const maxConcurrentTools = 5

// defaultToolTimeout is the maximum time a tool handler may run before its
// context is cancelled.
const defaultToolTimeout = 2 * time.Minute

// Client is a Murmur tool provider that connects to the IRC bus, registers
// tools with the server, and handles tool execution requests.
type Client struct {
	cfg          *config.ClientConfig
	conn         *irc.Connection
	handler      *irc.MessageHandler
	sender       *bus.Sender
	receiver     *bus.Receiver
	tools        []bus.ToolDef
	toolHandlers map[string]tools.Tool
	toolSem      chan struct{} // concurrency semaphore
	cronRunner   *CronRunner   // client-side cron job runner, always initialized
	startTime    time.Time
	logger       *slog.Logger

	// httpServer is the REST API HTTP server, nil when API is disabled.
	httpServer *http.Server

	// isConnectedFunc overrides the IRC connectivity check for testing.
	// When nil, the default check (c.conn != nil && c.conn.IsConnected()) is used.
	isConnectedFunc func() bool

	// sendResponseFunc overrides the default response sending for testing.
	// When nil, responses are sent via sender.SendToolResponse.
	sendResponseFunc func(requestID, status, result string) error
}

// New creates a new client from the given configuration. It does not connect
// to IRC — call Run to start the client.
func New(cfg *config.ClientConfig, logger *slog.Logger) (*Client, error) {
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
				if err := vault.ResolveClientVaultRefs(v, cfg); err != nil {
					v.Close()
					return nil, fmt.Errorf("client.New: resolve vault refs: %w", err)
				}
				v.Close()
				logger.Info("vault references resolved")
			}
		}
	}

	channels := []string{cfg.IRC.BusChannel}

	conn, err := irc.NewConnection(cfg.IRC, channels, logger)
	if err != nil {
		return nil, fmt.Errorf("client.New: %w", err)
	}

	handler := irc.NewMessageHandler(conn, cfg.IRC.BusChannel)
	sender := bus.NewSender(conn, cfg.IRC.BusChannel, cfg.Security.BusKey, cfg.IRC.MaxLineLen, logger)
	receiver := bus.NewReceiver(cfg.Security.BusKey, logger)

	// Build tools from configuration. Vault references are already resolved
	// in the config, so no resolver is needed.
	builtTools, err := tools.BuildTools(tools.BuildToolsOpts{
		Config: &cfg.Tools,
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("client.New: build tools: %w", err)
	}

	// Build handler map for O(1) dispatch.
	toolHandlers := make(map[string]tools.Tool, len(builtTools))
	for _, t := range builtTools {
		toolHandlers[t.Name] = t
	}

	// Convert to bus tool definitions for registration.
	toolDefs := tools.ToBusToolDefs(builtTools)

	// Warn if execution-capable tools are enabled without bus authentication.
	if cfg.Tools.HasExecutionTools() && cfg.Security.BusKey == "" {
		logger.Warn("execution-capable tools (shell/code_exec) are enabled without bus_key authentication — any IRC channel participant could trigger tool execution")
	}

	// Create the cron runner. Even with no initial cron jobs, the runner is
	// created so that jobs can be added at runtime via bus CronAdd messages.
	cronRunner, err := NewCronRunner(cfg.Cron, toolHandlers, sender, cfg.Client.ID, logger)
	if err != nil {
		return nil, fmt.Errorf("client.New: cron runner: %w", err)
	}

	c := &Client{
		cfg:          cfg,
		conn:         conn,
		handler:      handler,
		sender:       sender,
		receiver:     receiver,
		tools:        toolDefs,
		toolHandlers: toolHandlers,
		toolSem:      make(chan struct{}, maxConcurrentTools),
		cronRunner:   cronRunner,
		logger:       logger,
	}

	c.registerBusHandlers()

	return c, nil
}

// Run starts the client: connects to IRC, registers with the server, starts
// the heartbeat, and blocks until the context is cancelled. On shutdown, it
// deregisters and disconnects gracefully.
func (c *Client) Run(ctx context.Context) error {
	c.startTime = time.Now()
	c.logger.Info("starting murmur client",
		"client_id", c.cfg.Client.ID,
		"tools", len(c.tools),
	)

	// Wire the bus receiver to the message handler.
	c.handler.RegisterBusHandler(c.receiver.HandleRaw)

	// Register with the server on each connect (including reconnects).
	c.conn.OnConnect(func() {
		c.register()
	})

	// Parse heartbeat interval.
	interval, err := c.cfg.ParseHeartbeatInterval()
	if err != nil {
		return fmt.Errorf("client.Run: %w", err)
	}

	// Start heartbeat goroutine.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.startHeartbeat(subCtx, interval)
	}()

	// Start cron runner goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.cronRunner.Run(subCtx)
	}()

	// Start the REST API server if enabled.
	if c.cfg.API.Enabled {
		mux := newClientAPIMux(c)
		c.httpServer = api.NewHTTPServer(c.cfg.API.Listen, mux, c.logger)

		wg.Add(1)
		go func() {
			defer wg.Done()
			c.logger.Info("starting REST API server", "listen", c.cfg.API.Listen)
			if err := c.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				c.logger.Error("REST API server error", "error", err)
			}
		}()

		// Graceful shutdown goroutine: waits for subCtx cancellation.
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-subCtx.Done()
			api.GracefulShutdown(context.Background(), c.httpServer, c.logger)
		}()
	}

	// Connect to IRC (blocks until context cancelled or fatal error).
	connectErr := c.conn.Connect(ctx)

	// Send deregistration before shutting down.
	c.deregister()

	// Cancel sub-goroutines and wait for them to finish.
	subCancel()
	wg.Wait()

	c.logger.Info("murmur client stopped", "client_id", c.cfg.Client.ID)
	return connectErr
}

// registerBusHandlers sets up handlers for bus protocol messages directed
// at this client.
func (c *Client) registerBusHandlers() {
	c.receiver.On(bus.TypeToolRequest, func(nick string, msg any) {
		m, ok := msg.(*bus.ToolRequestMessage)
		if !ok {
			c.logger.Error("unexpected message type for tool_request", "type", fmt.Sprintf("%T", msg))
			return
		}
		c.handleToolRequest(nick, m)
	})

	// Cron bus handlers — only process messages addressed to this client.
	c.receiver.On(bus.TypeCronAdd, func(nick string, msg any) {
		m, ok := msg.(*bus.CronAddMessage)
		if !ok {
			return
		}
		if m.ClientID != c.cfg.Client.ID {
			return
		}
		if err := c.cronRunner.AddJob(m.Job); err != nil {
			c.logger.Error("failed to add cron job via bus", "error", err)
		}
	})

	c.receiver.On(bus.TypeCronRemove, func(nick string, msg any) {
		m, ok := msg.(*bus.CronRemoveMessage)
		if !ok {
			return
		}
		if m.ClientID != c.cfg.Client.ID {
			return
		}
		if err := c.cronRunner.RemoveJob(m.JobName); err != nil {
			c.logger.Error("failed to remove cron job via bus", "error", err)
		}
	})

	c.receiver.On(bus.TypeCronList, func(nick string, msg any) {
		m, ok := msg.(*bus.CronListMessage)
		if !ok {
			return
		}
		if m.ClientID != c.cfg.Client.ID {
			return
		}
		resp := &bus.CronListResponseMessage{
			Type:     bus.TypeCronListResponse,
			ClientID: c.cfg.Client.ID,
			Jobs:     c.cronRunner.ListJobs(),
		}
		if err := c.sender.Send(resp); err != nil {
			c.logger.Error("failed to send cron list response", "error", err)
		}
	})
}

// handleToolRequest processes a tool execution request from the server.
// It looks up the requested tool in the client's handler map. If the tool
// is not owned by this client, the request is silently ignored (the bus
// broadcasts to all clients — only the owner should respond). If the tool
// is found, it is executed with a timeout context and the result is sent
// back via the bus.
func (c *Client) handleToolRequest(nick string, msg *bus.ToolRequestMessage) {
	// Look up the tool — silently ignore if not ours.
	tool, ok := c.toolHandlers[msg.Tool]
	if !ok {
		return
	}

	c.logger.Info("handling tool request",
		"request_id", msg.RequestID,
		"tool", msg.Tool,
		"from", nick,
	)

	// Acquire concurrency semaphore (non-blocking).
	select {
	case c.toolSem <- struct{}{}:
		defer func() { <-c.toolSem }()
	default:
		c.logger.Warn("tool request rejected: too many concurrent executions",
			"request_id", msg.RequestID,
			"tool", msg.Tool,
		)
		c.sendResponse(msg.RequestID, "error", "client busy: too many concurrent tool executions")
		return
	}

	// Panic recovery — a bug in a tool handler should not crash the client.
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("tool handler panicked",
				"tool", msg.Tool,
				"request_id", msg.RequestID,
				"panic", fmt.Sprintf("%v", r),
			)
			c.sendResponse(msg.RequestID, "error", "internal error: tool handler panicked")
		}
	}()

	// Unmarshal arguments.
	var args map[string]any
	if len(msg.Arguments) > 0 {
		if err := json.Unmarshal(msg.Arguments, &args); err != nil {
			c.sendResponse(msg.RequestID, "error", fmt.Sprintf("invalid arguments: %v", err))
			return
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	// Execute with timeout context.
	ctx, cancel := context.WithTimeout(context.Background(), defaultToolTimeout)
	defer cancel()

	start := time.Now()
	result, err := tool.Handler(ctx, args)
	duration := time.Since(start)

	// Audit log.
	c.logger.Info("tool executed",
		"tool", msg.Tool,
		"request_id", msg.RequestID,
		"duration", duration,
		"success", err == nil,
	)

	// Send response.
	if err != nil {
		c.sendResponse(msg.RequestID, "error", err.Error())
		return
	}
	c.sendResponse(msg.RequestID, "success", result)
}

// isConnected returns true if the IRC connection is active. It uses
// isConnectedFunc if set (for testing), otherwise checks the real connection.
func (c *Client) isConnected() bool {
	if c.isConnectedFunc != nil {
		return c.isConnectedFunc()
	}
	return c.conn != nil && c.conn.IsConnected()
}

// sendResponse sends a tool response, using sendResponseFunc if set (for
// testing) or sender.SendToolResponse otherwise.
func (c *Client) sendResponse(requestID, status, result string) {
	if c.sendResponseFunc != nil {
		if err := c.sendResponseFunc(requestID, status, result); err != nil {
			c.logger.Error("failed to send tool response", "error", err)
		}
		return
	}
	if err := c.sender.SendToolResponse(requestID, status, result); err != nil {
		c.logger.Error("failed to send tool response", "error", err)
	}
}
