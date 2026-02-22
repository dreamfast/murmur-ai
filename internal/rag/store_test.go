package rag

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"murmur/internal/db"
)

// newTestRAGStore creates an in-memory RAGStore for testing.
func newTestRAGStore(t *testing.T) *RAGStore {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewRAGStore(database, nil, logger, RAGStoreConfig{})
}

func TestRAGIngest_ChunksCorrectly(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	// Create content larger than the default chunk size (2048 chars).
	// Use paragraphs separated by double newlines for natural breaks.
	var sb strings.Builder
	for i := 0; i < 5; i++ {
		sb.WriteString(strings.Repeat("word ", 250)) // ~1250 chars per paragraph
		sb.WriteString("\n\n")
	}
	content := sb.String() // ~6250 chars total

	if err := store.Ingest("test-doc", content); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	count, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount: %v", err)
	}
	if count < 2 {
		t.Errorf("expected at least 2 chunks for %d chars, got %d", len(content), count)
	}
}

func TestRAGIngest_SmallContent(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	if err := store.Ingest("small", "Hello, world!"); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	count, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 chunk for small content, got %d", count)
	}
}

func TestRAGSearch_FTS5(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	// Ingest documents with distinct content.
	if err := store.Ingest("golang-guide", "Go is a statically typed compiled programming language designed at Google."); err != nil {
		t.Fatalf("Ingest golang: %v", err)
	}
	if err := store.Ingest("python-guide", "Python is a high-level interpreted programming language known for readability."); err != nil {
		t.Fatalf("Ingest python: %v", err)
	}
	if err := store.Ingest("cooking-recipe", "To make pasta, boil water and add salt before cooking the noodles."); err != nil {
		t.Fatalf("Ingest cooking: %v", err)
	}

	results, err := store.Search(context.Background(), "Go programming language", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'Go programming language'")
	}

	// The Go guide should be in the results.
	found := false
	for _, r := range results {
		if r.Source == "golang-guide" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected golang-guide in results, got sources: %v", resultSources(results))
	}
}

func TestRAGSearch_EmptyStore(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	results, err := store.Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty store, got %d", len(results))
	}
}

func TestRAGSearch_EmptyQuery(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	results, err := store.Search(context.Background(), "", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for empty query, got %v", results)
	}
}

func TestRAGIngest_Deduplication(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	content := "This is a test document for deduplication."

	if err := store.Ingest("dedup-source", content); err != nil {
		t.Fatalf("Ingest 1: %v", err)
	}
	count1, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount 1: %v", err)
	}

	// Ingest the same content again with the same source.
	if err := store.Ingest("dedup-source", content); err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}
	count2, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount 2: %v", err)
	}

	if count1 != count2 {
		t.Errorf("deduplication failed: count after first ingest = %d, after second = %d", count1, count2)
	}
}

func TestRAGIngest_UpdateContent(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	if err := store.Ingest("updatable", "Original content here."); err != nil {
		t.Fatalf("Ingest original: %v", err)
	}

	// Re-ingest with updated content.
	if err := store.Ingest("updatable", "Updated content here."); err != nil {
		t.Fatalf("Ingest updated: %v", err)
	}

	// Search for the updated content.
	results, err := store.Search(context.Background(), "Updated", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for updated content")
	}
	if !strings.Contains(results[0].Content, "Updated") {
		t.Errorf("expected updated content, got: %s", results[0].Content)
	}
}

func TestRAGIngest_EmptySource(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	err := store.Ingest("", "some content")
	if err == nil {
		t.Fatal("expected error for empty source")
	}
}

func TestRAGIngest_EmptyContent(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	err := store.Ingest("source", "")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestDeleteSource(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	if err := store.Ingest("to-delete", "Content that will be deleted."); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := store.Ingest("to-keep", "Content that will be kept."); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	count, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 chunks, got %d", count)
	}

	if err := store.DeleteSource("to-delete"); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}

	count, err = store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount after delete: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 chunk after delete, got %d", count)
	}

	// Verify the deleted source is not searchable.
	results, err := store.Search(context.Background(), "deleted", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.Source == "to-delete" {
			t.Errorf("deleted source still appears in search results")
		}
	}
}

