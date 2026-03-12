package sandbox

import "testing"

func TestBackendTypeConstantsMatchServiceNames(t *testing.T) {
	t.Parallel()

	if string(BackendDocker) != "docker" {
		t.Fatalf("BackendDocker = %q, want docker", BackendDocker)
	}
	if string(BackendGVisor) != "gvisor" {
		t.Fatalf("BackendGVisor = %q, want gvisor", BackendGVisor)
	}
}
