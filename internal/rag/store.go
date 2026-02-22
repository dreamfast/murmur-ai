// Package rag provides retrieval-augmented generation (RAG) capabilities
// using SQLite FTS5 for full-text search and optional embedding-based
// semantic search with Reciprocal Rank Fusion (RRF) for hybrid results.
package rag

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"murmur/internal/db"
)

// Default chunking parameters.
const (
	// DefaultChunkSize is the target size in characters for each chunk.
	DefaultChunkSize = 2048
	// DefaultChunkOverlap is the number of overlapping characters between
	// consecutive chunks to preserve context across boundaries.
	DefaultChunkOverlap = 256
)

// RAGStore provides ingestion and search over a corpus of text documents
// stored in SQLite with FTS5 full-text indexing. Documents are split into
// chunks for granular retrieval.
type RAGStore struct {
	db       *db.DB
	embedder EmbeddingProvider
	logger   *slog.Logger

	chunkSize    int
	chunkOverlap int
}

// RAGStoreConfig holds configuration for creating a RAGStore.
type RAGStoreConfig struct {
	// ChunkSize is the target chunk size in characters. Defaults to DefaultChunkSize.
	ChunkSize int
	// ChunkOverlap is the overlap between chunks in characters. Zero means no
	// overlap. Negative values default to DefaultChunkOverlap (256).
	ChunkOverlap int
}

// NewRAGStore creates a new RAGStore backed by the given database.
// The embedder may be nil to disable embedding-based search (FTS5-only mode).
func NewRAGStore(database *db.DB, embedder EmbeddingProvider, logger *slog.Logger, cfg RAGStoreConfig) *RAGStore {
	chunkSize := cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	chunkOverlap := cfg.ChunkOverlap
	if chunkOverlap < 0 {
		chunkOverlap = DefaultChunkOverlap
	}
	return &RAGStore{
		db:           database,
		embedder:     embedder,
		logger:       logger,
		chunkSize:    chunkSize,
		chunkOverlap: chunkOverlap,
	}
}

