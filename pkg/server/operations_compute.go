package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ethpandaops/panda/pkg/compute"
	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

// computeBaseURL is a placeholder base URL for the generated compute client.
// The host is never dialed: computeProxyDoer rewrites every request through
// the credential proxy, so only the request path and query are used.
const computeBaseURL = "http://compute.invalid"

// computeProxyDoer routes generated compute-client requests through the
// credential proxy instead of dialing the backend directly. This reuses the
// proxy's auth, user-subject forwarding, and attribution for every endpoint.
type computeProxyDoer struct {
	svc        *service
	datasource string
}

// Do forwards the request through the proxy's /compute mount and returns the
// upstream response. The proxy strips the /compute prefix before forwarding to
// the backend, so the backend sees the original /v1/... path.
func (d *computeProxyDoer) Do(req *http.Request) (*http.Response, error) {
	var body io.Reader
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()

		body = req.Body
	}

	requestPath := "/compute" + req.URL.Path
	if req.URL.RawQuery != "" {
		requestPath += "?" + req.URL.RawQuery
	}

	// Forward every header the generated client set (Content-Type,
	// Idempotency-Key, Accept, ...). The proxy strips caller credentials and
	// injects the service token downstream, so passing them through is safe and
	// avoids dropping headers the backend requires.
	headers := req.Header.Clone()
	if headers == nil {
		headers = http.Header{}
	}

	headers.Set(handlers.DatasourceHeader, d.datasource)

	// Compute mutations (exec above all) must never run twice, so only
	// read-only methods keep the proxy's invalidate-and-retry on auth rejection.
	replayable := proxyNoReplay
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		replayable = proxyReplayable
	}

	respBody, status, respHeaders, err := d.svc.proxyDatasourceRequest(
		req.Context(),
		"compute",
		d.datasource,
		req.Method,
		requestPath,
		body,
		headers,
		replayable,
	)
	if err != nil {
		return nil, err
	}

	if respHeaders == nil {
		respHeaders = http.Header{}
	}

	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     respHeaders,
		Body:       io.NopCloser(bytes.NewReader(respBody)),
		Request:    req,
	}, nil
}

// computeOpFunc performs a single typed compute client call and returns the
// raw HTTP response for passthrough.
type computeOpFunc func(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error)

// computeOpIDFunc is computeOpFunc with a required path id resolved up-front.
type computeOpIDFunc func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error)

