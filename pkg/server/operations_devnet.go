package server

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethpandaops/panda/pkg/devnet"
	"github.com/ethpandaops/panda/pkg/operations"
)

// handleDevnetOperation dispatches devnet.* operations. Unlike the datasource
// operations these run locally on the server (which holds the Kurtosis engine
// connection) rather than being proxied upstream.
//
// The caller's identity is derived server-side from the authenticated request
// (authOwnerID), never from client-supplied args — so when this moves behind
// the cloud proxy in production, enclave ownership and authorization are
// enforced on identity the client cannot forge.
func (s *service) handleDevnetOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "devnet.up":
		s.handleDevnetUp(w, r)
	case "devnet.ls":
		s.handleDevnetLs(w, r)
	case "devnet.inspect":
		s.handleDevnetInspect(w, r)
	case "devnet.services":
		s.handleDevnetServices(w, r)
	case "devnet.logs":
		s.handleDevnetLogs(w, r)
	case "devnet.down":
		s.handleDevnetDown(w, r)
	default:
		return false
	}

	return true
}

// devnetClient connects to the local Kurtosis engine after pointing Kurtosis at
// the configured cluster: it selects the kubeconfig context (so Kurtosis targets
// the right cluster) and activates the Kurtosis cluster. The connection is lazy
// (per operation): these are infrequent, long-running commands, so a fresh gRPC
// handshake is cheap relative to the work.
func (s *service) devnetClient(out *bytes.Buffer) (*devnet.Client, error) {
	if err := devnet.EnsureKubeContext(s.clusterCfg.KubeconfigContext, out); err != nil {
		return nil, err
	}

	if err := devnet.EnsureCluster(s.clusterCfg.Name, out); err != nil {
		return nil, err
	}

	return devnet.NewClient()
}

func (s *service) handleDevnetUp(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	owner := authOwnerID(r)

	pkg := optionalStringArg(req.Args, "package")
	if pkg == "" {
		pkg = s.devnetCfg.Package
	}

	// docker_cache: an explicit (even empty) arg overrides the configured
	// default; otherwise fall back to config.
	dockerCache := s.devnetCfg.DockerCache
	if _, ok := req.Args["docker_cache"]; ok {
		dockerCache = optionalStringArg(req.Args, "docker_cache")
	}

	alwaysPull, err := optionalBoolArg(req.Args, "always_pull")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	dryRun, err := optionalBoolArg(req.Args, "dry_run")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	var out bytes.Buffer
	client, err := s.devnetClient(&out)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	s.log.WithField("owner", owner).WithField("enclave", optionalStringArg(req.Args, "name")).
		Info("devnet up requested")

	enclaveName, runErr := client.Up(r.Context(), devnet.UpOptions{
		EnclaveName:    optionalStringArg(req.Args, "name"),
		Package:        pkg,
		SerializedArgs: optionalStringArg(req.Args, "args"),
		AlwaysPull:     boolOrFalse(alwaysPull),
		DryRun:         boolOrFalse(dryRun),
		DockerCacheURL: dockerCache,
	}, &out)

	data := map[string]any{
		"enclave": enclaveName,
		"output":  out.String(),
	}
	if runErr != nil {
		data["error"] = runErr.Error()
		writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
			Kind: "devnet.up",
			Data: data,
			Meta: map[string]any{"success": false},
		})
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "devnet.up",
		Data: data,
		Meta: map[string]any{"success": true},
	})
}

func (s *service) handleDevnetLs(w http.ResponseWriter, r *http.Request) {
	var out bytes.Buffer
	client, err := s.devnetClient(&out)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	enclaves, err := client.List(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "devnet.ls",
		Data: enclaves,
	})
}

func (s *service) handleDevnetInspect(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	enclave, err := requiredStringArg(req.Args, "enclave")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	var out bytes.Buffer
	client, err := s.devnetClient(&out)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	info, err := client.Inspect(r.Context(), enclave)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "devnet.inspect",
		Data: info,
	})
}

