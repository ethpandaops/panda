package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/ethpandaops/panda/pkg/operations"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

// benchmarkoorAPIPrefix is the benchmarkoor API root behind the proxy's
// /benchmarkoor mount.
const benchmarkoorAPIPrefix = "/benchmarkoor/api/v1"

// benchmarkoorFilterKey matches PostgREST-style filter column names. Filters
// are forwarded loosely (the upstream rejects unknown columns with a clear
// 400) but the key shape is pinned so filters can never smuggle reserved or
// structurally invalid query parameters.
var benchmarkoorFilterKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// benchmarkoorReservedParams are query parameters with dedicated operation
// args; they cannot be set through the filters map.
var benchmarkoorReservedParams = map[string]struct{}{
	"limit":  {},
	"offset": {},
	"select": {},
	"order":  {},
}

func (s *service) handleBenchmarkoorOperation(operationID string, w http.ResponseWriter, r *http.Request) bool {
	switch operationID {
	case "benchmarkoor.list_datasources":
		s.handleBenchmarkoorListDatasources(w)
	case "benchmarkoor.get_index":
		s.handleBenchmarkoorPassthrough(w, r, "/index/", nil)
	case "benchmarkoor.list_runs":
		s.handleBenchmarkoorQuery(w, r, "/index/query/runs", map[string]string{
			"run_id": "run_id", "client": "client", "status": "status", "suite_hash": "suite_hash",
		})
	case "benchmarkoor.get_run":
		s.handleBenchmarkoorGetRun(w, r)
	case "benchmarkoor.list_suites":
		s.handleBenchmarkoorQuery(w, r, "/index/query/suites", map[string]string{
			"suite_hash": "suite_hash",
		})
	case "benchmarkoor.get_suite_stats":
		s.handleBenchmarkoorSuiteStats(w, r)
	case "benchmarkoor.query_test_stats":
		s.handleBenchmarkoorQuery(w, r, "/index/query/test_stats", map[string]string{
			"run_id": "run_id", "client": "client", "test_name": "test_name", "suite_hash": "suite_hash",
		})
	case "benchmarkoor.query_block_logs":
		s.handleBenchmarkoorQuery(w, r, "/index/query/test_stats_block_logs", map[string]string{
			"run_id": "run_id", "client": "client", "test_name": "test_name", "suite_hash": "suite_hash",
		})
	case "benchmarkoor.list_live_runs":
		s.handleBenchmarkoorPassthrough(w, r, "/index/live_runs", nil)
	case "benchmarkoor.get_file":
		s.handleBenchmarkoorGetFile(w, r)
	case "benchmarkoor.link_run":
		s.handleBenchmarkoorLink(w, r, "run_id", "/runs/%s")
	case "benchmarkoor.link_suite":
		s.handleBenchmarkoorLink(w, r, "suite_hash", "/suites/%s")
	default:
		return false
	}

	return true
}

func (s *service) handleBenchmarkoorListDatasources(w http.ResponseWriter) {
	infos, err := s.benchmarkoorDatasources()
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
			Type:        "benchmarkoor",
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

// handleBenchmarkoorPassthrough forwards a GET to the benchmarkoor API with
// the given pre-built query params.
func (s *service) handleBenchmarkoorPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	apiPath string,
	params url.Values,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, status, err := s.benchmarkoorDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	s.proxyPassthroughGet(w, r, "benchmarkoor", benchmarkoorAPIPrefix+apiPath, params, datasource)
}

// handleBenchmarkoorQuery forwards a PostgREST-style query endpoint request:
// limit/offset/select/order plus eq-shorthand args and a raw filters map.
func (s *service) handleBenchmarkoorQuery(
	w http.ResponseWriter,
	r *http.Request,
	apiPath string,
	eqArgs map[string]string,
) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, status, err := s.benchmarkoorDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	params, err := benchmarkoorQueryParams(req.Args, eqArgs)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.proxyPassthroughGet(w, r, "benchmarkoor", benchmarkoorAPIPrefix+apiPath, params, datasource)
}

// handleBenchmarkoorGetRun fetches a single indexed run by run_id and unwraps
// the query envelope to the run object.
func (s *service) handleBenchmarkoorGetRun(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, status, err := s.benchmarkoorDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	runID, err := requiredStringArg(req.Args, "run_id")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params := url.Values{}
	params.Set("run_id", "eq."+runID)
	params.Set("limit", "1")

	body, status, _, err := s.proxyDatasourceRequest(
		r.Context(),
		"benchmarkoor",
		datasource,
		http.MethodGet,
		benchmarkoorAPIPrefix+"/index/query/runs?"+params.Encode(),
		nil,
		http.Header{handlers.DatasourceHeader: []string{datasource}},
	)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	if status < 200 || status >= 300 {
		writeAPIError(w, status, strings.TrimSpace(string(body)))
		return
	}

	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}

	if err := json.Unmarshal(body, &envelope); err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("decoding benchmarkoor response: %v", err))
		return
	}

	if len(envelope.Data) == 0 {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("run %q not found", runID))
		return
	}

	writePassthroughResponse(w, http.StatusOK, "application/json", envelope.Data[0])
}

func (s *service) handleBenchmarkoorSuiteStats(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, status, err := s.benchmarkoorDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	suiteHash, err := requiredStringArg(req.Args, "suite_hash")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	params := url.Values{}
	// max_runs_per_client is implemented upstream but missing from the
	// benchmarkoor OpenAPI spec; the e2e harness pins it instead of the
	// contract test.
	if maxRuns := optionalIntArg(req.Args, "max_runs_per_client", 0); maxRuns > 0 {
		params.Set("max_runs_per_client", fmt.Sprintf("%d", maxRuns))
	}

	apiPath := fmt.Sprintf("/index/suites/%s/stats", url.PathEscape(suiteHash))
	s.proxyPassthroughGet(w, r, "benchmarkoor", benchmarkoorAPIPrefix+apiPath, params, datasource)
}

