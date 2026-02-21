package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openCodeDefaultTimeout is the default session timeout for OpenCode operations.
const openCodeDefaultTimeout = 5 * time.Minute

// openCodeMaxResponseBytes is the maximum size of an OpenCode API response body.
const openCodeMaxResponseBytes = 2 * 1024 * 1024 // 2MB

// OpenCodeToolConfig holds the configuration for the opencode tool.
type OpenCodeToolConfig struct {
	// URL is the base URL of the OpenCode API (e.g., "http://localhost:3000").
	URL string
	// Username is the HTTP Basic Auth username. Optional.
	Username string
	// Password is the HTTP Basic Auth password. Optional.
	Password string
	// SessionTimeout is the maximum time to wait for a session to complete.
	SessionTimeout time.Duration
}

// openCodeSession represents an OpenCode session from the API.
type openCodeSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// openCodePart represents a message part in the OpenCode API.
// Parts can be text, tool calls, tool results, etc.
type openCodePart struct {
	Type string `json:"type"`
	// Text content (for type "text").
	Text string `json:"text,omitempty"`
	// Tool call fields (for type "tool-invocation").
	ToolName string `json:"toolName,omitempty"`
	State    string `json:"state,omitempty"`
	// Generic content for other part types.
	Content json.RawMessage `json:"content,omitempty"`
}

// openCodeMessageInfo represents message metadata in the OpenCode API.
type openCodeMessageInfo struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
}

// openCodeMessageWithParts represents a message with its parts from the API.
type openCodeMessageWithParts struct {
	Info  openCodeMessageInfo `json:"info"`
	Parts []openCodePart      `json:"parts"`
}

// openCodeSSEEvent represents a parsed SSE event.
type openCodeSSEEvent struct {
	Event string
	Data  string
}

// NewOpenCodeTool creates the opencode tool for interacting with an OpenCode
// coding agent via its REST+SSE API. The httpClient parameter allows injection
// of a custom client for testing; pass nil to use a default client.
func NewOpenCodeTool(cfg OpenCodeToolConfig, httpClient *http.Client) Tool {
	if httpClient == nil {
		httpClient = &http.Client{
			// No global timeout — SSE connections are long-lived.
			// Per-request timeouts are handled via context.
		}
	}

	timeout := cfg.SessionTimeout
	if timeout <= 0 {
		timeout = openCodeDefaultTimeout
	}

	return Tool{
		Name:        "opencode",
		Description: "Interact with an OpenCode coding agent. Supports creating chat sessions, listing sessions, and retrieving session details. Chat sessions are multi-step: the agent can read files, write code, run commands, and iterate until done.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"description": "Action to perform: 'chat', 'list_sessions', 'get_session'",
					"enum": ["chat", "list_sessions", "get_session"]
				},
				"message": {
					"type": "string",
					"description": "Message to send to the coding agent (required for 'chat' action)"
				},
				"session_id": {
					"type": "string",
					"description": "Session ID (required for 'get_session', optional for 'chat' to continue an existing session)"
				}
			},
			"required": ["action"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleOpenCode(ctx, args, cfg, timeout, httpClient)
		},
	}
}

// handleOpenCode dispatches to the appropriate OpenCode action handler.
func handleOpenCode(ctx context.Context, args map[string]any, cfg OpenCodeToolConfig, timeout time.Duration, client *http.Client) (string, error) {
	action, err := RequireStringArg(args, "action")
	if err != nil {
		return "", err
	}

	switch action {
	case "chat":
		return handleOpenCodeChat(ctx, args, cfg, timeout, client)
	case "list_sessions":
		return handleOpenCodeListSessions(ctx, cfg, client)
	case "get_session":
		return handleOpenCodeGetSession(ctx, args, cfg, client)
	default:
		return "", fmt.Errorf("handleOpenCode: unknown action %q, must be one of: chat, list_sessions, get_session", action)
	}
}

