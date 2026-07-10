//go:build !linux

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/ethpandaops/panda/pkg/config"
)

// The direct backend's confinement is Linux-only; elsewhere it fails closed —
// preflight refuses to start, so the trampoline is never reached.

// RunDirectSandboxInitIfRequested is a no-op off Linux (no trampoline exists).
func RunDirectSandboxInitIfRequested() bool { return false }

func newHardenedSandboxCmd(_ context.Context, _, _, _ string, _, _ int, _ []string) (*exec.Cmd, func(), error) {
	// Unreachable: preflightDirectHardening fails first. Guard anyway.
	return &exec.Cmd{Path: "/nonexistent/direct-backend-requires-linux"}, func() {}, nil
}

func preflightDirectHardening(_ config.SandboxConfig) error {
	return fmt.Errorf("direct sandbox backend is only supported on linux (running on %s)", runtime.GOOS)
}

func setServerNonDumpable() error { return nil }
