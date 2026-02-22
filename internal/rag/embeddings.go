package rag

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// EmbeddingProvider generates vector embeddings for text. Implementations
// call an external API (e.g., OpenAI /v1/embeddings) to produce dense
// float32 vectors suitable for cosine similarity search.
type EmbeddingProvider interface {
	// Embed generates embeddings for the given texts. Returns one vector
	// per input text.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimensions returns the dimensionality of the embedding vectors.
	Dimensions() int
}

// OpenAIEmbeddingConfig holds configuration for the OpenAI-compatible
// embedding provider.
type OpenAIEmbeddingConfig struct {
	// APIBase is the base URL (e.g., "https://api.openai.com/v1").
	APIBase string
	// APIKey is the API key for authentication.
	APIKey string
	// Model is the embedding model name (e.g., "text-embedding-3-small").
	Model string
	// Dims is the embedding dimensionality. Defaults to 1536.
	Dims int
	// BatchSize is the maximum number of texts per API call. Defaults to 100.
	BatchSize int
	// HTTPClient allows injection of a custom HTTP client for testing.
	HTTPClient *http.Client
}

// OpenAIEmbeddingProvider implements EmbeddingProvider using the OpenAI
// /v1/embeddings API. Compatible with OpenAI, OpenRouter, and Ollama.
type OpenAIEmbeddingProvider struct {
	cfg    OpenAIEmbeddingConfig
	client *http.Client
}

// NewOpenAIEmbeddingProvider creates a new embedding provider.
func NewOpenAIEmbeddingProvider(cfg OpenAIEmbeddingConfig) *OpenAIEmbeddingProvider {
	if cfg.Model == "" {
		cfg.Model = "text-embedding-3-small"
	}
	if cfg.Dims <= 0 {
		cfg.Dims = 1536
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &OpenAIEmbeddingProvider{cfg: cfg, client: client}
}

// Embed generates embeddings for the given texts, batching as needed.
func (p *OpenAIEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var allEmbeddings [][]float32

	for i := 0; i < len(texts); i += p.cfg.BatchSize {
		end := i + p.cfg.BatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		embeddings, err := p.embedBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("Embed: batch %d-%d: %w", i, end, err)
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}

	return allEmbeddings, nil
}

// Dimensions returns the configured embedding dimensionality.
func (p *OpenAIEmbeddingProvider) Dimensions() int {
	return p.cfg.Dims
}

// embeddingRequest is the request body for /v1/embeddings.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingResponse is the response from /v1/embeddings.
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// embedBatch sends a single batch to the embeddings API.
func (p *OpenAIEmbeddingProvider) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := embeddingRequest{
		Model: p.cfg.Model,
		Input: texts,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("embedBatch: marshal: %w", err)
	}

	url := strings.TrimRight(p.cfg.APIBase, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedBatch: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedBatch: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, fmt.Errorf("embedBatch: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedBatch: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("embedBatch: decode: %w", err)
	}

	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("embedBatch: expected %d embeddings, got %d", len(texts), len(embResp.Data))
	}

	// Sort by index to ensure correct ordering.
	result := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedBatch: invalid index %d", d.Index)
		}
		result[d.Index] = d.Embedding
	}

	return result, nil
}

// EncodeEmbedding serializes a float32 vector to a byte slice for storage
// as a BLOB in SQLite.
func EncodeEmbedding(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeEmbedding deserializes a byte slice back to a float32 vector.
func DecodeEmbedding(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0 if either vector is zero-length or they have different dimensions.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
