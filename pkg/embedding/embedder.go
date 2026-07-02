// Package embedding provides text embedding capabilities.
package embedding

// Embedder provides text embedding capabilities for retrieval. The methods
// select which side of asymmetric retrieval a text embeds on: Embed and
// EmbedQueryBatch produce QUERY-space vectors, EmbedBatch produces
// DOCUMENT-space vectors for the indexed texts queries are matched against.
type Embedder interface {
	// Embed returns the L2-normalized QUERY embedding vector for a single
	// search-query string.
	Embed(text string) ([]float32, error)

	// EmbedQueryBatch returns L2-normalized QUERY embedding vectors for
	// query-shaped index texts (e.g. runbook triggers — authored hypothetical
	// queries that incoming queries should match near-verbatim, which only
	// works when both sit in the same query space).
	EmbedQueryBatch(texts []string) ([][]float32, error)

	// EmbedBatch returns L2-normalized DOCUMENT embedding vectors for the
	// texts of an index build.
	EmbedBatch(texts []string) ([][]float32, error)

	// Close releases resources held by the embedder.
	Close() error
}
