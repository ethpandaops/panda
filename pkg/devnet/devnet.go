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
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/services"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/kurtosis_engine_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"gopkg.in/yaml.v3"
)

// DefaultPackage is the Kurtosis package launched by `panda devnet up` when no
// other package is specified.
const DefaultPackage = "github.com/ethpandaops/ethereum-package"

// defaultLogTailLines is how many recent lines per service `Logs` returns when
// the caller doesn't ask for a specific tail.
const defaultLogTailLines = 200

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

// Port is a network port exposed by a service.
type Port struct {
	// Name is the port's logical name (e.g. "rpc", "engine-rpc", "metrics").
	Name string `json:"name"`
	// Number is the in-cluster (private) container port.
	Number uint16 `json:"number"`
	// Transport is the L4 protocol ("TCP"/"UDP").
	Transport string `json:"transport"`
	// Application is the L7 protocol if known (e.g. "http", "ws"); may be empty.
	Application string `json:"application,omitempty"`
}

// Service is a service (EL/CL/VC client or tool) running in a devnet enclave.
type Service struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
	// PrivateIP is the service's in-cluster IP.
	PrivateIP string `json:"private_ip,omitempty"`
	// Ports are the in-cluster ports the service exposes, sorted by name.
	Ports []Port `json:"ports,omitempty"`
}

// Endpoint returns the in-cluster address for a named port (e.g.
// "el-1-geth-lighthouse:8545"), or "" if the service has no such port.
func (s Service) Endpoint(portName string) string {
	for _, p := range s.Ports {
		if p.Name == portName {
			return fmt.Sprintf("%s:%d", s.Name, p.Number)
		}
	}

	return ""
}

// Services lists the services running in an enclave, sorted by name, with their
// in-cluster ports. The names are what `Logs` accepts to select services.
func (c *Client) Services(ctx context.Context, enclave string) ([]Service, error) {
	enclaveCtx, err := c.kurtosis.GetEnclaveContext(ctx, enclave)
	if err != nil {
		return nil, fmt.Errorf("getting enclave %q: %w", enclave, err)
	}

	// An empty identifier set asks for every service.
	ctxs, err := enclaveCtx.GetServiceContexts(map[string]bool{})
	if err != nil {
		return nil, fmt.Errorf("listing services in %q: %w", enclave, err)
	}

	out := make([]Service, 0, len(ctxs))
	for name, sc := range ctxs {
		out = append(out, Service{
			Name:      string(name),
			UUID:      string(sc.GetServiceUUID()),
			PrivateIP: sc.GetPrivateIPAddress(),
			Ports:     toPorts(sc.GetPrivatePorts()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// toPorts flattens a Kurtosis port-spec map into a name-sorted []Port.
func toPorts(specs map[string]*services.PortSpec) []Port {
	ports := make([]Port, 0, len(specs))
	for name, spec := range specs {
		ports = append(ports, Port{
			Name:        name,
			Number:      spec.GetNumber(),
			Transport:   kurtosis_core_rpc_api_bindings.Port_TransportProtocol(spec.GetTransportProtocol()).String(),
			Application: spec.GetMaybeApplicationProtocol(),
		})
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Name < ports[j].Name })

	return ports
}

// LogOptions configures a logs fetch.
type LogOptions struct {
	// Services selects services by name; empty means every service.
	Services []string
	// TailLines is the number of recent lines per service. 0 uses
	// defaultLogTailLines.
	TailLines uint32
}

// Logs writes recent logs for the selected services (or all) to out, each line
// prefixed with its service name. It does not follow: it returns once the
// requested history has been written, so it works over the plain
// request/response operation path (and thus through the cloud proxy).
//
// On the Kubernetes backend it reads pod logs directly from the cluster: this
// fork ships container logs to OTel/ClickHouse, so the engine's file-based log
// store (what the Kurtosis log API reads) is empty. Raw pod logs are always
// available and need no aggregator.
func (c *Client) Logs(ctx context.Context, enclave string, opts LogOptions, out io.Writer) error {
	uuid, wanted, tail, err := c.resolveLogTargets(ctx, enclave, opts)
	if err != nil {
		return err
	}

	return podLogs(ctx, uuid, wanted, tail, out)
}

// FollowLogs streams logs for the selected services (or all) to out until ctx is
// cancelled, each line prefixed with its service name. flush, if non-nil, is
// called after each line so an HTTP handler can push it to the client
// immediately. Like Logs it reads pod logs directly from the cluster.
func (c *Client) FollowLogs(ctx context.Context, enclave string, opts LogOptions, out io.Writer, flush func()) error {
	uuid, wanted, tail, err := c.resolveLogTargets(ctx, enclave, opts)
	if err != nil {
		return err
	}

	return followPodLogs(ctx, uuid, wanted, tail, out, flush)
}

// resolveLogTargets validates the enclave and selected services, returning the
// enclave UUID, the resolved service names, and the per-service tail count.
func (c *Client) resolveLogTargets(ctx context.Context, enclave string, opts LogOptions) (string, []string, int64, error) {
	info, err := c.kurtosis.GetEnclave(ctx, enclave)
	if err != nil {
		return "", nil, 0, fmt.Errorf("inspecting enclave %q: %w", enclave, err)
	}

	all, err := c.Services(ctx, enclave)
	if err != nil {
		return "", nil, 0, err
	}

	wanted := opts.Services
	if len(wanted) == 0 {
		for _, s := range all {
			wanted = append(wanted, s.Name)
		}
	} else {
		known := map[string]bool{}
		for _, s := range all {
			known[s.Name] = true
		}
		for _, w := range wanted {
			if !known[w] {
				return "", nil, 0, fmt.Errorf("service %q not found in enclave %q", w, enclave)
			}
		}
	}
	if len(wanted) == 0 {
		return "", nil, 0, fmt.Errorf("enclave %q has no services", enclave)
	}

	tail := opts.TailLines
	if tail == 0 {
		tail = defaultLogTailLines
	}

	return info.GetEnclaveUuid(), wanted, int64(tail), nil
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