func (s *service) handleComputeOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "compute.list_datasources":
		s.handleComputeListDatasources(w)

	// Sandboxes.
	case "compute.list_sandboxes":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error) {
			return c.ListSandboxes(ctx, &compute.ListSandboxesParams{
				Limit:  computeLimit(args),
				Cursor: computeCursor(args),
				Offset: computeOffset(args),
				Filter: computeFilter(args),
			})
		})
	case "compute.get_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetSandbox(ctx, id)
		})
	case "compute.create_sandbox":
		s.computeOp(w, r, s.computeCreateSandbox)
	case "compute.delete_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.DeleteSandbox(ctx, id, &compute.DeleteSandboxParams{IdempotencyKey: computeIdem(args)})
		})
	case "compute.stop_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.StopSandbox(ctx, id, &compute.StopSandboxParams{IdempotencyKey: computeIdem(args)})
		})
	case "compute.start_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.StartSandbox(ctx, id, &compute.StartSandboxParams{IdempotencyKey: computeIdem(args)})
		})
	case "compute.snapshot_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.SnapshotSandbox(ctx, id,
				&compute.SnapshotSandboxParams{IdempotencyKey: computeIdem(args)},
				compute.SnapshotSandboxJSONRequestBody{
					Note: computeOptStr(args, "note"),
					Ttl:  computeOptStr(args, "ttl"),
				},
			)
		})
	case "compute.lease_sandbox":
		s.computeOpWithID(w, r, "id", s.computeLeaseSandbox)
	case "compute.prepare_sandbox_ssh":
		s.computeOpWithID(w, r, "id", s.computePrepareSandboxSSH)
	case "compute.get_sandbox_images":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetSandboxImages(ctx, id)
		})
	case "compute.expose_port":
		s.computeOpWithID(w, r, "id", s.computeExposePort)
	case "compute.unexpose_port":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			port := optionalIntArg(args, "port", 0)
			if port < 1 {
				return nil, &computeArgError{err: fmt.Errorf("port is required and must be at least 1")}
			}

			return c.UnexposePort(ctx, id, port, computeIdemHeaderEditor(args))
		})
	case "compute.get_sandbox_operations":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetSandboxOperations(ctx, id)
		})
	case "compute.get_sandbox_logs":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			params := &compute.GetSandboxLogsParams{}
			if v := optionalIntArg(args, "tail_bytes", 0); v > 0 {
				tail := int64(v)
				params.TailBytes = &tail
			}
			if v := optionalStringArg(args, "source"); v != "" {
				source := compute.GetSandboxLogsParamsSource(v)
				params.Source = &source
			}
			return c.GetSandboxLogs(ctx, id, params)
		})
	case "compute.get_sandbox_lineage":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetSandboxLineage(ctx, id)
		})
	case "compute.exec_sandbox":
		s.computeOpWithID(w, r, "id", s.computeExecSandbox)
	case "compute.get_sandbox_metrics":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetSandboxMetrics(ctx, id, nil)
		})
	case "compute.pause_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.PauseSandbox(ctx, id, &compute.PauseSandboxParams{IdempotencyKey: computeIdem(args)})
		})
	case "compute.resume_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.ResumeSandbox(ctx, id, &compute.ResumeSandboxParams{IdempotencyKey: computeIdem(args)})
		})
	case "compute.get_sandbox_hooks":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetSandboxHooks(ctx, id, nil)
		})
	case "compute.get_sandbox_hook_runs":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetSandboxHookRuns(ctx, id, nil)
		})
	case "compute.fork_sandbox":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			body, err := computeForkRequest(args)
			if err != nil {
				return nil, err
			}

			return c.ForkSandbox(ctx, id,
				&compute.ForkSandboxParams{IdempotencyKey: computeIdem(args)}, *body)
		})

	// Forks.
	case "compute.list_forks":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, _ map[string]any) (*http.Response, error) {
			return c.ListForks(ctx)
		})
	case "compute.get_fork":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetFork(ctx, id)
		})

	// Images (raw snapshots and named images behind one surface).
	case "compute.list_images":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error) {
			return c.ListImages(ctx, &compute.ListImagesParams{
				Limit:  computeLimit(args),
				Cursor: computeCursor(args),
				Offset: computeOffset(args),
				Filter: computeFilter(args),
			})
		})
	case "compute.get_image":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetImage(ctx, id)
		})
	case "compute.delete_image":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.DeleteImage(ctx, id, &compute.DeleteImageParams{IdempotencyKey: computeIdem(args)})
		})
	case "compute.fork_image":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			body, err := computeForkRequest(args)
			if err != nil {
				return nil, err
			}

			return c.ForkImage(ctx, id,
				&compute.ForkImageParams{IdempotencyKey: computeIdem(args)}, *body)
		})
	case "compute.promote_image":
		s.computeOpWithID(w, r, "id", s.computePromoteImage)
	case "compute.deactivate_image":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
			return c.DeactivateImage(ctx, id, computeIdemHeaderEditor(args))
		})
	case "compute.get_image_restored_by":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetImageRestoredBy(ctx, id)
		})
	case "compute.get_image_lineage":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetImageLineage(ctx, id)
		})

	// Bakes.
	case "compute.list_bakes":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error) {
			return c.ListBakes(ctx, &compute.ListBakesParams{
				Limit:  computeLimit(args),
				Cursor: computeCursor(args),
				Offset: computeOffset(args),
				Filter: computeFilter(args),
			})
		})
	case "compute.run_bake":
		s.computeOpWithID(w, r, "name", func(ctx context.Context, c *compute.Client, name string, args map[string]any) (*http.Response, error) {
			return c.RunBake(ctx, name, computeIdemHeaderEditor(args))
		})

	// Operations.
	case "compute.list_operations":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error) {
			return c.ListOperations(ctx, &compute.ListOperationsParams{
				Limit:  computeLimit(args),
				Cursor: computeCursor(args),
				Offset: computeOffset(args),
				Filter: computeFilter(args),
			})
		})
	case "compute.get_operation":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetOperation(ctx, id)
		})

	// SSH keys (caller's own).
	case "compute.list_ssh_keys":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error) {
			return c.ListSSHPublicKeys(ctx, &compute.ListSSHPublicKeysParams{
				Limit:  computeLimit(args),
				Cursor: computeCursor(args),
				Offset: computeOffset(args),
				Filter: computeFilter(args),
			})
		})
	case "compute.add_ssh_key":
		s.computeOp(w, r, s.computeAddSSHKey)
	case "compute.delete_ssh_key":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.DeleteSSHPublicKey(ctx, id)
		})

	// Directory and audit (read-only).
	case "compute.list_users":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, _ map[string]any) (*http.Response, error) {
			return c.ListUsers(ctx)
		})
	case "compute.get_user":
		s.computeOpWithID(w, r, "handle", func(ctx context.Context, c *compute.Client, handle string, _ map[string]any) (*http.Response, error) {
			return c.GetUser(ctx, handle)
		})
	case "compute.list_nodes":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, _ map[string]any) (*http.Response, error) {
			return c.ListNodes(ctx)
		})
	case "compute.get_node":
		s.computeOpWithID(w, r, "id", func(ctx context.Context, c *compute.Client, id string, _ map[string]any) (*http.Response, error) {
			return c.GetNode(ctx, id)
		})
	case "compute.list_audit":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, _ map[string]any) (*http.Response, error) {
			return c.ListAudit(ctx)
		})
	case "compute.meta":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, _ map[string]any) (*http.Response, error) {
			return c.Meta(ctx)
		})
	case "compute.auth_session":
		s.computeOp(w, r, func(ctx context.Context, c *compute.Client, _ map[string]any) (*http.Response, error) {
			return c.AuthSession(ctx)
		})

	default:
		return false
	}

	return true
}

