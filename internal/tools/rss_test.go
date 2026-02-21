package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testRSS2Feed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Blog</title>
    <link>https://example.com</link>
    <description>A test blog</description>
    <item>
      <title>First Post</title>
      <link>https://example.com/first</link>
      <pubDate>Mon, 01 Jan 2024 12:00:00 GMT</pubDate>
      <description>This is the first post content.</description>
    </item>
    <item>
      <title>Second Post</title>
      <link>https://example.com/second</link>
      <pubDate>Tue, 02 Jan 2024 12:00:00 GMT</pubDate>
      <description>&lt;p&gt;HTML content &amp; entities&lt;/p&gt;</description>
    </item>
    <item>
      <title>Third Post</title>
      <link>https://example.com/third</link>
      <pubDate>Wed, 03 Jan 2024 12:00:00 GMT</pubDate>
      <description>Third post description.</description>
    </item>
  </channel>
</rss>`

const testAtomFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Test Atom Blog</title>
  <link href="https://example.com" rel="alternate"/>
  <entry>
    <title>Atom Entry One</title>
    <link href="https://example.com/atom-one" rel="alternate"/>
    <published>2024-01-01T12:00:00Z</published>
    <summary>Summary of atom entry one.</summary>
  </entry>
  <entry>
    <title>Atom Entry Two</title>
    <link href="https://example.com/atom-two"/>
    <updated>2024-01-02T12:00:00Z</updated>
    <content>Content of atom entry two with no summary.</content>
  </entry>
</feed>`

func TestRSS_ParseRSS2(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(testRSS2Feed))
	}))
	defer srv.Close()

	tool := NewRSSTool(RSSToolConfig{MaxItems: 10, HTTPClient: srv.Client()})
	result, err := tool.Handler(context.Background(), map[string]any{
		"url": srv.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "First Post") {
		t.Errorf("expected 'First Post' in result, got: %s", result)
	}
	if !strings.Contains(result, "Second Post") {
		t.Errorf("expected 'Second Post' in result, got: %s", result)
	}
	if !strings.Contains(result, "https://example.com/first") {
		t.Errorf("expected link in result, got: %s", result)
	}
	// HTML entities should be decoded.
	if !strings.Contains(result, "HTML content & entities") {
		t.Errorf("expected decoded HTML entities, got: %s", result)
	}
}

func TestRSS_ParseAtom(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(testAtomFeed))
	}))
	defer srv.Close()

	tool := NewRSSTool(RSSToolConfig{MaxItems: 10, HTTPClient: srv.Client()})
	result, err := tool.Handler(context.Background(), map[string]any{
		"url": srv.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Atom Entry One") {
		t.Errorf("expected 'Atom Entry One' in result, got: %s", result)
	}
	if !strings.Contains(result, "Atom Entry Two") {
		t.Errorf("expected 'Atom Entry Two' in result, got: %s", result)
	}
	if !strings.Contains(result, "https://example.com/atom-one") {
		t.Errorf("expected atom link in result, got: %s", result)
	}
	// Entry two has no published date, should fall back to updated.
	if !strings.Contains(result, "2024-01-02") {
		t.Errorf("expected updated date fallback, got: %s", result)
	}
	// Entry two has no summary, should fall back to content.
	if !strings.Contains(result, "Content of atom entry two") {
		t.Errorf("expected content fallback, got: %s", result)
	}
}

func TestRSS_EmptyFeed(t *testing.T) {
	t.Parallel()

	emptyFeed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Empty</title>
  </channel>
</rss>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(emptyFeed))
	}))
	defer srv.Close()

	tool := NewRSSTool(RSSToolConfig{MaxItems: 10, HTTPClient: srv.Client()})
	result, err := tool.Handler(context.Background(), map[string]any{
		"url": srv.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "No items") {
		t.Errorf("expected 'No items' message, got: %s", result)
	}
}

