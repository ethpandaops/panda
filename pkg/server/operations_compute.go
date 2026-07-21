package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ethpandaops/panda/pkg/compute"
	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

const (
	// computeSpecTTL bounds how long a fetched interface document is reused
	// before it is refreshed from the service.
	computeSpecTTL = 5 * time.Minute
	// computeSpecRetryBackoff floors refetches triggered by unknown-operation
	// lookups so bad operation names cannot hammer the upstream.
	computeSpecRetryBackoff = 30 * time.Second
	// computeSpecFetchTimeout bounds a single interface-document fetch.
	computeSpecFetchTimeout = 15 * time.Second
)

// computeSpecEntry is one cached, parsed interface document.
type computeSpecEntry struct {
	index     *compute.Index
	fetchedAt time.Time
}

// computeArgAdapters keeps legacy flat argument shapes working by rewriting
// them into the wire shape before generic dispatch.
var computeArgAdapters = map[string]func(map[string]any) error{
	"create_sandbox": adaptComputeCreateSandbox,
	"fork_sandbox":   adaptComputeFork,
	"fork_image":     adaptComputeFork,
}

// handleComputeOperation dispatches compute.* operations. Aside from the
// locally served list_datasources and list_api_operations, every operation is
// resolved against the service's own interface document at runtime, so new
// upstream operations work without a panda change.
func (s *service) handleComputeOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	name, ok := strings.CutPrefix(operationID, "compute.")
	if !ok {
		return false
	}

	if name == "list_datasources" {
		s.handleComputeListDatasources(w)

		return true
	}

	s.handleComputeAPIOperation(name, w, r)

	return true
}

func (s *service) handleComputeAPIOperation(name string, w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	datasource, status, err := s.computeDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())

		return
	}

	index, err := s.computeIndex(r.Context(), datasource, false)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())

		return
	}

	if name == "list_api_operations" {
		s.writeComputeAPIOperations(w, index)

		return
	}

	op, ok := index.Lookup(name)
	if !ok {
		// The operation may have shipped upstream after the cached fetch.
		if refreshed, refreshErr := s.computeIndex(r.Context(), datasource, true); refreshErr == nil {
			index = refreshed
			op, ok = index.Lookup(name)
		}
	}

	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf(
			"unknown compute operation %q. Available: %s", name, strings.Join(index.Names(), ", ")))

		return
	}

	args := maps.Clone(req.Args)

	if adapter := computeArgAdapters[op.Name]; adapter != nil {
		if err := adapter(args); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	proxied, err := op.BuildRequest(args)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())

		return
	}

	headers := proxied.Header
	headers.Set(handlers.DatasourceHeader, datasource)

	var body io.Reader

	if proxied.Body != nil {
		headers.Set("Content-Type", "application/json")

		body = bytes.NewReader(proxied.Body)
	}

	// The proxy strips the /compute mount before forwarding, so the backend
	// sees the original /v1/... path.
	respBody, respStatus, respHeaders, err := s.proxyDatasourceRequest(
		r.Context(), "compute", datasource, proxied.Method, "/compute"+proxied.Path, body, headers)

	s.writeComputeResult(w, respBody, respStatus, respHeaders, err)
}

// computeIndex returns the cached operation index for a datasource, fetching
// the interface document through the proxy when the cache is cold or stale.
// A failed refresh serves the stale index rather than failing the operation.
// With forceRefresh, the TTL is bypassed but refetches are still rate-limited
// by computeSpecRetryBackoff.
func (s *service) computeIndex(ctx context.Context, datasource string, forceRefresh bool) (*compute.Index, error) {
	// The lock is held across the fetch so concurrent cold starts fetch once.
	s.computeSpecMu.Lock()
	defer s.computeSpecMu.Unlock()

	entry, cached := s.computeSpecs[datasource]

	maxAge := computeSpecTTL
	if forceRefresh {
		maxAge = computeSpecRetryBackoff
	}

	if cached && time.Since(entry.fetchedAt) < maxAge {
		return entry.index, nil
	}

	index, err := s.fetchComputeIndex(ctx, datasource)
	if err != nil {
		if cached {
			s.log.WithError(err).WithField("datasource", datasource).
				Warn("Compute interface refresh failed; serving cached interface")

			return entry.index, nil
		}

		return nil, err
	}

	if s.computeSpecs == nil {
		s.computeSpecs = make(map[string]computeSpecEntry)
	}

	s.computeSpecs[datasource] = computeSpecEntry{index: index, fetchedAt: time.Now()}

	return index, nil
}

