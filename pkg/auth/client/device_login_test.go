package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// newDeviceMockServer returns an httptest server exposing OIDC discovery, a
// device-authorization endpoint, and a token endpoint. The token endpoint
// returns authorization_pending for the first pendingPolls calls, then either
// the supplied tokens or the supplied OAuth error.
func newDeviceMockServer(t *testing.T, pendingPolls int, finalErr string) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var polls atomic.Int64

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                        server.URL,
				"token_endpoint":                server.URL + "/token",
				"device_authorization_endpoint": server.URL + "/device",
			})
		case "/device":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "device-code-secret",
				"user_code":                 "USER-CODE",
				"verification_uri":          server.URL + "/activate",
				"verification_uri_complete": server.URL + "/activate?code=USER-CODE",
				"expires_in":                600,
				"interval":                  1,
			})
		case "/token":
			n := polls.Add(1)
			if int(n) <= pendingPolls {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})

				return
			}

			if finalErr != "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": finalErr})

				return
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	return server, &polls
}

func TestBeginDeviceLoginReturnsVerificationDetails(t *testing.T) {
	t.Parallel()

	server, _ := newDeviceMockServer(t, 0, "")
	defer server.Close()

	c := New(logrus.New(), Config{IssuerURL: server.URL, ClientID: "panda"})

	device, err := c.BeginDeviceLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginDeviceLogin: %v", err)
	}

	if device.UserCode != "USER-CODE" {
		t.Errorf("user code = %q, want USER-CODE", device.UserCode)
	}

	if device.VerificationURI != server.URL+"/activate" {
		t.Errorf("verification uri = %q", device.VerificationURI)
	}

	if device.DeviceCode != "device-code-secret" {
		t.Errorf("device code not captured for polling")
	}
}

func TestPollDeviceLoginReturnsTokensAfterPending(t *testing.T) {
	t.Parallel()

	// One pending poll then success exercises the pending->approved transition.
	server, polls := newDeviceMockServer(t, 1, "")
	defer server.Close()

	c := New(logrus.New(), Config{IssuerURL: server.URL, ClientID: "panda"})

	device, err := c.BeginDeviceLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginDeviceLogin: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tokens, err := c.PollDeviceLogin(ctx, device)
	if err != nil {
		t.Fatalf("PollDeviceLogin: %v", err)
	}

	if tokens.AccessToken != "access-token" || tokens.RefreshToken != "refresh-token" {
		t.Errorf("unexpected tokens: %+v", tokens)
	}

	if polls.Load() < 2 {
		t.Errorf("expected at least 2 token polls (pending then success), got %d", polls.Load())
	}
}

func TestPollDeviceLoginSurfacesDenied(t *testing.T) {
	t.Parallel()

	server, _ := newDeviceMockServer(t, 0, "access_denied")
	defer server.Close()

	c := New(logrus.New(), Config{IssuerURL: server.URL, ClientID: "panda"})

	device, err := c.BeginDeviceLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginDeviceLogin: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := c.PollDeviceLogin(ctx, device); err == nil {
		t.Fatal("expected denied error, got nil")
	}
}

func TestPollDeviceLoginRequiresDeviceCode(t *testing.T) {
	t.Parallel()

	c := New(logrus.New(), Config{IssuerURL: "https://issuer.example", ClientID: "panda"})

	if _, err := c.PollDeviceLogin(context.Background(), &DeviceAuth{}); err == nil {
		t.Fatal("expected error for empty device code")
	}
}