func TestRSS_InvalidURL(t *testing.T) {
	t.Parallel()

	tool := NewRSSTool(RSSToolConfig{MaxItems: 10})
	_, err := tool.Handler(context.Background(), map[string]any{
		"url": "ftp://example.com/feed.xml",
	})
	if err == nil {
		t.Fatal("expected error for non-http URL")
	}
	if !strings.Contains(err.Error(), "http://") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestRSS_MaxItemsCapped(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testRSS2Feed))
	}))
	defer srv.Close()

	// MaxItems is 2, feed has 3 items.
	tool := NewRSSTool(RSSToolConfig{MaxItems: 2, HTTPClient: srv.Client()})
	result, err := tool.Handler(context.Background(), map[string]any{
		"url":   srv.URL,
		"count": float64(100), // Request more than max.
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have 2 items (capped by MaxItems).
	if strings.Contains(result, "Third Post") {
		t.Errorf("expected only 2 items, but third post appeared: %s", result)
	}
	if !strings.Contains(result, "First Post") || !strings.Contains(result, "Second Post") {
		t.Errorf("expected first two posts, got: %s", result)
	}
}

func TestRSS_CountLessThanMax(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testRSS2Feed))
	}))
	defer srv.Close()

	// MaxItems is 10, but request only 1.
	tool := NewRSSTool(RSSToolConfig{MaxItems: 10, HTTPClient: srv.Client()})
	result, err := tool.Handler(context.Background(), map[string]any{
		"url":   srv.URL,
		"count": float64(1),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "First Post") {
		t.Errorf("expected first post, got: %s", result)
	}
	if strings.Contains(result, "Second Post") {
		t.Errorf("expected only 1 item, but second post appeared: %s", result)
	}
}

func TestRSS_HTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tool := NewRSSTool(RSSToolConfig{MaxItems: 10, HTTPClient: srv.Client()})
	_, err := tool.Handler(context.Background(), map[string]any{
		"url": srv.URL,
	})
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 in error, got: %v", err)
	}
}

func TestRSS_Timeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		_, _ = w.Write([]byte(testRSS2Feed))
	}))
	defer srv.Close()

	// Use a very short timeout.
	client := srv.Client()
	client.Timeout = 100 * time.Millisecond

	tool := NewRSSTool(RSSToolConfig{MaxItems: 10, HTTPClient: client})
	_, err := tool.Handler(context.Background(), map[string]any{
		"url": srv.URL,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRSS_MissingURL(t *testing.T) {
	t.Parallel()

	tool := NewRSSTool(RSSToolConfig{MaxItems: 10})
	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing URL")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseFeed_RSS2(t *testing.T) {
	t.Parallel()

	items, err := parseFeed([]byte(testRSS2Feed))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Title != "First Post" {
		t.Errorf("expected 'First Post', got %q", items[0].Title)
	}
}

func TestParseFeed_Atom(t *testing.T) {
	t.Parallel()

	items, err := parseFeed([]byte(testAtomFeed))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Title != "Atom Entry One" {
		t.Errorf("expected 'Atom Entry One', got %q", items[0].Title)
	}
}

func TestCleanText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain text", input: "hello world", want: "hello world"},
		{name: "html tags", input: "<p>hello <b>world</b></p>", want: "hello world"},
		{name: "html entities", input: "&amp; &lt; &gt;", want: "& < >"},
		{name: "mixed", input: "<a href='x'>link &amp; text</a>", want: "link & text"},
		{name: "whitespace", input: "  trimmed  ", want: "trimmed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := cleanText(tt.input)
			if got != tt.want {
				t.Errorf("cleanText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSnippetText(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 300)
	result := snippetText(long, 200)
	if len(result) != 203 { // 200 + "..."
		t.Errorf("expected 203 chars, got %d", len(result))
	}
	if !strings.HasSuffix(result, "...") {
		t.Errorf("expected '...' suffix, got: %s", result)
	}

	short := "short text"
	result = snippetText(short, 200)
	if result != "short text" {
		t.Errorf("expected 'short text', got: %s", result)
	}
}
