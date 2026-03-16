package types

// EIP represents a parsed Ethereum Improvement Proposal.
type EIP struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author,omitempty"`
	Status      string `json:"status"`
	Type        string `json:"type"`
	Category    string `json:"category,omitempty"`
	Created     string `json:"created,omitempty"`
	Requires    string `json:"requires,omitempty"`
	Content     string `json:"content,omitempty"`
	URL         string `json:"url"`
}

// EIPVector stores a cached embedding vector for a chunk of EIP text.
type EIPVector struct {
	TextHash string    `json:"text_hash"`
	Vector   []float32 `json:"vector"`
}
