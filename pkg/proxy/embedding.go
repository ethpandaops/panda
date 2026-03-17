package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cache"
)

const embeddingAPITimeout = 2 * time.Minute

// EmbedCheckRequest is the request payload for the /embed/check endpoint.
type EmbedCheckRequest struct {
	Model  string   `json:"model"`
	Hashes []string `json:"hashes"`
}

// EmbedCheckResponse is the response from /embed/check.
type EmbedCheckResponse struct {
	Cached []EmbedResult `json:"cached"`
}

// EmbedRequest is the request payload for the /embed endpoint.
type EmbedRequest struct {
	Items []EmbedItem `json:"items"`
}

// EmbedItem is a single item to embed.
type EmbedItem struct {
	Hash string `json:"hash"`
	Text string `json:"text"`
}

// EmbedResponse is the response payload from the /embed endpoint.
type EmbedResponse struct {
	Results []EmbedResult `json:"results"`
	Model   string        `json:"model"`
}

// EmbedResult is a single embedding result.
type EmbedResult struct {
	Hash   string    `json:"hash"`
	Vector []float32 `json:"vector"`
}

// EmbeddingService handles embedding requests using a remote API with caching.
type EmbeddingService struct {
	log          logrus.FieldLogger
	cache        cache.Cache
	apiKey       string
	model        string
	apiURL       string
	client       *http.Client
	costPerToken float64
}

// NewEmbeddingService creates a new EmbeddingService.
func NewEmbeddingService(
	log logrus.FieldLogger,
	c cache.Cache,
	apiKey, model, apiURL string,
	costPerToken float64,
) *EmbeddingService {
	return &EmbeddingService{
		log:          log.WithField("component", "embedding-service"),
		cache:        c,
		apiKey:       apiKey,
		model:        model,
		apiURL:       strings.TrimRight(apiURL, "/"),
		client:       &http.Client{Timeout: embeddingAPITimeout},
		costPerToken: costPerToken,
	}
}

// Model returns the configured embedding model name.
func (s *EmbeddingService) Model() string {
	return s.model
}

// Embed computes embeddings for the given items, using the cache where possible.
func (s *EmbeddingService) Embed(ctx context.Context, items []EmbedItem) (*EmbedResponse, error) {
	if len(items) == 0 {
		return &EmbedResponse{Model: s.model}, nil
	}

	// Build cache keys: {model}:{hash}.
	cacheKeys := make([]string, len(items))

	for i, item := range items {
		cacheKeys[i] = s.model + ":" + item.Hash
	}

	// Check cache for existing vectors.
	cached, err := s.cache.GetMulti(ctx, cacheKeys)
	if err != nil {
		s.log.WithError(err).Warn("Cache GetMulti failed, will embed all items")

		cached = nil
	}

	// Separate hits from misses.
	results := make([]EmbedResult, len(items))
	var misses []int

	for i, item := range items {
		key := cacheKeys[i]
		if data, ok := cached[key]; ok {
			var vec []float32
			if err := json.Unmarshal(data, &vec); err != nil {
				s.log.WithError(err).WithField("key", key).Warn("Cache deserialization failed, will re-embed")

				misses = append(misses, i)

				continue
			}

			results[i] = EmbedResult{Hash: item.Hash, Vector: vec}

			continue
		}

		misses = append(misses, i)
	}

	// Record cache metrics.
	cacheHits := len(items) - len(misses)
	if cacheHits > 0 {
		EmbeddingItemsTotal.WithLabelValues("cache_hit").Add(float64(cacheHits))
	}

	if len(misses) > 0 {
		EmbeddingItemsTotal.WithLabelValues("cache_miss").Add(float64(len(misses)))

		s.log.WithFields(logrus.Fields{
			"total":        len(items),
			"cache_hits":   cacheHits,
			"cache_misses": len(misses),
		}).Debug("Embedding cache stats")

		// Collect texts for the misses.
		missTexts := make([]string, len(misses))
		for j, idx := range misses {
			missTexts[j] = items[idx].Text
		}

		// Call OpenRouter API.
		vectors, _, err := s.callEmbeddingAPI(ctx, missTexts)
		if err != nil {
			return nil, fmt.Errorf("calling embedding API: %w", err)
		}

		if len(vectors) != len(misses) {
			return nil, fmt.Errorf(
				"embedding API returned %d vectors, expected %d",
				len(vectors), len(misses),
			)
		}

		// L2-normalize and store results + cache entries.
		toCache := make(map[string][]byte, len(misses))

		for j, idx := range misses {
			vec := l2Normalize(vectors[j])
			results[idx] = EmbedResult{Hash: items[idx].Hash, Vector: vec}

			data, err := json.Marshal(vec)
			if err != nil {
				s.log.WithError(err).Warn("Failed to marshal vector for cache")

				continue
			}

			toCache[cacheKeys[idx]] = data
		}

		if len(toCache) > 0 {
			if err := s.cache.SetMulti(ctx, toCache); err != nil {
				s.log.WithError(err).Warn("Cache SetMulti failed")
			}
		}
	}

	return &EmbedResponse{
		Results: results,
		Model:   s.model,
	}, nil
}

