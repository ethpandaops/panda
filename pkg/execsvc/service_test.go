package execsvc

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/attribution"
	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/sandbox"
	"github.com/ethpandaops/panda/pkg/tokenstore"
)

func TestSandboxAPIURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "nil config",
			cfg:  nil,
			want: "",
		},
		{
			name: "sandbox url wins and trailing slash trimmed",
			cfg: &config.Config{Server: config.ServerConfig{
				SandboxURL: "http://sandbox:1234/",
				BaseURL:    "http://base:5678",
			}},
			want: "http://sandbox:1234",
		},
		{
			name: "base url used when sandbox url empty",
			cfg:  &config.Config{Server: config.ServerConfig{BaseURL: "http://base:5678"}},
			want: "http://base:5678",
		},
		{
			name: "host.docker.internal default uses configured port",
			cfg:  &config.Config{Server: config.ServerConfig{Port: 9999}},
			want: "http://host.docker.internal:9999",
		},
		{
			name: "host.docker.internal default falls back to 2480",
			cfg:  &config.Config{Server: config.ServerConfig{}},
			want: "http://host.docker.internal:2480",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, sandboxAPIURL(tt.cfg))
		})
	}
}

type fakeSandbox struct {
	onExecute func(req sandbox.ExecuteRequest)
}

func (f *fakeSandbox) Start(_ context.Context) error { return nil }
func (f *fakeSandbox) Stop(_ context.Context) error  { return nil }
func (f *fakeSandbox) Name() string                  { return "fake" }

func (f *fakeSandbox) Execute(_ context.Context, req sandbox.ExecuteRequest) (*sandbox.ExecutionResult, error) {
	if f.onExecute != nil {
		f.onExecute(req)
	}

	return &sandbox.ExecutionResult{}, nil
}

func (f *fakeSandbox) ListSessions(_ context.Context, _ string) ([]sandbox.SessionInfo, error) {
	return nil, nil
}

func (f *fakeSandbox) CreateSession(_ context.Context, _ string, _ map[string]string) (string, error) {
	return "", nil
}

func (f *fakeSandbox) DestroySession(_ context.Context, _, _ string) error { return nil }

func (f *fakeSandbox) CanCreateSession(_ context.Context, _ string) (bool, int, int) {
	return true, 0, 1
}

func (f *fakeSandbox) SessionsEnabled() bool { return false }

func TestExecuteCarriesAttributionForRuntimeCallbacks(t *testing.T) {
	log := logrus.New()
	tokens := tokenstore.New(time.Minute)
	defer tokens.Stop()

	fake := &fakeSandbox{}
	svc := New(log, fake, &config.Config{
		Server:  config.ServerConfig{SandboxURL: "http://sandbox:1234"},
		Sandbox: config.SandboxConfig{Timeout: 30},
	}, module.NewRegistry(log), tokens)

	var (
		executionID string
		midFlight   string
	)

	fake.onExecute = func(req sandbox.ExecuteRequest) {
		executionID = req.ExecutionID
		// This is what runtimeAuthMiddleware sees while sandbox code calls back.
		midFlight = svc.Attribution(req.ExecutionID)
	}

	ctx := attribution.WithValue(context.Background(), "discord:sam")
	_, err := svc.Execute(ctx, ExecuteRequest{Code: "print(1)", Timeout: 30})
	require.NoError(t, err)

	require.Equal(t, "discord:sam", midFlight, "attribution must be visible during execution")
	require.Empty(t, svc.Attribution(executionID), "attribution must be cleared after execution")
}

func TestExecuteWithoutAttributionStoresNothing(t *testing.T) {
	log := logrus.New()
	tokens := tokenstore.New(time.Minute)
	defer tokens.Stop()

	fake := &fakeSandbox{}
	svc := New(log, fake, &config.Config{
		Server:  config.ServerConfig{SandboxURL: "http://sandbox:1234"},
		Sandbox: config.SandboxConfig{Timeout: 30},
	}, module.NewRegistry(log), tokens)

	var midFlight string

	fake.onExecute = func(req sandbox.ExecuteRequest) {
		midFlight = svc.Attribution(req.ExecutionID)
	}

	_, err := svc.Execute(context.Background(), ExecuteRequest{Code: "print(1)", Timeout: 30})
	require.NoError(t, err)
	require.Empty(t, midFlight)
}
