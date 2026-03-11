package operations

import (
	"encoding/json"
	"fmt"
)

// TypedRequest preserves a concrete args schema while keeping the wire format
// compatible with the existing {"args": ...} operation envelope.
type TypedRequest[T any] struct {
	Args T `json:"args"`
}

func DecodeArgs[T any](args map[string]any) (T, error) {
	return decodeJSONValue[T](args, "operation args")
}

func DecodeValue[T any](value any) (T, error) {
	return decodeJSONValue[T](value, "operation value")
}

func DecodeResponseData[T any](response *Response) (T, error) {
	var target T
	if response == nil {
		return target, fmt.Errorf("operation response is required")
	}

	return decodeJSONValue[T](response.Data, "operation data")
}

func NewObjectResponse[T any](data T, meta map[string]any) Response {
	return Response{
		Kind: ResultKindObject,
		Data: data,
		Meta: meta,
	}
}

func decodeJSONValue[T any](value any, label string) (T, error) {
	var target T

	payload, err := json.Marshal(value)
	if err != nil {
		return target, fmt.Errorf("marshaling %s: %w", label, err)
	}

	if err := json.Unmarshal(payload, &target); err != nil {
		return target, fmt.Errorf("decoding %s: %w", label, err)
	}

	return target, nil
}
