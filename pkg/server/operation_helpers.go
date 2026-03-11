package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/operations"
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
	w.Header().Set("X-Operation-Transport", "passthrough")
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

func parsePrometheusTime(value string, now time.Time) (string, error) {
	if value == "" {
		return "", nil
	}

	if value == "now" {
		return strconv.FormatInt(now.Unix(), 10), nil
	}

	if strings.HasPrefix(value, "now-") {
		seconds, err := parseDurationSeconds(strings.TrimPrefix(value, "now-"))
		if err != nil {
			return "", err
		}

		return strconv.FormatInt(now.Add(-time.Duration(seconds)*time.Second).Unix(), 10), nil
	}

	if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
		return strconv.FormatInt(intValue, 10), nil
	}

	if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.FormatInt(int64(floatValue), 10), nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("cannot parse time %q: %w", value, err)
	}

	return strconv.FormatInt(parsed.Unix(), 10), nil
}

func parseLokiTime(value string, now time.Time) (string, error) {
	if value == "" {
		return "", nil
	}

	if value == "now" {
		return strconv.FormatInt(now.UnixNano(), 10), nil
	}

	if strings.HasPrefix(value, "now-") {
		seconds, err := parseDurationSeconds(strings.TrimPrefix(value, "now-"))
		if err != nil {
			return "", err
		}

		return strconv.FormatInt(now.Add(-time.Duration(seconds)*time.Second).UnixNano(), 10), nil
	}

	if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
		if intValue < 1_000_000_000_000 {
			return strconv.FormatInt(intValue*1_000_000_000, 10), nil
		}

		return strconv.FormatInt(intValue, 10), nil
	}

	if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.FormatInt(int64(floatValue*1_000_000_000), 10), nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("cannot parse time %q: %w", value, err)
	}

	return strconv.FormatInt(parsed.UnixNano(), 10), nil
}

func parseHexUint64(value string) (uint64, error) {
	value = strings.TrimPrefix(value, "0x")
	if value == "" {
		return 0, nil
	}

	parsed, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex value %q: %w", value, err)
	}

	return parsed, nil
}
