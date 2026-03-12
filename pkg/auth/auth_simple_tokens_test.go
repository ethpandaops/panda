package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAccessTokenRejectsMissingAndWrongAudience(t *testing.T) {
	svc := newTestSimpleService(t)

	_, err := svc.validateAccessToken("https://proxy.test", "")
	require.Error(t, err)
	assert.EqualError(t, err, "missing or invalid Authorization header")

	token, err := svc.issueAccessToken("https://proxy.test", "https://other.example", "sam", 42, []string{"ethpandaops"})
	require.NoError(t, err)

	_, err = svc.validateAccessToken("https://proxy.test", token)
	require.Error(t, err)
	assert.EqualError(t, err, "token audience mismatch")
}

func TestValidateRefreshTokenRejectsAccessTokens(t *testing.T) {
	svc := newTestSimpleService(t)

	accessToken, err := svc.issueAccessToken("https://proxy.test", "https://proxy.test", "sam", 42, nil)
	require.NoError(t, err)

	claims, message := svc.validateRefreshToken("https://proxy.test", accessToken, "panda", "https://proxy.test")
	assert.Nil(t, claims)
	assert.Equal(t, "invalid refresh token", message)
}

func TestWriteTokenResponseAndUnauthorizedPayloads(t *testing.T) {
	svc := newTestSimpleService(t)

	tokenRecorder := httptest.NewRecorder()
	svc.writeTokenResponse(tokenRecorder, "access-token", "")
	require.Equal(t, http.StatusOK, tokenRecorder.Code)
	assert.Equal(t, "application/json", tokenRecorder.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", tokenRecorder.Header().Get("Cache-Control"))

	var tokenPayload map[string]any
	require.NoError(t, json.Unmarshal(tokenRecorder.Body.Bytes(), &tokenPayload))
	assert.Equal(t, "access-token", tokenPayload["access_token"])
	assert.NotContains(t, tokenPayload, "refresh_token")

	unauthorizedRecorder := httptest.NewRecorder()
	svc.writeUnauthorized(unauthorizedRecorder, "https://proxy.test", "token expired")
	require.Equal(t, http.StatusUnauthorized, unauthorizedRecorder.Code)
	assert.Contains(t, unauthorizedRecorder.Header().Get("WWW-Authenticate"), "resource_metadata=")
	assert.Contains(t, unauthorizedRecorder.Body.String(), "token expired")
}

func TestMiddlewareAllowsWellKnownAndAuthRoutesAndGetAuthUserNil(t *testing.T) {
	svc := newTestSimpleService(t)
	handler := svc.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Nil(t, GetAuthUser(context.Background()))
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/auth/authorize"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://proxy.test"+path, nil)
		handler.ServeHTTP(recorder, req)
		assert.Equal(t, http.StatusNoContent, recorder.Code)
	}
}