func (s *service) handleDevnetServices(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	enclave, err := requiredStringArg(req.Args, "enclave")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	var out bytes.Buffer
	client, err := s.devnetClient(&out)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	svcs, err := client.Services(r.Context(), enclave)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "devnet.services",
		Data: svcs,
	})
}

func (s *service) handleDevnetLogs(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	enclave, err := requiredStringArg(req.Args, "enclave")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	var serviceNames []string
	for _, v := range optionalSliceArg(req.Args, "services") {
		if name, ok := v.(string); ok && name != "" {
			serviceNames = append(serviceNames, name)
		}
	}

	tail := optionalIntArg(req.Args, "tail", 0)
	if tail < 0 {
		tail = 0
	}

	var out bytes.Buffer
	client, err := s.devnetClient(&out)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	var logs bytes.Buffer
	if err := client.Logs(r.Context(), enclave, devnet.LogOptions{
		Services:  serviceNames,
		TailLines: uint32(tail),
	}, &logs); err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "devnet.logs",
		Data: map[string]any{"logs": logs.String()},
	})
}

// handleDevnetLogsStream follows service logs and streams them to the client as
// chunked plain text (one prefixed line at a time), flushing each line so a
// remote viewer sees logs live. It runs until the client disconnects (which
// cancels the request context and stops the upstream pod log streams).
func (s *service) handleDevnetLogsStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	enclave := strings.TrimSpace(q.Get("enclave"))
	if enclave == "" {
		writeAPIError(w, http.StatusBadRequest, "enclave is required")
		return
	}

	var serviceNames []string
	for _, name := range q["service"] {
		if name = strings.TrimSpace(name); name != "" {
			serviceNames = append(serviceNames, name)
		}
	}

	tail := 0
	if t := strings.TrimSpace(q.Get("tail")); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 {
			writeAPIError(w, http.StatusBadRequest, "tail must be a non-negative integer")
			return
		}
		tail = n
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "response writer does not support streaming")
		return
	}

	var out bytes.Buffer
	client, err := s.devnetClient(&out)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Validate the enclave (and any named services) before committing to a 200
	// stream, so genuine errors come back as proper status codes.
	known, err := client.Services(r.Context(), enclave)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	knownNames := map[string]bool{}
	for _, svc := range known {
		knownNames[svc.Name] = true
	}
	for _, name := range serviceNames {
		if !knownNames[name] {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("service %q not found in enclave %q", name, enclave))
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	s.log.WithField("owner", authOwnerID(r)).WithField("enclave", enclave).Info("devnet logs follow")

	if err := client.FollowLogs(r.Context(), enclave, devnet.LogOptions{
		Services:  serviceNames,
		TailLines: uint32(tail),
	}, w, flusher.Flush); err != nil && r.Context().Err() == nil {
		fmt.Fprintf(w, "<log stream error: %v>\n", err)
		flusher.Flush()
	}
}

func (s *service) handleDevnetDown(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	all, err := optionalBoolArg(req.Args, "all")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	var out bytes.Buffer
	client, err := s.devnetClient(&out)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}

	var targets []string
	if boolOrFalse(all) {
		enclaves, listErr := client.List(r.Context())
		if listErr != nil {
			writeAPIError(w, http.StatusBadGateway, listErr.Error())
			return
		}
		for _, e := range enclaves {
			targets = append(targets, e.Name)
		}
	} else {
		enclave, reqErr := requiredStringArg(req.Args, "enclave")
		if reqErr != nil {
			writeAPIError(w, http.StatusBadRequest, "enclave is required, or pass all=true")
			return
		}
		targets = []string{enclave}
	}

	destroyed := make([]string, 0, len(targets))
	for _, name := range targets {
		if err := client.Down(r.Context(), name); err != nil {
			writeAPIError(w, http.StatusBadGateway, err.Error())
			return
		}
		destroyed = append(destroyed, name)
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: "devnet.down",
		Data: map[string]any{"destroyed": destroyed},
	})
}

func boolOrFalse(b *bool) bool {
	return b != nil && *b
}
