package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cache"
)

const (
	remoteEmbedTimeout = 2 * time.Minute
	// maxBatchSize limits how many items are sent in a single /embed request.
	// The proxy accepts up to 500 items and sub-batches to the upstream API internally.
	maxBatchSize = 500
)

// embedCheckRequest is the request payload for the proxy /embed/check endpoint.
type embedCheckRequest struct {
	Model  string   `json:"model"`
	Hashes []string `json:"hashes"`
}

// embedCheckResponse is the response from /embed/check.
type embedCheckResponse struct {
	Cached []embedResult `json:"cached"`
}

// embedRequest is the request payload for the proxy /embed endpoint.
type embedRequest struct {
	Items []embedItem `json:"items"`
}

// embedItem is a single item to embed.
type embedItem struct {
	Hash string `json:"hash"`
	Text string `json:"text"`
}

// embedResponse is the response payload from the proxy /embed endpoint.
type embedResponse struct {
	Results []embedResult `json:"results"`
	Model   string        `json:"model"`
}

// embedResult is a single embedding result.
type embedResult struct {
	Hash   string    `json:"hash"`
	Vector []float32 `json:"vector"`
}

// RemoteEmbedder implements Embedder by calling the proxy's /embed endpoint.
// An optional local cache avoids round-trips to the proxy on warm restarts.
type RemoteEmbedder struct {
	log        logrus.FieldLogger
	proxyURL   string
	httpClient *http.Client
	tokenFn    func() string
	localCache cache.Cache
	model      string
}

// Compile-time interface check.
var _ Embedder = (*RemoteEmbedder)(nil)

// NewRemote creates a new RemoteEmbedder that calls the proxy's /embed endpoint.
// tokenFn is called on each request to get the current auth token.
// localCache and model are optional — when both are set, embedding vectors are
// cached locally using {model}:{textHash} keys to avoid proxy round-trips.
func NewRemote(
	log logrus.FieldLogger,
	proxyURL string,
	tokenFn func() string,
	localCache cache.Cache,
	model string,
) *RemoteEmbedder {
	return &RemoteEmbedder{
		log:        log.WithField("component", "remote-embedder"),
		proxyURL:   proxyURL,
		httpClient: &http.Client{Timeout: remoteEmbedTimeout},
		tokenFn:    tokenFn,
		localCache: localCache,
		model:      model,
	}
}

// Embed returns the L2-normalized embedding vector for a single text string.
func (e *RemoteEmbedder) Embed(text string) ([]float32, error) {
	vectors, err := e.EmbedBatch([]string{text})
	if err != nil {
		return nil, err
	}

	if len(vectors) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return vectors[0], nil
}

// EmbedBatch returns L2-normalized embedding vectors for multiple texts.
// When a local cache is configured, vectors are checked there first.
// Remaining misses are split into sub-batches of maxBatchSize and sent
// to the proxy (which has its own Redis cache + upstream API).
func (e *RemoteEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	hashes := make([]string, len(texts))
	hashToIndices := make(map[string][]int, len(texts))

	for i, text := range texts {
		h := sha256Hex(text)
		hashes[i] = h
		hashToIndices[h] = append(hashToIndices[h], i)
	}

	// For single items, try the local cache then embed directly (skip proxy
	// cache check — the proxy's Embed handler checks Redis internally anyway).
	if len(texts) == 1 {
		if e.localCache != nil {
			key := e.localCacheKey(hashes[0])

			data, found, err := e.localCache.Get(context.Background(), key)
			if err == nil && found {
				var vec []float32
				if err := json.Unmarshal(data, &vec); err == nil {
					return [][]float32{vec}, nil
				}
			}
		}

		vecs, err := e.embedDirect(texts, hashes, hashToIndices)
		if err != nil {
			return nil, err
		}

		// Cache the result for future queries.
		if e.localCache != nil && len(vecs) == 1 && vecs[0] != nil {
			toCache := make(map[string][]byte, 1)
			e.queueLocalCache(toCache, hashes[0], vecs[0])

			if len(toCache) > 0 {
				_ = e.localCache.SetMulti(context.Background(), toCache)
			}
		}

		return vecs, err
	}

	vectors := make([][]float32, len(texts))

	// Phase 1: check the local filesystem cache.
	var localHits int

	if e.localCache != nil {
		cacheKeys := make([]string, len(texts))
		for i, h := range hashes {
			cacheKeys[i] = e.localCacheKey(h)
		}

		cached, err := e.localCache.GetMulti(context.Background(), cacheKeys)
		if err != nil {
			e.log.WithError(err).Warn("Local cache read failed, falling through to proxy")
		} else {
			for i, key := range cacheKeys {
				if data, ok := cached[key]; ok {
					var vec []float32
					if err := json.Unmarshal(data, &vec); err != nil {
						e.log.WithError(err).Debug("Local cache entry corrupt, will re-fetch")

						continue
					}

					vectors[i] = vec
					localHits++
				}
			}
		}

		if localHits > 0 {
			e.log.WithFields(logrus.Fields{
				"total":      len(texts),
				"local_hits": localHits,
			}).Info("Local embedding cache hits")
		}
	}

	// Collect indices that still need embedding.
	var remoteIndices []int

	for i, v := range vectors {
		if v == nil {
			remoteIndices = append(remoteIndices, i)
		}
	}

	if len(remoteIndices) == 0 {
		return vectors, nil
	}

	// Phase 2: fetch remaining vectors from the proxy in sub-batches.
	totalBatches := (len(remoteIndices) + maxBatchSize - 1) / maxBatchSize
	toCache := make(map[string][]byte, len(remoteIndices))

	for batchStart := 0; batchStart < len(remoteIndices); batchStart += maxBatchSize {
		batchEnd := min(batchStart+maxBatchSize, len(remoteIndices))
		batchIndices := remoteIndices[batchStart:batchEnd]
		batchNum := batchStart/maxBatchSize + 1

		if totalBatches > 1 {
			e.log.WithFields(logrus.Fields{
				"batch":        fmt.Sprintf("%d/%d", batchNum, totalBatches),
				"items":        len(batchIndices),
				"remote_total": len(remoteIndices),
			}).Info("Embedding batch via proxy")
		}

		// Build hash list for this batch and check proxy cache.
		batchHashes := make([]string, len(batchIndices))
		for j, idx := range batchIndices {
			batchHashes[j] = hashes[idx]
		}

		cached, err := e.checkCached(batchHashes)
		if err != nil {
			e.log.WithError(err).Warn("Proxy cache check failed, embedding all items")

			cached = nil
		}

		// Fill in proxy-cached vectors.
		cachedHashSet := make(map[string][]float32, len(cached))
		for _, result := range cached {
			cachedHashSet[result.Hash] = result.Vector
		}

		var missItems []embedItem

		for _, idx := range batchIndices {
			if vec, ok := cachedHashSet[hashes[idx]]; ok {
				vectors[idx] = vec

				e.queueLocalCache(toCache, hashes[idx], vec)
			} else {
				missItems = append(missItems, embedItem{Hash: hashes[idx], Text: texts[idx]})
			}
		}

		if len(missItems) == 0 {
			continue
		}

		e.log.WithFields(logrus.Fields{
			"total":  len(batchIndices),
			"cached": len(batchIndices) - len(missItems),
			"misses": len(missItems),
		}).Info("Proxy cache stats")

		resp, err := e.callEmbed(missItems)
		if err != nil {
			return nil, fmt.Errorf("embedding batch %d/%d: %w", batchNum, totalBatches, err)
		}

		for _, result := range resp.Results {
			for _, idx := range hashToIndices[result.Hash] {
				vectors[idx] = result.Vector
			}

			e.queueLocalCache(toCache, result.Hash, result.Vector)
		}
	}

	// Phase 3: persist newly fetched vectors to local cache.
	if e.localCache != nil && len(toCache) > 0 {
		if err := e.localCache.SetMulti(context.Background(), toCache); err != nil {
			e.log.WithError(err).Warn("Failed to write local embedding cache")
		} else {
			e.log.WithField("entries", len(toCache)).Info("Wrote vectors to local cache")
		}
	}

	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("missing embedding for text at index %d", i)
		}
	}

	return vectors, nil
}

