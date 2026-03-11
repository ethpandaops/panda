package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadClientUsesSharedConfigLoader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `server:
  host: 0.0.0.0
proxy:
  url: ${PANDA_PROXY_URL:-http://proxy.internal}
observability:
  metrics_port: 3001
`

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadClient(path)
	if err != nil {
		t.Fatalf("LoadClient failed: %v", err)
	}

	if got := cfg.ServerURL(); got != "http://localhost:2480" {
		t.Fatalf("ServerURL() = %q, want %q", got, "http://localhost:2480")
	}

	if got := cfg.Proxy.URL; got != "http://proxy.internal" {
		t.Fatalf("Proxy.URL = %q, want %q", got, "http://proxy.internal")
	}

	if got := cfg.Observability.MetricsPort; got != 3001 {
		t.Fatalf("Observability.MetricsPort = %d, want %d", got, 3001)
	}

	if got := cfg.Path(); got != path {
		t.Fatalf("Path() = %q, want %q", got, path)
	}
}