// CheckCached returns cached vectors for the given hashes.
// Only hashes that exist in the cache are returned.
func (s *EmbeddingService) CheckCached(ctx context.Context, hashes []string) ([]EmbedResult, error) {
	if len(hashes) == 0 {
		return nil, nil
	}

	cacheKeys := make([]string, len(hashes))
	for i, h := range hashes {
		cacheKeys[i] = s.model + ":" + h
	}

	cached, err := s.cache.GetMulti(ctx, cacheKeys)
	if err != nil {
		return nil, fmt.Errorf("cache lookup: %w", err)
	}

	results := make([]EmbedResult, 0, len(cached))
	for i, h := range hashes {
		data, ok := cached[cacheKeys[i]]
		if !ok {
			continue
		}

		var vec []float32
		if err := json.Unmarshal(data, &vec); err != nil {
			s.log.WithError(err).WithField("hash", h).Warn("Cache deserialization failed, skipping")

			continue
		}

		results = append(results, EmbedResult{Hash: h, Vector: vec})
	}

	if len(results) > 0 {
		EmbeddingItemsTotal.WithLabelValues("cache_hit").Add(float64(len(results)))
	}

	return results, nil
}

// Close releases resources held by the embedding service.
func (s *EmbeddingService) Close() error {
	return s.cache.Close()
}

// openRouterRequest is the request body for the OpenRouter embeddings API.
type openRouterRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// openRouterResponse is the response body from the OpenRouter embeddings API.
type openRouterResponse struct {
	Data  []openRouterEmbedding `json:"data"`
	Usage *openRouterUsage      `json:"usage,omitempty"`
}

// openRouterUsage contains token consumption from the API response.
type openRouterUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// openRouterEmbedding is a single embedding in the OpenRouter response.
type openRouterEmbedding struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func (s *EmbeddingService) callEmbeddingAPI(ctx context.Context, texts []string) ([][]float32, *openRouterUsage, error) {
	reqBody := openRouterRequest{
		Model: s.model,
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := s.apiURL + "/embeddings"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	start := time.Now()

	resp, err := s.client.Do(req)
	if err != nil {
		EmbeddingRequestsTotal.WithLabelValues("error").Inc()

		return nil, nil, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	duration := time.Since(start).Seconds()

	if resp.StatusCode != http.StatusOK {
		EmbeddingRequestsTotal.WithLabelValues("error").Inc()
		EmbeddingRequestDurationSeconds.Observe(duration)

		respBody, _ := io.ReadAll(resp.Body)

		return nil, nil, fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, nil, fmt.Errorf("decoding response: %w", err)
	}

	EmbeddingRequestsTotal.WithLabelValues("success").Inc()
	EmbeddingRequestDurationSeconds.Observe(duration)

	if apiResp.Usage != nil {
		EmbeddingTokensTotal.WithLabelValues("prompt").Add(float64(apiResp.Usage.PromptTokens))
		EmbeddingTokensTotal.WithLabelValues("total").Add(float64(apiResp.Usage.TotalTokens))

		if s.costPerToken > 0 {
			EmbeddingCostUSD.Add(float64(apiResp.Usage.TotalTokens) * s.costPerToken)
		}
	}

	// Sort by index to maintain input order.
	vectors := make([][]float32, len(texts))
	for _, emb := range apiResp.Data {
		if emb.Index < 0 || emb.Index >= len(vectors) {
			return nil, nil, fmt.Errorf("embedding API returned invalid index %d", emb.Index)
		}

		vectors[emb.Index] = emb.Embedding
	}

	for i, v := range vectors {
		if v == nil {
			return nil, nil, fmt.Errorf("embedding API missing vector for index %d", i)
		}
	}

	return vectors, apiResp.Usage, nil
}

const embeddingCacheTTL = 30 * 24 * time.Hour

// buildEmbeddingCache creates a cache instance based on the config.
func buildEmbeddingCache(cfg EmbeddingCacheConfig) (cache.Cache, error) {
	switch cfg.Backend {
	case "redis":
		return cache.NewRedis(cfg.RedisURL, "panda:embed:", embeddingCacheTTL)
	case "memory", "":
		return cache.NewInMemory(embeddingCacheTTL), nil
	default:
		return nil, fmt.Errorf("unsupported cache backend: %s", cfg.Backend)
	}
}

// l2Normalize normalizes a vector to unit length.
func l2Normalize(vec []float32) []float32 {
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}

	norm = math.Sqrt(norm)
	if norm == 0 {
		return vec
	}

	result := make([]float32, len(vec))
	for i, v := range vec {
		result[i] = float32(float64(v) / norm)
	}

	return result
}
