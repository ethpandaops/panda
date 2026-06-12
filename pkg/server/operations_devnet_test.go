package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/devnet"
)

func newDevnetTestService() *service {
	log := logrus.New()
	log.SetOutput(testWriter{})

	return &service{
		log:       log,
		devnetCfg: config.DevnetConfig{},
	}
}

// testWriter discards log output during tests.
type testWriter struct{}

func (testWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestHandleDevnetOperation_UnknownIsNotHandled(t *testing.T) {
	s := newDevnetTestService()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/operations/devnet.bogus", strings.NewReader(`{"args":{}}`))

	handled := s.handleDevnetOperation("devnet.bogus", w, r)
	assert.False(t, handled, "unknown devnet operation must fall through to the 404 path")
}

func TestHandleDevnetOperation_DownRequiresTarget(t *testing.T) {
	s := newDevnetTestService()
	w := httptest.NewRecorder()
	// No enclave and no all=true — must be a 400 before any engine call.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/operations/devnet.down", strings.NewReader(`{"args":{}}`))

	handled := s.handleDevnetOperation("devnet.down", w, r)
	require.True(t, handled)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDevnetOperation_InspectRequiresEnclave(t *testing.T) {
	s := newDevnetTestService()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/operations/devnet.inspect", strings.NewReader(`{"args":{}}`))

	handled := s.handleDevnetOperation("devnet.inspect", w, r)
	require.True(t, handled)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleDevnetLs_Integration exercises the full handler path against a live
// Kurtosis engine (decode → connect → list → JSON response). It skips when no
// engine/gateway is reachable, so it is safe in CI but validates locally.
func TestHandleDevnetLs_Integration(t *testing.T) {
	s := newDevnetTestService()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/operations/devnet.ls", strings.NewReader(`{"args":{}}`))

	require.True(t, s.handleDevnetOperation("devnet.ls", w, r))

	if w.Code != http.StatusOK {
		t.Skipf("Kurtosis engine not reachable (status %d) — skipping integration check: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}

	var resp struct {
		Kind string           `json:"kind"`
		Data []devnet.Enclave `json:"data"`
		Meta map[string]any   `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "devnet.ls", resp.Kind)
	// Data may be empty (no enclaves) — the point is it decoded as the enclave list.
}
