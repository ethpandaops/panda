package serverapi

import (
	"time"

	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/types"
)

// APIDocResponse is the response for the python://ethpandaops resource.
type APIDocResponse struct {
	Library     string                     `json:"library"`
	Description string                     `json:"description"`
	Modules     map[string]types.ModuleDoc `json:"modules"`
}

type DatasourcesResponse struct {
	Datasources        []types.DatasourceInfo `json:"datasources"`
	S3Bucket           string                 `json:"s3_bucket,omitempty"`
	S3PublicURLPrefix  string                 `json:"s3_public_url_prefix,omitempty"`
	EthNodeAvailable   bool                   `json:"ethnode_available,omitempty"`
}

type ProxyAuthMetadataResponse struct {
	Enabled   bool   `json:"enabled"`
	IssuerURL string `json:"issuer_url,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	Resource  string `json:"resource,omitempty"`
}

type ResourceResponse struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type"`
	Content  string `json:"content"`
}

// ResourceInfo describes a single static resource.
type ResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
}

// ResourceTemplateInfo describes a resource template with URI parameters.
type ResourceTemplateInfo struct {
	URITemplate string `json:"uri_template"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
}

// ListResourcesResponse is the response for GET /api/v1/resources.
type ListResourcesResponse struct {
	Resources []ResourceInfo         `json:"resources"`
	Templates []ResourceTemplateInfo `json:"templates,omitempty"`
}

type RuntimeStorageUploadResponse struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

type RuntimeStorageFile struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"last_modified,omitempty"`
	URL          string `json:"url,omitempty"`
}

type RuntimeStorageListResponse struct {
	Files []RuntimeStorageFile `json:"files"`
}

type RuntimeStorageURLResponse struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

type SearchExampleResult struct {
	CategoryKey     string  `json:"category_key"`
	CategoryName    string  `json:"category_name"`
	ExampleName     string  `json:"example_name"`
	Description     string  `json:"description"`
	Query           string  `json:"query"`
	TargetCluster   string  `json:"target_cluster"`
	SimilarityScore float64 `json:"similarity_score"`
}

type SearchExamplesResponse struct {
	Type                string                 `json:"type"`
	Query               string                 `json:"query"`
	CategoryFilter      string                 `json:"category_filter,omitempty"`
	TotalMatches        int                    `json:"total_matches"`
	Results             []*SearchExampleResult `json:"results"`
	AvailableCategories []string               `json:"available_categories"`
}

type SearchRunbookResult struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Tags            []string `json:"tags"`
	Prerequisites   []string `json:"prerequisites"`
	Content         string   `json:"content"`
	FilePath        string   `json:"file_path"`
	SimilarityScore float64  `json:"similarity_score"`
}

type SearchRunbooksResponse struct {
	Type          string                 `json:"type"`
	Query         string                 `json:"query"`
	TagFilter     string                 `json:"tag_filter,omitempty"`
	TotalMatches  int                    `json:"total_matches"`
	Results       []*SearchRunbookResult `json:"results"`
	AvailableTags []string               `json:"available_tags"`
}

type ExecuteRequest struct {
	Code      string `json:"code"`
	Timeout   int    `json:"timeout,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type ExecuteResponse struct {
	Stdout              string                `json:"stdout,omitempty"`
	Stderr              string                `json:"stderr,omitempty"`
	ExitCode            int                   `json:"exit_code"`
	ExecutionID         string                `json:"execution_id"`
	OutputFiles         []string              `json:"output_files,omitempty"`
	Metrics             map[string]any        `json:"metrics,omitempty"`
	DurationSeconds     float64               `json:"duration_seconds"`
	SessionID           string                `json:"session_id,omitempty"`
	SessionFiles        []sandbox.SessionFile `json:"session_files,omitempty"`
	SessionTTLRemaining string                `json:"session_ttl_remaining,omitempty"`
}

type SessionResponse struct {
	SessionID      string                `json:"session_id"`
	CreatedAt      time.Time             `json:"created_at"`
	LastUsed       time.Time             `json:"last_used"`
	TTLRemaining   string                `json:"ttl_remaining"`
	WorkspaceFiles []sandbox.SessionFile `json:"workspace_files,omitempty"`
}

type ListSessionsResponse struct {
	Sessions    []SessionResponse `json:"sessions"`
	Total       int               `json:"total"`
	MaxSessions int               `json:"max_sessions"`
}

type CreateSessionResponse struct {
	SessionID    string `json:"session_id"`
	TTLRemaining string `json:"ttl_remaining,omitempty"`
}