// Ingest splits content into chunks and stores them in the memory_documents
// table. Chunks are deduplicated via chunk_id = sha256(source + chunk_index).
// Existing chunks with the same chunk_id are updated (upsert). If an embedding
// provider is configured, embeddings are generated in batch and stored alongside
// the content. Stale chunks from prior ingestions of the same source (with a
// higher chunk index) are deleted to prevent outdated content from lingering.
func (s *RAGStore) Ingest(source, content string) error {
	if strings.TrimSpace(source) == "" {
		return fmt.Errorf("Ingest: source must not be empty")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("Ingest: content must not be empty")
	}

	chunks := chunkText(content, s.chunkSize, s.chunkOverlap)
	if len(chunks) == 0 {
		return nil
	}

	// Generate embeddings in batch if an embedder is configured.
	var embeddings [][]byte
	if s.embedder != nil {
		vecs, err := s.embedder.Embed(context.Background(), chunks)
		if err != nil {
			// Embedding failure is non-fatal — fall back to FTS5-only.
			s.logger.Warn("Ingest: embedding failed, storing without embeddings",
				"source", source,
				"error", err,
			)
		} else if len(vecs) == len(chunks) {
			embeddings = make([][]byte, len(vecs))
			for i, v := range vecs {
				embeddings[i] = EncodeEmbedding(v)
			}
		} else {
			s.logger.Warn("Ingest: embedding count mismatch, storing without embeddings",
				"source", source,
				"chunks", len(chunks),
				"embeddings", len(vecs),
			)
		}
	}

	// Build the set of chunk IDs for this ingestion to delete stale chunks.
	chunkIDs := make([]string, len(chunks))
	for i := range chunks {
		chunkIDs[i] = makeChunkID(source, i)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("Ingest: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO memory_documents (source, chunk_id, content, embedding, updated)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(chunk_id) DO UPDATE SET
			content = excluded.content,
			source = excluded.source,
			embedding = excluded.embedding,
			updated = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return fmt.Errorf("Ingest: prepare: %w", err)
	}
	defer stmt.Close()

	for i, chunk := range chunks {
		var emb []byte
		if i < len(embeddings) {
			emb = embeddings[i]
		}
		if _, err := stmt.Exec(source, chunkIDs[i], chunk, emb); err != nil {
			return fmt.Errorf("Ingest: insert chunk %d: %w", i, err)
		}
	}

	// Delete stale chunks from prior ingestions of this source that are no
	// longer part of the current chunk set. This handles the case where
	// re-ingesting with shorter content produces fewer chunks.
	if len(chunkIDs) > 0 {
		placeholders := strings.Repeat("?,", len(chunkIDs))
		placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
		args := make([]any, 0, 1+len(chunkIDs))
		args = append(args, source)
		for _, id := range chunkIDs {
			args = append(args, id)
		}
		_, err := tx.Exec(
			`DELETE FROM memory_documents WHERE source = ? AND chunk_id NOT IN (`+placeholders+`)`,
			args...,
		)
		if err != nil {
			return fmt.Errorf("Ingest: delete stale chunks: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("Ingest: commit: %w", err)
	}

	s.logger.Debug("ingested document",
		"source", source,
		"chunks", len(chunks),
		"embeddings", len(embeddings) > 0,
	)

	return nil
}

// IngestFile reads a file from disk and ingests its content. The file path
// is used as the source identifier. Paths starting with ~ are expanded to
// the user's home directory.
func (s *RAGStore) IngestFile(path string) error {
	expanded, err := expandHome(path)
	if err != nil {
		return fmt.Errorf("IngestFile: %w", err)
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		return fmt.Errorf("IngestFile: %w", err)
	}
	return s.Ingest(expanded, string(data))
}

// expandHome replaces a leading ~/ or standalone ~ with the user's home
// directory. Does not handle ~user forms (e.g., ~alice/file).
func expandHome(path string) (string, error) {
	if path == "~" {
		return os.UserHomeDir()
	}
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expandHome: %w", err)
	}
	return filepath.Join(home, path[2:]), nil
}

// DocumentCount returns the total number of chunks in the store.
func (s *RAGStore) DocumentCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM memory_documents`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("DocumentCount: %w", err)
	}
	return count, nil
}

// DeleteSource removes all chunks for a given source.
func (s *RAGStore) DeleteSource(source string) error {
	_, err := s.db.Exec(`DELETE FROM memory_documents WHERE source = ?`, source)
	if err != nil {
		return fmt.Errorf("DeleteSource: %w", err)
	}
	return nil
}

// makeChunkID generates a deterministic chunk ID from source and index.
func makeChunkID(source string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", source, index)))
	return fmt.Sprintf("%x", h[:16]) // 32 hex chars
}

// chunkText splits text into chunks of approximately targetSize characters
// with overlap characters of overlap between consecutive chunks.
//
// Splitting strategy (in order of preference):
//  1. Paragraph boundaries (double newline)
//  2. Sentence boundaries (". " followed by uppercase or newline)
//  3. Hard character boundary at targetSize
func chunkText(text string, targetSize, overlap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= targetSize {
		return []string{text}
	}

	var chunks []string
	start := 0

	for start < len(text) {
		end := start + targetSize
		if end >= len(text) {
			chunks = append(chunks, strings.TrimSpace(text[start:]))
			break
		}

		// Try to find a paragraph boundary (double newline) near the end.
		breakPoint := findBreak(text, start, end, "\n\n")
		if breakPoint < 0 {
			// Try sentence boundary.
			breakPoint = findSentenceBreak(text, start, end)
		}
		if breakPoint < 0 {
			// Try single newline.
			breakPoint = findBreak(text, start, end, "\n")
		}
		if breakPoint < 0 {
			// Hard break at targetSize.
			breakPoint = end
		}

		chunk := strings.TrimSpace(text[start:breakPoint])
		if chunk != "" {
			chunks = append(chunks, chunk)
		}

		// Advance with overlap.
		start = breakPoint - overlap
		if start < 0 {
			start = 0
		}
		// Ensure forward progress.
		if start <= (breakPoint - targetSize + overlap) {
			start = breakPoint
		}
	}

	return chunks
}

// findBreak searches backward from end for the delimiter, returning the
// position after the delimiter. Returns -1 if not found in the search window
// (last 25% of the chunk).
func findBreak(text string, start, end int, delim string) int {
	// Search in the last 25% of the chunk for a natural break.
	searchStart := start + (end-start)*3/4
	idx := strings.LastIndex(text[searchStart:end], delim)
	if idx < 0 {
		return -1
	}
	return searchStart + idx + len(delim)
}

// findSentenceBreak searches backward from end for a sentence boundary
// (". " pattern). Returns -1 if not found.
func findSentenceBreak(text string, start, end int) int {
	searchStart := start + (end-start)*3/4
	idx := strings.LastIndex(text[searchStart:end], ". ")
	if idx < 0 {
		return -1
	}
	// Break after the period and space.
	return searchStart + idx + 2
}
