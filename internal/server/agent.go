package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/irc"
	"murmur/internal/llm"
)

// maxIterations is the maximum number of LLM call iterations per user message.
// This prevents runaway tool-calling loops.
const maxIterations = 10

// maxConsecutiveToolFailures is the number of consecutive failures for the
// same tool before the agent injects a system message telling the LLM to
// stop using that tool and respond with what it has.
const maxConsecutiveToolFailures = 2

// crossChannelMaxMsgLen is the maximum length (in runes) of a single message
// included in the cross-channel context section of the system prompt.
const crossChannelMaxMsgLen = 300

// crossChannelMaxChannels is the maximum number of other channels to include
// in the cross-channel context. This caps the number of database queries per
// prompt build to a reasonable bound.
const crossChannelMaxChannels = 5

// defaultSystemPrompt is used when no system prompt file is configured or the
// file cannot be read.
const defaultSystemPrompt = `You are Murmur, a personal AI assistant communicating over IRC. Be helpful and concise.`

// requestNickKey is the context key used to propagate the requesting user's
// IRC nick to server-side tool handlers. This enables defense-in-depth checks
// (e.g., the permissions_manage tool verifying admin status) without changing
// the tool handler signature.
type requestNickKey struct{}

// agentConfig holds the mutable configuration fields of the Agent that can be
// changed at runtime via hot config reload. All fields are protected by Agent.mu
// and should be read via Agent.loadConfig() to get a consistent snapshot.
type agentConfig struct {
	activeProvider  string
	systemPrompt    string
	maxHistory      int
	crossChCtx      int // messages per other channel to include in system prompt (0 = disabled)
	approvalTimeout time.Duration
	verbose         bool
	debug           config.DebugConfig // granular debug log category flags
}

// Agent runs the LLM agent loop. It ties together LLM providers, tool routing,
// and conversation memory. Each user message triggers a loop that may involve
// multiple LLM calls and tool invocations before producing a final text response.
type Agent struct {
	providers atomic.Pointer[map[string]llm.Provider]
	mu        sync.RWMutex // protects cfg (agentConfig fields)
	cfg       agentConfig

	serverTools     *ToolRegistry
	registry        *Registry
	memory          *Memory
	router          *Router
	approvals       *ApprovalManager
	conn            *irc.Connection
	serverName      string                // server's target name for shell routing
	busChannel      string                // bus channel name, excluded from cross-channel context
	channelSettings *ChannelSettingsStore // per-channel provider/settings; may be nil
	permissions     *PermissionManager    // user/channel permissions; may be nil
	toolTimeout     time.Duration
	logger          *slog.Logger

	// sendFunc overrides the default send behavior for testing.
	// When nil, messages are sent via conn.Send.
	sendFunc func(channel, message string)

	// routeToolFunc overrides the default tool routing behavior for testing.
	// When nil, tool calls are routed via router.RouteToolCall.
	routeToolFunc func(ctx context.Context, toolName string, arguments json.RawMessage) (string, error)

	// lastTopics tracks the last topic set per channel to avoid redundant
	// SetTopic calls. Protected by topicMu.
	topicMu    sync.Mutex
	lastTopics map[string]string

	// Per-channel locks to prevent concurrent agent loops on the same channel.
	// The lock is held for the entire HandleMessage duration (including I/O)
	// to ensure conversation coherence. This means messages to the same channel
	// serialize completely. This is acceptable for a personal agent with few
	// channels. The chanLocks map grows monotonically — entries are never
	// removed. For a personal agent with a bounded number of IRC channels,
	// this is not a concern.
	chanMu    sync.Mutex
	chanLocks map[string]*sync.Mutex
}

// AgentParams holds all parameters for creating a new Agent. Using a struct
// instead of positional parameters makes the constructor readable and allows
// adding new fields without breaking existing call sites.
type AgentParams struct {
	// Providers is the map of named LLM providers. May be empty (commands
	// still work, but HandleMessage returns errors).
	Providers map[string]llm.Provider
	// DefaultProvider is the name of the global default provider. Must exist
	// in Providers if Providers is non-empty.
	DefaultProvider string
	// ServerTools holds tools that execute locally on the server without bus
	// routing. May be nil (an empty registry is created).
	ServerTools *ToolRegistry
	// Registry is the client registry for bus-connected tool providers.
	Registry *Registry
	// Memory is the conversation memory store.
	Memory *Memory
	// Router routes tool calls to clients via the bus.
	Router *Router
	// Approvals manages the tool call approval flow. May be nil (all tools
	// execute immediately).
	Approvals *ApprovalManager
	// Conn is the IRC connection. May be nil in tests.
	Conn *irc.Connection
	// SystemPrompt is the base system prompt text.
	SystemPrompt string
	// ServerName is the server's target name for shell routing.
	ServerName string
	// BusChannel is the bus channel name, excluded from cross-channel context.
	BusChannel string
	// MaxHistory is the maximum number of messages to include in LLM context.
	MaxHistory int
	// CrossChannelContext is the number of messages per other channel to
	// include in the system prompt (0 = disabled).
	CrossChannelContext int
	// ChannelSettings stores per-channel provider/settings. May be nil.
	ChannelSettings *ChannelSettingsStore
	// ToolTimeout is the maximum duration for a single tool call.
	ToolTimeout time.Duration
	// ApprovalTimeout is how long to wait for user approval before denying.
	ApprovalTimeout time.Duration
	// Verbose enables real-time status messages to IRC.
	Verbose bool
	// Debug holds granular debug log category flags.
	Debug config.DebugConfig
	// Logger is the structured logger.
	Logger *slog.Logger
}

