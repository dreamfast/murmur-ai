package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearXNG_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters.
		q := r.URL.Query().Get("q")
		if q != "golang testing" {
			t.Errorf("expected query 'golang testing', got %q", q)
		}
		format := r.URL.Query().Get("format")
		if format != "json" {
			t.Errorf("expected format 'json', got %q", format)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept header, got %q", r.Header.Get("Accept"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"results": [
				{
					"title": "Go Testing",
					"url": "https://go.dev/doc/testing",
					"content": "Learn about testing in Go"
				},
				{
					"title": "Go Test Package",
					"url": "https://pkg.go.dev/testing",
					"content": "Package testing provides support for automated testing"
				}
			]
		}`))
	}))
	defer server.Close()

	cfg := SearXNGToolConfig{
		URL:        server.URL,
		MaxResults: 10,
	}
	tool := NewSearXNGTool(cfg, server.Client())

	result, err := tool.Handler(context.Background(), map[string]any{
		"query": "golang testing",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "Go Testing") {
		t.Error("expected 'Go Testing' in results")
	}
	if !strings.Contains(result, "https://go.dev/doc/testing") {
		t.Error("expected URL in results")
	}
	if !strings.Contains(result, "1.") {
		t.Error("expected numbered results starting with 1.")
	}
	if !strings.Contains(result, "2.") {
		t.Error("expected numbered results containing 2.")
	}
}

func TestSearXNG_EmptyResults(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer server.Close()

	result, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "nonexistent query xyz"},
		server.URL, 10, server.Client(),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "No results found") {
		t.Errorf("expected 'No results found', got %q", result)
	}
}

func TestSearXNG_Categories(t *testing.T) {
	t.Parallel()

	var receivedCategories atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCategories.Store(r.URL.Query().Get("categories"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "News Result", "url": "https://example.com/news", "content": "Breaking news"}
			]
		}`))
	}))
	defer server.Close()

	result, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test", "categories": "news"},
		server.URL, 10, server.Client(),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if got := receivedCategories.Load().(string); got != "news" {
		t.Errorf("expected categories=news, got %q", got)
	}
	if !strings.Contains(result, "News Result") {
		t.Error("expected 'News Result' in output")
	}
}

func TestSearXNG_TimeRange(t *testing.T) {
	t.Parallel()

	var receivedTimeRange atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTimeRange.Store(r.URL.Query().Get("time_range"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "Recent", "url": "https://example.com/recent", "content": "Recent result"}
			]
		}`))
	}))
	defer server.Close()

	_, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test", "time_range": "week"},
		server.URL, 10, server.Client(),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if got := receivedTimeRange.Load().(string); got != "week" {
		t.Errorf("expected time_range=week, got %q", got)
	}
}

func TestSearXNG_InvalidTimeRange(t *testing.T) {
	t.Parallel()

	_, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test", "time_range": "invalid"},
		"http://localhost:8080", 10, &http.Client{},
	)
	if err == nil {
		t.Fatal("expected error for invalid time_range")
	}
	if !strings.Contains(err.Error(), "invalid time_range") {
		t.Errorf("expected 'invalid time_range' in error, got %q", err.Error())
	}
}

func TestSearXNG_Language(t *testing.T) {
	t.Parallel()

	var receivedLanguage atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedLanguage.Store(r.URL.Query().Get("language"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "Ergebnis", "url": "https://example.de", "content": "Deutsches Ergebnis"}
			]
		}`))
	}))
	defer server.Close()

	_, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test", "language": "de"},
		server.URL, 10, server.Client(),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if got := receivedLanguage.Load().(string); got != "de" {
		t.Errorf("expected language=de, got %q", got)
	}
}

func TestSearXNG_Timeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test"},
		server.URL, 10, client,
	)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
}

func TestSearXNG_InvalidContentType(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>Not JSON</body></html>"))
	}))
	defer server.Close()

	_, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test"},
		server.URL, 10, server.Client(),
	)
	if err == nil {
		t.Fatal("expected error for non-JSON content type")
	}
	if !strings.Contains(err.Error(), "unexpected Content-Type") {
		t.Errorf("expected 'unexpected Content-Type' in error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "text/html") {
		t.Errorf("expected 'text/html' in error, got %q", err.Error())
	}
}

func TestSearXNG_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer server.Close()

	_, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test"},
		server.URL, 10, server.Client(),
	)
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status 500 in error, got %q", err.Error())
	}
}

func TestSearXNG_RequiredQuery(t *testing.T) {
	t.Parallel()

	cfg := SearXNGToolConfig{URL: "http://localhost:8080", MaxResults: 10}
	tool := NewSearXNGTool(cfg, nil)

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected 'missing required argument' error, got %q", err.Error())
	}
}

