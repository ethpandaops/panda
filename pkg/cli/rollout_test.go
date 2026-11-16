package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
)

// rolloutTestServer answers the rolloor operations with fixed data and
// records the action requests.
func rolloutTestServer(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()

	var (
		mu      sync.Mutex
		actions []map[string]any
	)

	data := map[string]any{
		"rolloor.list_networks": map[string]any{"networks": []any{map[string]any{"name": "glamsterdam-devnet-12", "url": "https://rolloor.glamsterdam-devnet-12.ethpandaops.io", "type": "rolloor"}}},
		"rolloor.fleet": map[string]any{"maxUnavailable": "10%", "unavailable": "3.1%", "groups": []any{
			map[string]any{"name": "lighthouse", "onDesired": 5, "targets": 8, "sync": "OutOfSync", "health": "Progressing", "reason": "Batch 2 in soak", "rollout": map[string]any{"state": "Soaking"}},
			map[string]any{"name": "geth", "onDesired": 8, "targets": 8, "sync": "Synced", "health": "Healthy", "reason": ""},
		}},
		"rolloor.rollouts": []any{map[string]any{"id": "6cb3b0cd89ed", "group": "lighthouse", "state": "Halted", "digest": "88f9568", "onNewBuild": 2, "total": 8, "createdAt": "2026-09-23T04:58:39.1Z"}},
		"rolloor.rollout": map[string]any{"id": "6cb3b0cd89ed", "group": "lighthouse", "state": "Halted", "reason": "Halted at batch 1", "digest": "88f9568", "revision": "e423a66",
			"onNewBuild": 2, "total": 8, "unavailable": "0.0% of 10%", "batches": []any{
				map[string]any{"number": 1, "wave": 1, "targets": []any{"node-1/beacon", "node-1/validator"}, "passed": false, "endedAt": "2026-09-23T05:00:00Z"},
				map[string]any{"number": 2, "wave": 1, "targets": []any{"node-2/beacon"}, "passed": false},
			}},
		"rolloor.history": []any{map[string]any{"at": "2026-09-23T05:06:14Z", "actor": "controller", "action": "rollout.halted", "group": "lighthouse", "reason": "node-1/beacon not ready"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := strings.TrimPrefix(r.URL.Path, "/api/v1/operations/")
		w.Header().Set("Content-Type", "application/json")

		if op == "rolloor.action" {
			raw, _ := io.ReadAll(r.Body)

			var req operations.Request
			require.NoError(t, json.Unmarshal(raw, &req))

			mu.Lock()
			actions = append(actions, req.Args)
			mu.Unlock()

			var out any = map[string]any{"rollouts": []any{"6cb3b0cd89ed"}}

			switch req.Args["verb"] {
			case "resume":
				out = map[string]any{"lifted": 2}
			case "pause":
				out = map[string]any{"id": "6cb3b0cd89ed", "state": "Running"}
			case "suspend":
				out = map[string]any{"id": "s1"}
			case "refresh":
				out = map[string]any{"status": "refreshing"}
			}

			_ = json.NewEncoder(w).Encode(operations.Response{Kind: operations.ResultKindObject, Data: out})

			return
		}

		var req operations.Request

		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)

		d, ok := data[op]
		if !ok || req.Args["network"] == "nowhere" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"rolloor is not running on x"}`))

			return
		}

		_ = json.NewEncoder(w).Encode(operations.Response{Kind: operations.ResultKindObject, Data: d})
	}))
	t.Cleanup(srv.Close)

	return srv, &actions
}

func runRollout(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var err error

	out := captureStdout(t, func() {
		cmd, rest, ferr := rolloutCmd.Find(args)
		require.NoError(t, ferr)
		cmd.SetContext(testCommand().Context())
		require.NoError(t, cmd.ParseFlags(rest))
		err = cmd.RunE(cmd, cmd.Flags().Args())
	})

	return out, err
}

func TestRolloutReads(t *testing.T) {
	srv, _ := rolloutTestServer(t)
	setClientConfig(t, srv.URL)
	setOutputFormat(t, "text")

	out, err := runRollout(t, "networks")
	require.NoError(t, err)
	assert.Contains(t, out, "glamsterdam-devnet-12")

	out, err = runRollout(t, "status", "glamsterdam-devnet-12")
	require.NoError(t, err)
	assert.Contains(t, out, "3.1% of 10% unavailable")
	assert.Contains(t, out, "lighthouse")
	assert.Contains(t, out, "5/8")
	assert.Contains(t, out, "Soaking")

	out, err = runRollout(t, "list", "glamsterdam-devnet-12")
	require.NoError(t, err)
	assert.Contains(t, out, "6cb3b0cd89ed")
	assert.Contains(t, out, "2/8")
	assert.Contains(t, out, "2026-09-23 04:58:39")

	out, err = runRollout(t, "show", "glamsterdam-devnet-12", "6cb3b0cd89ed")
	require.NoError(t, err)
	assert.Contains(t, out, "88f9568 (e423a66)")
	assert.Contains(t, out, "default")
	assert.Contains(t, out, "halted")
	assert.Contains(t, out, "open")
	assert.Contains(t, out, "node-1/beacon, node-1/validator")

	out, err = runRollout(t, "history", "glamsterdam-devnet-12", "--group", "lighthouse")
	require.NoError(t, err)
	assert.Contains(t, out, "rollout.halted")

	_, err = runRollout(t, "status", "nowhere")
	require.Error(t, err)

	setOutputFormat(t, "json")
	out, err = runRollout(t, "status", "glamsterdam-devnet-12")
	require.NoError(t, err)
	assert.Contains(t, out, `"maxUnavailable"`)
}

func TestRolloutActions(t *testing.T) {
	srv, actions := rolloutTestServer(t)
	setClientConfig(t, srv.URL)
	setOutputFormat(t, "text")

	out, err := runRollout(t, "sync", "glamsterdam-devnet-12", "client=lighthouse", "--strategy", "fast", "--confirm")
	require.NoError(t, err)
	assert.Contains(t, out, "sync: rollouts 6cb3b0cd89ed")

	out, err = runRollout(t, "pause", "glamsterdam-devnet-12", "6cb3b0cd89ed")
	require.NoError(t, err)
	assert.Contains(t, out, "pause: 6cb3b0cd89ed is Running")

	out, err = runRollout(t, "resume", "glamsterdam-devnet-12", "node=a")
	require.NoError(t, err)
	assert.Contains(t, out, "2 suspensions lifted")

	out, err = runRollout(t, "suspend", "glamsterdam-devnet-12", "node=a", "--reason", "debugging", "--for", "4h")
	require.NoError(t, err)
	assert.Contains(t, out, "suspend: s1")

	out, err = runRollout(t, "refresh", "glamsterdam-devnet-12")
	require.NoError(t, err)
	assert.Contains(t, out, "refresh: done")

	rolloutReason = ""
	_, err = runRollout(t, "retry", "glamsterdam-devnet-12", "6cb3b0cd89ed")
	require.ErrorContains(t, err, "--reason")

	require.Len(t, *actions, 5)
	assert.Equal(t, map[string]any{"selector": "client=lighthouse", "strategy": "fast", "force": false, "confirm": true}, (*actions)[0]["body"])
	assert.Equal(t, "4h0m0s", (*actions)[3]["body"].(map[string]any)["expiresIn"])
}