// NewAgent creates a new Agent from the given parameters. See AgentParams for
// field documentation.
func NewAgent(p AgentParams) *Agent {
	serverTools := p.ServerTools
	serverName := p.ServerName
	if serverTools == nil {
		serverTools = NewToolRegistry()
	}
	if serverName == "" {
		serverName = "server"
	}
	a := &Agent{
		cfg: agentConfig{
			activeProvider:  p.DefaultProvider,
			systemPrompt:    p.SystemPrompt,
			maxHistory:      p.MaxHistory,
			crossChCtx:      p.CrossChannelContext,
			approvalTimeout: p.ApprovalTimeout,
			verbose:         p.Verbose,
			debug:           p.Debug,
		},
		serverTools:     serverTools,
		registry:        p.Registry,
		memory:          p.Memory,
		router:          p.Router,
		approvals:       p.Approvals,
		conn:            p.Conn,
		serverName:      serverName,
		busChannel:      p.BusChannel,
		channelSettings: p.ChannelSettings,
		toolTimeout:     p.ToolTimeout,
		logger:          p.Logger,
		lastTopics:      make(map[string]string),
		chanLocks:       make(map[string]*sync.Mutex),
	}
	providers := p.Providers
	a.providers.Store(&providers)
	return a
}

// loadProviders returns the current providers map from the atomic pointer.
func (a *Agent) loadProviders() map[string]llm.Provider {
	return *a.providers.Load()
}

// UpdateProviders atomically replaces the providers map and updates the
// default provider name. This is called during hot config reload to swap
// in a new set of LLM providers without restarting. The activeProvider is
// updated under mu (write lock) BEFORE the providers map is stored via
// atomic pointer. This ordering ensures that getActiveProvider() never
// sees a new providers map with a stale activeProvider name — the worst
// case is briefly seeing the new activeProvider with the old map, which
// safely falls back to "provider not found" (a transient, retryable error).
func (a *Agent) UpdateProviders(providers map[string]llm.Provider, defaultName string) {
	a.mu.Lock()
	a.cfg.activeProvider = defaultName
	a.mu.Unlock()
	a.providers.Store(&providers)
}

// SetPermissions replaces the agent's permission manager. This is safe for
// concurrent use — the permissions field is read under mu.RLock in runLoop
// and written under mu.Lock here.
func (a *Agent) SetPermissions(pm *PermissionManager) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.permissions = pm
}

// UpdateConfig updates the agent's simple configuration fields under the
// mu write lock. This is called during hot config reload for fields that
// don't require structural changes (no new goroutines, no connection changes).
func (a *Agent) UpdateConfig(verbose bool, maxHistory, crossChCtx int, approvalTimeout time.Duration, systemPrompt string, debug config.DebugConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.verbose = verbose
	a.cfg.maxHistory = maxHistory
	a.cfg.crossChCtx = crossChCtx
	a.cfg.approvalTimeout = approvalTimeout
	a.cfg.systemPrompt = systemPrompt
	a.cfg.debug = debug
}

// loadConfig returns a snapshot of the agent's mutable configuration fields.
// The snapshot is taken under RLock so all fields are consistent. Callers
// should snapshot once at the start of a logical operation (e.g., runLoop
// iteration) and use the snapshot throughout, avoiding repeated locking.
func (a *Agent) loadConfig() agentConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// HandleMessage processes a user message through the LLM agent loop. It
// acquires a per-channel lock to prevent concurrent loops on the same channel,
// stores the user message in memory, and iterates: calling the LLM, routing
// any tool calls, and feeding results back until the LLM produces a text
// response or the iteration limit is reached.
func (a *Agent) HandleMessage(ctx context.Context, channel, nick, message string) {
	// Acquire per-channel lock.
	chLock := a.getChannelLock(channel)
	chLock.Lock()
	defer chLock.Unlock()

	// Store the user message in memory.
	if err := a.memory.AddMessage(channel, llm.RoleUser, nick+": "+message, "", ""); err != nil {
		a.logger.Error("failed to store user message", "error", err, "channel", channel)
		a.send(channel, "error: failed to store message")
		return
	}

	a.runLoop(ctx, channel, nick)
}

// RunScheduledTask executes a scheduled task by storing it as a system message
// and running the LLM agent loop. This is called by the Scheduler for due tasks.
// The createdBy parameter is the nick of the user who created the task; their
// current permissions are used for tool filtering. If empty, no filtering is applied.
func (a *Agent) RunScheduledTask(ctx context.Context, channel, taskDescription, createdBy string) {
	// Acquire per-channel lock.
	chLock := a.getChannelLock(channel)
	chLock.Lock()
	defer chLock.Unlock()

	// Store the task as a system message with a prefix.
	content := "[Scheduled Task] " + taskDescription
	if err := a.memory.AddMessage(channel, llm.RoleSystem, content, "", ""); err != nil {
		a.logger.Error("failed to store scheduled task message", "error", err, "channel", channel)
		return
	}

	a.runLoop(ctx, channel, createdBy)
}

// HandleEvent processes an external event through the LLM agent loop. It
// acquires the per-channel lock, stores the event as a system message, and
// runs the agent loop. The event is formatted as a system message so the LLM
// can decide how to respond. The nick parameter identifies the user whose
// permissions should apply; use "_system" for system-level events that should
// bypass permission filtering.
func (a *Agent) HandleEvent(ctx context.Context, channel, nick, source, eventType, summary, data string) error {
	chLock := a.getChannelLock(channel)
	chLock.Lock()
	defer chLock.Unlock()

	content := fmt.Sprintf("[Event from %s] %s: %s", source, eventType, summary)
	if data != "" {
		content += "\n" + data
	}

	if err := a.memory.AddMessage(channel, llm.RoleSystem, content, "", ""); err != nil {
		return fmt.Errorf("HandleEvent: store message: %w", err)
	}

	a.runLoop(ctx, channel, nick)
	return nil
}

