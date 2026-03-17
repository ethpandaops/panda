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
)

const remoteEmbedTimeout = 2 * time.Minute

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
type RemoteEmbedder struct {
	log        logrus.FieldLogger
	proxyURL   string
	httpClient *http.Client
	tokenFn    func() string
}

// Compile-time interface check.
var _ Embedder = (*RemoteEmbedder)(nil)

// NewRemote creates a new RemoteEmbedder that calls the proxy's /embed endpoint.
// tokenFn is called on each request to get the current auth token.
func NewRemote(log logrus.FieldLogger, proxyURL string, tokenFn func() string) *RemoteEmbedder {
	return &RemoteEmbedder{
		log:        log.WithField("component", "remote-embedder"),
		proxyURL:   proxyURL,
		httpClient: &http.Client{Timeout: remoteEmbedTimeout},
		tokenFn:    tokenFn,
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
// For batches larger than 1, it first checks the proxy cache with just hashes,
// then only sends text for uncached items.
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

	vectors := make([][]float32, len(texts))

	// For single items, skip the check and just embed directly.
	if len(texts) == 1 {
		return e.embedDirect(texts, hashes, hashToIndices)
	}

	// Phase 1: check which hashes are already cached.
	cached, err := e.checkCached(hashes)
	if err != nil {
		e.log.WithError(err).Warn("Cache check failed, falling back to full embed")

		return e.embedDirect(texts, hashes, hashToIndices)
	}

	// Fill in cached vectors.
	for _, result := range cached {
		for _, idx := range hashToIndices[result.Hash] {
			vectors[idx] = result.Vector
		}
	}

	// Collect misses.
	var missItems []embedItem
	for i, text := range texts {
		if vectors[i] == nil {
			missItems = append(missItems, embedItem{Hash: hashes[i], Text: text})
		}
	}

	if len(missItems) == 0 {
		return vectors, nil
	}

	e.log.WithFields(logrus.Fields{
		"total":  len(texts),
		"cached": len(texts) - len(missItems),
		"misses": len(missItems),
	}).Debug("Embedding batch cache stats")

	// Phase 2: embed only the misses.
	resp, err := e.callEmbed(missItems)
	if err != nil {
		return nil, err
	}

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

// Close is a no-op for the remote embedder.
func (e *RemoteEmbedder) Close() error {
	return nil
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
