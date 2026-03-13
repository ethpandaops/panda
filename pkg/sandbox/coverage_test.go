package sandbox

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/system"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestSecurityHelpers(t *testing.T) {
	t.Parallel()

	cfg, err := DefaultSecurityConfig("128M", 1.5)
	if err != nil {
		t.Fatalf("DefaultSecurityConfig() error = %v", err)
	}

	if cfg.User != "nobody" || cfg.Runtime != "" || cfg.MemoryLimit == 0 {
		t.Fatalf("DefaultSecurityConfig() = %#v, want default sandbox config", cfg)
	}

	if cfg.CPUQuota != 150000 || cfg.CPUPeriod != 100000 {
		t.Fatalf("CPU limits = (%d, %d), want (150000, 100000)", cfg.CPUQuota, cfg.CPUPeriod)
	}

	gvisorCfg, err := GVisorSecurityConfig("128M", 0.5)
	if err != nil {
		t.Fatalf("GVisorSecurityConfig() error = %v", err)
	}

	if gvisorCfg.Runtime != "runsc" {
		t.Fatalf("GVisor runtime = %q, want runsc", gvisorCfg.Runtime)
	}

	if _, err := DefaultSecurityConfig("definitely-not-memory", 1); err == nil {
		t.Fatal("DefaultSecurityConfig() error = nil, want invalid memory error")
	}

	hostConfig := &container.HostConfig{}
	cfg.ApplyToHostConfig(hostConfig)

	if hostConfig.Memory != cfg.MemoryLimit || hostConfig.CPUQuota != cfg.CPUQuota {
		t.Fatalf("ApplyToHostConfig() resource limits = %#v, want cfg values", hostConfig)
	}

	if hostConfig.PidsLimit == nil || *hostConfig.PidsLimit != cfg.PidsLimit {
		t.Fatalf("PidsLimit = %#v, want %d", hostConfig.PidsLimit, cfg.PidsLimit)
	}

	if !hostConfig.ReadonlyRootfs || hostConfig.Runtime != "" {
		t.Fatalf("ApplyToHostConfig() = %#v, want readonly rootfs and default runtime", hostConfig)
	}

	if got := hostConfig.Tmpfs["/tmp"]; got != "size=100M,mode=1777" {
		t.Fatalf("Tmpfs[/tmp] = %q, want size=100M,mode=1777", got)
	}

	mounts := CreateMounts("/tmp/shared", "/tmp/output")
	if len(mounts) != 2 {
		t.Fatalf("CreateMounts() len = %d, want 2", len(mounts))
	}

	if mounts[0] != (mount.Mount{Type: mount.TypeBind, Source: "/tmp/shared", Target: "/shared", ReadOnly: true}) {
		t.Fatalf("shared mount = %#v, want /shared readonly bind", mounts[0])
	}

	if mounts[1] != (mount.Mount{Type: mount.TypeBind, Source: "/tmp/output", Target: "/output", ReadOnly: false}) {
		t.Fatalf("output mount = %#v, want /output read-write bind", mounts[1])
	}

	env := SandboxEnvDefaults()
	if env["HOME"] != "/tmp" || env["MPLCONFIGDIR"] != "/tmp" || env["XDG_CACHE_HOME"] != "/tmp" {
		t.Fatalf("SandboxEnvDefaults() = %#v, want tmp-backed defaults", env)
	}
}

func TestNewConstructsSupportedBackends(t *testing.T) {
	t.Parallel()

	dockerSvc, err := New(config.SandboxConfig{Backend: string(BackendDocker)}, testSandboxLogger())
	if err != nil {
		t.Fatalf("New(docker) error = %v", err)
	}

	if dockerSvc.Name() != "docker" {
		t.Fatalf("docker service name = %q, want docker", dockerSvc.Name())
	}

	gvisorSvc, err := New(config.SandboxConfig{Backend: string(BackendGVisor)}, testSandboxLogger())
	if err != nil {
		t.Fatalf("New(gvisor) error = %v", err)
	}

	if gvisorSvc.Name() != "gvisor" {
		t.Fatalf("gvisor service name = %q, want gvisor", gvisorSvc.Name())
	}

	if _, err := New(config.SandboxConfig{Backend: "firecracker"}, testSandboxLogger()); err == nil || !strings.Contains(err.Error(), "unsupported sandbox backend") {
		t.Fatalf("New(unsupported) error = %v, want unsupported backend", err)
	}
}

