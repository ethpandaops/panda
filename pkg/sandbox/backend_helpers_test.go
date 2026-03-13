package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/system"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestNewDockerBackendInitializesDefaults(t *testing.T) {
	t.Parallel()

	backend, err := NewDockerBackend(testSandboxConfig(), logrus.New())
	require.NoError(t, err)

	assert.Equal(t, "docker", backend.Name())
	assert.NotNil(t, backend.sessionManager)
	assert.NotNil(t, backend.activeContainers)
	assert.Empty(t, backend.activeContainers)
	assert.NotNil(t, backend.securityConfigFunc)
}

func TestDockerBackendExecuteRequiresInitializedClient(t *testing.T) {
	t.Parallel()

	backend, err := NewDockerBackend(testSandboxConfig(), logrus.New())
	require.NoError(t, err)

	result, err := backend.Execute(context.Background(), ExecuteRequest{Code: "print('hi')"})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "docker client not initialized")
}

func TestDockerBackendStopWithoutClientSucceeds(t *testing.T) {
	t.Parallel()

	backend, err := NewDockerBackend(testSandboxConfig(), logrus.New())
	require.NoError(t, err)

	require.NoError(t, backend.Stop(context.Background()))
}

func TestParseContainerCreatedAt(t *testing.T) {
	t.Parallel()

	fromLabel := parseContainerCreatedAt(map[string]string{
		LabelCreatedAt: "1700000000",
	}, 42)
	assert.Equal(t, time.Unix(1700000000, 0), fromLabel)

	fromDocker := parseContainerCreatedAt(map[string]string{
		LabelCreatedAt: "not-a-timestamp",
	}, 42)
	assert.Equal(t, time.Unix(42, 0), fromDocker)
}

func TestGetSecurityConfigUsesInjectedFunc(t *testing.T) {
	t.Parallel()

	backend := &DockerBackend{
		cfg: config.SandboxConfig{
			MemoryLimit: "256MiB",
			CPULimit:    1.5,
		},
		securityConfigFunc: func(memoryLimit string, cpuLimit float64) (*SecurityConfig, error) {
			assert.Equal(t, "256MiB", memoryLimit)
			assert.Equal(t, 1.5, cpuLimit)

			return &SecurityConfig{Runtime: "custom-runtime"}, nil
		},
	}

	securityConfig, err := backend.getSecurityConfig()
	require.NoError(t, err)
	assert.Equal(t, "custom-runtime", securityConfig.Runtime)
}

func TestCreateExecutionDirsSupportsDirectAndHostSharedModes(t *testing.T) {
	t.Parallel()

	t.Run("direct mode uses temp directory", func(t *testing.T) {
		t.Parallel()

		backend := &DockerBackend{cfg: testSandboxConfig()}
		baseDir, err := backend.createExecutionDirs("exec-1")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(baseDir) })

		assert.Contains(t, baseDir, filepath.Join(os.TempDir(), "ethpandaops-panda-sandbox-exec-1"))
		assert.DirExists(t, filepath.Join(baseDir, "shared"))
		assert.DirExists(t, filepath.Join(baseDir, "output"))
	})

	t.Run("host shared mode uses configured root", func(t *testing.T) {
		t.Parallel()

		hostRoot := t.TempDir()
		backend := &DockerBackend{cfg: config.SandboxConfig{HostSharedPath: hostRoot}}

		baseDir, err := backend.createExecutionDirs("exec-2")
		require.NoError(t, err)

		assert.Equal(t, filepath.Join(hostRoot, "exec-2"), baseDir)
		assert.DirExists(t, filepath.Join(baseDir, "shared"))
		assert.DirExists(t, filepath.Join(baseDir, "output"))
	})
}

func TestBuildContainerConfigMergesEnvAndRewritesHostSharedPaths(t *testing.T) {
	t.Parallel()

	hostRoot := filepath.Join(t.TempDir(), "host-root")
	backend := &DockerBackend{
		cfg: config.SandboxConfig{
			Image:          "sandbox:test",
			Network:        "custom-net",
			HostSharedPath: hostRoot,
		},
		securityConfigFunc: func(string, float64) (*SecurityConfig, error) {
			return &SecurityConfig{
				ReadonlyRootfs: true,
				DropCapabilities: []string{
					"ALL",
				},
				SecurityOpts: []string{
					"no-new-privileges:true",
				},
				PidsLimit:   42,
				MemoryLimit: 1024,
				CPUQuota:    200000,
				CPUPeriod:   100000,
				TmpfsSize:   "64M",
				Runtime:     "custom-runtime",
			}, nil
		},
	}

	containerConfig, hostConfig, err := backend.buildContainerConfig(
		filepath.Join("/tmp/work", "exec-123", "shared"),
		filepath.Join("/tmp/work", "exec-123", "output"),
		map[string]string{
			"ALPHA": "beta",
			"HOME":  "/custom-home",
		},
	)
	require.NoError(t, err)

	assert.Equal(t, "sandbox:test", containerConfig.Image)
	assert.Equal(t, "python", containerConfig.Cmd[0])
	assert.Equal(t, "/shared/script.py", containerConfig.Cmd[1])
	assert.Equal(t, "nobody", containerConfig.User)
	assert.Equal(t, "true", containerConfig.Labels[LabelManaged])
	assert.NotEmpty(t, containerConfig.Labels[LabelCreatedAt])
	assert.Contains(t, containerConfig.Env, "ALPHA=beta")
	assert.Contains(t, containerConfig.Env, "HOME=/custom-home")
	assert.Contains(t, containerConfig.Env, "MPLCONFIGDIR=/tmp")

	assert.Equal(t, container.NetworkMode("custom-net"), hostConfig.NetworkMode)
	assert.Equal(t, []string{"host.docker.internal:host-gateway"}, hostConfig.ExtraHosts)
	assert.Len(t, hostConfig.Mounts, 2)
	assert.Equal(t, filepath.Join(hostRoot, "exec-123", "shared"), hostConfig.Mounts[0].Source)
	assert.Equal(t, filepath.Join(hostRoot, "exec-123", "output"), hostConfig.Mounts[1].Source)
	assert.Equal(t, "custom-runtime", hostConfig.Runtime)
	assert.True(t, hostConfig.ReadonlyRootfs)
	if assert.NotNil(t, hostConfig.PidsLimit) {
		assert.EqualValues(t, 42, *hostConfig.PidsLimit)
	}
	assert.EqualValues(t, 1024, hostConfig.Memory)
	assert.EqualValues(t, 200000, hostConfig.CPUQuota)
	assert.EqualValues(t, 100000, hostConfig.CPUPeriod)
	assert.Equal(t, map[string]string{"/tmp": "size=64M,mode=1777"}, hostConfig.Tmpfs)
}

