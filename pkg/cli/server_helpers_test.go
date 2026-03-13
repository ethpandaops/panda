package cli

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerDoBuildsRequestsAndReturnsResponseMetadata(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/inspect", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "bar", r.URL.Query().Get("foo"))
		assert.Equal(t, "value", r.Header.Get("X-Test"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "payload", string(body))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		_, err = w.Write([]byte("ok"))
		require.NoError(t, err)
	})

	newCLIHarness(t, mux)

	data, status, headers, err := serverDo(
		context.Background(),
		http.MethodPost,
		"/inspect",
		strings.NewReader("payload"),
		url.Values{"foo": []string{"bar"}},
		map[string]string{"X-Test": "value"},
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "text/plain", headers.Get("Content-Type"))
	assert.Equal(t, []byte("ok"), data)
}

func TestServerJSONHelpersAndDeleteHandleErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/bad-json", func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("{"))
		require.NoError(t, err)
	})
	mux.HandleFunc("/delete", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusBadRequest, map[string]string{"error": "cannot delete"})
	})

	newCLIHarness(t, mux)

	var okPayload map[string]string
	require.NoError(t, serverGetJSON(context.Background(), "/ok", nil, &okPayload))
	assert.Equal(t, "ok", okPayload["status"])

	err := serverGetJSON(context.Background(), "/bad-json", nil, &okPayload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding response")

	err = serverDelete(context.Background(), "/delete")
	require.Error(t, err)
	assert.EqualError(t, err, "HTTP 400: cannot delete")
}

func TestServerPostAndOperationHelpers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		writeJSONResponse(t, w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/operations/test.object", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, http.StatusOK, operations.NewObjectResponse(operations.DatasourcesPayload{
			Datasources: []operations.Datasource{{Name: "xatu"}},
		}, nil))
	})
	mux.HandleFunc("/api/v1/operations/test.raw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"ok":true}`))
		require.NoError(t, err)
	})

	newCLIHarness(t, mux)

	var postPayload map[string]string
	require.NoError(t, serverPostJSON(context.Background(), "/echo", map[string]string{"hello": "world"}, &postPayload))
	assert.Equal(t, "ok", postPayload["status"])

	result, err := serverOperationJSON[operations.NoArgs, operations.DatasourcesPayload](context.Background(), "test.object", operations.NoArgs{})
	require.NoError(t, err)
	require.Len(t, result.Datasources, 1)
	assert.Equal(t, "xatu", result.Datasources[0].Name)

	raw, err := serverOperationRaw(context.Background(), "test.raw", operations.NoArgs{})
	require.NoError(t, err)
	assert.Equal(t, "application/json", raw.ContentType)
	assert.JSONEq(t, `{"ok":true}`, string(raw.Body))
}

func TestServerHelperErrorUtilities(t *testing.T) {
	newCLIHarness(t, http.NewServeMux())

	err := serverPostJSON(context.Background(), "/unused", make(chan int), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshaling request")

	assert.EqualError(t, decodeAPIError(http.StatusBadRequest, []byte(`{"error":"bad request"}`)), "HTTP 400: bad request")
	assert.EqualError(t, decodeAPIError(http.StatusBadGateway, []byte("gateway down")), "HTTP 502: gateway down")

	stdout, _ := captureOutput(t, func() {
		require.NoError(t, printJSONBytes([]byte("not-json")))
	})
	assert.Equal(t, "not-json\n", stdout)
}
