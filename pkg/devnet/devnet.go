// Package devnet drives Kurtosis enclaves running the ethpandaops
// ethereum-package, so client developers can spin up multi-client devnets
// with a single command.
//
// It talks to a Kurtosis engine over the engine's gRPC API via the Kurtosis
// Go SDK. On a Kubernetes backend the engine runs in-cluster and is reached
// on localhost through `kurtosis gateway`; on a Docker backend it is the
// local engine. Either way the connection point is the same.
package devnet

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/kurtosis_engine_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"gopkg.in/yaml.v3"
)

// DefaultPackage is the Kurtosis package launched by `panda devnet up` when no
// other package is specified.
const DefaultPackage = "github.com/ethpandaops/ethereum-package"

// Client wraps a connection to a Kurtosis engine.
type Client struct {
	kurtosis *kurtosis_context.KurtosisContext
}

// NewClient connects to the local Kurtosis engine. On a Kubernetes backend the
// engine is exposed on localhost by `kurtosis gateway`, which must be running.
func NewClient() (*Client, error) {
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		return nil, fmt.Errorf("connecting to the Kurtosis engine: %w\n"+
			"Is the engine running (`kurtosis engine status`)? On a Kubernetes backend, "+
			"`kurtosis gateway` must also be running in another terminal", err)
	}

	return &Client{kurtosis: kurtosisCtx}, nil
}

// UpOptions configures a devnet launch.
type UpOptions struct {
	// EnclaveName is the enclave to create. Empty lets Kurtosis generate one.
	EnclaveName string
	// Package overrides the package to run. Empty uses DefaultPackage.
	Package string
	// SerializedArgs is the package args as JSON or YAML. Empty means "{}".
	SerializedArgs string
	// AlwaysPull forces re-pulling images (ImageDownloadMode=always). Use for
	// devnet branches whose tags are mutable.
	AlwaysPull bool
	// DryRun validates and plans the run without applying it.
	DryRun bool
	// DockerCacheURL routes every package image through a pull-through registry
	// cache at this host (e.g. "docker.ethquokkaops.io") by injecting
	// docker_cache_params into the args. Empty disables it. An explicit
	// docker_cache_params already present in the args is left untouched.
	DockerCacheURL string
}

// Up creates an enclave and runs the package in it, streaming progress to out.
// It returns the (possibly generated) enclave name even on failure, so callers
// can surface or clean up the partial enclave.
func (c *Client) Up(ctx context.Context, opts UpOptions, out io.Writer) (string, error) {
	pkg := opts.Package
	if pkg == "" {
		pkg = DefaultPackage
	}

	// Build the args before creating the enclave so a bad config fails fast
	// without leaving a dangling enclave behind.
	args := strings.TrimSpace(opts.SerializedArgs)
	if args == "" {
		args = "{}"
	}
	if opts.DockerCacheURL != "" {
		merged, err := injectDockerCache(args, opts.DockerCacheURL)
		if err != nil {
			return opts.EnclaveName, err
		}
		args = merged
	}

	enclaveCtx, err := c.kurtosis.CreateEnclave(ctx, opts.EnclaveName)
	if err != nil {
		return opts.EnclaveName, fmt.Errorf("creating enclave: %w", err)
	}
	enclaveName := enclaveCtx.GetEnclaveName()

	downloadMode := kurtosis_core_rpc_api_bindings.ImageDownloadMode_missing
	if opts.AlwaysPull {
		downloadMode = kurtosis_core_rpc_api_bindings.ImageDownloadMode_always
	}

	runConfig := starlark_run_config.NewRunStarlarkConfig(
		starlark_run_config.WithSerializedParams(args),
		starlark_run_config.WithImageDownloadMode(downloadMode),
		starlark_run_config.WithDryRun(opts.DryRun),
	)

	lineChan, cancelFunc, err := enclaveCtx.RunStarlarkRemotePackage(ctx, pkg, runConfig)
	if err != nil {
		return enclaveName, fmt.Errorf("starting package %q: %w", pkg, err)
	}
	defer cancelFunc()

	if err := streamRun(out, lineChan); err != nil {
		return enclaveName, err
	}

	return enclaveName, nil
}