func TestBuildContainerConfigReturnsSecurityErrors(t *testing.T) {
	t.Parallel()

	backend := &DockerBackend{
		cfg: testSandboxConfig(),
		securityConfigFunc: func(string, float64) (*SecurityConfig, error) {
			return nil, assert.AnError
		},
	}

	containerConfig, hostConfig, err := backend.buildContainerConfig("/tmp/shared", "/tmp/output", nil)
	require.Error(t, err)
	assert.Nil(t, containerConfig)
	assert.Nil(t, hostConfig)
	assert.Contains(t, err.Error(), "getting security config")
}

func TestCollectOutputFilesSkipsHiddenFilesAndDirectories(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "visible.txt"), []byte("data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, ".hidden"), []byte("secret"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(outputDir, "nested"), 0o755))

	backend := &DockerBackend{}
	files, err := backend.collectOutputFiles(outputDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"visible.txt"}, files)
}

func TestReadMetricsHandlesMissingInvalidAndValidFiles(t *testing.T) {
	t.Parallel()

	backend := &DockerBackend{log: logrus.New()}

	t.Run("missing metrics file", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, backend.readMetrics(t.TempDir()))
	})

	t.Run("invalid metrics file", func(t *testing.T) {
		t.Parallel()
		outputDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outputDir, ".metrics.json"), []byte("{"), 0o644))
		assert.Nil(t, backend.readMetrics(outputDir))
	})

	t.Run("valid metrics file", func(t *testing.T) {
		t.Parallel()
		outputDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outputDir, ".metrics.json"), []byte(`{"duration":1.5,"ok":true}`), 0o644))

		metrics := backend.readMetrics(outputDir)
		if assert.NotNil(t, metrics) {
			assert.EqualValues(t, 1.5, metrics["duration"])
			assert.Equal(t, true, metrics["ok"])
		}
	})
}

func TestTrackAndUntrackContainer(t *testing.T) {
	t.Parallel()

	backend := &DockerBackend{
		activeContainers: make(map[string]string),
	}

	backend.trackContainer("exec-1", "container-1")
	assert.Equal(t, "container-1", backend.activeContainers["exec-1"])

	backend.untrackContainer("exec-1")
	assert.Empty(t, backend.activeContainers)
}

func TestNewGVisorBackendSetsRuntimeAndHelpers(t *testing.T) {
	t.Parallel()

	backend, err := NewGVisorBackend(testSandboxConfig(), logrus.New())
	require.NoError(t, err)

	assert.Equal(t, "gvisor", backend.Name())

	securityConfig, err := backend.getSecurityConfig()
	require.NoError(t, err)
	assert.Equal(t, "runsc", securityConfig.Runtime)

	info := system.Info{
		Runtimes: map[string]system.RuntimeWithStatus{
			"runc":  {},
			"runsc": {},
		},
	}
	assert.True(t, hasRuntime(info, "runsc"))
	assert.False(t, hasRuntime(info, "kata"))
	assert.ElementsMatch(t, []string{"runc", "runsc"}, getRuntimeNames(info))
}

func TestNewSelectsConfiguredBackend(t *testing.T) {
	t.Parallel()

	t.Run("docker", func(t *testing.T) {
		t.Parallel()

		cfg := testSandboxConfig()
		cfg.Backend = string(BackendDocker)

		service, err := New(cfg, logrus.New())
		require.NoError(t, err)
		assert.IsType(t, &DockerBackend{}, service)
		assert.Equal(t, "docker", service.Name())
	})

	t.Run("gvisor", func(t *testing.T) {
		t.Parallel()

		cfg := testSandboxConfig()
		cfg.Backend = string(BackendGVisor)

		service, err := New(cfg, logrus.New())
		require.NoError(t, err)
		assert.IsType(t, &GVisorBackend{}, service)
		assert.Equal(t, "gvisor", service.Name())
	})

	t.Run("unsupported backend", func(t *testing.T) {
		t.Parallel()

		cfg := testSandboxConfig()
		cfg.Backend = "unsupported"

		service, err := New(cfg, logrus.New())
		require.Error(t, err)
		assert.Nil(t, service)
		assert.Contains(t, err.Error(), "unsupported sandbox backend")
	})
}

func testSandboxConfig() config.SandboxConfig {
	return config.SandboxConfig{
		Backend:     string(BackendDocker),
		Image:       "sandbox:test",
		Timeout:     30,
		MemoryLimit: "256MiB",
		CPULimit:    1.5,
		Network:     "bridge",
		Sessions: config.SessionConfig{
			TTL:         10 * time.Minute,
			MaxDuration: time.Hour,
			MaxSessions: 4,
		},
	}
}
