package rag

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// SearchResult represents a single search result from the RAG store.
type SearchResult struct {
	// Source is the document source identifier.
	Source string
	// Content is the chunk text.
	Content string
	// Score is the relevance score (higher is better).
	Score float64
	// ChunkID is the unique chunk identifier.
	ChunkID string
}

// Search performs a full-text search over the memory_documents table using
// FTS5. If an embedding provider is configured, it also performs semantic
// search and merges results using Reciprocal Rank Fusion (RRF).
func (s *RAGStore) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	// Always perform FTS5 search.
	ftsResults, err := s.searchFTS5(query, limit*2) // fetch more for RRF merge
	if err != nil {
		return nil, fmt.Errorf("Search: fts5: %w", err)
	}

	// If no embedder, return FTS5 results directly.
	if s.embedder == nil {
		if len(ftsResults) > limit {
			ftsResults = ftsResults[:limit]
		}
		return ftsResults, nil
	}

	// Perform semantic search via embeddings.
	semanticResults, err := s.searchSemantic(ctx, query, limit*2)
	if err != nil {
		// Fall back to FTS5-only on embedding error.
		s.logger.Warn("semantic search failed, using FTS5 only", "error", err)
		if len(ftsResults) > limit {
			ftsResults = ftsResults[:limit]
		}
		return ftsResults, nil
	}

	// Merge with Reciprocal Rank Fusion.
	merged := mergeRRF(ftsResults, semanticResults, limit)
	return merged, nil
}

// searchFTS5 performs a full-text search using the FTS5 index.
func (s *RAGStore) searchFTS5(query string, limit int) ([]SearchResult, error) {
	// Escape FTS5 special characters and wrap terms in quotes for safety.
	safeQuery := escapeFTS5Query(query)
	if safeQuery == "" {
		return nil, nil
	}

	rows, err := s.db.Query(`
		SELECT md.source, md.content, md.chunk_id, rank
		FROM memory_documents_fts fts
		JOIN memory_documents md ON md.id = fts.rowid
		WHERE memory_documents_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, safeQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("searchFTS5: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var rank float64
		if err := rows.Scan(&r.Source, &r.Content, &r.ChunkID, &rank); err != nil {
			return nil, fmt.Errorf("searchFTS5: scan: %w", err)
		}
		// FTS5 rank is negative (lower = better), negate for our scoring.
		r.Score = -rank
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("searchFTS5: rows: %w", err)
	}
	return results, nil
}

// searchSemantic performs embedding-based semantic search by computing cosine
// similarity between the query embedding and all stored embeddings.
//
// Performance note: this loads all embedded documents into memory for brute-force
// cosine similarity. This is feasible for corpora up to ~10K chunks (~30MB at
// 768 dimensions). For larger corpora, consider adding approximate nearest
// neighbor indexing (e.g., HNSW) or pre-filtering by FTS5 candidates.
func (s *RAGStore) searchSemantic(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	// Embed the query.
	embeddings, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("searchSemantic: embed query: %w", err)
	}
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("searchSemantic: empty embedding returned")
	}
	queryVec := embeddings[0]

	// Load all documents with embeddings.
	rows, err := s.db.Query(`
		SELECT source, content, chunk_id, embedding
		FROM memory_documents
		WHERE embedding IS NOT NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("searchSemantic: query: %w", err)
	}
	defer rows.Close()

	type scored struct {
		result SearchResult
		score  float64
	}
	var candidates []scored

	for rows.Next() {
		var source, content, chunkID string
		var embBlob sql.RawBytes
		if err := rows.Scan(&source, &content, &chunkID, &embBlob); err != nil {
			return nil, fmt.Errorf("searchSemantic: scan: %w", err)
		}
		docVec := DecodeEmbedding(embBlob)
		if len(docVec) == 0 {
			continue
		}
		sim := CosineSimilarity(queryVec, docVec)
		candidates = append(candidates, scored{
			result: SearchResult{
				Source:  source,
				Content: content,
				ChunkID: chunkID,
			},
			score: sim,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("searchSemantic: rows: %w", err)
	}

	// Sort by similarity descending.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	results := make([]SearchResult, len(candidates))
	for i, c := range candidates {
		c.result.Score = c.score
		results[i] = c.result
	}
	return results, nil
}

// mergeRRF merges two ranked result lists using Reciprocal Rank Fusion.
// RRF score = sum(1 / (k + rank_i)) where k=60 is the standard constant.
func mergeRRF(ftsResults, semanticResults []SearchResult, limit int) []SearchResult {
	const k = 60.0

	scores := make(map[string]float64)
	byChunk := make(map[string]SearchResult)

	for rank, r := range ftsResults {
		scores[r.ChunkID] += 1.0 / (k + float64(rank+1))
		byChunk[r.ChunkID] = r
	}
	for rank, r := range semanticResults {
		scores[r.ChunkID] += 1.0 / (k + float64(rank+1))
		if _, exists := byChunk[r.ChunkID]; !exists {
			byChunk[r.ChunkID] = r
		}
	}

	type scored struct {
		chunkID string
		score   float64
	}
	var ranked []scored
	for id, score := range scores {
		ranked = append(ranked, scored{id, score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	results := make([]SearchResult, len(ranked))
	for i, r := range ranked {
		result := byChunk[r.chunkID]
		result.Score = r.score
		results[i] = result
	}
	return results
}

// escapeFTS5Query escapes special FTS5 characters and wraps each term in
// double quotes to prevent query syntax errors. Terms are joined with OR
// for broad matching.
func escapeFTS5Query(query string) string {
	// Split into words and quote each one.
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}

	var quoted []string
	for _, w := range words {
		// Remove any existing quotes and FTS5 operators.
		w = strings.ReplaceAll(w, `"`, "")
		w = strings.TrimSpace(w)
		if w == "" || w == "OR" || w == "AND" || w == "NOT" || w == "NEAR" {
			continue
		}
		quoted = append(quoted, `"`+w+`"`)
	}
	if len(quoted) == 0 {
		return ""
	}
	return strings.Join(quoted, " OR ")
}
