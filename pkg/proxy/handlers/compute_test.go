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

// TestComputeHandlerForwardsCallerToken verifies the reverse proxy forwards the
// caller's verified bearer token unchanged (the compute backend validates it
// itself), strips the /compute prefix and cookies, forwards all methods, and
// fixes the upstream Host. No service token is injected.
func TestComputeHandlerForwardsCallerToken(t *testing.T) {
	var got struct {
		host, path, rawQuery, auth, cookie, subject, method string
		bodyLen                                             int
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.host = r.Host
		got.path = r.URL.Path
		got.rawQuery = r.URL.RawQuery
		got.auth = r.Header.Get("Authorization")
		got.cookie = r.Header.Get("Cookie")
		got.subject = r.Header.Get("X-Authentik-Sub")
		got.method = r.Method
		got.bodyLen = len(body)

		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "backend-ok")
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	h := NewComputeHandler(logrus.New(), []ComputeConfig{{
		Name: "production",
		URL:  backend.URL,
	}})

	req := httptest.NewRequest(http.MethodPost, "/compute/v1/sandboxes?limit=1", http.NoBody)
	req.Header.Set(DatasourceHeader, "production")
	req.Header.Set("Authorization", "Bearer user-jwt")
	req.Header.Set("Cookie", "compute_session=stolen")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	require.Equal(t, http.StatusAccepted, res.StatusCode, "proxy should return the backend status")

	// /compute prefix stripped, the rest of the path and query preserved.
	assert.Equal(t, "/v1/sandboxes", got.path)
	assert.Equal(t, "limit=1", got.rawQuery)
	assert.Equal(t, http.MethodPost, got.method, "mutating methods must be forwarded")

	// Host rewritten to the upstream target.
	assert.Equal(t, backendURL.Host, got.host)

	// The caller's verified token is forwarded unchanged; cookies dropped.
	assert.Equal(t, "Bearer user-jwt", got.auth)
	assert.Empty(t, got.cookie)

	// No forwarded-subject header is set; the backend reads the token instead.
	assert.Empty(t, got.subject)
}

// TestComputeHandlerForwardsBody verifies request bodies pass through for
// mutating calls.
func TestComputeHandlerForwardsBody(t *testing.T) {
	var gotBody string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer backend.Close()

	h := NewComputeHandler(logrus.New(), []ComputeConfig{{
		Name: "production",
		URL:  backend.URL,
	}})

	req := httptest.NewRequest(http.MethodPost, "/compute/v1/sandboxes", strings.NewReader(`{"template":"ubuntu/24.04"}`))
	req.Header.Set(DatasourceHeader, "production")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	assert.JSONEq(t, `{"template":"ubuntu/24.04"}`, gotBody)
}