// handleOpenCodeChat creates or continues a chat session with the OpenCode agent.
// Flow: create/reuse session -> subscribe to global SSE -> send message async -> wait for idle -> fetch messages.
func handleOpenCodeChat(ctx context.Context, args map[string]any, cfg OpenCodeToolConfig, timeout time.Duration, client *http.Client) (string, error) {
	message, err := RequireStringArg(args, "message")
	if err != nil {
		return "", err
	}

	sessionID := OptionalStringArg(args, "session_id", "")

	// Apply timeout to the entire chat operation.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Step 1: Create or reuse session.
	if sessionID == "" {
		sid, err := openCodeCreateSession(ctx, cfg, client)
		if err != nil {
			return "", fmt.Errorf("handleOpenCodeChat: create session: %w", err)
		}
		sessionID = sid
	}

	// Step 2: Subscribe to global SSE events.
	sseCh, sseErrCh, sseCancel := openCodeSubscribeSSE(ctx, cfg, client)
	defer sseCancel()

	// Step 3: Send the message asynchronously.
	if err := openCodeSendMessageAsync(ctx, cfg, client, sessionID, message); err != nil {
		return "", fmt.Errorf("handleOpenCodeChat: send message: %w", err)
	}

	// Step 4: Wait for session to become idle by watching SSE events.
	if err := openCodeWaitForIdle(ctx, sseCh, sseErrCh, sessionID); err != nil {
		return "", fmt.Errorf("handleOpenCodeChat: wait for completion: %w", err)
	}

	// Step 5: Fetch the session messages.
	messages, err := openCodeFetchMessages(ctx, cfg, client, sessionID)
	if err != nil {
		return "", fmt.Errorf("handleOpenCodeChat: fetch result: %w", err)
	}

	return formatOpenCodeMessages(sessionID, messages), nil
}

