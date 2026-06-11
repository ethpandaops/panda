package sandbox

import (
	"fmt"

	"github.com/docker/go-units"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// SecurityConfig holds security settings for container execution.
type SecurityConfig struct {
	// User to run the container as (e.g., "nobody").
	User string
	// ReadonlyRootfs makes the root filesystem read-only.
	ReadonlyRootfs bool
	// DropCapabilities lists Linux capabilities to drop.
	DropCapabilities []string
	// SecurityOpts are additional security options (e.g., "no-new-privileges:true").
	SecurityOpts []string
	// PidsLimit restricts the number of processes in the container.
	PidsLimit int64
	// MemoryLimit in bytes.
	MemoryLimit int64
	// CPUQuota as a percentage of one CPU (100000 = 1 CPU).
	CPUQuota int64
	// CPUPeriod is the CPU CFS scheduler period.
	CPUPeriod int64
	// TmpfsSize is the size of the /tmp tmpfs mount.
	TmpfsSize string
	// Runtime specifies the container runtime (e.g., "" for default, "runsc" for gVisor).
	Runtime string
}

// DefaultSecurityConfig returns the default security configuration for sandboxed execution.
// Returns an error if the memory limit cannot be parsed.
func DefaultSecurityConfig(memoryLimit string, cpuLimit float64) (*SecurityConfig, error) {
	memBytes, err := units.RAMInBytes(memoryLimit)
	if err != nil {
		return nil, fmt.Errorf("invalid memory limit %q: %w", memoryLimit, err)
	}

	return &SecurityConfig{
		User:           "nobody",
		ReadonlyRootfs: true,
		DropCapabilities: []string{
			"ALL",
		},
		SecurityOpts: []string{
			"no-new-privileges:true",
		},
		PidsLimit:   100,
		MemoryLimit: memBytes,
		CPUQuota:    int64(100000 * cpuLimit),
		CPUPeriod:   100000,
		TmpfsSize:   "100M",
		Runtime:     "",
	}, nil
}

// GVisorSecurityConfig returns security configuration for gVisor-based execution.
// Returns an error if the memory limit cannot be parsed.
func GVisorSecurityConfig(memoryLimit string, cpuLimit float64) (*SecurityConfig, error) {
	cfg, err := DefaultSecurityConfig(memoryLimit, cpuLimit)
	if err != nil {
		return nil, err
	}

	cfg.Runtime = "runsc"

	return cfg, nil
}

// ApplyToHostConfig applies security settings to a Docker HostConfig.
func (s *SecurityConfig) ApplyToHostConfig(hostConfig *container.HostConfig) {
	// Resource limits.
	hostConfig.Memory = s.MemoryLimit
	hostConfig.CPUQuota = s.CPUQuota
	hostConfig.CPUPeriod = s.CPUPeriod
	hostConfig.PidsLimit = &s.PidsLimit

	// Security hardening.
	hostConfig.ReadonlyRootfs = s.ReadonlyRootfs
	hostConfig.SecurityOpt = s.SecurityOpts
	hostConfig.CapDrop = s.DropCapabilities

	// Runtime (for gVisor).
	if s.Runtime != "" {
		hostConfig.Runtime = s.Runtime
	}

	// Writable /tmp as tmpfs.
	if s.TmpfsSize != "" {
		hostConfig.Tmpfs = map[string]string{
			"/tmp": "size=" + s.TmpfsSize + ",mode=1777",
		}
	}
}

// CreateMounts creates the volume mounts for sandbox execution.
// sharedDir is mounted read-only at /shared (contains the script).
// outputDir is mounted read-write at /output (for generated files).
func CreateMounts(sharedDir, outputDir string) []mount.Mount {
	return []mount.Mount{
		{
			Type:     mount.TypeBind,
			Source:   sharedDir,
			Target:   "/shared",
			ReadOnly: true,
		},
		{
			Type:     mount.TypeBind,
			Source:   outputDir,
			Target:   "/output",
			ReadOnly: false,
		},
	}
}

// SandboxEnvDefaults returns default environment variables for sandbox execution.
// These ensure proper operation as the "nobody" user with no home directory.
func SandboxEnvDefaults() map[string]string {
	return map[string]string{
		"HOME":           "/tmp",
		"MPLCONFIGDIR":   "/tmp",
		"XDG_CACHE_HOME": "/tmp",
	}
}
