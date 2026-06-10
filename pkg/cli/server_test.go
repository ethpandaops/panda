package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/configpath"
)

// TestResolveComposeFile is not parallel because the subtests mutate
// the package-level composeFile variable.
func TestResolveComposeFile(t *testing.T) {
	original := composeFile

	t.Cleanup(func() { composeFile = original })

	t.Run("returns flag value when set", func(t *testing.T) {
		composeFile = "/custom/path/docker-compose.yaml"

		result := resolveComposeFile()
		assert.Equal(t, "/custom/path/docker-compose.yaml", result)
	})

	t.Run("returns default path when flag is empty", func(t *testing.T) {
		composeFile = ""

		expected := filepath.Join(
			configpath.DefaultConfigDir(),
			"docker-compose.yaml",
		)

		result := resolveComposeFile()
		assert.Equal(t, expected, result)
	})
}

func TestComposeOverrideFile(t *testing.T) {
	t.Parallel()

	t.Run("returns empty when no override exists", func(t *testing.T) {
		t.Parallel()

		compose := filepath.Join(t.TempDir(), "docker-compose.yaml")

		assert.Empty(t, composeOverrideFile(compose))
	})

	t.Run("returns override path when present", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		compose := filepath.Join(dir, "docker-compose.yaml")
		override := filepath.Join(dir, "docker-compose.override.yaml")

		require.NoError(t, os.WriteFile(override, []byte("services: {}\n"), 0o644))

		assert.Equal(t, override, composeOverrideFile(compose))
	})
}

func TestWaitForServerHealthSucceedsAfterTemporaryFailure(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}

		if attempts.Add(1) == 1 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setServerHealthWaitIntervals(t, 5*time.Millisecond, time.Hour)

	err := waitForServerHealth(context.Background(), 200*time.Millisecond)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, attempts.Load(), int32(2))
}

func TestWaitForServerHealthTimesOutWithLogsHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}

		http.Error(w, "starting", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setServerHealthWaitIntervals(t, 10*time.Millisecond, 5*time.Millisecond)

	var err error
	output := captureStdout(t, func() {
		err = waitForServerHealth(context.Background(), 200*time.Millisecond)
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "server did not become healthy within")
	assert.Contains(t, err.Error(), "panda server logs")
	assert.Contains(t, output, "Still waiting for server to become healthy...")
}

func TestRunServerRestartLogsAndWaitsForHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setServerHealthWaitIntervals(t, time.Millisecond, time.Hour)

	originalComposeFile := composeFile
	originalRunner := dockerComposeRunner
	composeFile = filepath.Join(t.TempDir(), "docker-compose.yaml")

	var runnerCalls atomic.Int32
	var runnerCompose string
	var runnerArgs []string
	dockerComposeRunner = func(_ context.Context, compose string, args ...string) error {
		runnerCalls.Add(1)
		runnerCompose = compose
		runnerArgs = append([]string(nil), args...)

		return nil
	}

	t.Cleanup(func() {
		composeFile = originalComposeFile
		dockerComposeRunner = originalRunner
	})

	cmd := &cobra.Command{}

	output := captureStdout(t, func() {
		err := runServerRestart(cmd, nil)
		require.NoError(t, err)
	})

	assert.Equal(t, int32(1), runnerCalls.Load())
	assert.Equal(t, composeFile, runnerCompose)
	assert.Equal(t, []string{"restart"}, runnerArgs)
	assertContainsInOrder(t, output,
		"Restarting server...",
		"Waiting for server to become healthy...",
		"Server ready.",
	)
}

func TestRunServerStartLogsAndWaitsForHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	setClientConfig(t, server.URL)
	setServerHealthWaitIntervals(t, time.Millisecond, time.Hour)

	originalComposeFile := composeFile
	originalRunner := dockerComposeRunner
	composeFile = filepath.Join(t.TempDir(), "docker-compose.yaml")

	var runnerCalls atomic.Int32
	var runnerCompose string
	var runnerArgs []string
	dockerComposeRunner = func(_ context.Context, compose string, args ...string) error {
		runnerCalls.Add(1)
		runnerCompose = compose
		runnerArgs = append([]string(nil), args...)

		return nil
	}

	t.Cleanup(func() {
		composeFile = originalComposeFile
		dockerComposeRunner = originalRunner
	})

	cmd := &cobra.Command{}

	output := captureStdout(t, func() {
		err := runServerStart(cmd, nil)
		require.NoError(t, err)
	})

	assert.Equal(t, int32(1), runnerCalls.Load())
	assert.Equal(t, composeFile, runnerCompose)
	assert.Equal(t, []string{"up", "-d", "--force-recreate"}, runnerArgs)
	assertContainsInOrder(t, output,
		"Starting server...",
		"Waiting for server to become healthy...",
		"Server ready.",
	)
}

func setClientConfig(t *testing.T, serverURL string) {
	t.Helper()

	original := cfgFile
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(fmt.Sprintf("server:\n  url: %q\n", serverURL))

	require.NoError(t, os.WriteFile(path, data, 0o600))

	cfgFile = path
	t.Cleanup(func() { cfgFile = original })
}

func setServerHealthWaitIntervals(t *testing.T, poll, progress time.Duration) {
	t.Helper()

	originalPoll := serverHealthPollInterval
	originalProgress := serverHealthProgressInterval

	serverHealthPollInterval = poll
	serverHealthProgressInterval = progress

	t.Cleanup(func() {
		serverHealthPollInterval = originalPoll
		serverHealthProgressInterval = originalProgress
	})
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	output := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, reader)
		output <- buf.String()
	}()

	os.Stdout = writer
	defer func() { os.Stdout = original }()

	fn()

	require.NoError(t, writer.Close())

	return <-output
}

func assertContainsInOrder(t *testing.T, s string, values ...string) {
	t.Helper()

	offset := 0
	for _, value := range values {
		index := strings.Index(s[offset:], value)
		if index == -1 {
			t.Fatalf("expected %q to contain %q after offset %d", s, value, offset)
		}

		offset += index + len(value)
	}
}
