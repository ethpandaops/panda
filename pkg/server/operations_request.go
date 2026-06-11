package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/operations"
)

func decodeOperationRequest(r *http.Request) (operations.Request, error) {
	defer func() { _ = r.Body.Close() }()

	var req operations.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return operations.Request{}, fmt.Errorf("invalid request body: %w", err)
	}

	if req.Args == nil {
		req.Args = make(map[string]any)
	}

	return req, nil
}

func writeOperationResponse(log logrus.FieldLogger, w http.ResponseWriter, status int, response operations.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.WithError(err).Error("Failed to encode operation response")
	}
}

func writePassthroughResponse(w http.ResponseWriter, status int, contentType string, body []byte) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	if len(body) == 0 {
		return
	}

	_, _ = w.Write(body)
}

func requiredStringArg(args map[string]any, key string) (string, error) {
	value, _ := args[key].(string)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}

	return value, nil
}

func optionalStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func optionalMapArg(args map[string]any, key string) map[string]any {
	value, _ := args[key].(map[string]any)
	if value == nil {
		return make(map[string]any)
	}

	return value
}

func optionalSliceArg(args map[string]any, key string) []any {
	value, _ := args[key].([]any)
	return value
}

func optionalIntArg(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return fallback
	}
}

func optionalBoolArg(args map[string]any, key string) (*bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}

	switch v := raw.(type) {
	case bool:
		return &v, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			t := true
			return &t, nil
		case "false", "0", "no":
			f := false
			return &f, nil
		}
		return nil, fmt.Errorf("%s must be a boolean", key)
	default:
		return nil, fmt.Errorf("%s must be a boolean", key)
	}
}

func parseInt64Arg(raw any, key string) (int64, error) {
	switch value := raw.(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}

		return int64(value), nil
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", key, err)
		}

		return parsed, nil
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", key, err)
		}

		return parsed, nil
	default:
		return 0, fmt.Errorf("%s is required", key)
	}
}

func parseDurationSeconds(duration string) (int, error) {
	units := map[byte]int{
		's': 1,
		'm': 60,
		'h': 3600,
		'd': 86400,
		'w': 604800,
	}

	if duration == "" {
		return 0, nil
	}

	unit := duration[len(duration)-1]
	multiplier, ok := units[unit]
	if !ok {
		return 0, fmt.Errorf("unknown duration unit: %c", unit)
	}

	value, err := strconv.Atoi(duration[:len(duration)-1])
	if err != nil {
		return 0, fmt.Errorf("invalid duration value: %s", duration)
	}

	return value * multiplier, nil
}
