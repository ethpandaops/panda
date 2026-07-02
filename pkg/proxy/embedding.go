package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cache"
)

const (
	embeddingAPITimeout = 2 * time.Minute
	// maxEmbedBatchSize limits items sent to the upstream API per request.
	maxEmbedBatchSize = 100
	// maxEmbedItems is the hard cap on items accepted in a single /embed request.
	maxEmbedItems = 500
)

// Embedding task types. Retrieval-tuned models embed queries and documents
// asymmetrically ("questions and their answers are not semantically similar"),
// so every embed request declares which side of retrieval its texts sit on.
const (
	// EmbedTaskQuery marks texts that are search queries.
	EmbedTaskQuery = "query"
	// EmbedTaskDocument marks texts that are indexed documents.
	EmbedTaskDocument = "document"
)

// embedTaskParams maps a request task to the upstream input_type and the
// cache-key segment that isolates its vector space. Task is required — there
// is no symmetric fallback.
func embedTaskParams(task string) (inputType, keySegment string, err error) {
	switch task {
	case EmbedTaskQuery:
		return "search_query", EmbedTaskQuery + ":", nil
	case EmbedTaskDocument:
		return "search_document", EmbedTaskDocument + ":", nil
	default:
		return "", "", fmt.Errorf("embed task is required and must be %q or %q, got %q", EmbedTaskQuery, EmbedTaskDocument, task)
	}
}

// EmbedCheckRequest is the request payload for the /embedding/check endpoint.
type EmbedCheckRequest struct {
	Hashes []string `json:"hashes"`
	// Task scopes the lookup to a task-typed vector space ("query" or
	// "document"). Required.
	Task string `json:"task"`
}

// EmbedCheckResponse is the response from /embedding/check. It advertises the
// model and dimensions so clients can detect a change of either — the embedding
// space is identified by model/dimensions/task, and a dimensionality change
// under the same model needs a re-index just as much as a model swap.
type EmbedCheckResponse struct {
	Model      string        `json:"model"`
	Dimensions int           `json:"dimensions"`
	Cached     []EmbedResult `json:"cached"`
}

// EmbedRequest is the request payload for the /embedding endpoint.
type EmbedRequest struct {
	Items []EmbedItem `json:"items"`
	// Task declares which side of retrieval the texts sit on ("query" or
	// "document"). Required.
	Task string `json:"task"`
}

// EmbedItem is a single item to embed.
type EmbedItem struct {
	Hash string `json:"hash"`
	Text string `json:"text"`
}

// EmbedResponse is the response payload from the /embedding endpoint. It
// advertises the model and dimensions; vectors are fp32.
type EmbedResponse struct {
	Model      string        `json:"model"`
	Dimensions int           `json:"dimensions"`
	Results    []EmbedResult `json:"results"`
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
	// dimensions is the fixed output dimensionality requested from the
	// embedding API (Matryoshka truncation). Always > 0 — part of the
	// embedding-space identity.
	dimensions int
}

// NewEmbeddingService creates a new EmbeddingService that requests a fixed
// output dimensionality from the embedding API. If costPerToken is 0, the
// service fetches pricing from the API's /models endpoint.
func NewEmbeddingService(
	log logrus.FieldLogger,
	c cache.Cache,
	apiKey, model, apiURL string,
	costPerToken float64,
	dimensions int,
) *EmbeddingService {
	svcLog := log.WithField("component", "embedding-service")
	normalizedURL := strings.TrimRight(apiURL, "/")
	httpClient := &http.Client{Timeout: embeddingAPITimeout}

	if costPerToken == 0 {
		fetched, err := fetchModelCostPerToken(httpClient, normalizedURL, model, apiKey)
		if err != nil {
			svcLog.WithError(err).Warn("Failed to fetch model pricing, cost metrics will be unavailable")
		} else {
			costPerToken = fetched
			svcLog.WithFields(logrus.Fields{
				"model":          model,
				"cost_per_token": costPerToken,
			}).Info("Fetched embedding model pricing")
		}
	}

	return &EmbeddingService{
		log:          svcLog,
		cache:        c,
		apiKey:       apiKey,
		model:        model,
		apiURL:       normalizedURL,
		client:       httpClient,
		costPerToken: costPerToken,
		dimensions:   dimensions,
	}
}

