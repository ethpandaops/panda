package types

// ConsensusSpec represents a parsed consensus-specs document.
type ConsensusSpec struct {
	Fork    string `json:"fork"`
	Topic   string `json:"topic"`
	Title   string `json:"title"`
	Content string `json:"content,omitempty"`
	URL     string `json:"url"`
}

// SpecConstant represents a protocol constant from consensus-specs presets.
type SpecConstant struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Fork  string `json:"fork"`
}
