package sandbox

import "testing"

func TestGVisorSecurityConfigSetsRuntime(t *testing.T) {
	t.Parallel()

	cfg, err := GVisorSecurityConfig("128M", 1)
	if err != nil {
		t.Fatalf("GVisorSecurityConfig() error = %v", err)
	}
	if cfg.Runtime != gVisorRuntimeName {
		t.Fatalf("Runtime = %q, want %q", cfg.Runtime, gVisorRuntimeName)
	}
}
