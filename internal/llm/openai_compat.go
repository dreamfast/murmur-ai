package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"murmur/internal/config"
)

// errPermanent wraps errors that should not be retried (4xx responses
// other than 429).
type errPermanent struct{ err error }

func (e *errPermanent) Error() string { return e.err.Error() }
func (e *errPermanent) Unwrap() error { return e.err }

// errRateLimited wraps 429 Too Many Requests errors. These are distinct
// from other 4xx errors because they are retryable via provider failover.
type errRateLimited struct{ err error }

func (e *errRateLimited) Error() string { return e.err.Error() }
func (e *errRateLimited) Unwrap() error { return e.err }

// IsPermanent reports whether err is a permanent (non-retryable) error,
// typically a 4xx HTTP response other than 429.
func IsPermanent(err error) bool {
	var perm *errPermanent
	return errors.As(err, &perm)
}

// IsRateLimited reports whether err is a 429 rate limit error.
func IsRateLimited(err error) bool {
	var rl *errRateLimited
	return errors.As(err, &rl)
}

// NewPermanentError wraps err as a permanent (non-retryable) error.
// This is primarily useful for testing failover logic from other packages.
func NewPermanentError(err error) error {
	return &errPermanent{err: err}
}

// NewRateLimitedError wraps err as a rate-limited (429) error.
// This is primarily useful for testing failover logic from other packages.
func NewRateLimitedError(err error) error {
	return &errRateLimited{err: err}
}

// OpenAICompatProvider implements Provider using the OpenAI-compatible
// /v1/chat/completions endpoint. It works with OpenRouter, Kimi, GLM,
// Ollama, and any other OpenAI-compatible API.
type OpenAICompatProvider struct {
	name        string
	apiBase     string
	apiKey      string
	model       string
	maxTokens   int
	temperature float64
	userAgent   string
	reasoning   bool
	httpClient  *http.Client
	logger      *slog.Logger
}

// defaultMaxTokens is the default maximum number of tokens when not configured.
const defaultMaxTokens = 4096

// defaultTemperature is the default sampling temperature when not configured.
const defaultTemperature = 0.7

// NewOpenAICompatProvider creates a new OpenAI-compatible LLM provider.
// If logger is nil, slog.Default() is used. MaxTokens defaults to 4096 when
// zero; Temperature defaults to 0.7 when negative (use 0.0 explicitly for
// deterministic output by setting a non-negative value in config).
func NewOpenAICompatProvider(name string, cfg config.LLMProviderConfig, logger *slog.Logger) *OpenAICompatProvider {
	if logger == nil {
		logger = slog.Default()
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	temperature := cfg.Temperature
	if temperature < 0 {
		temperature = defaultTemperature
	}
	return &OpenAICompatProvider{
		name:        name,
		apiBase:     strings.TrimRight(cfg.APIBase, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		maxTokens:   maxTokens,
		temperature: temperature,
		userAgent:   cfg.UserAgent,
		reasoning:   cfg.Reasoning,
		httpClient:  &http.Client{Timeout: 120 * time.Second},
		logger:      logger,
	}
}

// Name returns the provider's configured name.
func (p *OpenAICompatProvider) Name() string {
	return p.name
}

// openAIRequest is the JSON body sent to the chat completions endpoint.
type openAIRequest struct {
	Model       string            `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	Tools       []ToolDef         `json:"tools,omitempty"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature"`
}

// wireMessage is the on-the-wire JSON format for a message sent to the API.
// Pointer fields (*string) use omitempty to distinguish "omit the field" (nil)
// from "include as empty string" (pointer to ""). This is needed for:
//   - Content: some providers (e.g., Qwen3 via OpenRouter) require "content" on
//     all non-assistant messages, even when the tool result is empty.
//   - ReasoningContent: Kimi's thinking mode demands reasoning_content be present
//     (even empty) on assistant messages with tool_calls.
type wireMessage struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content,omitempty"`
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

// openAIResponse is the JSON response from the chat completions endpoint.
type openAIResponse struct {
	Choices []struct {
		Message struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// ChatCompletion sends a chat completion request with exponential backoff retry
// on 5xx and timeout errors (max 3 attempts).
func (p *OpenAICompatProvider) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	// Marshal messages with provider-specific reasoning mode handling.
	wireMessages, err := p.marshalMessages(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("ChatCompletion: marshal messages: %w", err)
	}

	body := openAIRequest{
		Model:       p.model,
		Messages:    wireMessages,
		Tools:       req.Tools,
		MaxTokens:   p.maxTokens,
		Temperature: p.temperature,
	}

	// Don't send empty tools array — some providers reject it.
	if len(req.Tools) == 0 {
		body.Tools = nil
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ChatCompletion: marshal request: %w", err)
	}

	url := p.apiBase + "/chat/completions"

	p.logger.Debug("LLM request", "url", url, "model", p.model, "tools", len(req.Tools))

	const maxAttempts = 3
	backoff := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := p.doRequest(ctx, url, payload)
		if err != nil {
			// Context cancelled — don't retry.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("ChatCompletion: context cancelled: %w", ctx.Err())
			}
			// Permanent errors (4xx except 429) — don't retry.
			var perm *errPermanent
			if errors.As(err, &perm) {
				return nil, err
			}
			// Rate limited (429) — don't retry within same provider,
			// let the caller handle failover to another provider.
			var rl *errRateLimited
			if errors.As(err, &rl) {
				return nil, err
			}
			// Retryable errors (5xx, network) — retry with backoff.
			if attempt == maxAttempts {
				return nil, fmt.Errorf("ChatCompletion: after %d attempts: %w", maxAttempts, err)
			}
			p.logger.Warn("LLM request failed, retrying",
				"attempt", attempt, "error", err, "backoff", backoff)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("ChatCompletion: context cancelled during retry: %w", ctx.Err())
			case <-timer.C:
			}
			backoff *= 2
			continue
		}
		return resp, nil
	}

	// Unreachable, but satisfies the compiler.
	return nil, fmt.Errorf("ChatCompletion: exhausted retries")
}