// openCodeCreateSession creates a new OpenCode session via POST /session.
func openCodeCreateSession(ctx context.Context, cfg OpenCodeToolConfig, client *http.Client) (string, error) {
	reqURL := strings.TrimRight(cfg.URL, "/") + "/session"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(`{}`))
	if err != nil {
		return "", fmt.Errorf("openCodeCreateSession: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	openCodeSetAuth(req, cfg)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openCodeCreateSession: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, openCodeMaxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("openCodeCreateSession: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("openCodeCreateSession: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var session openCodeSession
	if err := json.Unmarshal(respBody, &session); err != nil {
		return "", fmt.Errorf("openCodeCreateSession: parse response: %w", err)
	}

	if session.ID == "" {
		return "", fmt.Errorf("openCodeCreateSession: empty session ID in response")
	}

	return session.ID, nil
}

// openCodeSubscribeSSE subscribes to the global SSE event stream at GET /event.
// It returns a channel of events, an error channel, and a cancel function.
// The caller must call the cancel function when done.
func openCodeSubscribeSSE(ctx context.Context, cfg OpenCodeToolConfig, client *http.Client) (<-chan openCodeSSEEvent, <-chan error, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	eventCh := make(chan openCodeSSEEvent, 16)
	errCh := make(chan error, 1)

	reqURL := strings.TrimRight(cfg.URL, "/") + "/event"

	go func() {
		defer close(eventCh)
		defer close(errCh)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			errCh <- fmt.Errorf("openCodeSubscribeSSE: create request: %w", err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		openCodeSetAuth(req, cfg)

		resp, err := client.Do(req)
		if err != nil {
			// Context cancellation is expected during cleanup.
			if ctx.Err() != nil {
				return
			}
			errCh <- fmt.Errorf("openCodeSubscribeSSE: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			errCh <- fmt.Errorf("openCodeSubscribeSSE: API returned status %d: %s", resp.StatusCode, string(body))
			return
		}

		parseSSEStream(ctx, resp.Body, eventCh, errCh)
	}()

	return eventCh, errCh, cancel
}

// parseSSEStream reads an SSE stream and sends parsed events to the channel.
// Handles multi-line data fields per the SSE specification.
func parseSSEStream(ctx context.Context, reader io.Reader, eventCh chan<- openCodeSSEEvent, errCh chan<- error) {
	scanner := bufio.NewScanner(reader)
	var currentEvent openCodeSSEEvent
	var dataLines []string

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()

		// Empty line = event dispatch.
		if line == "" {
			if len(dataLines) > 0 {
				currentEvent.Data = strings.Join(dataLines, "\n")
				select {
				case eventCh <- currentEvent:
				case <-ctx.Done():
					return
				}
			}
			currentEvent = openCodeSSEEvent{}
			dataLines = nil
			continue
		}

		// Parse field per SSE spec: strip field name + colon, then strip
		// at most one leading space from the value.
		if strings.HasPrefix(line, "event:") {
			currentEvent.Event = stripSSEValue(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, stripSSEValue(strings.TrimPrefix(line, "data:")))
		}
		// Ignore id:, retry:, and comment lines (starting with :).
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return
		}
		errCh <- fmt.Errorf("parseSSEStream: %w", err)
	}
}

// stripSSEValue strips at most one leading space from an SSE field value,
// per the SSE specification (https://html.spec.whatwg.org/multipage/server-sent-events.html).
func stripSSEValue(s string) string {
	if len(s) > 0 && s[0] == ' ' {
		return s[1:]
	}
	return s
}

// openCodeWaitForIdle waits for the target session to become idle by watching
// the global SSE event stream. It filters events by session ID.
func openCodeWaitForIdle(ctx context.Context, eventCh <-chan openCodeSSEEvent, errCh <-chan error, sessionID string) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("openCodeWaitForIdle: %w", ctx.Err())
		case err := <-errCh:
			if err != nil {
				return err
			}
		case event, ok := <-eventCh:
			if !ok {
				// Channel closed — SSE stream ended without seeing idle.
				return fmt.Errorf("openCodeWaitForIdle: SSE stream ended before session completed")
			}
			// Look for session status change events.
			if isSessionStatusEvent(event.Event) {
				var payload struct {
					Properties struct {
						SessionID string `json:"sessionId"`
						Status    string `json:"status"`
					} `json:"properties"`
				}
				if json.Unmarshal([]byte(event.Data), &payload) == nil {
					// Filter by our session ID.
					if payload.Properties.SessionID != "" && payload.Properties.SessionID != sessionID {
						continue
					}
					switch payload.Properties.Status {
					case "idle", "completed":
						return nil
					case "error", "failed":
						return fmt.Errorf("openCodeWaitForIdle: session ended with status %q", payload.Properties.Status)
					}
				}
				// Also try flat structure for simpler event formats.
				var flat struct {
					SessionID string `json:"sessionId"`
					Status    string `json:"status"`
				}
				if json.Unmarshal([]byte(event.Data), &flat) == nil {
					if flat.SessionID != "" && flat.SessionID != sessionID {
						continue
					}
					switch flat.Status {
					case "idle", "completed":
						return nil
					case "error", "failed":
						return fmt.Errorf("openCodeWaitForIdle: session ended with status %q", flat.Status)
					}
				}
			}
		}
	}
}

// isSessionStatusEvent returns true if the SSE event name indicates a session
// status change that we should inspect.
func isSessionStatusEvent(name string) bool {
	switch name {
	case "session.updated", "session.idle", "session.completed",
		"session.status", "session.error":
		return true
	}
	return false
}

