package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/cache"
)

const (
	// remoteEmbedTimeout bounds batch embed calls. It deliberately outlasts
	// the proxy's 2-minute upstream timeout so a wedged upstream surfaces as
	// the proxy's specific error instead of a generic client timeout here.
	remoteEmbedTimeout = 130 * time.Second
	// queryEmbedTimeout bounds single-item embeds — the interactive search
	// path. One short text normally embeds in well under a second; inheriting
	// the batch timeout meant a hung upstream blocked every search for two
	// minutes (observed 2026-07-08).
	queryEmbedTimeout = 15 * time.Second
	// maxBatchSize limits how many items are sent in a single embedding request.
	// The proxy accepts up to 500 items and sub-batches to the upstream API internally.
	maxBatchSize = 500

	// Proxy embedding routes.
	embedPathV1      = "/embed"
	embedCheckPathV1 = "/embed/check"
	embedPathV2      = "/v2/embedding"
	embedCheckPathV2 = "/v2/embedding/check"
	embedPathV3      = "/v3/embedding"
	embedCheckPathV3 = "/v3/embedding/check"

	// Embedding task types (wire values shared with pkg/proxy). v3 sends these
	// on the wire; v1/v2 are symmetric and omit task.
	taskQuery    = "query"
	taskDocument = "document"
)

// embedCheckRequest is the request payload for the proxy embed-check endpoint.
type embedCheckRequest struct {
	Hashes []string `json:"hashes"`
	Task   string   `json:"task,omitempty"`
}

// embedCheckResponse is the response from the embed-check endpoint. Model and
// Dimensions advertise the embedding space the proxy is currently serving.
type embedCheckResponse struct {
	Model      string        `json:"model"`
	Dimensions int           `json:"dimensions"`
	Cached     []embedResult `json:"cached"`
}

// embedRequest is the request payload for the proxy embed endpoint.
type embedRequest struct {
	Items []embedItem `json:"items"`
	Task  string      `json:"task,omitempty"`
}

// embedItem is a single item to embed.
type embedItem struct {
	Hash string `json:"hash"`
	Text string `json:"text"`
}

// embedResponse is the response payload from the proxy embed endpoint. Model
// and Dimensions advertise the embedding space the vectors belong to.
type embedResponse struct {
	Results    []embedResult `json:"results"`
	Model      string        `json:"model"`
	Dimensions int           `json:"dimensions"`
}

// embedResult is a single embedding result.
type embedResult struct {
	Hash   string    `json:"hash"`
	Vector []float32 `json:"vector"`
}

// RemoteEmbedder implements Embedder by calling the negotiated proxy embedding
// routes. v3 is task-typed; v2 and v1 are symmetric.
// An optional local cache avoids round-trips to the proxy on warm restarts.
type RemoteEmbedder struct {
	log          logrus.FieldLogger
	proxyURL     string
	httpClient   *http.Client
	tokenFn      func() string
	invalidateFn func()
	localCache   cache.Cache
	model        string
	// dimensions and protocol are part of the local embedding-space identity,
	// so a proxy upgrade or dimensionality change never serves stale vectors.
	dimensions int
	protocol   Protocol
	progressFn func(completed, total int)
}

// Compile-time interface check.
var _ Embedder = (*RemoteEmbedder)(nil)

// NewRemote creates a RemoteEmbedder for the negotiated embedding space.
// tokenFn is called on each request to get the current auth token, and
// invalidateFn drops the cached token so a 401/403 can be retried with a fresh
// one (it may be nil to disable the retry).
// localCache is optional — when set, embedding vectors are cached locally
// using protocol-specific keys to avoid proxy round-trips.
func NewRemote(
	log logrus.FieldLogger,
	proxyURL string,
	tokenFn func() string,
	invalidateFn func(),
	localCache cache.Cache,
	model string,
	dimensions int,
	protocol Protocol,
) *RemoteEmbedder {
	return &RemoteEmbedder{
		log:      log.WithField("component", "remote-embedder"),
		proxyURL: proxyURL,
		// Per-call deadlines are set via request contexts in doWithAuthRetry;
		// the client stays unbounded so a query deadline is never silently
		// stretched to the batch one.
		httpClient:   &http.Client{},
		tokenFn:      tokenFn,
		invalidateFn: invalidateFn,
		localCache:   localCache,
		model:        model,
		dimensions:   dimensions,
		protocol:     protocol,
	}
}

// Model returns the embedding model this embedder is keyed to. Index builds use
// it to tag the embedding space they were built in, so a later change to the
// proxy's served model can be detected and trigger a re-index.
func (e *RemoteEmbedder) Model() string {
	return e.model
}

// OnProgress registers a callback invoked during EmbedBatch with the number of
// documents embedded so far and the total in the batch. It enables
// document-level progress reporting for index builds.
func (e *RemoteEmbedder) OnProgress(fn func(completed, total int)) {
	e.progressFn = fn
}