// Close releases resources held by the embedder.
func (e *RemoteEmbedder) Close() error {
	if e.localCache != nil {
		return e.localCache.Close()
	}

	return nil
}

func (e *RemoteEmbedder) localCacheKey(textHash string) string {
	return e.model + ":" + textHash
}

func (e *RemoteEmbedder) queueLocalCache(toCache map[string][]byte, textHash string, vec []float32) {
	if e.localCache == nil {
		return
	}

	data, err := json.Marshal(vec)
	if err != nil {
		return
	}

	toCache[e.localCacheKey(textHash)] = data
}

// embedDirect sends all items to /embed without checking the cache first.
func (e *RemoteEmbedder) embedDirect(
	texts []string,
	hashes []string,
	hashToIndices map[string][]int,
) ([][]float32, error) {
	items := make([]embedItem, len(texts))
	for i, text := range texts {
		items[i] = embedItem{Hash: hashes[i], Text: text}
	}

	resp, err := e.callEmbed(items)
	if err != nil {
		return nil, err
	}

	vectors := make([][]float32, len(texts))
	for _, result := range resp.Results {
		for _, idx := range hashToIndices[result.Hash] {
			vectors[idx] = result.Vector
		}
	}

	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("missing embedding for text at index %d", i)
		}
	}

	return vectors, nil
}

func (e *RemoteEmbedder) checkCached(hashes []string) ([]embedResult, error) {
	reqBody, err := json.Marshal(embedCheckRequest{Hashes: hashes})
	if err != nil {
		return nil, fmt.Errorf("marshaling check request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		e.proxyURL+"/embed/check", bytes.NewReader(reqBody),
	)
	if err != nil {
		return nil, fmt.Errorf("creating check request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if token := e.tokenFn(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling embed check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("embed check returned status %d: %s", resp.StatusCode, string(body))
	}

	var checkResp embedCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&checkResp); err != nil {
		return nil, fmt.Errorf("decoding check response: %w", err)
	}

	return checkResp.Cached, nil
}

func (e *RemoteEmbedder) callEmbed(items []embedItem) (*embedResponse, error) {
	reqBody, err := json.Marshal(embedRequest{Items: items})
	if err != nil {
		return nil, fmt.Errorf("marshaling embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		e.proxyURL+"/embed", bytes.NewReader(reqBody),
	)
	if err != nil {
		return nil, fmt.Errorf("creating embed request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if token := e.tokenFn(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling proxy embed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("proxy embed returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("decoding embed response: %w", err)
	}

	return &embedResp, nil
}

func sha256Hex(text string) string {
	h := sha256.Sum256([]byte(text))

	return fmt.Sprintf("%x", h)
}