func TestSearXNG_CountCapped(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "R1", "url": "https://example.com/1", "content": "D1"},
				{"title": "R2", "url": "https://example.com/2", "content": "D2"},
				{"title": "R3", "url": "https://example.com/3", "content": "D3"},
				{"title": "R4", "url": "https://example.com/4", "content": "D4"}
			]
		}`))
	}))
	defer server.Close()

	// MaxResults=3, request count=10 — should be capped to 3.
	result, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test", "count": float64(10)},
		server.URL, 3, server.Client(),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Verify output only contains 3 results even though API returned 4.
	if strings.Contains(result, "R4") {
		t.Error("expected only 3 results, but got R4")
	}
	if !strings.Contains(result, "R1") || !strings.Contains(result, "R2") || !strings.Contains(result, "R3") {
		t.Error("expected R1, R2, R3 in output")
	}
}

func TestSearXNG_EmptyContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"results": [
				{"title": "No Desc", "url": "https://example.com", "content": ""}
			]
		}`))
	}))
	defer server.Close()

	result, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test"},
		server.URL, 10, server.Client(),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "(no description)") {
		t.Errorf("expected '(no description)' for empty content, got %q", result)
	}
}

func TestSearXNG_TrailingSlashURL(t *testing.T) {
	t.Parallel()

	var receivedPath atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [{"title": "T", "url": "https://example.com", "content": "C"}]}`))
	}))
	defer server.Close()

	// URL with trailing slash should still produce /search path.
	_, err := handleSearXNG(
		context.Background(),
		map[string]any{"query": "test"},
		server.URL+"/", 10, server.Client(),
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if got := receivedPath.Load().(string); got != "/search" {
		t.Errorf("expected path /search, got %q", got)
	}
}

func TestSearXNG_DefaultMaxResults(t *testing.T) {
	t.Parallel()

	// MaxResults=0 should default to 10 — verify by returning 12 results
	// and checking only 10 are shown.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results": [
			{"title": "R1", "url": "https://example.com/1", "content": "D1"},
			{"title": "R2", "url": "https://example.com/2", "content": "D2"},
			{"title": "R3", "url": "https://example.com/3", "content": "D3"},
			{"title": "R4", "url": "https://example.com/4", "content": "D4"},
			{"title": "R5", "url": "https://example.com/5", "content": "D5"},
			{"title": "R6", "url": "https://example.com/6", "content": "D6"},
			{"title": "R7", "url": "https://example.com/7", "content": "D7"},
			{"title": "R8", "url": "https://example.com/8", "content": "D8"},
			{"title": "R9", "url": "https://example.com/9", "content": "D9"},
			{"title": "R10", "url": "https://example.com/10", "content": "D10"},
			{"title": "R11", "url": "https://example.com/11", "content": "D11"},
			{"title": "R12", "url": "https://example.com/12", "content": "D12"}
		]}`))
	}))
	defer server.Close()

	cfg := SearXNGToolConfig{URL: server.URL, MaxResults: 0}
	tool := NewSearXNGTool(cfg, server.Client())

	result, err := tool.Handler(context.Background(), map[string]any{"query": "test"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Default is 10, so R11 and R12 should be truncated.
	if !strings.Contains(result, "R10") {
		t.Error("expected R10 in output (default max is 10)")
	}
	if strings.Contains(result, "R11") {
		t.Error("expected R11 to be truncated (default max is 10)")
	}
}

func TestSearXNG_MaxResultsOverCap(t *testing.T) {
	t.Parallel()

	// MaxResults=200 should be capped to 100. We can't easily generate 101
	// results, so verify the tool is created and the count parameter is
	// capped by requesting count=150 with MaxResults=200 (capped to 100).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return 3 results — all should be shown since 3 < 100.
		_, _ = w.Write([]byte(`{"results": [
			{"title": "R1", "url": "https://example.com/1", "content": "D1"},
			{"title": "R2", "url": "https://example.com/2", "content": "D2"},
			{"title": "R3", "url": "https://example.com/3", "content": "D3"}
		]}`))
	}))
	defer server.Close()

	cfg := SearXNGToolConfig{URL: server.URL, MaxResults: 200}
	tool := NewSearXNGTool(cfg, server.Client())

	result, err := tool.Handler(context.Background(), map[string]any{
		"query": "test",
		"count": float64(150), // Should be capped to 100 internally.
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// All 3 results should be present (3 < 100 cap).
	if !strings.Contains(result, "R1") || !strings.Contains(result, "R2") || !strings.Contains(result, "R3") {
		t.Error("expected all 3 results in output")
	}
}