// openCodeSendMessageAsync sends a chat message to an OpenCode session
// asynchronously via POST /session/:id/prompt_async. Returns 204 immediately.
func openCodeSendMessageAsync(ctx context.Context, cfg OpenCodeToolConfig, client *http.Client, sessionID, message string) error {
	msgJSON, err := marshalJSONString(message)
	if err != nil {
		return fmt.Errorf("openCodeSendMessageAsync: marshal message: %w", err)
	}
	// The API expects parts array with text parts.
	body := fmt.Sprintf(`{"parts": [{"type": "text", "text": %s}]}`, msgJSON)
	reqURL := strings.TrimRight(cfg.URL, "/") + "/session/" + sessionID + "/prompt_async"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("openCodeSendMessageAsync: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	openCodeSetAuth(req, cfg)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("openCodeSendMessageAsync: %w", err)
	}
	defer resp.Body.Close()

	// prompt_async returns 204 No Content on success.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("openCodeSendMessageAsync: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// openCodeFetchMessages fetches messages for a session via GET /session/:id/message.
func openCodeFetchMessages(ctx context.Context, cfg OpenCodeToolConfig, client *http.Client, sessionID string) ([]openCodeMessageWithParts, error) {
	reqURL := strings.TrimRight(cfg.URL, "/") + "/session/" + sessionID + "/message"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("openCodeFetchMessages: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	openCodeSetAuth(req, cfg)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openCodeFetchMessages: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, openCodeMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("openCodeFetchMessages: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openCodeFetchMessages: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var messages []openCodeMessageWithParts
	if err := json.Unmarshal(respBody, &messages); err != nil {
		return nil, fmt.Errorf("openCodeFetchMessages: parse response: %w", err)
	}

	return messages, nil
}

// handleOpenCodeListSessions lists all OpenCode sessions via GET /session.
func handleOpenCodeListSessions(ctx context.Context, cfg OpenCodeToolConfig, client *http.Client) (string, error) {
	reqURL := strings.TrimRight(cfg.URL, "/") + "/session"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("handleOpenCodeListSessions: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	openCodeSetAuth(req, cfg)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("handleOpenCodeListSessions: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, openCodeMaxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("handleOpenCodeListSessions: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("handleOpenCodeListSessions: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var sessions []openCodeSession
	if err := json.Unmarshal(respBody, &sessions); err != nil {
		return "", fmt.Errorf("handleOpenCodeListSessions: parse response: %w", err)
	}

	if len(sessions) == 0 {
		return "No sessions found.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d session(s):\n\n", len(sessions))
	for i, s := range sessions {
		fmt.Fprintf(&b, "%d. ID: %s\n   Title: %s\n   Created: %s\n",
			i+1, s.ID, s.Title, s.CreatedAt)
		if i < len(sessions)-1 {
			b.WriteString("\n")
		}
	}

	return TruncateOutput(b.String()), nil
}

// handleOpenCodeGetSession retrieves details for a specific OpenCode session,
// including its messages.
func handleOpenCodeGetSession(ctx context.Context, args map[string]any, cfg OpenCodeToolConfig, client *http.Client) (string, error) {
	sessionID, err := RequireStringArg(args, "session_id")
	if err != nil {
		return "", fmt.Errorf("handleOpenCodeGetSession: %w", err)
	}

	messages, err := openCodeFetchMessages(ctx, cfg, client, sessionID)
	if err != nil {
		return "", err
	}

	return formatOpenCodeMessages(sessionID, messages), nil
}

// formatOpenCodeMessages formats OpenCode messages as a readable string.
// It extracts text content from message parts and shows the last assistant response.
func formatOpenCodeMessages(sessionID string, messages []openCodeMessageWithParts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Session: %s\n\n", sessionID)

	if len(messages) == 0 {
		b.WriteString("No messages in session.\n")
		return TruncateOutput(b.String())
	}

	// Find the last assistant message and extract its text parts.
	var lastAssistantText string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Info.Role == "assistant" {
			lastAssistantText = extractTextFromParts(messages[i].Parts)
			break
		}
	}

	if lastAssistantText != "" {
		fmt.Fprintf(&b, "Result:\n%s\n", lastAssistantText)
	} else {
		// Fall back to showing all messages.
		for _, msg := range messages {
			text := extractTextFromParts(msg.Parts)
			if text != "" {
				fmt.Fprintf(&b, "[%s] %s\n", msg.Info.Role, text)
			}
		}
	}

	return TruncateOutput(b.String())
}

// extractTextFromParts extracts and concatenates text content from message parts.
func extractTextFromParts(parts []openCodePart) string {
	var texts []string
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// openCodeSetAuth sets HTTP Basic Auth on a request if credentials are configured.
func openCodeSetAuth(req *http.Request, cfg OpenCodeToolConfig) {
	if cfg.Username != "" || cfg.Password != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
}

// marshalJSONString marshals a string to a JSON string literal, properly
// escaping special characters. Returns an error if marshaling fails.
func marshalJSONString(s string) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshalJSONString: %w", err)
	}
	return string(b), nil
}
