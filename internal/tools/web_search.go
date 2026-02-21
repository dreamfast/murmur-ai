package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes is the maximum size of the Brave Search API response body.
const maxResponseBytes = 2 * 1024 * 1024 // 2MB

// maxSearchResults is the hard cap on the number of search results.
const maxSearchResults = 20

// defaultSearchTimeout is the HTTP client timeout for search requests.
const defaultSearchTimeout = 15 * time.Second

// WebSearchToolConfig holds the configuration for the web_search tool.
type WebSearchToolConfig struct {
	APIKey     string
	MaxResults int
}

// braveSearchResponse represents the relevant parts of the Brave Search API response.
type braveSearchResponse struct {
	Web struct {
		Results []braveSearchResult `json:"results"`
	} `json:"web"`
}

// braveSearchResult represents a single search result from the Brave API.
type braveSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// NewWebSearchTool creates the web_search tool for searching the web via the
// Brave Search API. The httpClient parameter allows injection of a custom
// client for testing; pass nil to use a default client with a 15s timeout.
func NewWebSearchTool(cfg WebSearchToolConfig, httpClient *http.Client) Tool {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultSearchTimeout}
	}

	maxResults := cfg.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > maxSearchResults {
		maxResults = maxSearchResults
	}

	return Tool{
		Name:        "web_search",
		Description: "Search the web using the Brave Search API. Returns titles, URLs, and descriptions.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Search query (required)"
				},
				"count": {
					"type": "integer",
					"description": "Number of results to return (default: 5, max: 20)"
				}
			},
			"required": ["query"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleWebSearch(ctx, args, cfg.APIKey, maxResults, httpClient)
		},
	}
}

// braveSearchBaseURL is the default Brave Search API endpoint.
const braveSearchBaseURL = "https://api.search.brave.com/res/v1/web/search"

// handleWebSearch performs the Brave Search API request and formats the results.
func handleWebSearch(ctx context.Context, args map[string]any, apiKey string, maxResults int, client *http.Client) (string, error) {
	return handleWebSearchWithURL(ctx, args, apiKey, maxResults, client, braveSearchBaseURL)
}

// handleWebSearchWithURL is the internal implementation that accepts a custom
// base URL for testing.
func handleWebSearchWithURL(ctx context.Context, args map[string]any, apiKey string, maxResults int, client *http.Client, baseURL string) (string, error) {
	query, err := RequireStringArg(args, "query")
	if err != nil {
		return "", err
	}

	count := optionalIntArg(args, "count", maxResults)
	if count <= 0 {
		count = maxResults
	}
	if count > maxResults {
		count = maxResults
	}

	// Build the request URL.
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", fmt.Sprintf("%d", count))
	reqURL := baseURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("handleWebSearch: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("handleWebSearch: %w", err)
	}
	defer resp.Body.Close()

	// Limit response body size.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("handleWebSearch: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("handleWebSearch: API returned status %d: %s", resp.StatusCode, string(body))
	}

	var searchResp braveSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("handleWebSearch: parse response: %w", err)
	}

	results := searchResp.Web.Results
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q.", query), nil
	}

	// Cap results to requested count.
	if len(results) > count {
		results = results[:count]
	}

	return formatSearchResults(results), nil
}

// formatSearchResults formats search results as a numbered list.
func formatSearchResults(results []braveSearchResult) string {
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, r.Description)
		if i < len(results)-1 {
			b.WriteString("\n")
		}
	}
	return TruncateOutput(b.String())
}
