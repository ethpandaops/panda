package server

import (
	"context"
	"errors"
	"testing"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
	"github.com/ethpandaops/panda/pkg/resource"
	"github.com/ethpandaops/panda/pkg/tool"
)

func TestServiceTransportRunnersStopOnCancellation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		run  func(*service, context.Context) error
	}{
		{
			name: "sse",
			run: func(s *service, ctx context.Context) error {
				return s.runSSE(ctx)
			},
		},
		{
			name: "streamable-http",
			run: func(s *service, ctx context.Context) error {
				return s.runStreamableHTTP(ctx)
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := &service{
				log:       logrus.New(),
				cfg:       config.ServerConfig{Host: "127.0.0.1", Port: 0},
				mcpServer: mcpserver.NewMCPServer("test", "v1"),
				done:      make(chan struct{}),
			}

			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)

			if err := tc.run(srv, ctx); err != nil {
				t.Fatalf("%s error = %v", tc.name, err)
			}
		})
	}
}

func TestServiceStartRejectsDoubleStartAndStopHandlesIdleService(t *testing.T) {
	t.Parallel()

	srv := &service{
		log:     logrus.New(),
		cfg:     config.ServerConfig{Transport: TransportSSE},
		running: true,
		done:    make(chan struct{}),
	}

	if err := srv.Start(context.Background()); err == nil || err.Error() != "server already running" {
		t.Fatalf("Start(already running) error = %v, want server already running", err)
	}

	srv = &service{log: logrus.New(), done: make(chan struct{})}
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop(idle) error = %v, want nil", err)
	}
}

func TestServiceStartRunsStdioTransport(t *testing.T) {
	t.Parallel()

	srv := NewService(
		logrus.New(),
		config.ServerConfig{Transport: TransportStdio},
		tool.NewRegistry(logrus.New()),
		resource.NewRegistry(logrus.New()),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	).(*service)

	started := make(chan struct{}, 1)
	srv.serveStdioFunc = func(*mcpserver.MCPServer) error {
		started <- struct{}{}
		return nil
	}

	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start(stdio) error = %v", err)
	}

	select {
	case <-started:
	default:
		t.Fatal("Start(stdio) did not invoke serveStdio")
	}
}

func TestRunStdioReturnsServeErrors(t *testing.T) {
	t.Parallel()

	srv := &service{
		log:       logrus.New(),
		mcpServer: mcpserver.NewMCPServer("test", "v1"),
		done:      make(chan struct{}),
		serveStdioFunc: func(*mcpserver.MCPServer) error {
			return errors.New("stdio failed")
		},
	}

	if err := srv.runStdio(context.Background()); err == nil || err.Error() != "stdio failed" {
		t.Fatalf("runStdio() error = %v, want stdio failed", err)
	}
}