func (s *service) handleComputeListDatasources(w http.ResponseWriter) {
	infos, err := s.computeDatasources()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, err.Error())

		return
	}

	items := make([]listItem, 0, len(infos))
	for _, info := range infos {
		items = append(items, listItem{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
			Type:        "compute",
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"datasources": items},
	})
}

// computeOp resolves the datasource, builds a proxy-backed client, runs fn, and
// forwards the upstream response.
func (s *service) computeOp(w http.ResponseWriter, r *http.Request, fn computeOpFunc) {
	client, args, ok := s.computeClientFor(w, r)
	if !ok {
		return
	}

	resp, err := fn(r.Context(), client, args)
	s.writeComputeResult(w, resp, err)
}

// computeOpWithID is computeOp with a required string argument (a path id)
// extracted before the call.
func (s *service) computeOpWithID(w http.ResponseWriter, r *http.Request, idArg string, fn computeOpIDFunc) {
	client, args, ok := s.computeClientFor(w, r)
	if !ok {
		return
	}

	id, err := requiredStringArg(args, idArg)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	resp, err := fn(r.Context(), client, id, args)
	s.writeComputeResult(w, resp, err)
}

func (s *service) computeCreateSandbox(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error) {
	template := optionalStringArg(args, "template")
	snapshotID := optionalStringArg(args, "snapshot_id")
	if (template == "") == (snapshotID == "") {
		return nil, &computeArgError{err: fmt.Errorf("exactly one of template or snapshot_id is required")}
	}

	body := compute.CreateSandboxJSONRequestBody{
		Ttl:      computeOptStr(args, "ttl"),
		Name:     computeOptStr(args, "name"),
		Vcpu:     computeOptInt(args, "vcpu"),
		MemoryMb: computeOptInt(args, "memory_mb"),
		DiskGb:   computeOptInt(args, "disk_gb"),
	}
	if template != "" {
		body.Template = &template
	} else {
		source := compute.SnapshotBootSource{
			Kind:       compute.SnapshotBootSourceKind("snapshot"),
			SnapshotId: snapshotID,
		}
		if v := optionalStringArg(args, "flavor"); v != "" {
			flavor := compute.SnapshotBootSourceFlavor(v)
			source.Flavor = &flavor
		}
		var union compute.CreateSandboxSource
		if err := union.FromSnapshotBootSource(source); err != nil {
			return nil, &computeArgError{err: err}
		}
		body.Source = &union
	}

	env, err := computeOptStringMap(args, "env")
	if err != nil {
		return nil, &computeArgError{err: err}
	}
	body.Env = env
	labels, err := computeOptStringMap(args, "labels")
	if err != nil {
		return nil, &computeArgError{err: err}
	}
	body.Labels = labels

	if raw, ok := args["hooks"]; ok {
		var hooks []compute.HookDeclaration
		if err := reencodeJSONArg(raw, &hooks); err != nil {
			return nil, &computeArgError{err: fmt.Errorf("hooks: %w", err)}
		}
		body.Hooks = &hooks
	}
	if raw, ok := args["watchdog"]; ok {
		var watchdog compute.WatchdogDeclaration
		if err := reencodeJSONArg(raw, &watchdog); err != nil {
			return nil, &computeArgError{err: fmt.Errorf("watchdog: %w", err)}
		}
		body.Watchdog = &watchdog
	}
	if raw, ok := args["exposed_ports"]; ok {
		var ports []compute.PortExposureRequest
		if err := reencodeJSONArg(raw, &ports); err != nil {
			return nil, &computeArgError{err: fmt.Errorf("exposed_ports: %w", err)}
		}
		body.ExposedPorts = &ports
	}

	if onDelete := optionalStringArg(args, "on_delete"); onDelete != "" {
		disposition := compute.CreateSandboxRequestOnDelete(onDelete)
		body.OnDelete = &disposition
	}

	paused, err := optionalBoolArg(args, "paused")
	if err != nil {
		return nil, &computeArgError{err: err}
	}
	body.Paused = paused

	return c.CreateSandbox(ctx, &compute.CreateSandboxParams{IdempotencyKey: computeIdem(args)}, body)
}