func (s *service) fetchComputeIndex(ctx context.Context, datasource string) (*compute.Index, error) {
	ctx, cancel := context.WithTimeout(ctx, computeSpecFetchTimeout)
	defer cancel()

	body, status, _, err := s.proxyDatasourceRequest(
		ctx, "compute", datasource, http.MethodGet, "/compute"+compute.SpecPath, nil,
		http.Header{handlers.DatasourceHeader: []string{datasource}},
	)
	if err != nil {
		return nil, fmt.Errorf("fetching compute interface: %w", err)
	}

	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("fetching compute interface: upstream returned status %d", status)
	}

	index, err := compute.ParseSpec(body)
	if err != nil {
		return nil, err
	}

	return index, nil
}

// writeComputeAPIOperations serves the discovered operation catalog. Only
// structural fields are exposed; the interface document's free text never
// reaches callers.
func (s *service) writeComputeAPIOperations(w http.ResponseWriter, index *compute.Index) {
	ops := index.Operations()

	items := make([]map[string]any, 0, len(ops))

	for _, op := range ops {
		item := map[string]any{
			"operation": op.Name,
			"method":    op.Method,
			"path":      op.Path,
		}

		if len(op.PathParams) > 0 {
			item["path_args"] = op.PathParams
		}

		if len(op.QueryParams) > 0 {
			item["query_args"] = op.QueryParams
		}

		if op.HasBody {
			item["body"] = true

			if len(op.RequiredBody) > 0 {
				item["required"] = op.RequiredBody
			}
		}

		items = append(items, item)
	}

	payload, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("encoding operations: %v", err))

		return
	}

	writePassthroughResponse(w, http.StatusOK, "application/json", payload)
}

// adaptComputeCreateSandbox keeps the legacy flat create arguments working:
// snapshot_id and flavor become the snapshot boot source the API expects.
func adaptComputeCreateSandbox(args map[string]any) error {
	if _, ok := args["source"]; ok {
		return nil
	}

	template := optionalStringArg(args, "template")
	snapshotID := optionalStringArg(args, "snapshot_id")

	if (template == "") == (snapshotID == "") {
		return fmt.Errorf("exactly one of template or snapshot_id is required")
	}

	if template != "" {
		delete(args, "flavor")

		return nil
	}

	source := map[string]any{"kind": "snapshot", "snapshot_id": snapshotID}
	if flavor := optionalStringArg(args, "flavor"); flavor != "" {
		source["flavor"] = flavor
	}

	delete(args, "snapshot_id")
	delete(args, "flavor")
	args["source"] = source

	return nil
}

// adaptComputeFork nests the legacy flat identity arguments into the identity
// object the API expects.
func adaptComputeFork(args map[string]any) error {
	if _, ok := args["identity"]; ok {
		return nil
	}

	rng := optionalStringArg(args, "identity_rng")
	clock := optionalStringArg(args, "identity_clock")

	if rng == "" || clock == "" {
		return fmt.Errorf("identity_rng and identity_clock are required")
	}

	delete(args, "identity_rng")
	delete(args, "identity_clock")
	args["identity"] = map[string]any{"rng": rng, "clock": clock}

	return nil
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

// writeComputeResult forwards an upstream compute response to the caller,
// translating transport errors and non-2xx statuses into API errors.
func (s *service) writeComputeResult(w http.ResponseWriter, body []byte, status int, headers http.Header, err error) {
	if err != nil {
		var argErr *compute.ArgError
		if errors.As(err, &argErr) {
			writeAPIError(w, http.StatusBadRequest, argErr.Error())

			return
		}

		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("compute request failed: %v", err))

		return
	}

	if status < 200 || status >= 300 {
		writeAPIError(w, status, strings.TrimSpace(string(body)))

		return
	}

	contentType := ""
	if headers != nil {
		contentType = headers.Get("Content-Type")
	}

	if contentType == "" {
		contentType = "application/json"
	}

	// Preserve the upstream 2xx status so callers can tell apart created (201),
	// accepted (202), and no-content (204) results.
	writePassthroughResponse(w, status, contentType, body)
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