// doRequest performs a single HTTP request to the chat completions endpoint.
// Returns an error for network failures and 5xx responses (retryable).
// Returns the parsed response for 2xx. Returns an error for 4xx (not retried).
func (p *OpenAICompatProvider) doRequest(ctx context.Context, url string, payload []byte) (*ChatResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("doRequest: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	if p.userAgent != "" {
		httpReq.Header.Set("User-Agent", p.userAgent)
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("doRequest: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("doRequest: read response body: %w", err)
	}

	// 5xx errors are retryable.
	if httpResp.StatusCode >= 500 {
		return nil, fmt.Errorf("doRequest: server error %d: %s", httpResp.StatusCode, truncate(string(respBody), 200))
	}

	// 429 Too Many Requests — retryable via failover, distinct from permanent 4xx.
	if httpResp.StatusCode == http.StatusTooManyRequests {
		var apiResp openAIResponse
		if jsonErr := json.Unmarshal(respBody, &apiResp); jsonErr == nil && apiResp.Error != nil {
			return nil, &errRateLimited{fmt.Errorf("doRequest: rate limited (429): %s", apiResp.Error.Message)}
		}
		return nil, &errRateLimited{fmt.Errorf("doRequest: rate limited (429): %s", truncate(string(respBody), 200))}
	}

	// Other 4xx errors are not retried — wrap as permanent.
	if httpResp.StatusCode >= 400 {
		var apiResp openAIResponse
		if jsonErr := json.Unmarshal(respBody, &apiResp); jsonErr == nil && apiResp.Error != nil {
			return nil, &errPermanent{fmt.Errorf("doRequest: API error %d: %s", httpResp.StatusCode, apiResp.Error.Message)}
		}
		return nil, &errPermanent{fmt.Errorf("doRequest: client error %d: %s", httpResp.StatusCode, truncate(string(respBody), 200))}
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("doRequest: parse response: %w", err)
	}

	// Check for API-level error in the response body (some providers return 200 with error).
	if apiResp.Error != nil {
		return nil, fmt.Errorf("doRequest: API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("doRequest: empty choices in response")
	}

	choice := apiResp.Choices[0]
	return &ChatResponse{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
		ToolCalls:        choice.Message.ToolCalls,
		Usage: Usage{
			PromptTokens:     apiResp.Usage.PromptTokens,
			CompletionTokens: apiResp.Usage.CompletionTokens,
			TotalTokens:      apiResp.Usage.TotalTokens,
		},
	}, nil
}

// marshalMessages converts Message values to provider-appropriate JSON.
// When reasoning mode is enabled (e.g., Kimi), assistant messages with
// tool_calls always include reasoning_content (even if empty). When
// reasoning mode is disabled, reasoning_content is omitted from all messages.
func (p *OpenAICompatProvider) marshalMessages(msgs []Message) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(msgs))
	for i, msg := range msgs {
		wm := wireMessage{
			Role:       msg.Role,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		}

		// Content handling: non-assistant messages (user, system, tool) must
		// always include "content" in the JSON, even when empty. Some providers
		// (e.g., Qwen3 via OpenRouter) reject messages without it. For
		// assistant messages, only include content when non-empty to avoid
		// sending "content":"" alongside tool_calls (which some providers dislike).
		if msg.Role != RoleAssistant {
			c := msg.Content
			wm.Content = &c
		} else if msg.Content != "" {
			c := msg.Content
			wm.Content = &c
		}
		// Assistant with empty content: Content stays nil → omitted.

		if p.reasoning && msg.Role == RoleAssistant && len(msg.ToolCalls) > 0 {
			// Reasoning provider: always include reasoning_content on
			// assistant+tool_calls messages, even if empty.
			rc := msg.ReasoningContent
			wm.ReasoningContent = &rc
		}
		// Non-reasoning providers: ReasoningContent stays nil → omitted.

		raw, err := json.Marshal(wm)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		out[i] = raw
	}
	return out, nil
}

// truncate shortens a string to maxLen characters for error messages.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