// computeForkRequest builds the fork body shared by the sandbox- and
// snapshot-sourced fork operations.
func computeForkRequest(args map[string]any) (*compute.ForkRequest, error) {
	count := optionalIntArg(args, "count", 0)
	if count < 1 {
		return nil, &computeArgError{err: fmt.Errorf("count is required and must be at least 1")}
	}

	rng := compute.ForkIdentityRng(optionalStringArg(args, "identity_rng"))
	clock := compute.ForkIdentityClock(optionalStringArg(args, "identity_clock"))
	if !rng.Valid() || !clock.Valid() {
		return nil, &computeArgError{err: fmt.Errorf("identity_rng (reseed) and identity_clock (correct|inherit) are required")}
	}

	body := &compute.ForkRequest{
		Count:    count,
		Identity: compute.ForkIdentity{Rng: rng, Clock: clock},
		Ttl:      computeOptStr(args, "ttl"),
		Deadline: computeOptStr(args, "deadline"),
		MinReady: computeOptInt(args, "min_ready"),
	}

	if v := optionalStringArg(args, "flavor"); v != "" {
		flavor := compute.ForkRequestFlavor(v)
		body.Flavor = &flavor
	}

	paused, err := optionalBoolArg(args, "paused")
	if err != nil {
		return nil, &computeArgError{err: err}
	}
	body.Paused = paused

	return body, nil
}

func (s *service) computeExecSandbox(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
	rawCommand, ok := args["command"].([]any)
	if !ok || len(rawCommand) == 0 {
		return nil, &computeArgError{err: fmt.Errorf("command is required and must be a non-empty argument vector")}
	}
	command := make([]string, 0, len(rawCommand))
	for _, item := range rawCommand {
		arg, ok := item.(string)
		if !ok {
			return nil, &computeArgError{err: fmt.Errorf("command entries must be strings")}
		}
		command = append(command, arg)
	}

	return c.ExecSandbox(ctx, id, compute.ExecSandboxJSONRequestBody{
		Command: command,
		Timeout: computeOptStr(args, "timeout"),
	})
}