// Model returns the configured embedding model name.
func (s *EmbeddingService) Model() string {
	return s.model
}

// Dimensions returns the configured output dimensionality, or 0 for native.
func (s *EmbeddingService) Dimensions() int {
	return s.dimensions
}

// cacheKeyPrefix returns the cache-key namespace for this service's vectors:
// {model}:{dims}:. Dimensionality is always folded in, so two services on the
// same model id but different output dimensions never collide.
func (s *EmbeddingService) cacheKeyPrefix() string {
	return s.model + ":" + strconv.Itoa(s.dimensions) + ":"
}

// Embed computes embeddings for the given items, using the cache where possible.
// Uncached items are sent to the upstream API in sub-batches of maxEmbedBatchSize.
// Task is required — "query" or "document" — and selects the asymmetric
// retrieval embedding plus the task-scoped cache key space.
func (s *EmbeddingService) Embed(ctx context.Context, items []EmbedItem, task string) (*EmbedResponse, error) {
	inputType, keySegment, err := embedTaskParams(task)
	if err != nil {
		return nil, err
	}

	if len(items) == 0 {
		return &EmbedResponse{Model: s.model, Dimensions: s.dimensions}, nil
	}

	if len(items) > maxEmbedItems {
		return nil, fmt.Errorf("too many items: %d exceeds maximum of %d", len(items), maxEmbedItems)
	}

	s.log.WithFields(logrus.Fields{"items": len(items), "task": task}).Info("Embed request received")

	// Build cache keys: {model}:{dims}:{task}:{hash}.
	cacheKeys := make([]string, len(items))

	for i, item := range items {
		cacheKeys[i] = s.cacheKeyPrefix() + keySegment + item.Hash
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

	s.log.WithFields(logrus.Fields{
		"total":        len(items),
		"cache_hits":   cacheHits,
		"cache_misses": len(misses),
	}).Info("Embedding cache stats")

	if cacheHits > 0 {
		EmbeddingItemsTotal.WithLabelValues("cache_hit").Add(float64(cacheHits))
	}

	if len(misses) > 0 {
		EmbeddingItemsTotal.WithLabelValues("cache_miss").Add(float64(len(misses)))

		// Embed misses in sub-batches to avoid upstream API limits.
		totalBatches := (len(misses) + maxEmbedBatchSize - 1) / maxEmbedBatchSize
		toCache := make(map[string][]byte, len(misses))

		for batchStart := 0; batchStart < len(misses); batchStart += maxEmbedBatchSize {
			batchEnd := min(batchStart+maxEmbedBatchSize, len(misses))
			batchMisses := misses[batchStart:batchEnd]
			batchNum := batchStart/maxEmbedBatchSize + 1

			missTexts := make([]string, len(batchMisses))
			for j, idx := range batchMisses {
				missTexts[j] = items[idx].Text
			}

			if totalBatches > 1 {
				s.log.WithFields(logrus.Fields{
					"batch":        fmt.Sprintf("%d/%d", batchNum, totalBatches),
					"items":        len(missTexts),
					"total_misses": len(misses),
				}).Info("Calling upstream embedding API")
			} else {
				s.log.WithField("items", len(missTexts)).Info("Calling upstream embedding API")
			}

			vectors, usage, err := s.callEmbeddingAPI(ctx, missTexts, inputType)
			if err != nil {
				return nil, fmt.Errorf("calling embedding API (batch %d/%d): %w", batchNum, totalBatches, err)
			}

			if len(vectors) != len(batchMisses) {
				return nil, fmt.Errorf(
					"embedding API returned %d vectors, expected %d",
					len(vectors), len(batchMisses),
				)
			}

			if usage != nil {
				s.log.WithFields(logrus.Fields{
					"prompt_tokens": usage.PromptTokens,
					"total_tokens":  usage.TotalTokens,
				}).Info("Upstream API token usage")
			}

			// L2-normalize and store results + cache entries.
			for j, idx := range batchMisses {
				vec := l2Normalize(vectors[j])
				results[idx] = EmbedResult{Hash: items[idx].Hash, Vector: vec}

				data, err := json.Marshal(vec)
				if err != nil {
					s.log.WithError(err).Warn("Failed to marshal vector for cache")

					continue
				}

				toCache[cacheKeys[idx]] = data
			}
		}

		if len(toCache) > 0 {
			if err := s.cache.SetMulti(ctx, toCache); err != nil {
				s.log.WithError(err).Warn("Cache SetMulti failed")
			} else {
				s.log.WithField("entries", len(toCache)).Info("Cached embedding vectors")
			}
		}
	}

	s.log.WithField("results", len(results)).Info("Embed request complete")

	return &EmbedResponse{
		Model:      s.model,
		Dimensions: s.dimensions,
		Results:    results,
	}, nil
}

// CheckCached returns cached vectors for the given hashes, scoped to the
// required task's vector space. Only cached hashes are returned.
func (s *EmbeddingService) CheckCached(ctx context.Context, hashes []string, task string) ([]EmbedResult, error) {
	_, keySegment, err := embedTaskParams(task)
	if err != nil {
		return nil, err
	}

	if len(hashes) == 0 {
		return nil, nil
	}

	s.log.WithFields(logrus.Fields{"hashes": len(hashes), "task": task}).Info("Embed check request received")

	cacheKeys := make([]string, len(hashes))
	for i, h := range hashes {
		cacheKeys[i] = s.cacheKeyPrefix() + keySegment + h
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

	s.log.WithFields(logrus.Fields{
		"requested": len(hashes),
		"found":     len(results),
	}).Info("Embed check complete")

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
	// Dimensions requests a fixed output size (Matryoshka). Omitted when 0 so
	// callers using native dimensionality send a byte-identical request.
	Dimensions int `json:"dimensions,omitempty"`
	// InputType asks retrieval-tuned models to embed asymmetrically
	// ("search_query" or "search_document"). Always set — task is required.
	InputType string `json:"input_type"`
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

func (s *EmbeddingService) callEmbeddingAPI(ctx context.Context, texts []string, inputType string) ([][]float32, *openRouterUsage, error) {
	reqBody := openRouterRequest{
		Model:      s.model,
		Input:      texts,
		Dimensions: s.dimensions,
		InputType:  inputType,
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

	// Sort by index to maintain input order. Vector length must match the
	// configured dimensionality: the service advertises Dimensions as part of
	// the embedding-space identity, so an upstream that ignores or mishandles
	// the Matryoshka request must fail loudly here rather than silently serve
	// vectors under a lying identity.
	vectors := make([][]float32, len(texts))
	for _, emb := range apiResp.Data {
		if emb.Index < 0 || emb.Index >= len(vectors) {
			return nil, nil, fmt.Errorf("embedding API returned invalid index %d", emb.Index)
		}

		if len(emb.Embedding) != s.dimensions {
			return nil, nil, fmt.Errorf(
				"embedding API returned a %d-dimensional vector, expected %d — upstream ignored the dimensions request",
				len(emb.Embedding), s.dimensions,
			)
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

// modelsResponse is the response from the OpenRouter /models endpoint.
type modelsResponse struct {
	Data []modelInfo `json:"data"`
}

// modelInfo represents a single model's metadata from the /models endpoint.
type modelInfo struct {
	ID      string       `json:"id"`
	Pricing modelPricing `json:"pricing"`
}

// modelPricing contains per-token costs from the /models endpoint.
type modelPricing struct {
	Prompt string `json:"prompt"`
}

// fetchModelCostPerToken queries the API's /models endpoint and returns the
// per-token prompt cost for the given model. Returns 0 if the model is not found.
func fetchModelCostPerToken(client *http.Client, apiURL, model, apiKey string) (float64, error) {
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet,
		apiURL+"/models", nil,
	)
	if err != nil {
		return 0, fmt.Errorf("creating models request: %w", err)
	}

	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetching models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return 0, fmt.Errorf("models endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var models modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return 0, fmt.Errorf("decoding models response: %w", err)
	}

	for _, m := range models.Data {
		if m.ID != model {
			continue
		}

		if m.Pricing.Prompt == "" {
			return 0, nil
		}

		cost, err := strconv.ParseFloat(m.Pricing.Prompt, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing prompt cost %q: %w", m.Pricing.Prompt, err)
		}

		return cost, nil
	}

	return 0, nil
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