// handleBenchmarkoorGetFile streams a stored result file. Paths are relative
// to a discovery path (e.g. "<discovery_path>/runs/<run_id>/result.json").
func (s *service) handleBenchmarkoorGetFile(w http.ResponseWriter, r *http.Request) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, status, err := s.benchmarkoorDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	filePath, err := requiredStringArg(req.Args, "path")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	filePath = strings.TrimPrefix(filePath, "/")
	if filePath == "" || strings.Contains(filePath, "..") {
		writeAPIError(w, http.StatusBadRequest, "path must be a relative storage path without '..'")
		return
	}

	segments := strings.Split(filePath, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	s.proxyPassthroughGet(w, r, "benchmarkoor", benchmarkoorAPIPrefix+"/files/"+strings.Join(segments, "/"), nil, datasource)
}

// handleBenchmarkoorLink builds a web UI deep link from the datasource's
// ui_url metadata.
func (s *service) handleBenchmarkoorLink(w http.ResponseWriter, r *http.Request, idArg, pathTemplate string) {
	req, err := decodeOperationRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	datasource, status, err := s.benchmarkoorDatasource(req.Args)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}

	id, err := requiredStringArg(req.Args, idArg)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	info, ok := s.benchmarkoorDatasourceInfo(datasource)
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("benchmarkoor datasource %q not found", datasource))
		return
	}

	uiURL := strings.TrimRight(info.Metadata["ui_url"], "/")
	if uiURL == "" {
		writeAPIError(w, http.StatusNotFound,
			fmt.Sprintf("benchmarkoor datasource %q has no ui_url configured", datasource))
		return
	}

	writeOperationResponse(s.log, w, http.StatusOK, operations.Response{
		Kind: operations.ResultKindObject,
		Data: map[string]any{"url": uiURL + fmt.Sprintf(pathTemplate, url.PathEscape(id))},
		Meta: map[string]any{"datasource": datasource},
	})
}

// benchmarkoorQueryParams builds PostgREST-style query parameters from
// operation args: limit/offset/select/order, eq-shorthand args (client ->
// client=eq.<value>), and a raw filters map of column -> "operator.value".
func benchmarkoorQueryParams(args map[string]any, eqArgs map[string]string) (url.Values, error) {
	params := url.Values{}

	if limit := optionalIntArg(args, "limit", 0); limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}

	if offset := optionalIntArg(args, "offset", 0); offset > 0 {
		params.Set("offset", fmt.Sprintf("%d", offset))
	}

	for _, key := range []string{"select", "order"} {
		if value := optionalStringArg(args, key); value != "" {
			params.Set(key, value)
		}
	}

	for argName, column := range eqArgs {
		if value := optionalStringArg(args, argName); value != "" {
			params.Set(column, "eq."+value)
		}
	}

	for column, raw := range optionalMapArg(args, "filters") {
		if !benchmarkoorFilterKey.MatchString(column) {
			return nil, fmt.Errorf("invalid filter column %q", column)
		}

		if _, reserved := benchmarkoorReservedParams[column]; reserved {
			return nil, fmt.Errorf("filter column %q collides with a reserved parameter; use the %s argument", column, column)
		}

		value, ok := raw.(string)
		if !ok || value == "" {
			return nil, fmt.Errorf("filter %q must be an 'operator.value' string (e.g. \"eq.geth\")", column)
		}

		params.Set(column, value)
	}

	return params, nil
}

// benchmarkoorDatasources returns the discovered benchmarkoor datasources.
func (s *service) benchmarkoorDatasources() ([]benchmarkoorInfo, error) {
	if s.proxyService == nil {
		return nil, fmt.Errorf("benchmarkoor is unavailable")
	}

	infos := s.proxyService.BenchmarkoorDatasourceInfo()

	result := make([]benchmarkoorInfo, 0, len(infos))
	for _, info := range infos {
		result = append(result, benchmarkoorInfo{
			Name:        info.Name,
			Description: info.Description,
			Metadata:    info.Metadata,
		})
	}

	return result, nil
}

type benchmarkoorInfo struct {
	Name        string
	Description string
	Metadata    map[string]string
}

func (s *service) benchmarkoorDatasourceInfo(name string) (benchmarkoorInfo, bool) {
	infos, err := s.benchmarkoorDatasources()
	if err != nil {
		return benchmarkoorInfo{}, false
	}

	for _, info := range infos {
		if info.Name == name {
			return info, true
		}
	}

	return benchmarkoorInfo{}, false
}

// benchmarkoorDatasource resolves the datasource argument: explicit name when
// given, otherwise the sole configured datasource.
func (s *service) benchmarkoorDatasource(args map[string]any) (string, int, error) {
	infos, err := s.benchmarkoorDatasources()
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

		return "", http.StatusNotFound, fmt.Errorf("unknown benchmarkoor datasource %q. Available: %v", datasource, names)
	}

	switch len(names) {
	case 0:
		return "", http.StatusServiceUnavailable, fmt.Errorf("no benchmarkoor datasources are available")
	case 1:
		return names[0], http.StatusOK, nil
	default:
		return "", http.StatusBadRequest, fmt.Errorf("datasource is required when multiple benchmarkoor datasources exist. Available: %v", names)
	}
}
