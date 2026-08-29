package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethpandaops/cartographoor/pkg/discovery"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/operations"
)

// newBuildoorTestUpstream fakes a devnet: the overview service's host list
// pointing at one instance server, whose handler is supplied by the test.
func newBuildoorTestUpstream(t *testing.T, instanceHandler http.HandlerFunc) (overview, instance *httptest.Server) {
	t.Helper()

	instance = httptest.NewServer(instanceHandler)
	t.Cleanup(instance.Close)

	overview = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/overview/hosts" {
			http.NotFound(w, r)
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"hosts": []map[string]any{
				{"id": 0, "url": instance.URL, "label": "api-buildoor-prysm-ethrex-1.srv.testnet.ethpandaops.io"},
			},
		})
	}))
	t.Cleanup(overview.Close)

	return overview, instance
}

func newBuildoorOperationService(overviewURL string) *service {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return &service{
		log:        log,
		httpClient: http.DefaultClient,
		cartographoorClient: doraOperationCartographoor{
			networks: map[string]discovery.Network{
				"testnet": {
					Name:   "testnet",
					Status: "active",
					ServiceURLs: &discovery.ServiceURLs{
						Buildoor: overviewURL,
					},
				},
			},
		},
	}
}

func newBuildoorOpRequest(t *testing.T, args map[string]any) *http.Request {
	t.Helper()

	body, err := json.Marshal(operations.Request{Args: args})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/runtime/operations/buildoor",
		io.NopCloser(bytes.NewReader(body)),
	)
	req.Header.Set("Content-Type", "application/json")

	return req
}

func TestBuildoorListInstancesDerivesShortNames(t *testing.T) {
	t.Parallel()

	overview, instance := newBuildoorTestUpstream(t, http.NotFound)

	svc := newBuildoorOperationService(overview.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation(
		"buildoor.list_instances", rec, newBuildoorOpRequest(t, map[string]any{"network": "testnet"}),
	)
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	var response operations.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))

	data, ok := response.Data.(map[string]any)
	require.True(t, ok)

	instances, ok := data["instances"].([]any)
	require.True(t, ok)
	require.Len(t, instances, 1)

	entry, ok := instances[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "prysm-ethrex-1", entry["name"])
	assert.Equal(t, instance.URL, entry["url"])
}

func TestBuildoorUpdateActionPlanForwardsTokenAndUpdates(t *testing.T) {
	t.Parallel()

	var (
		gotAuth string
		gotBody map[string]any
	)

	overview, _ := newBuildoorTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/buildoor/action-plan", r.URL.Path)

		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "updated",
			"slots":  []uint64{1234},
			"plans":  []any{nil},
		})
	})

	svc := newBuildoorOperationService(overview.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation("buildoor.update_action_plan", rec, newBuildoorOpRequest(t, map[string]any{
		"network":    "testnet",
		"instance":   "prysm-ethrex-1",
		"auth_token": "test-token",
		"updates": []any{
			map[string]any{
				"slots": []any{float64(1234)},
				"set":   map[string]any{"transforms.payload": ".gas_limit = 300000000"},
			},
		},
	}))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "Bearer test-token", gotAuth)

	updates, ok := gotBody["updates"].([]any)
	require.True(t, ok)
	require.Len(t, updates, 1)

	update, ok := updates[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t,
		map[string]any{"transforms.payload": ".gas_limit = 300000000"},
		update["set"],
	)

	var response map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Equal(t, "updated", response["status"])
}

func TestBuildoorUpdateActionPlanRequiresUpdates(t *testing.T) {
	t.Parallel()

	overview, _ := newBuildoorTestUpstream(t, http.NotFound)

	svc := newBuildoorOperationService(overview.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation("buildoor.update_action_plan", rec, newBuildoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"instance": "prysm-ethrex-1",
	}))
	require.True(t, handled)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "updates is required")
}

func TestBuildoorUpdateActionPlanSurfacesUpstreamConflict(t *testing.T) {
	t.Parallel()

	overview, _ := newBuildoorTestUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "slot 10 is frozen"})
	})

	svc := newBuildoorOperationService(overview.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation("buildoor.update_action_plan", rec, newBuildoorOpRequest(t, map[string]any{
		"network":    "testnet",
		"instance":   "prysm-ethrex-1",
		"auth_token": "test-token",
		"updates":    []any{map[string]any{"slots": []any{float64(10)}, "transforms": nil}},
	}))
	require.True(t, handled)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "slot 10 is frozen")
	assert.Contains(t, rec.Body.String(), "target slots ≥2 ahead")
}

func TestBuildoorSlotRangeGetPassesRangeThrough(t *testing.T) {
	t.Parallel()

	overview, _ := newBuildoorTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/buildoor/action-plan", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("min_slot"))
		assert.Equal(t, "132", r.URL.Query().Get("max_slot"))

		_ = json.NewEncoder(w).Encode(map[string]any{"plans": []any{}, "min_slot": 100, "max_slot": 132})
	})

	svc := newBuildoorOperationService(overview.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation("buildoor.get_action_plan", rec, newBuildoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"instance": "prysm-ethrex-1",
		"min_slot": float64(100),
		"max_slot": float64(132),
	}))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "plans")
}

func TestBuildoorTestTransformPostsExpression(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any

	overview, _ := newBuildoorTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/buildoor/action-plan/test-transform", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		_ = json.NewEncoder(w).Encode(map[string]any{
			"target":       "payload",
			"input":        "{}",
			"input_source": "template",
			"output":       "{}",
		})
	})

	svc := newBuildoorOperationService(overview.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation("buildoor.test_transform", rec, newBuildoorOpRequest(t, map[string]any{
		"network":     "testnet",
		"instance":    "prysm-ethrex-1",
		"target":      "payload",
		"expression":  ".gas_limit = 300000000",
		"sample_slot": float64(42),
	}))
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "payload", gotBody["target"])
	assert.Equal(t, ".gas_limit = 300000000", gotBody["expression"])
	assert.Equal(t, float64(42), gotBody["sample_slot"])
}

func TestBuildoorUnknownInstanceListsAvailable(t *testing.T) {
	t.Parallel()

	overview, _ := newBuildoorTestUpstream(t, http.NotFound)

	svc := newBuildoorOperationService(overview.URL)
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation("buildoor.get_overview", rec, newBuildoorOpRequest(t, map[string]any{
		"network":  "testnet",
		"instance": "nope",
	}))
	require.True(t, handled)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "prysm-ethrex-1")
}

func TestBuildoorUnknownNetworkListsAvailable(t *testing.T) {
	t.Parallel()

	svc := newBuildoorOperationService("http://unused.invalid")
	rec := httptest.NewRecorder()

	handled := svc.handleBuildoorOperation("buildoor.list_instances", rec, newBuildoorOpRequest(t, map[string]any{
		"network": "othernet",
	}))
	require.True(t, handled)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "testnet")
}

func TestBuildoorInstanceName(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		label, url, want string
	}{
		"devnet host":     {"api-buildoor-lighthouse-geth-1.srv.devnet.ethpandaops.io", "", "lighthouse-geth-1"},
		"plain host":      {"builder-a.example.com", "", "builder-a"},
		"ip host":         {"10.0.0.1:8085", "", "10.0.0.1:8085"},
		"host with port":  {"a:8082", "", "a:8082"},
		"label from url":  {"", "http://10.0.0.1:8085", "10.0.0.1:8085"},
		"api only prefix": {"api-builder.example.com", "", "builder"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, buildoorInstanceName(tc.label, tc.url))
		})
	}
}
