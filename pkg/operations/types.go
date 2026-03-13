package operations

const (
	ResultKindObject = "object"
)

type Request struct {
	Args map[string]any `json:"args"`
}

type Response struct {
	Kind        string           `json:"kind"`
	RowEncoding string           `json:"row_encoding,omitempty"`
	Columns     []string         `json:"columns,omitempty"`
	Rows        []map[string]any `json:"rows,omitempty"`
	Matrix      [][]any          `json:"matrix,omitempty"`
	Data        any              `json:"data,omitempty"`
	Meta        map[string]any   `json:"meta,omitempty"`
}
