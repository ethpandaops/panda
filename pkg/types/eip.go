package types

// EIP represents an Ethereum Improvement Proposal.
type EIP struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Status      string `json:"status"`
	Type        string `json:"type"`
	Category    string `json:"category,omitempty"`
	Created     string `json:"created,omitempty"`
	Requires    string `json:"requires,omitempty"`
	Content     string `json:"content,omitempty"`
	URL         string `json:"url"`
}

// EIPVector holds a cached embedding vector with a hash of the source text
// used to generate it. When the text changes, the vector must be recomputed.
type EIPVector struct {
	TextHash string    `json:"text_hash"`
	Vector   []float32 `json:"vector"`
}
