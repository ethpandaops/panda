package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/serverapi"
)

func TestRunAuthStatusReportsAuthenticated(t *testing.T) {
	expires := time.Now().Add(time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/status", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)

		_ = json.NewEncoder(w).Encode(serverapi.AuthStatusResponse{
			Enabled:             true,
			Authenticated:       true,
			IssuerURL:           "https://issuer.example",
			ClientID:            "panda",
			ExpiresAt:           &expires,
			RefreshTokenPresent: true,
			CredentialsPath:     "/home/panda/.config/panda/credentials/abc.json",
		})
	}))
	defer server.Close()

	setClientConfig(t, server.URL)

	output := captureStdout(t, func() {
		require.NoError(t, runAuthStatus(testCommand(), nil))
	})

	assert.Contains(t, output, "Status: Authenticated")
	assert.Contains(t, output, "Refresh token: present")
	assert.Contains(t, output, "https://issuer.example")
}

func TestRunAuthStatusNotEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(serverapi.AuthStatusResponse{Enabled: false})
	}))
	defer server.Close()

	setClientConfig(t, server.URL)

	output := captureStdout(t, func() {
		require.NoError(t, runAuthStatus(testCommand(), nil))
	})

	assert.Contains(t, output, "not enabled")
}

func TestRunAuthStatusServerUnreachable(t *testing.T) {
	// Point at a closed port so the call fails — the CLI cannot operate without
	// a running server and must surface an error rather than touch any file.
	setClientConfig(t, "http://127.0.0.1:1")

	err := runAuthStatus(testCommand(), nil)
	require.Error(t, err)
}

func TestRunAuthLoginShowsCodeAndCompletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/login", r.URL.Path)

		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(serverapi.AuthLoginResponse{
				Enabled:         true,
				UserCode:        "WXYZ-1234",
				VerificationURI: "https://issuer.example/activate",
				ExpiresIn:       600,
				Interval:        1,
			})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(serverapi.AuthLoginStateResponse{
				State: serverapi.AuthLoginAuthenticated,
			})
		default:
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	setClientConfig(t, server.URL)

	output := captureStdout(t, func() {
		require.NoError(t, runAuthLogin(testCommand(), nil))
	})

	assert.Contains(t, output, "WXYZ-1234")
	assert.Contains(t, output, "https://issuer.example/activate")
	assert.Contains(t, output, "Authenticated.")
}

func TestRunAuthLoginReportsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(serverapi.AuthLoginResponse{
				Enabled: true, UserCode: "U", VerificationURI: "https://v", Interval: 1,
			})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(serverapi.AuthLoginStateResponse{
				State: serverapi.AuthLoginError, Error: "authorization was denied",
			})
		}
	}))
	defer server.Close()

	setClientConfig(t, server.URL)

	captureStdout(t, func() {
		err := runAuthLogin(testCommand(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "denied")
	})
}

func TestRunAuthLoginNotEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(serverapi.AuthLoginResponse{Enabled: false})
	}))
	defer server.Close()

	setClientConfig(t, server.URL)

	output := captureStdout(t, func() {
		require.NoError(t, runAuthLogin(testCommand(), nil))
	})

	assert.Contains(t, output, "not enabled")
}

func TestRunAuthLogout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/logout", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	setClientConfig(t, server.URL)

	output := captureStdout(t, func() {
		require.NoError(t, runAuthLogout(testCommand(), nil))
	})

	assert.Contains(t, output, "Removed")
}