// Embed returns the L2-normalized QUERY embedding vector for a single
// search-query string.
func (e *RemoteEmbedder) Embed(text string) ([]float32, error) {
	vectors, err := e.embedAll([]string{text}, e.queryTask())
	if err != nil {
		return nil, err
	}

	if len(vectors) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return vectors[0], nil
}

// EmbedBatch returns L2-normalized DOCUMENT embedding vectors for multiple texts.
func (e *RemoteEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	return e.embedAll(texts, e.documentTask())
}

// EmbedQueryBatch returns L2-normalized QUERY embedding vectors for multiple
// query-shaped texts. v1/v2 are symmetric, so this is equivalent to EmbedBatch.
func (e *RemoteEmbedder) EmbedQueryBatch(texts []string) ([][]float32, error) {
	return e.embedAll(texts, e.queryTask())
}

func (e *RemoteEmbedder) queryTask() string {
	if e.protocol == ProtocolV3 {
		return taskQuery
	}

	return ""
}

func (e *RemoteEmbedder) documentTask() string {
	if e.protocol == ProtocolV3 {
		return taskDocument
	}

	return ""
}

// embedAll embeds texts under one task type. When a local cache is configured,
// vectors are checked there first. Remaining misses are split into sub-batches
// of maxBatchSize and sent to the proxy (which has its own Redis cache +
// upstream API).
func (e *RemoteEmbedder) embedAll(texts []string, task string) ([][]float32, error) {
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
			key := e.localCacheKey(task, hashes[0])

			data, found, err := e.localCache.Get(context.Background(), key)
			if err == nil && found {
				var vec []float32
				if err := json.Unmarshal(data, &vec); err == nil {
					return [][]float32{vec}, nil
				}
			}
		}

		vecs, err := e.embedDirect(texts, hashes, hashToIndices, task)
		if err != nil {
			return nil, err
		}

		// Cache the result for future queries.
		if e.localCache != nil && len(vecs) == 1 && vecs[0] != nil {
			toCache := make(map[string][]byte, 1)
			e.queueLocalCache(toCache, task, hashes[0], vecs[0])

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
			cacheKeys[i] = e.localCacheKey(task, h)
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

	e.reportProgress(localHits, len(texts))

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

		cached, err := e.checkCached(batchHashes, task)
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

				e.queueLocalCache(toCache, task, hashes[idx], vec)
			} else {
				missItems = append(missItems, embedItem{Hash: hashes[idx], Text: texts[idx]})
			}
		}

		if len(missItems) > 0 {
			e.log.WithFields(logrus.Fields{
				"total":  len(batchIndices),
				"cached": len(batchIndices) - len(missItems),
				"misses": len(missItems),
			}).Info("Proxy cache stats")

			resp, err := e.callEmbed(missItems, task)
			if err != nil {
				return nil, fmt.Errorf("embedding batch %d/%d: %w", batchNum, totalBatches, err)
			}

			for _, result := range resp.Results {
				for _, idx := range hashToIndices[result.Hash] {
					vectors[idx] = result.Vector
				}

				e.queueLocalCache(toCache, task, result.Hash, result.Vector)
			}
		}

		// batchEnd is the cumulative count of remote items processed so far.
		e.reportProgress(localHits+batchEnd, len(texts))
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

func (e *RemoteEmbedder) reportProgress(completed, total int) {
	if e.progressFn != nil {
		e.progressFn(completed, total)
	}
}

func (e *RemoteEmbedder) embedPath() string {
	switch e.protocol {
	case ProtocolV3:
		return embedPathV3
	case ProtocolV2:
		return embedPathV2
	default:
		return embedPathV1
	}
}

func (e *RemoteEmbedder) checkPath() string {
	switch e.protocol {
	case ProtocolV3:
		return embedCheckPathV3
	case ProtocolV2:
		return embedCheckPathV2
	default:
		return embedCheckPathV1
	}
}

// localCacheKey builds the local cache key from the protocol embedding-space
// identity: v3 {model}:{dims}:{task}:{hash}, v2 {model}:{dims}:{hash}, v1
// {model}:{hash}.
func (e *RemoteEmbedder) localCacheKey(task, textHash string) string {
	switch e.protocol {
	case ProtocolV3:
		return e.model + ":" + strconv.Itoa(e.dimensions) + ":" + task + ":" + textHash
	case ProtocolV2:
		return e.model + ":" + strconv.Itoa(e.dimensions) + ":" + textHash
	default:
		return e.model + ":" + textHash
	}
}

func (e *RemoteEmbedder) queueLocalCache(toCache map[string][]byte, task, textHash string, vec []float32) {
	if e.localCache == nil {
		return
	}

	data, err := json.Marshal(vec)
	if err != nil {
		return
	}

	toCache[e.localCacheKey(task, textHash)] = data
}

