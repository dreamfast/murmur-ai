package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
)

// rssHTTPTimeout is the maximum time allowed for fetching a feed.
const rssHTTPTimeout = 15 * time.Second

// rssMaxBodyBytes is the maximum feed body size (2MB).
const rssMaxBodyBytes = 2 * 1024 * 1024

// rssDescriptionSnippetLen is the max length of a description snippet.
const rssDescriptionSnippetLen = 200

// RSSToolConfig holds configuration for the rss_read tool.
type RSSToolConfig struct {
	// MaxItems is the default number of items to return per feed.
	MaxItems int
	// HTTPClient is an optional HTTP client for testing. If nil, a default
	// client with rssHTTPTimeout is used.
	HTTPClient *http.Client
}

// NewRSSTool creates the rss_read tool for fetching and parsing RSS 2.0 and
// Atom feeds.
func NewRSSTool(cfg RSSToolConfig) Tool {
	return Tool{
		Name:        "rss_read",
		Description: "Fetch and parse RSS 2.0 or Atom feeds. Returns numbered items with title, link, date, and description snippet.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "The URL of the RSS or Atom feed to fetch"
				},
				"count": {
					"type": "integer",
					"description": "Number of items to return (default from config, max 50)"
				}
			},
			"required": ["url"]
		}`),
		Handler: newRSSHandler(cfg),
	}
}

// newRSSHandler returns a handler function closed over the RSS config.
func newRSSHandler(cfg RSSToolConfig) func(ctx context.Context, args map[string]any) (string, error) {
	client := NewHTTPClient(rssHTTPTimeout, cfg.HTTPClient)

	maxItems := cfg.MaxItems
	if maxItems <= 0 {
		maxItems = 10
	}
	if maxItems > 50 {
		maxItems = 50
	}

	return func(ctx context.Context, args map[string]any) (string, error) {
		feedURL, err := RequireStringArg(args, "url")
		if err != nil {
			return "", err
		}

		// Validate URL scheme.
		if !strings.HasPrefix(feedURL, "http://") && !strings.HasPrefix(feedURL, "https://") {
			return "", fmt.Errorf("feed URL must use http:// or https:// scheme: %q", feedURL)
		}

		// Determine item count.
		count := OptionalIntArg(args, "count", maxItems)
		if count <= 0 || count > maxItems {
			count = maxItems
		}

		// Fetch the feed.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
		if err != nil {
			return "", fmt.Errorf("rss_read: invalid URL: %w", err)
		}
		req.Header.Set("User-Agent", "Murmur/1.0 RSS Reader")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("rss_read: fetch failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("rss_read: HTTP %d from %s", resp.StatusCode, feedURL)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, rssMaxBodyBytes))
		if err != nil {
			return "", fmt.Errorf("rss_read: read body: %w", err)
		}

		items, err := parseFeed(body)
		if err != nil {
			return "", fmt.Errorf("rss_read: parse feed: %w", err)
		}

		if len(items) == 0 {
			return "No items found in feed.", nil
		}

		// Limit items.
		if len(items) > count {
			items = items[:count]
		}

		return formatFeedItems(items), nil
	}
}

// feedItem is a normalized feed item from either RSS 2.0 or Atom.
type feedItem struct {
	Title       string
	Link        string
	Published   string
	Description string
}

// parseFeed detects the feed format and parses it into normalized items.
func parseFeed(data []byte) ([]feedItem, error) {
	// Detect format by looking for root element.
	trimmed := strings.TrimSpace(string(data))
	if strings.Contains(trimmed[:min(500, len(trimmed))], "<feed") {
		return parseAtom(data)
	}
	return parseRSS2(data)
}

// rss2Feed represents an RSS 2.0 feed structure.
type rss2Feed struct {
	XMLName xml.Name    `xml:"rss"`
	Channel rss2Channel `xml:"channel"`
}

type rss2Channel struct {
	Items []rss2Item `xml:"item"`
}

type rss2Item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

// parseRSS2 parses an RSS 2.0 feed.
func parseRSS2(data []byte) ([]feedItem, error) {
	var feed rss2Feed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("invalid RSS 2.0: %w", err)
	}

	items := make([]feedItem, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		items = append(items, feedItem{
			Title:       cleanText(item.Title),
			Link:        strings.TrimSpace(item.Link),
			Published:   strings.TrimSpace(item.PubDate),
			Description: snippetText(item.Description, rssDescriptionSnippetLen),
		})
	}
	return items, nil
}

// atomFeed represents an Atom feed structure.
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// parseAtom parses an Atom feed.
func parseAtom(data []byte) ([]feedItem, error) {
	var feed atomFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("invalid Atom: %w", err)
	}

	items := make([]feedItem, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		link := ""
		for _, l := range entry.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		if link == "" && len(entry.Links) > 0 {
			link = entry.Links[0].Href
		}

		published := entry.Published
		if published == "" {
			published = entry.Updated
		}

		desc := entry.Summary
		if desc == "" {
			desc = entry.Content
		}

		items = append(items, feedItem{
			Title:       cleanText(entry.Title),
			Link:        strings.TrimSpace(link),
			Published:   strings.TrimSpace(published),
			Description: snippetText(desc, rssDescriptionSnippetLen),
		})
	}
	return items, nil
}

// formatFeedItems formats feed items as a numbered list.
func formatFeedItems(items []feedItem) string {
	var sb strings.Builder
	for i, item := range items {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, item.Title))
		if item.Link != "" {
			sb.WriteString(fmt.Sprintf("   Link: %s\n", item.Link))
		}
		if item.Published != "" {
			sb.WriteString(fmt.Sprintf("   Date: %s\n", item.Published))
		}
		if item.Description != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", item.Description))
		}
		if i < len(items)-1 {
			sb.WriteString("\n")
		}
	}
	return TruncateOutput(sb.String())
}

// cleanText strips HTML tags and decodes HTML entities from text.
func cleanText(s string) string {
	s = stripHTMLTags(s)
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}

// snippetText cleans text and truncates to maxLen characters.
func snippetText(s string, maxLen int) string {
	s = cleanText(s)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// stripHTMLTags removes HTML tags from a string using a simple state machine.
func stripHTMLTags(s string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
