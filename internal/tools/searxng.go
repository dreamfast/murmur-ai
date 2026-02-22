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

// searxngMaxResponseBytes is the maximum size of the SearXNG API response body.
const searxngMaxResponseBytes = 2 * 1024 * 1024 // 2MB

// searxngDefaultTimeout is the HTTP client timeout for SearXNG requests.
const searxngDefaultTimeout = 15 * time.Second

// searxngMaxResults is the hard cap on the number of SearXNG results.
const searxngMaxResults = 100

// SearXNGToolConfig holds the configuration for the searxng_search tool.
type SearXNGToolConfig struct {
	// URL is the base URL of the SearXNG instance (e.g., "http://localhost:8080").
	URL string
	// MaxResults is the default number of results to return. Defaults to 10, capped at 100.
	MaxResults int
}

// searxngResponse represents the relevant parts of the SearXNG JSON API response.
type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

// searxngResult represents a single search result from the SearXNG API.
type searxngResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// NewSearXNGTool creates the searxng_search tool for searching the web via a
// self-hosted SearXNG instance. The httpClient parameter allows injection of a
// custom client for testing; pass nil to use a default client with a 15s timeout.
func NewSearXNGTool(cfg SearXNGToolConfig, httpClient *http.Client) Tool {
	httpClient = NewHTTPClient(searxngDefaultTimeout, httpClient)

	maxResults := cfg.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > searxngMaxResults {
		maxResults = searxngMaxResults
	}

	return Tool{
		Name:        "searxng_search",
		Description: "Search the web using a self-hosted SearXNG instance. Returns titles, URLs, and descriptions. Supports filtering by category, time range, and language.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Search query (required)"
				},
				"count": {
					"type": "integer",
					"description": "Number of results to return (default: 10, max: 100)"
				},
				"categories": {
					"type": "string",
					"description": "Comma-separated search categories (e.g., 'general', 'images', 'news', 'videos', 'it', 'science', 'files', 'music', 'social media')"
				},
				"time_range": {
					"type": "string",
					"description": "Time range filter: 'day', 'week', 'month', 'year'"
				},
				"language": {
					"type": "string",
					"description": "Search language code (e.g., 'en', 'de', 'fr')"
				}
			},
			"required": ["query"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleSearXNG(ctx, args, cfg.URL, maxResults, httpClient)
		},
	}
}

// handleSearXNG performs the SearXNG API request and formats the results.
// Note: SearXNG's JSON API does not support a per-request result count
// parameter — it returns a full page of results. The count parameter controls
// local truncation of the returned results.
func handleSearXNG(ctx context.Context, args map[string]any, baseURL string, maxResults int, client *http.Client) (string, error) {
	query, err := RequireStringArg(args, "query")
	if err != nil {
		return "", err
	}

	count := OptionalIntArg(args, "count", maxResults)
	if count <= 0 {
		count = maxResults
	}
	if count > maxResults {
		count = maxResults
	}

	categories := OptionalStringArg(args, "categories", "")
	timeRange := OptionalStringArg(args, "time_range", "")
	language := OptionalStringArg(args, "language", "")

	// Validate time_range if provided.
	if timeRange != "" {
		switch timeRange {
		case "day", "week", "month", "year":
			// valid
		default:
			return "", fmt.Errorf("handleSearXNG: invalid time_range %q, must be one of: day, week, month, year", timeRange)
		}
	}

	// Build the request URL.
	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	if categories != "" {
		params.Set("categories", categories)
	}
	if timeRange != "" {
		params.Set("time_range", timeRange)
	}
	if language != "" {
		params.Set("language", language)
	}

	reqURL := strings.TrimRight(baseURL, "/") + "/search?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("handleSearXNG: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("handleSearXNG: %w", err)
	}
	defer resp.Body.Close()

	// Validate Content-Type is JSON.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("handleSearXNG: unexpected Content-Type %q (body: %s)", ct, string(body))
	}

	// Limit response body size.
	body, err := io.ReadAll(io.LimitReader(resp.Body, searxngMaxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("handleSearXNG: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("handleSearXNG: API returned status %d: %s", resp.StatusCode, string(body))
	}

	var searchResp searxngResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("handleSearXNG: parse response: %w", err)
	}

	results := searchResp.Results
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q.", query), nil
	}

	// Cap results to requested count.
	if len(results) > count {
		results = results[:count]
	}

	return formatSearXNGResults(results), nil
}

// formatSearXNGResults formats SearXNG search results as a numbered list.
func formatSearXNGResults(results []searxngResult) string {
	var b strings.Builder
	for i, r := range results {
		desc := r.Content
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, desc)
		if i < len(results)-1 {
			b.WriteString("\n")
		}
	}
	return TruncateOutput(b.String())
}