// embedDirect sends all items to the embed route without checking the cache first.
func (e *RemoteEmbedder) embedDirect(
	texts []string,
	hashes []string,
	hashToIndices map[string][]int,
	task string,
) ([][]float32, error) {
	items := make([]embedItem, len(texts))
	for i, text := range texts {
		items[i] = embedItem{Hash: hashes[i], Text: text}
	}

	resp, err := e.callEmbed(items, task)
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

// doWithAuthRetry POSTs jsonBody to the proxy path with the current auth token,
// retrying once after invalidating the token on a 401/403. It reads the whole
// response body so the per-call deadline covers the body too and the context
// can be released before returning.
func (e *RemoteEmbedder) doWithAuthRetry(path string, jsonBody []byte, timeout time.Duration) (int, []byte, error) {
	send := func() (int, []byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(
			ctx, http.MethodPost, e.proxyURL+path, bytes.NewReader(jsonBody),
		)
		if err != nil {
			return 0, nil, err
		}

		req.Header.Set("Content-Type", "application/json")

		if e.tokenFn != nil {
			if token := e.tokenFn(); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		}

		resp, err := e.httpClient.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, nil, err
		}

		return resp.StatusCode, body, nil
	}

	status, body, err := send()
	if err != nil {
		return 0, nil, err
	}

	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && e.invalidateFn != nil {
		e.invalidateFn()

		return send()
	}

	return status, body, nil
}

// verifyEmbeddingSpace fails fast when the proxy's advertised embedding space
// differs from the one this embedder is keyed to. Vectors from another space
// must never be cached under this space's keys or swapped into its index — a
// proxy config change mid-build would otherwise poison both silently (a
// same-dimensionality model swap is undetectable downstream).
func (e *RemoteEmbedder) verifyEmbeddingSpace(model string, dimensions int) error {
	if e.protocol == ProtocolV1 {
		return nil
	}

	if model != e.model {
		return fmt.Errorf(
			"proxy embedding space changed: serving %s/%d, expected %s/%d — re-resolve the space and re-index",
			model, dimensions, e.model, e.dimensions,
		)
	}

	// Historical v2 proxies predate the dimensions echo; zero means "not
	// advertised" and is accepted for the model/dimensions negotiated by ProbeV2.
	if e.protocol == ProtocolV2 && dimensions == 0 {
		return nil
	}

	if dimensions != e.dimensions {
		return fmt.Errorf(
			"proxy embedding space changed: serving %s/%d, expected %s/%d — re-resolve the space and re-index",
			model, dimensions, e.model, e.dimensions,
		)
	}

	return nil
}

func (e *RemoteEmbedder) checkCached(hashes []string, task string) ([]embedResult, error) {
	req := embedCheckRequest{Hashes: hashes}
	if e.protocol == ProtocolV3 {
		req.Task = task
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling check request: %w", err)
	}

	status, body, err := e.doWithAuthRetry(e.checkPath(), reqBody, remoteEmbedTimeout)
	if err != nil {
		return nil, fmt.Errorf("calling embed check: %w", err)
	}

	if status != http.StatusOK {
		return nil, fmt.Errorf("embed check returned status %d: %s", status, string(body))
	}

	var checkResp embedCheckResponse
	if err := json.Unmarshal(body, &checkResp); err != nil {
		return nil, fmt.Errorf("decoding check response: %w", err)
	}

	if err := e.verifyEmbeddingSpace(checkResp.Model, checkResp.Dimensions); err != nil {
		return nil, err
	}

	return checkResp.Cached, nil
}

func (e *RemoteEmbedder) callEmbed(items []embedItem, task string) (*embedResponse, error) {
	req := embedRequest{Items: items}
	if e.protocol == ProtocolV3 {
		req.Task = task
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling embed request: %w", err)
	}

	// A single item is the interactive-search shape: one short text embeds in
	// well under a second, so it must not inherit the batch deadline.
	timeout := remoteEmbedTimeout
	if len(items) == 1 {
		timeout = queryEmbedTimeout
	}

	status, body, err := e.doWithAuthRetry(e.embedPath(), reqBody, timeout)
	if err != nil {
		return nil, fmt.Errorf("calling proxy embed: %w", err)
	}

	if status != http.StatusOK {
		return nil, fmt.Errorf("proxy embed returned status %d: %s", status, string(body))
	}

	var embedResp embedResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return nil, fmt.Errorf("decoding embed response: %w", err)
	}

	if err := e.verifyEmbeddingSpace(embedResp.Model, embedResp.Dimensions); err != nil {
		return nil, err
	}

	return &embedResp, nil
}

func sha256Hex(text string) string {
	h := sha256.Sum256([]byte(text))

	return fmt.Sprintf("%x", h)
}
