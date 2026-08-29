package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeTestParams(t *testing.T, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	decoded, err := decodeOperationRequest(req)
	if err != nil {
		t.Fatalf("decodeOperationRequest: %v", err)
	}
	return optionalMapArg(decoded.Args, "parameters")
}

// Numeric ClickHouse parameters must render as plain integer literals that
// ClickHouse can parse, regardless of magnitude. Before the fix, values >= 1e6
// rendered in scientific notation and values above 2^53 lost precision.
func TestClickHouseNumericParamsRenderAsIntegers(t *testing.T) {
	params := decodeTestParams(t, `{"args":{"parameters":{"slot":7500000,"block":1000000,"ts":1700000000,"limit":42,"ids":[1000000,2000000]}}}`)

	cases := map[string]string{
		"slot":  "7500000",
		"block": "1000000",
		"ts":    "1700000000",
		"limit": "42",
	}
	for key, want := range cases {
		if got := formatClickHouseParamValue(params[key]); got != want {
			t.Errorf("param %s = %q, want %q", key, got, want)
		}
	}

	if got := formatClickHouseParamValue(params["ids"]); got != "[1000000,2000000]" {
		t.Errorf("array param = %q, want [1000000,2000000]", got)
	}
}

// Integers above 2^53 keep their exact value.
func TestClickHouseParamPreservesLargeIntegerPrecision(t *testing.T) {
	params := decodeTestParams(t, `{"args":{"parameters":{"bigid":9007199254740993}}}`)

	if got := formatClickHouseParamValue(params["bigid"]); got != "9007199254740993" {
		t.Errorf("bigid = %q, want 9007199254740993", got)
	}
}

// Non-integer numeric parameters render in plain decimal notation.
func TestClickHouseFloatParamRendersWithoutExponent(t *testing.T) {
	params := decodeTestParams(t, `{"args":{"parameters":{"ratio":1500000.5}}}`)

	got := formatClickHouseParamValue(params["ratio"])
	if strings.Contains(got, "e") || strings.Contains(got, "E") {
		t.Errorf("ratio = %q, want plain decimal without exponent", got)
	}
	if got != "1500000.5" {
		t.Errorf("ratio = %q, want 1500000.5", got)
	}
}
