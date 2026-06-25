package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSpecFetchService() *service {
	return &service{httpClient: http.DefaultClient}
}

func TestFetchNetworkSpecSuccess(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("# spec\n\n## Local testing\n"))
	}))
	defer srv.Close()

	md, status, err := newSpecFetchService().fetchNetworkSpec(context.Background(), srv.URL+"/@ethpandaops/x")

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, md, "## Local testing")
	assert.Equal(t, "/@ethpandaops/x/download", gotPath, "should request HackMD /download")
}

func TestFetchNetworkSpecDoesNotDoubleSuffixDownload(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, status, err := newSpecFetchService().fetchNetworkSpec(context.Background(), srv.URL+"/x/download")

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "/x/download", gotPath, "must not append a second /download")
}

func TestFetchNetworkSpecNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, status, err := newSpecFetchService().fetchNetworkSpec(context.Background(), srv.URL+"/missing")

	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Contains(t, err.Error(), "no spec page found")
}

func TestFetchNetworkSpecServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, status, err := newSpecFetchService().fetchNetworkSpec(context.Background(), srv.URL+"/x")

	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, status)
}

func TestFetchNetworkSpecRejectsOversize(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", networkSpecMaxBytes+10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, status, err := newSpecFetchService().fetchNetworkSpec(context.Background(), srv.URL+"/x")

	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, err.Error(), "exceeded")
}