func TestDockerAndGVisorBackendHelpers(t *testing.T) {
	t.Parallel()

	dockerBackend := newTestDockerBackend(t)
	if dockerBackend.Name() != "docker" {
		t.Fatalf("Docker backend name = %q, want docker", dockerBackend.Name())
	}

	if _, err := dockerBackend.Execute(context.Background(), ExecuteRequest{Code: "print(1)"}); err == nil || !strings.Contains(err.Error(), "docker client not initialized") {
		t.Fatalf("Execute() error = %v, want docker client not initialized", err)
	}

	if err := dockerBackend.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	gvisorBackend, err := NewGVisorBackend(baseSandboxConfig(), testSandboxLogger())
	if err != nil {
		t.Fatalf("NewGVisorBackend() error = %v", err)
	}

	if gvisorBackend.Name() != "gvisor" {
		t.Fatalf("GVisor backend name = %q, want gvisor", gvisorBackend.Name())
	}

	securityCfg, err := gvisorBackend.getSecurityConfig()
	if err != nil {
		t.Fatalf("gvisor getSecurityConfig() error = %v", err)
	}

	if securityCfg.Runtime != gVisorRuntimeName {
		t.Fatalf("gvisor runtime = %q, want %q", securityCfg.Runtime, gVisorRuntimeName)
	}

	info := system.Info{
		Runtimes: map[string]system.RuntimeWithStatus{
			"io.containerd.runc.v2": {},
			gVisorRuntimeName:       {},
		},
	}

	if !hasRuntime(info, gVisorRuntimeName) || hasRuntime(info, "missing") {
		t.Fatalf("hasRuntime() returned wrong results for %#v", info.Runtimes)
	}

	names := getRuntimeNames(info)
	if len(names) != 2 {
		t.Fatalf("getRuntimeNames() = %v, want two runtime names", names)
	}
}

func TestDockerInfraHelpers(t *testing.T) {
	t.Parallel()

	createdAt := parseContainerCreatedAt(map[string]string{
		LabelCreatedAt: "1700000000",
	}, 1)
	if createdAt.Unix() != 1700000000 {
		t.Fatalf("parseContainerCreatedAt(valid) = %v, want unix 1700000000", createdAt)
	}

	fallback := parseContainerCreatedAt(map[string]string{
		LabelCreatedAt: "invalid",
	}, 42)
	if fallback.Unix() != 42 {
		t.Fatalf("parseContainerCreatedAt(fallback) = %v, want unix 42", fallback)
	}

	backend := newTestDockerBackend(t)
	securityCfg, err := backend.getSecurityConfig()
	if err != nil {
		t.Fatalf("getSecurityConfig() error = %v", err)
	}

	if securityCfg.MemoryLimit == 0 {
		t.Fatalf("getSecurityConfig() = %#v, want parsed memory limit", securityCfg)
	}

	backend.cfg.MemoryLimit = "bad-limit"
	if _, err := backend.getSecurityConfig(); err == nil {
		t.Fatal("getSecurityConfig() error = nil, want invalid memory limit")
	}
}

