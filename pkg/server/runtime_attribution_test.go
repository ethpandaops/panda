package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/execsvc"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/tokenstore"
)

// attributionSandbox is a sandbox stub whose Execute invokes a callback with
// the live execution request, letting the test act as sandbox code calling
// back into the server mid-execution.
type attributionSandbox struct {
	onExecute func(req sandbox.ExecuteRequest)
}

func (f *attributionSandbox) Start(_ context.Context) error { return nil }
func (f *attributionSandbox) Stop(_ context.Context) error  { return nil }
func (f *attributionSandbox) Name() string                  { return "fake" }

func (f *attributionSandbox) Execute(_ context.Context, req sandbox.ExecuteRequest) (*sandbox.ExecutionResult, error) {
	if f.onExecute != nil {
		f.onExecute(req)
	}

	return &sandbox.ExecutionResult{}, nil
}

func (f *attributionSandbox) ListSessions(_ context.Context, _ string) ([]sandbox.SessionInfo, error) {
	return nil, nil
}

func (f *attributionSandbox) CreateSession(_ context.Context, _ string, _ map[string]string) (string, error) {
	return "", nil
}

func (f *attributionSandbox) DestroySession(_ context.Context, _, _ string) error { return nil }

func (f *attributionSandbox) CanCreateSession(_ context.Context, _ string) (bool, int, int) {
	return true, 0, 1
}

func (f *attributionSandbox) SessionsEnabled() bool { return false }

func TestRuntimeCallbacksInheritExecutionAttribution(t *testing.T) {
	log := logrus.New()
	tokens := tokenstore.New(time.Minute)
	defer tokens.Stop()

	fake := &attributionSandbox{}
	execSvc := execsvc.New(log, fake, &config.Config{
		Server:  config.ServerConfig{SandboxURL: "http://sandbox:1234"},
		Sandbox: config.SandboxConfig{Timeout: 30},
	}, module.NewRegistry(log), tokens)

	svc := &service{
		runtimeTokens: tokens,
		execService:   execSvc,
	}

	var inherited string

	handler := svc.runtimeAuthMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		inherited = attribution.FromContext(r.Context())
	}))

	// Mid-execution, replay what sandbox code does: call back into the
	// server authenticated only by the runtime token.
	fake.onExecute = func(req sandbox.ExecuteRequest) {
		callback := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/operations/clickhouse.query", nil)
		callback.Header.Set("Authorization", "Bearer "+req.Env[sandbox.EnvAPIToken])

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, callback)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	ctx := attribution.WithValue(context.Background(), "discord:sam")
	_, err := execSvc.Execute(ctx, execsvc.ExecuteRequest{Code: "print(1)", Timeout: 30})
	require.NoError(t, err)

	require.Equal(t, "discord:sam", inherited,
		"runtime callbacks must inherit the spawning execution's attribution")
}
