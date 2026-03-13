package operations

const (
	ResultKindObject = "object"
)

type Request struct {
	Args map[string]any `json:"args"`
}

type Response struct {
	Kind string         `json:"kind"`
	Data any            `json:"data,omitempty"`
	Meta map[string]any `json:"meta,omitempty"`
}
