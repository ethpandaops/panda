package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// callDevnet invokes a devnet operation handler with the given args and returns
// the recorder.
func callDevnet(s *service, op string, args map[string]any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"args": args})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/operations/"+op, strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.handleDevnetOperation(op, w, r)

	return w
}

// TestDevnetLifecycle_Live runs a real up → ls → inspect → down against a live
// Kurtosis engine on the configured cluster. It is heavyweight (spins up a
// devnet, ~minutes) so it only runs when PANDA_DEVNET_LIVE=1.
//
//	PANDA_DEVNET_LIVE=1 go test ./pkg/server/ -run TestDevnetLifecycle_Live -timeout 600s -v
func TestDevnetLifecycle_Live(t *testing.T) {
	if os.Getenv("PANDA_DEVNET_LIVE") == "" {
		t.Skip("set PANDA_DEVNET_LIVE=1 to run the live devnet lifecycle test")
	}

	s := newDevnetTestService()
	s.clusterCfg = config.ClusterConfig{Name: "bruno", KubeconfigContext: "bruno"}
	s.devnetCfg = config.DevnetConfig{DockerCache: "docker.ethquokkaops.io"}

	const enclave = "panda-live-test"
	params := "participants:\n  - el_type: geth\n    cl_type: lighthouse\n    count: 1\nadditional_services: []\n"

	// Best-effort cleanup of any prior run, and on exit.
	_ = callDevnet(s, "devnet.down", map[string]any{"enclave": enclave})
	t.Cleanup(func() { _ = callDevnet(s, "devnet.down", map[string]any{"enclave": enclave}) })

	t.Log("up (real devnet, this takes a few minutes)…")
	w := callDevnet(s, "devnet.up", map[string]any{"name": enclave, "args": params})
	require.Equal(t, http.StatusOK, w.Code, "up failed: %s", w.Body.String())

	var up struct {
		Data struct {
			Enclave string `json:"enclave"`
			Error   string `json:"error"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &up))
	require.Empty(t, up.Data.Error, "up returned a run error")
	require.Equal(t, enclave, up.Data.Enclave)
	assert.Equal(t, true, up.Meta["success"])

	// ls — the enclave should be listed.
	w = callDevnet(s, "devnet.ls", map[string]any{})
	require.Equal(t, http.StatusOK, w.Code)
	var ls struct {
		Data []devnet.Enclave `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ls))
	found := false
	for _, e := range ls.Data {
		if e.Name == enclave {
			found = true
		}
	}
	assert.True(t, found, "enclave not found in ls")

	// inspect.
	w = callDevnet(s, "devnet.inspect", map[string]any{"enclave": enclave})
	require.Equal(t, http.StatusOK, w.Code)
	var insp struct {
		Data devnet.Enclave `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &insp))
	assert.Equal(t, enclave, insp.Data.Name)

	// down.
	w = callDevnet(s, "devnet.down", map[string]any{"enclave": enclave})
	require.Equal(t, http.StatusOK, w.Code, "down failed: %s", w.Body.String())
	t.Log("lifecycle ok: up → ls → inspect → down")
}
