package auth

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleMetadataRoutesUseRequestBaseURL(t *testing.T) {
	svc := newTestSimpleService(t)

	req := httptest.NewRequest(http.MethodGet, "http://internal/.well-known/oauth-protected-resource", nil)
	req.Host = "internal"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "proxy.example")

	resourceRecorder := httptest.NewRecorder()
	svc.handleResourceMetadata(resourceRecorder, req)
	require.Equal(t, http.StatusOK, resourceRecorder.Code)

	var resourceMetadata map[string]any
	require.NoError(t, json.Unmarshal(resourceRecorder.Body.Bytes(), &resourceMetadata))
	assert.Equal(t, "https://proxy.example", resourceMetadata["resource"])

	serverRecorder := httptest.NewRecorder()
	svc.handleServerMetadata(serverRecorder, req)
	require.Equal(t, http.StatusOK, serverRecorder.Code)

	var serverMetadata map[string]any
	require.NoError(t, json.Unmarshal(serverRecorder.Body.Bytes(), &serverMetadata))
	assert.Equal(t, "https://proxy.example/auth/authorize", serverMetadata["authorization_endpoint"])
	assert.Equal(t, "https://proxy.example/auth/token", serverMetadata["token_endpoint"])
}

func TestHandleRefreshTokenGrantRejectsMissingParameters(t *testing.T) {
	svc := newTestSimpleService(t)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/auth/token", nil)

	svc.handleRefreshTokenGrant(recorder, req)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "missing required parameters")
}

func TestWriteErrorAndWriteHTMLErrorEscapeOutput(t *testing.T) {
	svc := newTestSimpleService(t)

	jsonRecorder := httptest.NewRecorder()
	svc.writeError(jsonRecorder, http.StatusBadRequest, "invalid_request", "bad value")
	require.Equal(t, http.StatusBadRequest, jsonRecorder.Code)
	assert.Equal(t, "application/json", jsonRecorder.Header().Get("Content-Type"))
	assert.Contains(t, jsonRecorder.Body.String(), `"error":"invalid_request"`)

	htmlRecorder := httptest.NewRecorder()
	svc.writeHTMLError(htmlRecorder, http.StatusForbidden, "Bad <Title>", "message & details")
	require.Equal(t, http.StatusForbidden, htmlRecorder.Code)
	assert.Equal(t, "text/html; charset=utf-8", htmlRecorder.Header().Get("Content-Type"))
	assert.Contains(t, htmlRecorder.Body.String(), "Bad &lt;Title&gt;")
	assert.Contains(t, htmlRecorder.Body.String(), "message &amp; details")
}

func TestBaseURLFromRequestHonorsTLSAndForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/path", nil)
	assert.Equal(t, "http://example.com", baseURLFromRequest(req))

	tlsReq := httptest.NewRequest(http.MethodGet, "https://example.com/path", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	assert.Equal(t, "https://example.com", baseURLFromRequest(tlsReq))

	forwardedReq := httptest.NewRequest(http.MethodGet, "http://internal/path", nil)
	forwardedReq.Host = "internal"
	forwardedReq.Header.Set("X-Forwarded-Proto", "https")
	forwardedReq.Header.Set("X-Forwarded-Host", "proxy.example")
	assert.Equal(t, "https://proxy.example", baseURLFromRequest(forwardedReq))
}