func TestDockerExecutionHelpers(t *testing.T) {
	t.Parallel()

	backend := newTestDockerBackend(t)

	baseDir, err := backend.createExecutionDirs("exec-123")
	if err != nil {
		t.Fatalf("createExecutionDirs() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })

	if _, err := os.Stat(filepath.Join(baseDir, "shared")); err != nil {
		t.Fatalf("shared dir stat error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(baseDir, "output")); err != nil {
		t.Fatalf("output dir stat error = %v", err)
	}

	backend.cfg.HostSharedPath = t.TempDir()
	hostBaseDir, err := backend.createExecutionDirs("exec-456")
	if err != nil {
		t.Fatalf("createExecutionDirs(host shared) error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(hostBaseDir) })

	sharedDir := filepath.Join("/tmp/local", "exec-789", "shared")
	outputDir := filepath.Join("/tmp/local", "exec-789", "output")
	containerCfg, hostCfg, err := backend.buildContainerConfig(sharedDir, outputDir, map[string]string{
		"CUSTOM": "value",
	})
	if err != nil {
		t.Fatalf("buildContainerConfig() error = %v", err)
	}

	if containerCfg.Image != backend.cfg.Image || containerCfg.User != "nobody" {
		t.Fatalf("container config = %#v, want image and nobody user", containerCfg)
	}

	env := envSliceToMap(containerCfg.Env)
	if env["CUSTOM"] != "value" || env["HOME"] != "/tmp" {
		t.Fatalf("container env = %#v, want merged defaults and custom env", env)
	}

	if hostCfg.NetworkMode != container.NetworkMode(backend.cfg.Network) {
		t.Fatalf("host network mode = %q, want %q", hostCfg.NetworkMode, backend.cfg.Network)
	}

	if len(hostCfg.Mounts) != 2 {
		t.Fatalf("host mounts len = %d, want 2", len(hostCfg.Mounts))
	}

	if got := hostCfg.Mounts[0].Source; got != filepath.Join(backend.cfg.HostSharedPath, "exec-789", "shared") {
		t.Fatalf("shared mount source = %q, want host shared path", got)
	}

	if got := hostCfg.Mounts[1].Source; got != filepath.Join(backend.cfg.HostSharedPath, "exec-789", "output") {
		t.Fatalf("output mount source = %q, want host shared path", got)
	}

	outputFilesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputFilesDir, "result.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("WriteFile(result.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputFilesDir, ".metrics.json"), []byte(`{"duration":1.2}`), 0o644); err != nil {
		t.Fatalf("WriteFile(.metrics.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputFilesDir, ".hidden"), []byte("ignore"), 0o644); err != nil {
		t.Fatalf("WriteFile(.hidden) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(outputFilesDir, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir(subdir) error = %v", err)
	}

	files, err := backend.collectOutputFiles(outputFilesDir)
	if err != nil {
		t.Fatalf("collectOutputFiles() error = %v", err)
	}

	if len(files) != 1 || files[0] != "result.txt" {
		t.Fatalf("collectOutputFiles() = %v, want [result.txt]", files)
	}

	metrics := backend.readMetrics(outputFilesDir)
	if metrics["duration"] != float64(1.2) {
		t.Fatalf("readMetrics() = %#v, want parsed metrics", metrics)
	}

	if err := os.WriteFile(filepath.Join(outputFilesDir, ".metrics.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("WriteFile(invalid metrics) error = %v", err)
	}

	if metrics := backend.readMetrics(outputFilesDir); metrics != nil {
		t.Fatalf("readMetrics(invalid) = %#v, want nil", metrics)
	}

	backend.trackContainer("exec-1", "container-1")
	if got := backend.activeContainers["exec-1"]; got != "container-1" {
		t.Fatalf("tracked container = %q, want container-1", got)
	}

	backend.untrackContainer("exec-1")
	if _, ok := backend.activeContainers["exec-1"]; ok {
		t.Fatalf("activeContainers still contains exec-1: %#v", backend.activeContainers)
	}

	backend.cfg.MemoryLimit = "bad-limit"
	if _, _, err := backend.buildContainerConfig(sharedDir, outputDir, nil); err == nil || !strings.Contains(err.Error(), "getting security config") {
		t.Fatalf("buildContainerConfig() error = %v, want security config failure", err)
	}
}

func testSandboxLogger() logrus.FieldLogger {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return logger
}

func newTestDockerBackend(t *testing.T) *DockerBackend {
	t.Helper()

	backend, err := NewDockerBackend(baseSandboxConfig(), testSandboxLogger())
	if err != nil {
		t.Fatalf("NewDockerBackend() error = %v", err)
	}

	return backend
}

func baseSandboxConfig() config.SandboxConfig {
	enabled := true

	return config.SandboxConfig{
		Backend:     string(BackendDocker),
		Image:       "python:3.11",
		Timeout:     30,
		MemoryLimit: "128M",
		CPULimit:    1.5,
		Network:     "bridge",
		Sessions: config.SessionConfig{
			Enabled:     &enabled,
			TTL:         time.Minute,
			MaxDuration: 4 * time.Hour,
			MaxSessions: 8,
		},
	}
}

func envSliceToMap(entries []string) map[string]string {
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		env[key] = value
	}

	return env
}