func TestDocumentCount(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	count, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for empty store, got %d", count)
	}

	if err := store.Ingest("doc1", "First document."); err != nil {
		t.Fatalf("Ingest 1: %v", err)
	}
	if err := store.Ingest("doc2", "Second document."); err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}

	count, err = store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestChunkText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		text       string
		targetSize int
		overlap    int
		wantMin    int // minimum expected chunks
		wantMax    int // maximum expected chunks
	}{
		{
			name:       "empty text",
			text:       "",
			targetSize: 100,
			overlap:    10,
			wantMin:    0,
			wantMax:    0,
		},
		{
			name:       "whitespace only",
			text:       "   \n\n  ",
			targetSize: 100,
			overlap:    10,
			wantMin:    0,
			wantMax:    0,
		},
		{
			name:       "short text fits in one chunk",
			text:       "Hello, world!",
			targetSize: 100,
			overlap:    10,
			wantMin:    1,
			wantMax:    1,
		},
		{
			name:       "text exactly at target size",
			text:       strings.Repeat("a", 100),
			targetSize: 100,
			overlap:    10,
			wantMin:    1,
			wantMax:    1,
		},
		{
			name:       "text slightly over target size",
			text:       strings.Repeat("a", 101),
			targetSize: 100,
			overlap:    10,
			wantMin:    2,
			wantMax:    2,
		},
		{
			name:       "paragraph breaks used for splitting",
			text:       strings.Repeat("word ", 30) + "\n\n" + strings.Repeat("word ", 30),
			targetSize: 100,
			overlap:    10,
			wantMin:    2,
			wantMax:    5,
		},
		{
			name:       "zero overlap",
			text:       strings.Repeat("a", 300),
			targetSize: 100,
			overlap:    0,
			wantMin:    3,
			wantMax:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			chunks := chunkText(tt.text, tt.targetSize, tt.overlap)
			if len(chunks) < tt.wantMin || len(chunks) > tt.wantMax {
				t.Errorf("chunkText(%d chars, size=%d, overlap=%d) = %d chunks, want [%d, %d]",
					len(tt.text), tt.targetSize, tt.overlap, len(chunks), tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestChunkText_PreservesContent(t *testing.T) {
	t.Parallel()

	// Verify that all original content appears in at least one chunk.
	original := "First paragraph with important info.\n\nSecond paragraph with more details.\n\nThird paragraph concluding."
	chunks := chunkText(original, 60, 10)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Check that key phrases appear in the chunks.
	allChunks := strings.Join(chunks, " ")
	for _, phrase := range []string{"First paragraph", "Second paragraph", "Third paragraph"} {
		if !strings.Contains(allChunks, phrase) {
			t.Errorf("phrase %q not found in any chunk", phrase)
		}
	}
}

func TestEscapeFTS5Query(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple words",
			input: "hello world",
			want:  `"hello" OR "world"`,
		},
		{
			name:  "single word",
			input: "test",
			want:  `"test"`,
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "strips FTS5 operators",
			input: "hello OR world AND NOT test",
			want:  `"hello" OR "world" OR "test"`,
		},
		{
			name:  "strips NEAR operator",
			input: "NEAR hello",
			want:  `"hello"`,
		},
		{
			name:  "removes embedded quotes",
			input: `"hello" "world"`,
			want:  `"hello" OR "world"`,
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  "",
		},
		{
			name:  "all operators",
			input: "OR AND NOT NEAR",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := escapeFTS5Query(tt.input)
			if got != tt.want {
				t.Errorf("escapeFTS5Query(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEncodeDecodeEmbedding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vec  []float32
	}{
		{
			name: "simple vector",
			vec:  []float32{1.0, 2.0, 3.0},
		},
		{
			name: "negative values",
			vec:  []float32{-1.5, 0.0, 1.5},
		},
		{
			name: "single element",
			vec:  []float32{42.0},
		},
		{
			name: "empty vector",
			vec:  []float32{},
		},
		{
			name: "small values",
			vec:  []float32{0.001, 0.002, 0.003, 0.004},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encoded := EncodeEmbedding(tt.vec)
			decoded := DecodeEmbedding(encoded)

			if len(decoded) != len(tt.vec) {
				t.Fatalf("decoded length = %d, want %d", len(decoded), len(tt.vec))
			}
			for i := range tt.vec {
				if decoded[i] != tt.vec[i] {
					t.Errorf("decoded[%d] = %f, want %f", i, decoded[i], tt.vec[i])
				}
			}
		})
	}
}

func TestDecodeEmbedding_InvalidLength(t *testing.T) {
	t.Parallel()

	// Byte slice not divisible by 4 should return nil.
	result := DecodeEmbedding([]byte{1, 2, 3})
	if result != nil {
		t.Errorf("expected nil for invalid byte length, got %v", result)
	}
}

func TestCosineSimilarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		a, b    []float32
		wantMin float64
		wantMax float64
	}{
		{
			name:    "identical vectors",
			a:       []float32{1, 0, 0},
			b:       []float32{1, 0, 0},
			wantMin: 0.999,
			wantMax: 1.001,
		},
		{
			name:    "orthogonal vectors",
			a:       []float32{1, 0, 0},
			b:       []float32{0, 1, 0},
			wantMin: -0.001,
			wantMax: 0.001,
		},
		{
			name:    "opposite vectors",
			a:       []float32{1, 0, 0},
			b:       []float32{-1, 0, 0},
			wantMin: -1.001,
			wantMax: -0.999,
		},
		{
			name:    "similar vectors",
			a:       []float32{1, 1, 0},
			b:       []float32{1, 0, 0},
			wantMin: 0.5,
			wantMax: 0.8,
		},
		{
			name:    "different lengths returns 0",
			a:       []float32{1, 0},
			b:       []float32{1, 0, 0},
			wantMin: -0.001,
			wantMax: 0.001,
		},
		{
			name:    "empty vectors returns 0",
			a:       []float32{},
			b:       []float32{},
			wantMin: -0.001,
			wantMax: 0.001,
		},
		{
			name:    "zero vector returns 0",
			a:       []float32{0, 0, 0},
			b:       []float32{1, 0, 0},
			wantMin: -0.001,
			wantMax: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CosineSimilarity(tt.a, tt.b)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("CosineSimilarity(%v, %v) = %f, want [%f, %f]",
					tt.a, tt.b, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestMergeRRF(t *testing.T) {
	t.Parallel()

	ftsResults := []SearchResult{
		{ChunkID: "a", Source: "src-a", Content: "content a", Score: 10},
		{ChunkID: "b", Source: "src-b", Content: "content b", Score: 8},
		{ChunkID: "c", Source: "src-c", Content: "content c", Score: 5},
	}
	semanticResults := []SearchResult{
		{ChunkID: "b", Source: "src-b", Content: "content b", Score: 0.9},
		{ChunkID: "d", Source: "src-d", Content: "content d", Score: 0.8},
		{ChunkID: "a", Source: "src-a", Content: "content a", Score: 0.7},
	}

	merged := mergeRRF(ftsResults, semanticResults, 3)

	if len(merged) != 3 {
		t.Fatalf("expected 3 results, got %d", len(merged))
	}

	// "a" and "b" appear in both lists, so they should have higher RRF scores.
	// "b" is rank 2 in FTS and rank 1 in semantic, "a" is rank 1 in FTS and rank 3 in semantic.
	// Both should be in the top results.
	topChunks := make(map[string]bool)
	for _, r := range merged {
		topChunks[r.ChunkID] = true
	}
	if !topChunks["a"] {
		t.Error("expected chunk 'a' in merged results (appears in both lists)")
	}
	if !topChunks["b"] {
		t.Error("expected chunk 'b' in merged results (appears in both lists)")
	}

	// Verify scores are positive.
	for _, r := range merged {
		if r.Score <= 0 {
			t.Errorf("expected positive RRF score for chunk %s, got %f", r.ChunkID, r.Score)
		}
	}
}

func TestMergeRRF_EmptyInputs(t *testing.T) {
	t.Parallel()

	// Both empty.
	merged := mergeRRF(nil, nil, 5)
	if len(merged) != 0 {
		t.Errorf("expected 0 results for empty inputs, got %d", len(merged))
	}

	// One empty.
	fts := []SearchResult{{ChunkID: "a", Source: "s", Content: "c", Score: 1}}
	merged = mergeRRF(fts, nil, 5)
	if len(merged) != 1 {
		t.Errorf("expected 1 result, got %d", len(merged))
	}
}

func TestMergeRRF_LimitRespected(t *testing.T) {
	t.Parallel()

	var results []SearchResult
	for i := 0; i < 10; i++ {
		results = append(results, SearchResult{
			ChunkID: string(rune('a' + i)),
			Source:  "src",
			Content: "content",
			Score:   float64(10 - i),
		})
	}

	merged := mergeRRF(results, nil, 3)
	if len(merged) != 3 {
		t.Errorf("expected 3 results (limit), got %d", len(merged))
	}
}

func TestRAGSearch_MultipleTerms(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	if err := store.Ingest("doc1", "The quick brown fox jumps over the lazy dog."); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if err := store.Ingest("doc2", "A slow red cat sleeps under the active tree."); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Search for terms that match doc1.
	results, err := store.Search(context.Background(), "quick fox", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Source != "doc1" {
		t.Errorf("expected doc1 as top result, got %s", results[0].Source)
	}
}

func TestMakeChunkID_Deterministic(t *testing.T) {
	t.Parallel()

	id1 := makeChunkID("source", 0)
	id2 := makeChunkID("source", 0)
	if id1 != id2 {
		t.Errorf("makeChunkID not deterministic: %s != %s", id1, id2)
	}

	// Different index produces different ID.
	id3 := makeChunkID("source", 1)
	if id1 == id3 {
		t.Errorf("different indices should produce different IDs")
	}

	// Different source produces different ID.
	id4 := makeChunkID("other", 0)
	if id1 == id4 {
		t.Errorf("different sources should produce different IDs")
	}

	// ID should be 32 hex chars.
	if len(id1) != 32 {
		t.Errorf("expected 32 char hex ID, got %d chars: %s", len(id1), id1)
	}
}

func TestRAGStoreConfig_Defaults(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Zero-value config: ChunkSize defaults to DefaultChunkSize,
	// ChunkOverlap of 0 is valid (no overlap), only negative triggers default.
	store := NewRAGStore(database, nil, logger, RAGStoreConfig{})
	if store.chunkSize != DefaultChunkSize {
		t.Errorf("chunkSize = %d, want %d", store.chunkSize, DefaultChunkSize)
	}
	if store.chunkOverlap != 0 {
		t.Errorf("chunkOverlap = %d, want 0 (zero is valid)", store.chunkOverlap)
	}

	// Negative overlap triggers default.
	storeNeg := NewRAGStore(database, nil, logger, RAGStoreConfig{ChunkOverlap: -1})
	if storeNeg.chunkOverlap != DefaultChunkOverlap {
		t.Errorf("chunkOverlap with -1 = %d, want %d", storeNeg.chunkOverlap, DefaultChunkOverlap)
	}

	// Custom config.
	store2 := NewRAGStore(database, nil, logger, RAGStoreConfig{ChunkSize: 512, ChunkOverlap: 64})
	if store2.chunkSize != 512 {
		t.Errorf("chunkSize = %d, want 512", store2.chunkSize)
	}
	if store2.chunkOverlap != 64 {
		t.Errorf("chunkOverlap = %d, want 64", store2.chunkOverlap)
	}
}

// mockEmbedder is a test embedding provider that returns deterministic vectors.
type mockEmbedder struct {
	dims int
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vec := make([]float32, m.dims)
		// Simple deterministic embedding: hash the text into the vector.
		for j, ch := range text {
			vec[j%m.dims] += float32(ch) / 1000.0
		}
		vecs[i] = vec
	}
	return vecs, nil
}

func (m *mockEmbedder) Dimensions() int { return m.dims }

func newTestRAGStoreWithEmbedder(t *testing.T) *RAGStore {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	embedder := &mockEmbedder{dims: 8}
	return NewRAGStore(database, embedder, logger, RAGStoreConfig{})
}

func TestRAGSearch_HybridWithEmbedder(t *testing.T) {
	t.Parallel()
	store := newTestRAGStoreWithEmbedder(t)

	// Ingest documents — embeddings will be generated by the mock embedder.
	if err := store.Ingest("go-doc", "Go is a statically typed compiled programming language."); err != nil {
		t.Fatalf("Ingest go: %v", err)
	}
	if err := store.Ingest("python-doc", "Python is a dynamically typed interpreted language."); err != nil {
		t.Fatalf("Ingest python: %v", err)
	}
	if err := store.Ingest("recipe-doc", "To bake a cake, preheat the oven to 350 degrees."); err != nil {
		t.Fatalf("Ingest recipe: %v", err)
	}

	// Verify embeddings were stored.
	var embCount int
	err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_documents WHERE embedding IS NOT NULL`).Scan(&embCount)
	if err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	if embCount != 3 {
		t.Errorf("expected 3 documents with embeddings, got %d", embCount)
	}

	// Search should use hybrid mode (FTS5 + semantic + RRF merge).
	results, err := store.Search(context.Background(), "programming language", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result from hybrid search")
	}

	// Programming-related docs should appear in results.
	foundProgramming := false
	for _, r := range results {
		if r.Source == "go-doc" || r.Source == "python-doc" {
			foundProgramming = true
			break
		}
	}
	if !foundProgramming {
		t.Errorf("expected programming docs in results, got: %v", resultSources(results))
	}
}

func TestRAGIngest_StaleChunksDeleted(t *testing.T) {
	t.Parallel()
	store := newTestRAGStore(t)

	// Ingest a large document that produces multiple chunks.
	largeContent := strings.Repeat("word ", 600) // ~3000 chars > 2048 chunk size
	if err := store.Ingest("shrinking-doc", largeContent); err != nil {
		t.Fatalf("Ingest large: %v", err)
	}
	count1, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount 1: %v", err)
	}
	if count1 < 2 {
		t.Fatalf("expected at least 2 chunks for large content, got %d", count1)
	}

	// Re-ingest with much shorter content (1 chunk).
	if err := store.Ingest("shrinking-doc", "Short content."); err != nil {
		t.Fatalf("Ingest short: %v", err)
	}
	count2, err := store.DocumentCount()
	if err != nil {
		t.Fatalf("DocumentCount 2: %v", err)
	}
	if count2 != 1 {
		t.Errorf("expected 1 chunk after re-ingest with shorter content, got %d", count2)
	}
}

// resultSources extracts source names from search results for error messages.
func resultSources(results []SearchResult) []string {
	sources := make([]string, len(results))
	for i, r := range results {
		sources[i] = r.Source
	}
	return sources
}
