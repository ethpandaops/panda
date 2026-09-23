package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRolloorHandlerForwardsTheCallersTokenAndLittleElse(t *testing.T) {
	var got *http.Request

	var body string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)

		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	h := NewRolloorHandler(logrus.New())

	var asked string

	h.upstream = func(network string) *url.URL {
		asked = network

		return target
	}

	req := httptest.NewRequest(http.MethodPost, "/rolloor/glamsterdam-devnet-12/api/v1/actions/sync", strings.NewReader(`{"selector":"client=lighthouse"}`))
	req.Header.Set("Authorization", "Bearer user-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "secret=1")
	req.Header.Set("X-Panda-Attribution", "agent")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "glamsterdam-devnet-12", asked)
	assert.Equal(t, "/api/v1/actions/sync", got.URL.Path)
	assert.Equal(t, "Bearer user-token", got.Header.Get("Authorization"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.Empty(t, got.Header.Get("Cookie"))
	assert.Empty(t, got.Header.Get("X-Panda-Attribution"))
	assert.JSONEq(t, `{"selector":"client=lighthouse"}`, body)

	// The default upstream is the network's rolloor host.
	assert.Equal(t, "rolloor.glamsterdam-devnet-12.ethpandaops.io", NewRolloorHandler(logrus.New()).upstream("glamsterdam-devnet-12").Host)
}

func TestRolloorHandlerRefusesOddRequests(t *testing.T) {
	h := NewRolloorHandler(logrus.New())

	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/rolloor/Bad_Net/api/v1/fleet", http.StatusBadRequest},
		{http.MethodGet, "/rolloor/devnet-1/healthz", http.StatusBadRequest},
		{http.MethodGet, "/rolloor/devnet-1/api/v1/../../x", http.StatusBadRequest},
		{http.MethodDelete, "/rolloor/devnet-1/api/v1/fleet", http.StatusMethodNotAllowed},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, http.NoBody))
		assert.Equal(t, tc.status, rec.Code, tc.path)
	}

	// An unreachable rolloor is a clean 502.
	h.upstream = func(string) *url.URL { return &url.URL{Scheme: "http", Host: "127.0.0.1:1"} }
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rolloor/devnet-1/api/v1/fleet", http.NoBody))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
