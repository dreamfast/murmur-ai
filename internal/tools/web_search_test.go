package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebSearch_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers.
		if r.Header.Get("X-Subscription-Token") != "test-api-key" {
			t.Errorf("expected API key header, got %q", r.Header.Get("X-Subscription-Token"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept header, got %q", r.Header.Get("Accept"))
		}

		// Verify query parameter.
		q := r.URL.Query().Get("q")
		if q != "golang testing" {
			t.Errorf("expected query 'golang testing', got %q", q)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"web": {
				"results": [
					{
						"title": "Go Testing",
						"url": "https://go.dev/doc/testing",
						"description": "Learn about testing in Go"
					},
					{
						"title": "Go Test Package",
						"url": "https://pkg.go.dev/testing",
						"description": "Package testing provides support for automated testing"
					}
				]
			}
		}`))
	}))
	defer server.Close()

	// Override the API URL by using a custom client that redirects.
	cfg := WebSearchToolConfig{
		APIKey:     "test-api-key",
		MaxResults: 5,
	}
	client := server.Client()

	// Create tool with mock server URL.
	tool := Tool{
		Name: "web_search",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleWebSearchWithURL(ctx, args, cfg.APIKey, cfg.MaxResults, client, server.URL+"/res/v1/web/search")
		},
	}

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
	if !strings.Contains(result, "1.") && !strings.Contains(result, "2.") {
		t.Error("expected numbered results")
	}
}

func TestWebSearch_EmptyResults(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"web": {"results": []}}`))
	}))
	defer server.Close()

	client := server.Client()
	result, err := handleWebSearchWithURL(
		context.Background(),
		map[string]any{"query": "nonexistent query xyz"},
		"test-key", 5, client, server.URL+"/res/v1/web/search",
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "No results found") {
		t.Errorf("expected 'No results found', got %q", result)
	}
}

func TestWebSearch_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": "invalid API key"}`))
	}))
	defer server.Close()

	client := server.Client()
	_, err := handleWebSearchWithURL(
		context.Background(),
		map[string]any{"query": "test"},
		"bad-key", 5, client, server.URL+"/res/v1/web/search",
	)
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected status 403 in error, got %q", err.Error())
	}
}

func TestWebSearch_Timeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := handleWebSearchWithURL(
		context.Background(),
		map[string]any{"query": "test"},
		"test-key", 5, client, server.URL+"/res/v1/web/search",
	)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
}

func TestWebSearch_RequiredQuery(t *testing.T) {
	t.Parallel()

	cfg := WebSearchToolConfig{APIKey: "test-key", MaxResults: 5}
	tool := NewWebSearchTool(cfg, nil)

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected 'missing required argument' error, got %q", err.Error())
	}
}

func TestWebSearch_MaxResultsCapped(t *testing.T) {
	t.Parallel()

	// Server records the count parameter it receives.
	var receivedCount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCount = r.URL.Query().Get("count")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"web": {"results": [
			{"title": "R1", "url": "https://example.com/1", "description": "D1"},
			{"title": "R2", "url": "https://example.com/2", "description": "D2"},
			{"title": "R3", "url": "https://example.com/3", "description": "D3"},
			{"title": "R4", "url": "https://example.com/4", "description": "D4"}
		]}}`))
	}))
	defer server.Close()

	// Config with MaxResults=3 — a request for count=10 should be capped to 3.
	client := server.Client()
	result, err := handleWebSearchWithURL(
		context.Background(),
		map[string]any{"query": "test", "count": float64(10)},
		"test-key", 3, client, server.URL+"/res/v1/web/search",
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Verify the API received the capped count.
	if receivedCount != "3" {
		t.Errorf("expected count=3 sent to API, got %q", receivedCount)
	}

	// Verify output only contains 3 results even though API returned 4.
	if strings.Contains(result, "R4") {
		t.Error("expected only 3 results, but got R4")
	}
	if !strings.Contains(result, "R1") || !strings.Contains(result, "R2") || !strings.Contains(result, "R3") {
		t.Error("expected R1, R2, R3 in output")
	}
}

func TestWebSearch_CountParameter(t *testing.T) {
	t.Parallel()

	// Server returns 5 results.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := r.URL.Query().Get("count")
		if count != "2" {
			t.Errorf("expected count=2, got %q", count)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"web": {
				"results": [
					{"title": "Result 1", "url": "https://example.com/1", "description": "First"},
					{"title": "Result 2", "url": "https://example.com/2", "description": "Second"},
					{"title": "Result 3", "url": "https://example.com/3", "description": "Third"}
				]
			}
		}`))
	}))
	defer server.Close()

	client := server.Client()
	result, err := handleWebSearchWithURL(
		context.Background(),
		map[string]any{"query": "test", "count": float64(2)},
		"test-key", 5, client, server.URL+"/res/v1/web/search",
	)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Should only show 2 results even though API returned 3.
	if strings.Contains(result, "Result 3") {
		t.Error("expected only 2 results, but got Result 3")
	}
	if !strings.Contains(result, "Result 1") || !strings.Contains(result, "Result 2") {
		t.Error("expected Result 1 and Result 2 in output")
	}
}