func (s *service) computeLeaseSandbox(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
	extend, err := requiredStringArg(args, "extend")
	if err != nil {
		return nil, &computeArgError{err: err}
	}

	return c.LeaseSandbox(ctx, id, compute.LeaseSandboxJSONRequestBody{Extend: extend})
}

func (s *service) computePrepareSandboxSSH(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
	publicKey, err := requiredStringArg(args, "public_key")
	if err != nil {
		return nil, &computeArgError{err: err}
	}

	return c.PrepareSandboxSSH(ctx, id, compute.PrepareSandboxSSHJSONRequestBody{PublicKey: publicKey})
}

func (s *service) computePromoteImage(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
	name, err := requiredStringArg(args, "name")
	if err != nil {
		return nil, &computeArgError{err: err}
	}

	replace, err := optionalBoolArg(args, "replace")
	if err != nil {
		return nil, &computeArgError{err: err}
	}

	return c.PromoteImage(ctx, id,
		compute.PromoteImageJSONRequestBody{
			Name:        name,
			Version:     computeOptStr(args, "version"),
			Replace:     replace,
			Description: computeOptStr(args, "description"),
			Tags:        computeOptStringSlice(args, "tags"),
		},
		computeIdemHeaderEditor(args),
	)
}

func (s *service) computeExposePort(ctx context.Context, c *compute.Client, id string, args map[string]any) (*http.Response, error) {
	port := optionalIntArg(args, "port", 0)
	if port < 1 {
		return nil, &computeArgError{err: fmt.Errorf("port is required and must be at least 1")}
	}

	managed, err := optionalBoolArg(args, "managed")
	if err != nil {
		return nil, &computeArgError{err: err}
	}

	return c.ExposePort(ctx, id,
		compute.ExposePortJSONRequestBody{
			Port:     port,
			Name:     computeOptStr(args, "name"),
			Protocol: computeOptStr(args, "protocol"),
			Service:  computeOptStr(args, "service"),
			Managed:  managed,
		},
		computeIdemHeaderEditor(args),
	)
}

// computeIdemHeaderEditor forwards the caller's idempotency key on operations
// whose generated client no longer models the header as a parameter.
func computeIdemHeaderEditor(args map[string]any) compute.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		if key := optionalStringArg(args, "idempotency_key"); key != "" {
			req.Header.Set("Idempotency-Key", key)
		}

		return nil
	}
}

func (s *service) computeAddSSHKey(ctx context.Context, c *compute.Client, args map[string]any) (*http.Response, error) {
	publicKey, err := requiredStringArg(args, "public_key")
	if err != nil {
		return nil, &computeArgError{err: err}
	}

	return c.AddSSHPublicKey(ctx, compute.AddSSHPublicKeyJSONRequestBody{
		PublicKey: publicKey,
		Name:      computeOptStr(args, "name"),
	})
}

// computeClientFor decodes the operation request, resolves the compute
// datasource, and builds a proxy-backed typed client. It writes the
// appropriate error and returns ok=false on failure.
func (s *service) computeClientFor(w http.ResponseWriter, r *http.Request) (*compute.Client, map[string]any, bool) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return nil, nil, false
	}

	datasource, status, err := s.computeDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())

		return nil, nil, false
	}

	client, err := compute.NewClient(computeBaseURL, compute.WithHTTPClient(&computeProxyDoer{svc: s, datasource: datasource}))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("building compute client: %v", err))

		return nil, nil, false
	}

	return client, req.Args, true
}

// writeComputeResult forwards an upstream compute response to the caller,
// translating transport, argument, and non-2xx errors into API errors.
func (s *service) writeComputeResult(w http.ResponseWriter, resp *http.Response, err error) {
	if err != nil {
		var argErr *computeArgError
		if errors.As(err, &argErr) {
			writeAPIError(w, http.StatusBadRequest, argErr.err.Error())

			return
		}

		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("compute request failed: %v", err))

		return
	}

	if resp == nil {
		writeAPIError(w, http.StatusBadGateway, "compute returned no response")

		return
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("reading compute response: %v", err))

		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeAPIError(w, resp.StatusCode, strings.TrimSpace(string(body)))

		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	// Preserve the upstream 2xx status so callers can tell apart created (201),
	// accepted (202), and no-content (204) results.
	writePassthroughResponse(w, resp.StatusCode, contentType, body)
}