// runLoop is the core LLM iteration loop shared by HandleMessage,
// RunScheduledTask, and HandleEvent. It assumes the per-channel lock is
// already held and the initial message (user or system) has been stored in
// memory. The nick parameter identifies the user for permission filtering;
// system-initiated actions use "_system" which bypasses filtering.
func (a *Agent) runLoop(ctx context.Context, channel, nick string) {
	// Inject the requesting user's nick into the context so server-side tool
	// handlers can perform defense-in-depth authorization checks.
	ctx = context.WithValue(ctx, requestNickKey{}, nick)

	// Track consecutive failures per tool to detect retry loops. When a tool
	// fails maxConsecutiveToolFailures times in a row, we inject a system
	// message telling the LLM to stop using it.
	toolFailCounts := make(map[string]int)

	for i := 0; i < maxIterations; i++ {
		// Check context before each iteration.
		if ctx.Err() != nil {
			a.logger.Info("agent loop cancelled", "channel", channel, "iteration", i)
			return
		}

		// Snapshot mutable config fields once per iteration so all reads
		// within this iteration see a consistent set of values.
		cfg := a.loadConfig()

		// Snapshot the permission manager under the same lock discipline.
		a.mu.RLock()
		pm := a.permissions
		a.mu.RUnlock()

		// Build the messages array: system prompt + conversation history.
		history, err := a.memory.GetHistory(channel, cfg.maxHistory)
		if err != nil {
			a.logger.Error("failed to get history", "error", err, "channel", channel)
			a.send(channel, "error: failed to retrieve conversation history")
			return
		}

		messages := make([]llm.Message, 0, 1+len(history))
		messages = append(messages, llm.Message{
			Role:    llm.RoleSystem,
			Content: a.buildSystemPrompt(channel, cfg),
		})
		// Reconstruct structured tool_calls on assistant messages so that
		// OpenAI-compatible APIs receive the correct message format.
		for _, msg := range history {
			messages = append(messages, reconstructToolCalls(msg))
		}

		// Get available tools: server-side tools first, then client tools.
		// Server tools take priority — if a server tool and client tool share
		// a name, only the server tool definition is sent to the LLM to avoid
		// duplicate function names in the request.
		serverDefs := a.serverTools.AllToolDefs()
		clientDefs := a.registry.AllTools()

		serverNames := make(map[string]struct{}, len(serverDefs))
		for _, sd := range serverDefs {
			serverNames[sd.Name] = struct{}{}
		}

		busTools := make([]bus.ToolDef, 0, len(serverDefs)+len(clientDefs))
		busTools = append(busTools, serverDefs...)
		for _, cd := range clientDefs {
			if _, collision := serverNames[cd.Name]; !collision {
				busTools = append(busTools, cd)
			}
		}

		// Enrich the shell tool description with available targets so the
		// LLM knows which hosts it can run commands on.
		clientTargets := a.registry.ShellTargets()
		if len(clientTargets) > 0 || a.serverTools.HasTool("shell") {
			for idx, td := range busTools {
				if td.Name == "shell" {
					targets := []string{a.serverName + " (server, default)"}
					for _, ct := range clientTargets {
						targets = append(targets, ct+" (client)")
					}
					busTools[idx].Description = fmt.Sprintf(
						"Execute a shell command inside a Docker container. Available targets: %s",
						strings.Join(targets, ", "),
					)
					break
				}
			}
		}

		// Remove tools that have hit the consecutive failure threshold so
		// the LLM cannot call them again.
		if len(toolFailCounts) > 0 {
			filtered := busTools[:0]
			for _, td := range busTools {
				if toolFailCounts[td.Name] >= maxConsecutiveToolFailures {
					continue
				}
				filtered = append(filtered, td)
			}
			busTools = filtered
		}

		// Filter tools based on user permissions. System nicks (starting
		// with _) and nil PermissionManager bypass filtering.
		if pm != nil {
			busTools = pm.FilterTools(busTools, nick, channel, a.GetProviderNames())
		}

		tools := llm.ConvertBusTools(busTools)

		// Get the provider for this channel (per-channel override or global default).
		provider, err := a.resolveProvider(channel, nick, pm)
		if err != nil {
			a.logger.Error("no active provider", "error", err, "channel", channel)
			a.send(channel, "error: no LLM provider available")
			return
		}

		if i == 0 {
			a.status(channel, cfg.verbose, fmt.Sprintf("thinking... [%s, %d tools]", provider.Name(), len(tools)))
		} else {
			a.status(channel, cfg.verbose, fmt.Sprintf("thinking... [%s, iteration %d]", provider.Name(), i+1))
		}

		// Call the LLM with timing measurement.
		llmStart := time.Now()
		resp, err := provider.ChatCompletion(ctx, &llm.ChatRequest{
			Messages: messages,
			Tools:    tools,
		})
		llmDuration := time.Since(llmStart)
		if err != nil {
			a.logger.Error("LLM call failed",
				"error", err,
				"provider", provider.Name(),
				"channel", channel,
				"iteration", i,
				"latency", llmDuration,
			)
			a.send(channel, fmt.Sprintf("error: LLM call failed: %v", err))
			return
		}
		if cfg.debug.LogLLMRequests {
			a.logger.Info("llm_request",
				"provider", provider.Name(),
				"channel", channel,
				"iteration", i,
				"latency", llmDuration,
				"prompt_tokens", resp.Usage.PromptTokens,
				"completion_tokens", resp.Usage.CompletionTokens,
				"total_tokens", resp.Usage.TotalTokens,
				"tool_calls", len(resp.ToolCalls),
				"has_content", resp.Content != "",
			)
		}

		// If the response has tool calls, process them.
		if len(resp.ToolCalls) > 0 {
			// Store the assistant message with tool calls in memory.
			// We use an envelope struct that bundles tool_calls with
			// reasoning_content (from providers like Kimi that use thinking
			// mode). Both are needed when replaying the conversation back
			// to the API. Any accompanying text content from the LLM is
			// logged but not stored — the tool_calls take priority.
			envelope := assistantToolCallMsg{
				ToolCalls:        resp.ToolCalls,
				ReasoningContent: resp.ReasoningContent,
			}
			envelopeJSON, err := json.Marshal(envelope)
			if err != nil {
				a.logger.Error("failed to marshal tool calls", "error", err)
				a.send(channel, "error: failed to process tool calls")
				return
			}

			if resp.Content != "" {
				a.logger.Debug("assistant message had both content and tool_calls, storing tool_calls only",
					"content_preview", truncate(resp.Content, 100),
					"channel", channel,
				)
			}
			if err := a.memory.AddMessage(channel, llm.RoleAssistant, string(envelopeJSON), "", ""); err != nil {
				a.logger.Error("failed to store assistant tool call message", "error", err)
			}

			// Route each tool call and store results.
			for _, tc := range resp.ToolCalls {
				if cfg.debug.LogToolCalls {
					a.logger.Info("routing tool call",
						"tool", tc.Function.Name,
						"call_id", tc.ID,
						"channel", channel,
					)
				}
				a.status(channel, cfg.verbose, fmt.Sprintf("calling %s...", tc.Function.Name))

				toolStart := time.Now()
				result, routeErr := a.routeToolCall(ctx, channel, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
				toolDuration := time.Since(toolStart)
				if routeErr != nil {
					if cfg.debug.LogToolCalls {
						a.logger.Error("tool_call_result",
							"tool", tc.Function.Name,
							"call_id", tc.ID,
							"channel", channel,
							"status", "error",
							"duration", toolDuration,
							"error", routeErr,
						)
					}
					a.status(channel, cfg.verbose, fmt.Sprintf("%s failed: %s", tc.Function.Name, truncate(routeErr.Error(), 80)))
					// Feed the error back to the LLM as a tool result.
					result = fmt.Sprintf("error: %v", routeErr)

					// Track consecutive failures for circuit breaker.
					toolFailCounts[tc.Function.Name]++
					if toolFailCounts[tc.Function.Name] >= maxConsecutiveToolFailures {
						a.logger.Warn("tool hit consecutive failure limit, removing from available tools",
							"tool", tc.Function.Name,
							"failures", toolFailCounts[tc.Function.Name],
							"channel", channel,
						)
						// Append a hint to the error result so the LLM knows
						// the tool is no longer available.
						result += fmt.Sprintf(
							" [SYSTEM: %s has failed %d times consecutively and is now unavailable. Do NOT call it again. Respond to the user with what you know.]",
							tc.Function.Name, toolFailCounts[tc.Function.Name],
						)
					}
				} else {
					if cfg.debug.LogToolCalls {
						a.logger.Info("tool_call_result",
							"tool", tc.Function.Name,
							"call_id", tc.ID,
							"channel", channel,
							"status", "ok",
							"duration", toolDuration,
							"result_bytes", len(result),
						)
					}
					a.status(channel, cfg.verbose, fmt.Sprintf("%s done (%d bytes)", tc.Function.Name, len(result)))
					// Reset failure count on success.
					delete(toolFailCounts, tc.Function.Name)
				}

				// Store the tool result in memory.
				if err := a.memory.AddMessage(channel, llm.RoleTool, result, tc.Function.Name, tc.ID); err != nil {
					a.logger.Error("failed to store tool result", "error", err)
				}
			}

			// Continue the loop — the LLM will see the tool results.
			continue
		}

		// Text-only response — store and send to IRC.
		if resp.Content != "" {
			if err := a.memory.AddMessage(channel, llm.RoleAssistant, resp.Content, "", ""); err != nil {
				a.logger.Error("failed to store assistant response", "error", err)
			}
			a.send(channel, resp.Content)
			return
		}

		// Empty response (no tool calls, no content) — unusual but handle it.
		a.logger.Warn("LLM returned empty response", "channel", channel, "iteration", i)
		a.send(channel, "I received an empty response from the LLM. Please try again.")
		return
	}

	// Max iterations reached.
	a.send(channel, "I've reached the maximum number of tool calls for this message. Please try again.")
}

// SetProvider switches the LLM provider for a specific channel. If name is
// "default" or empty, the channel-specific override is cleared and the channel
// falls back to the global default. Returns an error if the provider name is
// not found in the configured providers map.
func (a *Agent) SetProvider(channel, name string) error {
	// "default" or "" clears the per-channel override.
	if name == "default" || name == "" {
		if a.channelSettings != nil {
			if err := a.channelSettings.SetProvider(channel, ""); err != nil {
				return fmt.Errorf("SetProvider: clear channel override: %w", err)
			}
		}
		a.logger.Info("cleared per-channel provider override", "channel", channel)
		a.syncChannelTopic(channel)
		return nil
	}

	if _, ok := a.loadProviders()[name]; !ok {
		return fmt.Errorf("provider %q not found", name)
	}

	if a.channelSettings != nil {
		if err := a.channelSettings.SetProvider(channel, name); err != nil {
			return fmt.Errorf("SetProvider: set channel override: %w", err)
		}
	}
	a.logger.Info("switched per-channel LLM provider", "channel", channel, "provider", name)
	a.syncChannelTopic(channel)
	return nil
}

// syncChannelTopic sets the IRC topic for a channel to reflect the active
// model. The topic format is "[model: <name>]" or "[model: <name>] <prefix>"
// if a topic prefix is configured for the channel. The method is a no-op if:
//   - conn is nil (no IRC connection, e.g., in tests)
//   - the bot is not an IRC operator (no permission to set topics)
//   - the channel is the bus channel (protocol traffic, no user-facing topic)
//   - the topic hasn't changed since the last call (avoids redundant SET commands)
func (a *Agent) syncChannelTopic(channel string) {
	if a.conn == nil {
		return
	}
	if !a.conn.IsOper() {
		return
	}
	// Skip DMs (non-channel targets) — topics only apply to channels.
	if !a.conn.IsChannel(channel) {
		return
	}
	if strings.EqualFold(channel, a.busChannel) {
		return
	}

	modelName := a.GetProviderForChannel(channel)
	topic := "[model: " + modelName + "]"

	// Append topic prefix from channel settings if available.
	if a.channelSettings != nil {
		cs, err := a.channelSettings.Get(channel)
		if err != nil {
			a.logger.Warn("syncChannelTopic: failed to get channel settings",
				"channel", channel,
				"error", err,
			)
		} else if cs != nil && cs.TopicPrefix != "" {
			topic = topic + " " + cs.TopicPrefix
		}
	}

	// Normalize channel key for case-insensitive dedup (IRC channels are
	// case-insensitive).
	key := strings.ToLower(channel)

	// Skip if the topic hasn't changed.
	a.topicMu.Lock()
	if a.lastTopics[key] == topic {
		a.topicMu.Unlock()
		return
	}
	a.topicMu.Unlock()

	// Set the topic first; only update the cache on success so that
	// failures don't suppress future retries.
	if err := a.conn.SetTopic(channel, topic); err != nil {
		a.logger.Warn("syncChannelTopic: failed to set topic",
			"channel", channel,
			"topic", topic,
			"error", err,
		)
		return
	}

	a.topicMu.Lock()
	a.lastTopics[key] = topic
	a.topicMu.Unlock()
}

// SyncAllTopics sets the topic on all currently joined channels (except the
// bus channel). This is intended to be called on reconnect after OPER status
// is confirmed. The topic cache is cleared first so that topics are re-applied
// even if the computed text hasn't changed locally (the IRC server may have
// reset topics during the reconnect).
func (a *Agent) SyncAllTopics() {
	if a.conn == nil {
		return
	}

	// Clear the topic cache so all channels get a fresh SetTopic.
	a.topicMu.Lock()
	a.lastTopics = make(map[string]string)
	a.topicMu.Unlock()

	for _, ch := range a.conn.Channels() {
		a.syncChannelTopic(ch)
	}
}

// GetProvider returns the name of the global default LLM provider.
func (a *Agent) GetProvider() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.activeProvider
}

