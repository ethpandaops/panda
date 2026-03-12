package proxyserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
)

func TestNoneAuthenticatorAllowsRequestsAndReturnsNoUserID(t *testing.T) {
	auth := NewNoneAuthenticator(logrus.New())
	require.NoError(t, auth.Start(context.Background()))
	require.NoError(t, auth.Stop())

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/datasources", nil)

	auth.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "", GetUserID(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestSimpleServiceAuthenticatorDelegatesLifecycleAndMiddleware(t *testing.T) {
	svc := &stubSimpleAuthService{}
	auth := NewSimpleServiceAuthenticator(svc)

	require.NoError(t, auth.Start(context.Background()))
	require.NoError(t, auth.Stop())

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	auth.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})).ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusAccepted, recorder.Code)
	assert.True(t, svc.started)
	assert.True(t, svc.stopped)
	assert.True(t, svc.middlewareCalled)
}

type stubSimpleAuthService struct {
	started          bool
	stopped          bool
	middlewareCalled bool
}

func (s *stubSimpleAuthService) Start(context.Context) error { s.started = true; return nil }
func (s *stubSimpleAuthService) Stop() error                 { s.stopped = true; return nil }
func (s *stubSimpleAuthService) Enabled() bool              { return true }
func (s *stubSimpleAuthService) MountRoutes(chi.Router)     {}
func (s *stubSimpleAuthService) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.middlewareCalled = true
			next.ServeHTTP(w, r)
		})
	}
}

var _ simpleauth.SimpleService = (*stubSimpleAuthService)(nil)