func (s *service) computeDatasources() ([]types.DatasourceInfo, error) {
	if s.proxyService == nil {
		return nil, fmt.Errorf("compute is unavailable")
	}

	return s.proxyService.ComputeDatasourceInfo(), nil
}

// computeDatasource resolves the datasource argument: an explicit name when
// given, otherwise the sole configured datasource.
func (s *service) computeDatasource(args map[string]any) (string, int, error) {
	infos, err := s.computeDatasources()
	if err != nil {
		return "", http.StatusServiceUnavailable, err
	}

	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	sort.Strings(names)

	if datasource := optionalStringArg(args, "datasource"); datasource != "" {
		for _, name := range names {
			if name == datasource {
				return datasource, http.StatusOK, nil
			}
		}

		return "", http.StatusNotFound, fmt.Errorf("unknown compute datasource %q. Available: %v", datasource, names)
	}

	switch len(names) {
	case 0:
		return "", http.StatusServiceUnavailable, fmt.Errorf("no compute datasources are available")
	case 1:
		return names[0], http.StatusOK, nil
	default:
		return "", http.StatusBadRequest, fmt.Errorf("datasource is required when multiple compute datasources exist. Available: %v", names)
	}
}

// computeArgError marks an argument-validation failure so the operation layer
// can return 400 rather than 502.
type computeArgError struct {
	err error
}

func (e *computeArgError) Error() string { return e.err.Error() }

func computeLimit(args map[string]any) *compute.Limit {
	if v := optionalIntArg(args, "limit", 0); v > 0 {
		return &v
	}

	return nil
}

func computeOffset(args map[string]any) *compute.Offset {
	if v := optionalIntArg(args, "offset", 0); v > 0 {
		return &v
	}

	return nil
}

func computeCursor(args map[string]any) *compute.Cursor {
	if v := optionalStringArg(args, "cursor"); v != "" {
		return &v
	}

	return nil
}

// computeFilter coerces the repeated filter argument into the generated client's
// query parameter. The compute backend applies the filters before pagination.
func computeFilter(args map[string]any) *compute.Filter {
	raw := optionalSliceArg(args, "filter")
	if len(raw) == 0 {
		return nil
	}

	filters := make(compute.Filter, 0, len(raw))

	for _, item := range raw {
		if value, ok := item.(string); ok && value != "" {
			filters = append(filters, value)
		}
	}

	if len(filters) == 0 {
		return nil
	}

	return &filters
}

func computeIdem(args map[string]any) compute.IdempotencyKey {
	return optionalStringArg(args, "idempotency_key")
}

func computeOptStr(args map[string]any, key string) *string {
	if v := optionalStringArg(args, key); v != "" {
		return &v
	}

	return nil
}

func computeOptStringSlice(args map[string]any, key string) *[]string {
	var raw []any

	switch values := args[key].(type) {
	case []any:
		raw = values
	case []string:
		raw = make([]any, 0, len(values))
		for _, value := range values {
			raw = append(raw, value)
		}
	default:
		return nil
	}

	items := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok && value != "" {
			items = append(items, value)
		}
	}

	if len(items) == 0 {
		return nil
	}

	return &items
}

func computeOptInt(args map[string]any, key string) *int {
	if v := optionalIntArg(args, key, 0); v > 0 {
		return &v
	}

	return nil
}

func computeOptStringMap(args map[string]any, key string) (*map[string]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	entries, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object of string values", key)
	}
	out := make(map[string]string, len(entries))
	for k, v := range entries {
		value, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s must be a string", key, k)
		}
		out[k] = value
	}

	return &out, nil
}

// reencodeJSONArg round-trips a decoded JSON arg into a typed struct so op
// payloads reuse the generated API models without bespoke field mapping.
func reencodeJSONArg(raw any, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}

	return json.Unmarshal(encoded, target)
}