// GetProviderForChannel returns the effective provider name for a channel.
// If the channel has a per-channel override AND that provider exists in the
// configured providers map, the override is returned. Otherwise the global
// default is returned. This ensures consistency with resolveProvider — both
// methods agree on which provider is active for a given channel.
func (a *Agent) GetProviderForChannel(channel string) string {
	if a.channelSettings != nil {
		name, err := a.channelSettings.GetProvider(channel)
		if err != nil {
			a.logger.Warn("failed to read channel provider setting",
				"channel", channel,
				"error", err,
			)
		} else if name != "" {
			// Validate the override still exists in the providers map.
			if _, ok := a.loadProviders()[name]; ok {
				return name
			}
			a.logger.Warn("per-channel provider not found, reporting global default",
				"channel", channel,
				"provider", name,
			)
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.activeProvider
}

// resolveProvider returns the LLM provider to use for the given channel and
// user. It checks for a per-channel override first (via channelSettings),
// then falls back to the global default. If the resolved provider is not
// allowed for the user (via PermissionManager), it falls back to the first
// allowed provider. Returns an error if no provider can be resolved.
// The pm parameter is a snapshot of the PermissionManager taken under lock
// by the caller to avoid races during hot reload.
//
// ChannelSettingsStore is backed by SQLite and is safe for concurrent use.
func (a *Agent) resolveProvider(channel, nick string, pm *PermissionManager) (llm.Provider, error) {
	providers := a.loadProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	// Check per-channel override.
	var resolved llm.Provider
	if a.channelSettings != nil {
		name, err := a.channelSettings.GetProvider(channel)
		if err != nil {
			a.logger.Warn("failed to read channel provider setting",
				"channel", channel,
				"error", err,
			)
		} else if name != "" {
			if p, ok := providers[name]; ok {
				resolved = p
			} else {
				// Channel has an override but the provider doesn't exist — log
				// and fall through to global default.
				a.logger.Warn("per-channel provider not found, falling back to global default",
					"channel", channel,
					"provider", name,
				)
			}
		}
	}

	// Fall back to global default if no per-channel override resolved.
	if resolved == nil {
		p, err := a.getActiveProvider()
		if err != nil {
			return nil, err
		}
		resolved = p
	}

	// Check model permissions if PermissionManager is configured.
	// System nicks (_system, etc.) and empty nicks (legacy scheduled tasks) bypass.
	if pm != nil && nick != "" && !strings.HasPrefix(nick, "_") {
		allToolNames := a.getAllToolNames()
		allModelNames := a.GetProviderNames()
		if !pm.IsModelAllowed(nick, channel, resolved.Name(), allToolNames, allModelNames) {
			// Find the first allowed model.
			ep := pm.GetEffective(nick, channel, allToolNames, allModelNames)
			for _, modelName := range ep.Models {
				if p, ok := providers[modelName]; ok {
					cfg := a.loadConfig()
					if cfg.debug.LogPermissions {
						a.logger.Info("permission_denial",
							"nick", nick,
							"channel", channel,
							"resource", "model",
							"denied", resolved.Name(),
							"fallback", modelName,
						)
					}
					return p, nil
				}
			}
			return nil, fmt.Errorf("no allowed LLM provider for user %q in %s", nick, channel)
		}
	}

	return resolved, nil
}

// getAllToolNames returns the names of all currently available tools (server + client).
func (a *Agent) getAllToolNames() []string {
	var serverDefs []bus.ToolDef
	if a.serverTools != nil {
		serverDefs = a.serverTools.AllToolDefs()
	}
	var clientDefs []bus.ToolDef
	if a.registry != nil {
		clientDefs = a.registry.AllTools()
	}

	seen := make(map[string]struct{}, len(serverDefs)+len(clientDefs))
	names := make([]string, 0, len(serverDefs)+len(clientDefs))
	for _, td := range serverDefs {
		if _, ok := seen[td.Name]; !ok {
			seen[td.Name] = struct{}{}
			names = append(names, td.Name)
		}
	}
	for _, td := range clientDefs {
		if _, ok := seen[td.Name]; !ok {
			seen[td.Name] = struct{}{}
			names = append(names, td.Name)
		}
	}
	return names
}

// GetProviderNames returns all available provider names, sorted alphabetically.
func (a *Agent) GetProviderNames() []string {
	providers := a.loadProviders()
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// buildSystemPrompt constructs the full system prompt by appending dynamic
// context (active model, IRC state, cross-channel activity) to the static
// base prompt loaded from the config file. The cfg parameter is a snapshot
// of the agent's mutable config taken under RLock by the caller.
func (a *Agent) buildSystemPrompt(channel string, cfg agentConfig) string {
	var sb strings.Builder
	sb.WriteString(cfg.systemPrompt)

	// Active model context — always included, even without an IRC connection.
	modelName := a.GetProviderForChannel(channel)
	globalDefault := a.GetProvider()
	scope := "global default"
	if modelName != globalDefault {
		scope = "channel-specific"
	}
	fmt.Fprintf(&sb, "\n\n## Active Model\n- Active model: %s (%s)\n",
		sanitizePromptValue(modelName), scope)

	if a.conn == nil {
		return sb.String()
	}

	sb.WriteString("\n## IRC Context\n")
	fmt.Fprintf(&sb, "- Your nick: %s\n", sanitizePromptValue(a.conn.Nick()))

	// Detect DM context: if the channel doesn't start with '#', it's a
	// private conversation keyed by the user's nick.
	isDM := !a.conn.IsChannel(channel)
	if isDM {
		fmt.Fprintf(&sb, "- You are in a private conversation (DM) with %s\n", sanitizePromptValue(channel))
	} else {
		fmt.Fprintf(&sb, "- Current channel: %s\n", sanitizePromptValue(channel))
	}

	channels := a.conn.Channels()
	if len(channels) > 0 {
		sanitized := make([]string, len(channels))
		for i, ch := range channels {
			sanitized[i] = sanitizePromptValue(ch)
		}
		fmt.Fprintf(&sb, "- Joined channels: %s\n", strings.Join(sanitized, ", "))
	}

	if a.conn.IsOper() {
		sb.WriteString("- IRC operator: yes (you have elevated privileges: set topics, kick users, etc.)\n")
	} else {
		sb.WriteString("- IRC operator: no\n")
	}

	if a.serverName != "" {
		fmt.Fprintf(&sb, "- Server name: %s\n", sanitizePromptValue(a.serverName))
	}

	// List connected clients.
	if a.registry != nil {
		clients := a.registry.OnlineClients()
		if len(clients) > 0 {
			sanitized := make([]string, len(clients))
			for i, c := range clients {
				sanitized[i] = sanitizePromptValue(c)
			}
			fmt.Fprintf(&sb, "- Connected clients: %s\n", strings.Join(sanitized, ", "))
		} else {
			sb.WriteString("- Connected clients: none\n")
		}
	}

	// Cross-channel context: include recent messages from other joined
	// channels so the LLM has awareness of activity elsewhere (e.g., news
	// posted to #news can be referenced from #murmur). Skipped for DMs
	// to keep private conversations focused and avoid leaking channel context.
	if cfg.crossChCtx > 0 && a.memory != nil && !isDM {
		a.appendCrossChannelContext(&sb, channel, channels, cfg.crossChCtx)
	}

	return sb.String()
}

// appendCrossChannelContext fetches recent messages from other joined channels
// (excluding the current channel and the bus channel) and appends them to the
// system prompt. This gives the LLM awareness of activity in other channels
// for a personal agent setup. At most crossChannelMaxChannels channels are
// queried to bound the number of database calls per prompt build. Errors are
// logged but non-fatal — the prompt is still usable without cross-channel context.
// The crossChCtx parameter specifies how many messages to fetch per channel.
func (a *Agent) appendCrossChannelContext(sb *strings.Builder, currentChannel string, allChannels []string, crossChCtx int) {
	var sections []string
	queried := 0

	for _, ch := range allChannels {
		// Skip the current channel (already in history) and the bus channel
		// (protocol traffic, not useful context).
		if ch == currentChannel || ch == a.busChannel {
			continue
		}

		if queried >= crossChannelMaxChannels {
			break
		}
		queried++

		msgs, err := a.memory.GetRecentMessages(ch, crossChCtx)
		if err != nil {
			a.logger.Warn("failed to get cross-channel context",
				"channel", ch,
				"error", err,
			)
			continue
		}
		if len(msgs) == 0 {
			continue
		}

		var section strings.Builder
		fmt.Fprintf(&section, "### %s\n", sanitizePromptValue(ch))
		for _, msg := range msgs {
			content := truncate(sanitizePromptValue(msg.Content), crossChannelMaxMsgLen)
			fmt.Fprintf(&section, "<%s> %s\n", msg.Role, content)
		}
		sections = append(sections, section.String())
	}

	if len(sections) > 0 {
		sb.WriteString("\n## Other Channel Activity\n")
		for _, s := range sections {
			sb.WriteString(s)
		}
	}
}

// sanitizePromptValue strips control characters (newlines, carriage returns,
// tabs) from a value before injecting it into the system prompt. This prevents
// prompt injection via untrusted identifiers like client IDs or IRC nicks.
func sanitizePromptValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

// LoadSystemPrompt reads a system prompt from the given file path. If the path
// is empty or the file cannot be read, the default system prompt is returned.
func LoadSystemPrompt(path string) (string, error) {
	if path == "" {
		return defaultSystemPrompt, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultSystemPrompt, nil
		}
		return "", fmt.Errorf("load system prompt: %w", err)
	}

	content := string(data)
	if content == "" {
		return defaultSystemPrompt, nil
	}
	return content, nil
}

// assistantToolCallMsg is the JSON structure stored in the content column for
// assistant messages that contain tool calls. It bundles the tool calls with
// any reasoning content returned by the provider (e.g., Kimi's thinking mode)
// so that both can be faithfully replayed in subsequent API requests.
type assistantToolCallMsg struct {
	ToolCalls        []llm.ToolCall `json:"tool_calls"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
}

// reconstructToolCalls checks if an assistant message contains serialized
// tool calls in its content field and reconstructs the structured ToolCalls
// and ReasoningContent fields. This is necessary because Memory stores tool
// calls as JSON in the content column, but OpenAI-compatible APIs require the
// assistant message to have a structured tool_calls array alongside (or
// instead of) content. Providers like Kimi also require reasoning_content to
// be present on assistant messages that originally included it.
func reconstructToolCalls(msg llm.Message) llm.Message {
	if msg.Role != llm.RoleAssistant || msg.Content == "" {
		return msg
	}

	// First, try the new envelope format (assistantToolCallMsg).
	var envelope assistantToolCallMsg
	if err := json.Unmarshal([]byte(msg.Content), &envelope); err == nil {
		if len(envelope.ToolCalls) > 0 && envelope.ToolCalls[0].ID != "" && envelope.ToolCalls[0].Function.Name != "" {
			return llm.Message{
				Role:             msg.Role,
				Content:          "",
				ReasoningContent: envelope.ReasoningContent,
				ToolCalls:        envelope.ToolCalls,
			}
		}
	}

	// Fall back to the legacy format: bare []ToolCall array (for messages
	// stored before reasoning_content support was added).
	var toolCalls []llm.ToolCall
	if err := json.Unmarshal([]byte(msg.Content), &toolCalls); err != nil {
		return msg // Not JSON — regular text content.
	}

	// Validate that we actually parsed tool calls (not arbitrary JSON).
	if len(toolCalls) == 0 {
		return msg
	}
	// Check that at least the first element looks like a tool call.
	if toolCalls[0].ID == "" || toolCalls[0].Function.Name == "" {
		return msg
	}

	// Reconstruct: set ToolCalls and clear Content.
	return llm.Message{
		Role:      msg.Role,
		Content:   "",
		ToolCalls: toolCalls,
	}
}

// getChannelLock returns the mutex for the given channel, creating one if it
// doesn't exist. This provides per-channel serialization of agent loops.
func (a *Agent) getChannelLock(channel string) *sync.Mutex {
	a.chanMu.Lock()
	defer a.chanMu.Unlock()

	lock, ok := a.chanLocks[channel]
	if !ok {
		lock = &sync.Mutex{}
		a.chanLocks[channel] = lock
	}
	return lock
}

// getActiveProvider returns the global default LLM provider. Returns an error
// if no provider is configured or the default provider name is invalid. This
// method does NOT consider per-channel overrides — use resolveProvider for that.
func (a *Agent) getActiveProvider() (llm.Provider, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	providers := a.loadProviders()
	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	provider, ok := providers[a.cfg.activeProvider]
	if !ok {
		return nil, fmt.Errorf("active provider %q not found", a.cfg.activeProvider)
	}
	return provider, nil
}

// routeToolCall routes a tool call to the appropriate handler. It checks
// server-side tools first (local execution, no bus routing), then checks
// the client's autonomy level before routing client tools via the bus.
// If routeToolFunc is set (for testing), it replaces the final bus routing
// step but the approval gate still applies.
func (a *Agent) routeToolCall(ctx context.Context, channel, toolName string, arguments json.RawMessage) (string, error) {
	// Handle target-aware routing for the shell tool. When a "target" parameter
	// is present, the tool call is routed to the matching host (server or client).
	if toolName == "shell" {
		return a.routeShellCall(ctx, channel, arguments)
	}

	// Check server-side tools first — execute locally without bus routing.
	// Server tools run synchronously in-process (no bus overhead) but are
	// still subject to the same timeout as client tools for consistency.
	// Server-side tools always execute immediately (no approval needed).
	if st, ok := a.serverTools.Get(toolName); ok {
		var argsMap map[string]any
		if len(arguments) == 0 || string(arguments) == "null" {
			argsMap = make(map[string]any)
		} else if err := json.Unmarshal(arguments, &argsMap); err != nil {
			return "", fmt.Errorf("routeToolCall: unmarshal args for server tool %q: %w", toolName, err)
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, a.toolTimeout)
		defer cancel()

		return st.Handler(timeoutCtx, argsMap)
	}

	// Client tools — check autonomy level before routing.
	if a.approvals != nil {
		provider, found := a.registry.GetToolProvider(toolName)
		if found {
			autonomy := a.registry.GetClientAutonomy(provider.ClientID)
			switch autonomy {
			case "report":
				return "", fmt.Errorf("tool %q requires higher autonomy level (client %q has autonomy=%q)", toolName, provider.ClientID, autonomy)
			case "approve":
				result, err := a.requestAndWaitApproval(ctx, channel, toolName, arguments, provider.ClientID)
				if err != nil {
					return "", err
				}
				if !result {
					return "", fmt.Errorf("tool call %q denied by user", toolName)
				}
				// Approved — fall through to route the tool call.
			}
			// "auto" or unknown — fall through to route immediately.
		}
	}

	// Route to client tools — use test override if set, otherwise bus routing.
	if a.routeToolFunc != nil {
		return a.routeToolFunc(ctx, toolName, arguments)
	}
	return a.router.RouteToolCall(ctx, toolName, arguments, a.toolTimeout)
}

// routeShellCall handles target-aware routing for the shell tool. It extracts
// the optional "target" parameter from the arguments to determine where to
// execute the command:
//   - If target matches the server name (or is empty/omitted) and the server
//     has a shell tool configured: execute locally on the server
//   - If target matches a connected client ID (or the server has no shell tool
//     and no target was specified): route via bus to that client
//   - Otherwise: return an error listing available targets
func (a *Agent) routeShellCall(ctx context.Context, channel string, arguments json.RawMessage) (string, error) {
	var argsMap map[string]any
	if len(arguments) == 0 || string(arguments) == "null" {
		argsMap = make(map[string]any)
	} else if err := json.Unmarshal(arguments, &argsMap); err != nil {
		return "", fmt.Errorf("routeShellCall: unmarshal args: %w", err)
	}

	// Extract and remove the target parameter (it's not part of the shell tool's own args).
	target, _ := argsMap["target"].(string)
	delete(argsMap, "target")

	// Re-marshal the args without the target field.
	cleanArgs, err := json.Marshal(argsMap)
	if err != nil {
		return "", fmt.Errorf("routeShellCall: marshal args: %w", err)
	}

	// Determine the effective target.
	serverHasShell := a.serverTools.HasTool("shell")

	if target == "" {
		// No target specified — default to server if it has shell, otherwise
		// fall through to normal client routing.
		if serverHasShell {
			target = a.serverName
		}
	}

	// Route to server (local execution).
	if target == a.serverName && serverHasShell {
		st, _ := a.serverTools.Get("shell")
		timeoutCtx, cancel := context.WithTimeout(ctx, a.toolTimeout)
		defer cancel()
		return st.Handler(timeoutCtx, argsMap)
	}

	// Route to a client — either an explicit target or the default provider.
	// Resolve the client ID: use explicit target if set, otherwise find the
	// default provider for the shell tool.
	clientID := target
	if clientID == "" {
		provider, found := a.registry.GetToolProvider("shell")
		if found {
			clientID = provider.ClientID
		}
	}

	// Check autonomy level for the target client.
	if clientID != "" && a.approvals != nil {
		autonomy := a.registry.GetClientAutonomy(clientID)
		switch autonomy {
		case "report":
			return "", fmt.Errorf("shell on %q requires higher autonomy level (autonomy=%q)", clientID, autonomy)
		case "approve":
			result, err := a.requestAndWaitApproval(ctx, channel, "shell", cleanArgs, clientID)
			if err != nil {
				return "", err
			}
			if !result {
				return "", fmt.Errorf("shell call on %q denied by user", clientID)
			}
		}
	}

	// Use test override if set, otherwise bus routing.
	if a.routeToolFunc != nil {
		return a.routeToolFunc(ctx, "shell", cleanArgs)
	}
	return a.router.RouteToolCall(ctx, "shell", cleanArgs, a.toolTimeout)
}

// ExecuteTool executes a named tool directly, bypassing the LLM loop and
// approval gates. This is used by the pipeline backend to invoke tools as
// pipeline steps. It routes to server-side tools first, then to the shell
// tool (via shared routing logic), and finally to client tools via the bus.
//
// Pipeline steps are considered pre-approved — they skip the autonomy/approval
// checks that routeToolCall applies to LLM-initiated calls.
func (a *Agent) ExecuteTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	// Shell tool: delegate to routeShellCall for target-aware routing.
	if toolName == "shell" {
		argsJSON, err := json.Marshal(args)
		if err != nil {
			return "", fmt.Errorf("ExecuteTool: marshal shell args: %w", err)
		}
		return a.routeShellCall(ctx, "", argsJSON)
	}

	// Server-side tools: execute directly.
	if st, ok := a.serverTools.Get(toolName); ok {
		timeoutCtx, cancel := context.WithTimeout(ctx, a.toolTimeout)
		defer cancel()
		return st.Handler(timeoutCtx, args)
	}

	// Client/bus tools: route via bus, skipping approval (pre-approved).
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("ExecuteTool: marshal args: %w", err)
	}

	if a.routeToolFunc != nil {
		return a.routeToolFunc(ctx, toolName, argsJSON)
	}
	return a.router.RouteToolCall(ctx, toolName, argsJSON, a.toolTimeout)
}

// requestAndWaitApproval sends an approval request to IRC and waits for the
// user's decision. Returns true if approved, false if denied or timed out.
func (a *Agent) requestAndWaitApproval(ctx context.Context, channel, toolName string, arguments json.RawMessage, clientID string) (bool, error) {
	id, resultCh := a.approvals.RequestApproval(channel, toolName, arguments, clientID)

	// Summarize arguments for the IRC message (truncate to keep it readable).
	argsSummary := string(arguments)
	if len(argsSummary) > 200 {
		argsSummary = argsSummary[:200] + "..."
	}

	a.send(channel, fmt.Sprintf("\u26a0 Tool call requires approval: %s(%s). Reply !approve or !deny [id: %s]", toolName, argsSummary, id[:8]))

	// Wait for approval with timeout.
	timeout := a.loadConfig().approvalTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	select {
	case result := <-resultCh:
		return result.Approved, nil
	case <-time.After(timeout):
		a.approvals.Cancel(id)
		a.send(channel, fmt.Sprintf("approval timed out for %s — denied", toolName))
		return false, nil
	case <-ctx.Done():
		a.approvals.Cancel(id)
		return false, ctx.Err()
	}
}

// truncate returns the first n runes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// send sends a message to the given IRC channel, using sendFunc if set
// (for testing) or conn.Send otherwise. Markdown formatting in the message
// is converted to IRC formatting codes (bold, italic, colors, etc.).
func (a *Agent) send(channel, message string) {
	if a.sendFunc != nil {
		a.sendFunc(channel, message)
		return
	}
	a.conn.Send(channel, irc.MarkdownToIRC(message))
}

// status sends a grey, italic status message to IRC when verbose mode is on.
// These are ephemeral progress indicators (e.g., "thinking...", "calling shell")
// that help the user understand what the agent is doing. They are not stored
// in conversation memory. The verbose parameter should come from a config
// snapshot to avoid data races.
func (a *Agent) status(channel string, verbose bool, message string) {
	if !verbose {
		return
	}
	// Grey italic: \x1D\x0314message\x0F
	formatted := "\x1D\x0314" + message + "\x0F"
	if a.sendFunc != nil {
		a.sendFunc(channel, formatted)
		return
	}
	a.conn.Send(channel, formatted)
}
