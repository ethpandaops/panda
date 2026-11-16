package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authclient "github.com/ethpandaops/panda/pkg/auth/client"
	"github.com/ethpandaops/panda/pkg/operations"
)

const rolloorNet = "glamsterdam-devnet-12"

// fakeRolloor answers as rolloor for one network under /n/<network>, and
// as a proxy under /rolloor/<network>, recording what the proxy was sent.
type fakeRolloor struct {
	mu      sync.Mutex
	probes  int
	actions []string
	auth    []string
	srv     *httptest.Server
}

func newFakeRolloor(t *testing.T) *fakeRolloor {
	t.Helper()

	f := &fakeRolloor{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /n/"+rolloorNet+"/healthz", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.probes++
		f.mu.Unlock()

		_, _ = w.Write([]byte(`{"ok":true,"environment":"` + rolloorNet + `"}`))
	})
	mux.HandleFunc("GET /n/not-rolloor/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>dora</html>`))
	})
	mux.HandleFunc("GET /n/"+rolloorNet+"/api/v1/fleet", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"environment":"` + rolloorNet + `","maxUnavailable":"10%","groups":[]}`))
	})
	mux.HandleFunc("GET /n/"+rolloorNet+"/api/v1/rollouts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "abc" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such rollout"}`))

			return
		}

		_, _ = w.Write([]byte(`{"id":"abc","state":"Running"}`))
	})
	mux.HandleFunc("GET /n/"+rolloorNet+"/api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1,"action":"` + r.URL.Query().Get("group") + `","limit":"` + r.URL.Query().Get("limit") + `"}]`))
	})
	mux.HandleFunc("GET /n/"+rolloorNet+"/api/v1/rollouts", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	mux.HandleFunc("POST /rolloor/{network}/api/v1/actions/{verb}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.actions = append(f.actions, r.PathValue("network")+" "+r.PathValue("verb")+" "+string(body))
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()

		if r.PathValue("verb") == "abort" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"you're not listed under lighthouse"}`))

			return
		}

		_, _ = w.Write([]byte(`{"rollouts":["abc"]}`))
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	return f
}

func newRolloorService(t *testing.T, f *fakeRolloor) *service {
	t.Helper()

	orig := rolloorURLPattern
	rolloorURLPattern = f.srv.URL + "/n/%s"

	t.Cleanup(func() { rolloorURLPattern = orig })

	svc := newNetworkOperationService()
	svc.httpClient = f.srv.Client()
	svc.cartographoorClient = networkOperationCartographoor{networks: map[string]discovery.Network{
		rolloorNet:             {Name: rolloorNet, Status: "active"},
		"not-rolloor":          {Name: "not-rolloor", Status: "active"},
		"old-devnet-1":         {Name: "old-devnet-1", Status: "inactive"},
		"mainnet":              {Name: "mainnet", Status: "active"},
		"glamsterdam-devnet-9": {Name: "glamsterdam-devnet-9", Status: "active"},
	}}
	svc.proxyService = &fakeProxy{url: f.srv.URL, tokens: []string{"user-token"}}

	return svc
}

func runRolloorOp(t *testing.T, svc *service, op string, args map[string]any) (int, operations.Response, string) {
	t.Helper()

	rec := httptest.NewRecorder()
	require.True(t, svc.handleRolloorOperation(op, rec, newNetworkOpRequest(t, args)))

	var resp operations.Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	return rec.Code, resp, rec.Body.String()
}

func TestRolloorNetworksAreTheDevnetsThatAnswer(t *testing.T) {
	f := newFakeRolloor(t)
	svc := newRolloorService(t, f)

	code, resp, _ := runRolloorOp(t, svc, "rolloor.list_networks", map[string]any{})
	require.Equal(t, http.StatusOK, code)

	data, _ := resp.Data.(map[string]any)
	items, _ := data["networks"].([]any)
	require.Len(t, items, 1, "only the active devnet that answers as rolloor")
	assert.Equal(t, rolloorNet, items[0].(map[string]any)["name"])

	// Answers are remembered for a while.
	runRolloorOp(t, svc, "rolloor.list_networks", map[string]any{})
	f.mu.Lock()
	assert.Equal(t, 1, f.probes)
	f.mu.Unlock()

	// A network without rolloor says which have it.
	code, _, body := runRolloorOp(t, svc, "rolloor.fleet", map[string]any{"network": "glamsterdam-devnet-9"})
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, body, "rolloor is not running on glamsterdam-devnet-9. Networks with rolloor: "+rolloorNet)

	code, _, _ = runRolloorOp(t, svc, "rolloor.fleet", map[string]any{"network": "Bad_Name"})
	assert.Equal(t, http.StatusBadRequest, code)

	code, _, _ = runRolloorOp(t, svc, "rolloor.fleet", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, code)

	assert.False(t, svc.handleRolloorOperation("rolloor.nope", httptest.NewRecorder(), newNetworkOpRequest(t, nil)))
}

func TestRolloorNoNetworkRunsItYet(t *testing.T) {
	f := newFakeRolloor(t)
	svc := newRolloorService(t, f)
	svc.cartographoorClient = networkOperationCartographoor{networks: map[string]discovery.Network{"x-devnet-1": {Status: "active"}}}

	code, _, body := runRolloorOp(t, svc, "rolloor.fleet", map[string]any{"network": "x-devnet-1"})
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, body, "no network runs it yet")

	svc.cartographoorClient = nil
	assert.Empty(t, svc.rolloorCandidates())
}

func TestRolloorReads(t *testing.T) {
	f := newFakeRolloor(t)
	svc := newRolloorService(t, f)

	code, resp, _ := runRolloorOp(t, svc, "rolloor.fleet", map[string]any{"network": rolloorNet})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "10%", resp.Data.(map[string]any)["maxUnavailable"])
	assert.Equal(t, rolloorNet, resp.Meta["network"])

	code, resp, _ = runRolloorOp(t, svc, "rolloor.rollout", map[string]any{"network": rolloorNet, "id": "abc"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Running", resp.Data.(map[string]any)["state"])

	code, _, body := runRolloorOp(t, svc, "rolloor.rollout", map[string]any{"network": rolloorNet, "id": "zzz"})
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, body, "no such rollout")

	code, _, _ = runRolloorOp(t, svc, "rolloor.rollout", map[string]any{"network": rolloorNet})
	assert.Equal(t, http.StatusBadRequest, code, "id is required")

	code, resp, _ = runRolloorOp(t, svc, "rolloor.history", map[string]any{"network": rolloorNet, "group": "lighthouse", "limit": 5, "selector": "node=x"})
	require.Equal(t, http.StatusOK, code)
	first := resp.Data.([]any)[0].(map[string]any)
	assert.Equal(t, "lighthouse", first["action"])
	assert.Equal(t, "5", first["limit"])

	code, _, body = runRolloorOp(t, svc, "rolloor.rollouts", map[string]any{"network": rolloorNet})
	assert.Equal(t, http.StatusBadGateway, code)
	assert.Contains(t, body, "not JSON")
}

func TestRolloorActionsGoThroughTheProxyWithTheUsersToken(t *testing.T) {
	f := newFakeRolloor(t)
	svc := newRolloorService(t, f)

	// Signed out: a friendly refusal before anything leaves.
	code, _, body := runRolloorOp(t, svc, "rolloor.action", map[string]any{"network": rolloorNet, "verb": "sync"})
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Contains(t, body, "panda auth login")

	ctrl, path := newTestController(t, &fakeAuthClient{})
	seedTokens(t, path, &authclient.Tokens{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour)})
	svc.credentials = ctrl

	code, resp, _ := runRolloorOp(t, svc, "rolloor.action", map[string]any{
		"network": rolloorNet, "verb": "sync", "body": map[string]any{"selector": "client=lighthouse"},
	})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{"abc"}, resp.Data.(map[string]any)["rollouts"])

	f.mu.Lock()
	require.Len(t, f.actions, 1)
	assert.Equal(t, rolloorNet+` sync {"selector":"client=lighthouse"}`, f.actions[0])
	assert.Equal(t, "Bearer user-token", f.auth[0])
	f.mu.Unlock()

	// rolloor's refusal comes back with its reason.
	code, _, body = runRolloorOp(t, svc, "rolloor.action", map[string]any{"network": rolloorNet, "verb": "abort", "body": map[string]any{"rollout": "abc"}})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "not listed under lighthouse")

	for _, args := range []map[string]any{
		{"network": rolloorNet, "verb": "delete"},
		{"network": rolloorNet},
	} {
		code, _, _ = runRolloorOp(t, svc, "rolloor.action", args)
		assert.Equal(t, http.StatusBadRequest, code)
	}

	code, _, _ = runRolloorOp(t, svc, "rolloor.action", map[string]any{"network": "glamsterdam-devnet-9", "verb": "sync"})
	assert.Equal(t, http.StatusNotFound, code)

	svc.proxyService = nil
	code, _, _ = runRolloorOp(t, svc, "rolloor.action", map[string]any{"network": rolloorNet, "verb": "sync"})
	assert.Equal(t, http.StatusServiceUnavailable, code)
}

func TestRolloorResultWithoutAnErrorField(t *testing.T) {
	rec := httptest.NewRecorder()
	(&service{log: newNetworkOperationService().log}).writeRolloorResult(rec, http.StatusTeapot, []byte(`{}`), "n", "u")
	assert.Equal(t, http.StatusTeapot, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "rolloor answered 418"))
}