// streamRun consumes the Starlark response stream, printing human-readable
// progress and returning the first error encountered (or a generic error if
// the run finishes unsuccessfully without an explicit error line).
func streamRun(out io.Writer, lineChan <-chan *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine) error {
	var runErr error

	for line := range lineChan {
		switch {
		case line.GetInfo() != nil:
			fmt.Fprintln(out, line.GetInfo().GetInfoMessage())
		case line.GetWarning() != nil:
			fmt.Fprintf(out, "WARN: %s\n", line.GetWarning().GetWarningMessage())
		case line.GetInstructionResult() != nil:
			if result := strings.TrimSpace(line.GetInstructionResult().GetSerializedInstructionResult()); result != "" {
				fmt.Fprintln(out, result)
			}
		case line.GetError() != nil:
			if runErr == nil {
				runErr = starlarkError(line.GetError())
			}
		case line.GetRunFinishedEvent() != nil:
			if !line.GetRunFinishedEvent().GetIsRunSuccessful() && runErr == nil {
				runErr = fmt.Errorf("package run finished unsuccessfully")
			}
		}
	}

	return runErr
}

func starlarkError(e *kurtosis_core_rpc_api_bindings.StarlarkError) error {
	switch {
	case e.GetInterpretationError() != nil:
		return fmt.Errorf("interpretation error: %s", e.GetInterpretationError().GetErrorMessage())
	case e.GetValidationError() != nil:
		return fmt.Errorf("validation error: %s", e.GetValidationError().GetErrorMessage())
	case e.GetExecutionError() != nil:
		return fmt.Errorf("execution error: %s", e.GetExecutionError().GetErrorMessage())
	default:
		return fmt.Errorf("unknown Starlark error")
	}
}

// injectDockerCache merges docker_cache_params into the serialized package args
// so every image is pulled through the cache host. The input may be JSON or
// YAML (JSON is valid YAML); the output is YAML. An explicit docker_cache_params
// already present in the args is preserved.
func injectDockerCache(serializedArgs, cacheURL string) (string, error) {
	params := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(serializedArgs), &params); err != nil {
		return "", fmt.Errorf("parsing args to inject docker cache: %w", err)
	}

	if _, ok := params["docker_cache_params"]; !ok {
		params["docker_cache_params"] = map[string]interface{}{
			"enabled": true,
			"url":     cacheURL,
		}
	}

	out, err := yaml.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("serializing args with docker cache: %w", err)
	}

	return string(out), nil
}

// Enclave is a flattened view of a Kurtosis enclave for listing and inspection.
type Enclave struct {
	Name         string    `json:"name"`
	UUID         string    `json:"uuid"`
	Status       string    `json:"status"`
	APIContainer string    `json:"api_container"`
	CreationTime time.Time `json:"creation_time"`
}

// List returns all enclaves known to the engine, newest first.
func (c *Client) List(ctx context.Context) ([]Enclave, error) {
	enclaves, err := c.kurtosis.GetEnclaves(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing enclaves: %w", err)
	}

	var out []Enclave
	for _, info := range enclaves.GetEnclavesByUuid() {
		out = append(out, toEnclave(info))
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreationTime.After(out[j].CreationTime)
	})

	return out, nil
}

// Inspect returns a single enclave by name or UUID.
func (c *Client) Inspect(ctx context.Context, identifier string) (Enclave, error) {
	info, err := c.kurtosis.GetEnclave(ctx, identifier)
	if err != nil {
		return Enclave{}, fmt.Errorf("inspecting enclave %q: %w", identifier, err)
	}

	return toEnclave(info), nil
}

// Down destroys an enclave by name or UUID, tearing down its namespace/pods.
func (c *Client) Down(ctx context.Context, identifier string) error {
	if err := c.kurtosis.DestroyEnclave(ctx, identifier); err != nil {
		return fmt.Errorf("destroying enclave %q: %w", identifier, err)
	}

	return nil
}

func toEnclave(info *kurtosis_engine_rpc_api_bindings.EnclaveInfo) Enclave {
	e := Enclave{
		Name:         info.GetName(),
		UUID:         info.GetEnclaveUuid(),
		Status:       cleanStatus(info.GetContainersStatus().String()),
		APIContainer: cleanStatus(info.GetApiContainerStatus().String()),
	}
	if t := info.GetCreationTime(); t != nil {
		e.CreationTime = t.AsTime()
	}

	return e
}

// cleanStatus strips the proto enum type prefix (e.g.
// "EnclaveContainersStatus_RUNNING" -> "RUNNING").
func cleanStatus(s string) string {
	if i := strings.IndexByte(s, '_'); i >= 0 {
		return s[i+1:]
	}

	return s
}
