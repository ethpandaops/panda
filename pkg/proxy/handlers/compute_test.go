package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type computeSubjectKey struct{}

// computeTestSubject extracts a subject from the request context, modeling the
// proxy's verified-identity lookup without importing the proxy package.
func computeTestSubject(ctx context.Context) string {
	subject, _ := ctx.Value(computeSubjectKey{}).(string)

	return subject
}

// TestComputeHandlerForwardsServiceTokenAndSubject verifies the reverse proxy
// strips the caller's credentials, injects the configured service token, and
// forwards the verified end-user subject so the backend can authorize per user.
func TestComputeHandlerForwardsServiceTokenAndSubject(t *testing.T) {
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
		got.subject = r.Header.Get(DefaultForwardedSubjectHeader)
		got.method = r.Method
		got.bodyLen = len(body)

		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "backend-ok")
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	h := NewComputeHandler(logrus.New(), []ComputeConfig{{
		Name:  "production",
		URL:   backend.URL,
		Token: "svc_token",
	}}, computeTestSubject)

	req := httptest.NewRequest(http.MethodPost, "/compute/v1/sandboxes?limit=1", http.NoBody)
	req.Header.Set(DatasourceHeader, "production")
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set("Cookie", "compute_session=stolen")
	// A spoofed inbound subject header must never reach the backend.
	req.Header.Set(DefaultForwardedSubjectHeader, "spoofed-subject")
	req = req.WithContext(context.WithValue(req.Context(), computeSubjectKey{}, "verified-user"))

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

	// Caller credentials replaced with the service token; cookies dropped.
	assert.Equal(t, "Bearer svc_token", got.auth)
	assert.Empty(t, got.cookie)

	// The forwarded subject is the verified identity, not the spoofed header.
	assert.Equal(t, "verified-user", got.subject)
}

// TestComputeHandlerDropsSpoofedSubjectWhenUnauthenticated verifies that with
// no verified subject in context, the inbound (spoofable) subject header is
// stripped and not forwarded.
func TestComputeHandlerDropsSpoofedSubjectWhenUnauthenticated(t *testing.T) {
	var gotSubject string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubject = r.Header.Get(DefaultForwardedSubjectHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h := NewComputeHandler(logrus.New(), []ComputeConfig{{
		Name:  "production",
		URL:   backend.URL,
		Token: "svc_token",
	}}, computeTestSubject)

	req := httptest.NewRequest(http.MethodGet, "/compute/v1/sandboxes", nil)
	req.Header.Set(DatasourceHeader, "production")
	req.Header.Set(DefaultForwardedSubjectHeader, "spoofed-subject")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	assert.Empty(t, gotSubject, "an unverified inbound subject header must not be forwarded")
}

// TestComputeHandlerHonoursCustomSubjectHeader verifies a configured override of
// the forwarded subject header is used instead of the default.
func TestComputeHandlerHonoursCustomSubjectHeader(t *testing.T) {
	const customHeader = "X-Compute-Sub"

	var gotCustom, gotDefault string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get(customHeader)
		gotDefault = r.Header.Get(DefaultForwardedSubjectHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h := NewComputeHandler(logrus.New(), []ComputeConfig{{
		Name:                   "production",
		URL:                    backend.URL,
		Token:                  "svc_token",
		ForwardedSubjectHeader: customHeader,
	}}, computeTestSubject)

	req := httptest.NewRequest(http.MethodGet, "/compute/v1/sandboxes", nil)
	req.Header.Set(DatasourceHeader, "production")
	req = req.WithContext(context.WithValue(req.Context(), computeSubjectKey{}, "verified-user"))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	assert.Equal(t, "verified-user", gotCustom, "the custom subject header should carry the subject")
	assert.Empty(t, gotDefault, "the default header should be unused when overridden")
}
