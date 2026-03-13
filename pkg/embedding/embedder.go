// Package embedding provides text embedding capabilities using ONNX models via hugot.
package embedding

import (
	"fmt"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// Embedder provides text embedding capabilities using hugot's pure Go ONNX backend.
type Embedder struct {
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
}

// New creates a new Embedder with the given ONNX model directory path.
func New(modelPath string) (*Embedder, error) {
	if modelPath == "" {
		return nil, fmt.Errorf("model path is required")
	}

	session, err := hugot.NewGoSession()
	if err != nil {
		return nil, fmt.Errorf("creating hugot session: %w", err)
	}

	config := hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "embedder",
		OnnxFilename: "model.onnx",
		Options: []hugot.FeatureExtractionOption{
			pipelines.WithNormalization(),
		},
	}

	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("creating embedding pipeline from %s: %w", modelPath, err)
	}

	return &Embedder{session: session, pipeline: pipeline}, nil
}

// Embed returns the L2-normalized embedding vector for a single text string.
func (e *Embedder) Embed(text string) ([]float32, error) {
	result, err := e.pipeline.RunPipeline([]string{text})
	if err != nil {
		return nil, fmt.Errorf("embedding text: %w", err)
	}

	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	return result.Embeddings[0], nil
}

// EmbedBatch returns L2-normalized embedding vectors for multiple texts.
func (e *Embedder) EmbedBatch(texts []string) ([][]float32, error) {
	result, err := e.pipeline.RunPipeline(texts)
	if err != nil {
		return nil, fmt.Errorf("embedding batch: %w", err)
	}

	return result.Embeddings, nil
}

// Close releases resources held by the embedder.
func (e *Embedder) Close() error {
	if e == nil || e.session == nil {
		return nil
	}

	if e.session != nil {
		return e.session.Destroy()
	}

	return nil
}
