package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"murmur/internal/rag"
	"murmur/internal/tools"
)

// maxRAGResultPreview is the maximum length of content shown per search result.
const maxRAGResultPreview = 500

// RegisterRAGTools registers the memory_search and memory_ingest server-side
// tools on the given ToolRegistry. The memory_search tool is available to all
// users; memory_ingest is intended for admin use (enforced via permissions).
func RegisterRAGTools(registry *ToolRegistry, store *rag.RAGStore) error {
	if store == nil {
		return fmt.Errorf("RegisterRAGTools: store must not be nil")
	}

	ragTools := []tools.Tool{
		{
			Name:        "memory_search",
			Description: "Search the long-term memory store for relevant information. Uses full-text search (and optionally semantic search) over ingested documents, conversation summaries, and notes.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {
						"type": "string",
						"description": "Search query text"
					},
					"limit": {
						"type": "integer",
						"description": "Maximum number of results to return. Defaults to 5."
					}
				},
				"required": ["query"]
			}`),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				query, err := tools.RequireStringArg(args, "query")
				if err != nil {
					return "", err
				}
				limit := tools.OptionalIntArg(args, "limit", 5)
				if limit <= 0 {
					limit = 5
				}
				if limit > 20 {
					limit = 20
				}

				results, err := store.Search(ctx, query, limit)
				if err != nil {
					return "", fmt.Errorf("memory_search: %w", err)
				}
				if len(results) == 0 {
					return fmt.Sprintf("No results found for %q.", query), nil
				}

				var sb strings.Builder
				fmt.Fprintf(&sb, "Found %d result(s) for %q:\n\n", len(results), query)
				for i, r := range results {
					content := r.Content
					if len(content) > maxRAGResultPreview {
						content = content[:maxRAGResultPreview] + "..."
					}
					fmt.Fprintf(&sb, "[%d] Source: %s (score: %.4f)\n%s\n\n", i+1, r.Source, r.Score, content)
				}
				return sb.String(), nil
			},
		},
		{
			Name:        "memory_ingest",
			Description: "Ingest text content into the long-term memory store for later retrieval via memory_search. Content is automatically chunked and indexed.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"source": {
						"type": "string",
						"description": "Source label for the content (e.g., 'meeting-notes-2024-01', 'project-readme')"
					},
					"content": {
						"type": "string",
						"description": "Text content to ingest"
					}
				},
				"required": ["source", "content"]
			}`),
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				source, err := tools.RequireStringArg(args, "source")
				if err != nil {
					return "", err
				}
				content, err := tools.RequireStringArg(args, "content")
				if err != nil {
					return "", err
				}

				if err := store.Ingest(source, content); err != nil {
					return "", fmt.Errorf("memory_ingest: %w", err)
				}

				count, err := store.DocumentCount()
				if err != nil {
					return fmt.Sprintf("Ingested content from %q.", source), nil
				}
				return fmt.Sprintf("Ingested content from %q. Total chunks in store: %d.", source, count), nil
			},
		},
	}

	for _, t := range ragTools {
		if err := registry.Register(t); err != nil {
			return fmt.Errorf("RegisterRAGTools: %w", err)
		}
	}
	return nil
}
