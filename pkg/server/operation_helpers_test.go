package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/operations"
)

type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}

	return w.header
}

func (*failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func (*failingResponseWriter) WriteHeader(int) {}

func TestOperationHelpersDecodeAndParsePaths(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"args":{"datasource":"metrics","query":"up"}}`))
	args, err := decodeTypedOperationArgs[operations.PrometheusQueryArgs](req)
	if err != nil {
		t.Fatalf("decodeTypedOperationArgs() error = %v", err)
	}
	if args.Datasource != "metrics" || args.Query != "up" {
		t.Fatalf("decodeTypedOperationArgs() = %#v, want datasource=query fields", args)
	}

	if _, err := requiredStringArg(map[string]any{}, "datasource"); err == nil {
		t.Fatal("requiredStringArg(missing) error = nil, want error")
	}
	if got := optionalStringArg(map[string]any{"label": "job"}, "label"); got != "job" {
		t.Fatalf("optionalStringArg() = %q, want job", got)
	}
	if got := optionalSliceArg(map[string]any{"params": []any{"a", 1}}, "params"); len(got) != 2 {
		t.Fatalf("optionalSliceArg() len = %d, want 2", len(got))
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	if got, err := parsePrometheusTime("2023-11-14T22:13:20Z", now); err != nil || got != "1700000000" {
		t.Fatalf("parsePrometheusTime(RFC3339) = (%q, %v), want (1700000000, nil)", got, err)
	}
	if got, err := parseLokiTime("now", now); err != nil || got != "1700000000000000000" {
		t.Fatalf("parseLokiTime(now) = (%q, %v), want nanos for now", got, err)
	}
	if got, err := parseHexUint64(""); err != nil || got != 0 {
		t.Fatalf("parseHexUint64(\"\") = (%d, %v), want (0, nil)", got, err)
	}
	if _, err := parseHexUint64("not-hex"); err == nil {
		t.Fatal("parseHexUint64(invalid) error = nil, want error")
	}
}

func TestOperationHelpersWriteHelpers(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	writeOperationResponse(logrus.New(), rec, http.StatusAccepted, operations.NewObjectResponse(map[string]string{"status": "ok"}, nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("writeOperationResponse() status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("writeOperationResponse() body = %q, want encoded response", rec.Body.String())
	}

	msg := upstreamFailureMessage("prometheus.query", http.StatusBadGateway, []byte(strings.Repeat("x", 300)), "datasource=metrics")
	if !strings.Contains(msg, "datasource=metrics") || !strings.Contains(msg, "...") {
		t.Fatalf("upstreamFailureMessage() = %q, want context and truncation", msg)
	}

	writeOperationResponse(logrus.New(), &failingResponseWriter{}, http.StatusOK, operations.NewObjectResponse(map[string]string{"status": "ok"}, nil))
}

func TestOperationHelpersCoverFallbackParsers(t *testing.T) {
	t.Parallel()

	if _, err := decodeTypedOperationArgs[operations.PrometheusQueryArgs](httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{"))); err == nil {
		t.Fatal("decodeTypedOperationArgs(invalid JSON) error = nil, want error")
	}
	if got := optionalMapArg(map[string]any{}, "meta"); len(got) != 0 {
		t.Fatalf("optionalMapArg() len = %d, want 0", len(got))
	}
	if got := optionalIntArg(map[string]any{"limit": int64(7)}, "limit", 3); got != 7 {
		t.Fatalf("optionalIntArg(int64) = %d, want 7", got)
	}
	if got := optionalIntArg(map[string]any{}, "limit", 3); got != 3 {
		t.Fatalf("optionalIntArg(fallback) = %d, want 3", got)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	if got, err := parsePrometheusTime("now-5m", now); err != nil || got != "1699999700" {
		t.Fatalf("parsePrometheusTime(now-5m) = (%q, %v), want 1699999700", got, err)
	}
	if got, err := parsePrometheusTime("1700000000.9", now); err != nil || got != "1700000000" {
		t.Fatalf("parsePrometheusTime(float) = (%q, %v), want 1700000000", got, err)
	}
	if _, err := parsePrometheusTime("not-a-time", now); err == nil {
		t.Fatal("parsePrometheusTime(invalid) error = nil, want error")
	}

	if got, err := parseLokiTime("1700000000", now); err != nil || got != "1700000000000000000" {
		t.Fatalf("parseLokiTime(seconds) = (%q, %v), want nanoseconds", got, err)
	}
	if got, err := parseLokiTime("1700000000000000000", now); err != nil || got != "1700000000000000000" {
		t.Fatalf("parseLokiTime(nanos) = (%q, %v), want unchanged nanos", got, err)
	}
	if got, err := parseLokiTime("1700000000.5", now); err != nil || got != "1700000000500000000" {
		t.Fatalf("parseLokiTime(float) = (%q, %v), want float nanos", got, err)
	}
	if _, err := parseLokiTime("not-a-time", now); err == nil {
		t.Fatal("parseLokiTime(invalid) error = nil, want error")
	}
}
