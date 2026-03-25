package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/execsvc"
	"github.com/ethpandaops/panda/pkg/module"
	"github.com/ethpandaops/panda/pkg/serverapi"
	"github.com/ethpandaops/panda/pkg/types"
)

func (s *service) mountAPIRoutes(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/datasources", s.handleAPIDatasources)
		r.Get("/proxy/auth", s.handleAPIProxyAuthMetadata)
		r.Get("/search/examples", s.handleAPISearchExamples)
		r.Get("/search/runbooks", s.handleAPISearchRunbooks)
		r.Get("/search/eips", s.handleAPISearchEIPs)
		r.Get("/search/consensus-specs", s.handleAPISearchSpecs)
		r.Post("/execute", s.handleAPIExecute)
		r.Get("/sessions", s.handleAPIListSessions)
		r.Post("/sessions", s.handleAPICreateSession)
		r.Delete("/sessions/{sessionID}", s.handleAPIDestroySession)
		r.Get("/resources", s.handleAPIListResources)
		r.Get("/resources/read", s.handleAPIReadResource)
		r.HandleFunc("/operations/{operationID}", s.handleAPIOperation)

		// Public file serving (no auth — same as MinIO anonymous download).
		r.Get("/storage/files/*", s.handleStorageServeFile)

		r.Route("/runtime", func(r chi.Router) {
			r.Use(s.runtimeAuthMiddleware)
			r.HandleFunc("/operations/{operationID}", s.handleAPIOperation)
			r.Post("/storage/upload", s.handleRuntimeStorageUpload)
			r.Get("/storage/files", s.handleRuntimeStorageList)
			r.Get("/storage/url", s.handleRuntimeStorageURL)
		})
	})
}

type runtimeContextKey string

const runtimeExecutionIDKey runtimeContextKey = "runtime_execution_id"

func (s *service) runtimeAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.runtimeTokens == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "runtime token service is unavailable")
			return
		}

		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeAPIError(w, http.StatusUnauthorized, "missing runtime Authorization header")
			return
		}

		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		executionID := s.runtimeTokens.Validate(token)
		if executionID == "" {
			writeAPIError(w, http.StatusUnauthorized, "invalid or expired runtime token")
			return
		}

		ctx := context.WithValue(r.Context(), runtimeExecutionIDKey, executionID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *service) handleAPIProxyAuthMetadata(w http.ResponseWriter, _ *http.Request) {
	if s.proxyAuthMetadata == nil {
		writeJSON(w, http.StatusOK, serverapi.ProxyAuthMetadataResponse{})
		return
	}

	writeJSON(w, http.StatusOK, s.proxyAuthMetadata)
}

func (s *service) handleAPIDatasources(w http.ResponseWriter, r *http.Request) {
	if s.moduleRegistry == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "module registry is unavailable")
		return
	}

	filterType := strings.TrimSpace(r.URL.Query().Get("type"))
	all := s.moduleRegistry.DatasourceInfo()

	if filterType != "" {
		filtered := make([]types.DatasourceInfo, 0, len(all))
		for _, info := range all {
			if info.Type == filterType {
				filtered = append(filtered, info)
			}
		}

		all = filtered
	}

	writeJSON(w, http.StatusOK, serverapi.DatasourcesResponse{Datasources: all})
}

func (s *service) handleAPISearchExamples(w http.ResponseWriter, r *http.Request) {
	if s.searchService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "search service is unavailable")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		writeAPIError(w, http.StatusBadRequest, "query is required")
		return
	}

	limit, err := parseOptionalInt(r, "limit")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := s.searchService.SearchExamples(query, r.URL.Query().Get("category"), limit)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *service) handleAPISearchRunbooks(w http.ResponseWriter, r *http.Request) {
	if s.searchService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "search service is unavailable")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		writeAPIError(w, http.StatusBadRequest, "query is required")
		return
	}

	limit, err := parseOptionalInt(r, "limit")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := s.searchService.SearchRunbooks(query, r.URL.Query().Get("tag"), limit)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *service) handleAPISearchEIPs(w http.ResponseWriter, r *http.Request) {
	if s.searchService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "search service is unavailable")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		writeAPIError(w, http.StatusBadRequest, "query is required")
		return
	}

	limit, err := parseOptionalInt(r, "limit")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	q := r.URL.Query()

	resp, err := s.searchService.SearchEIPs(query, q.Get("status"), q.Get("category"), q.Get("type"), limit)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *service) handleAPISearchSpecs(w http.ResponseWriter, r *http.Request) {
	if s.searchService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "search service is unavailable")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		writeAPIError(w, http.StatusBadRequest, "query is required")
		return
	}

	limit, err := parseOptionalInt(r, "limit")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp, err := s.searchService.SearchSpecs(query, r.URL.Query().Get("fork"), limit)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *service) handleAPIExecute(w http.ResponseWriter, r *http.Request) {
	if s.execService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "execute service is unavailable")
		return
	}

	var req serverapi.ExecuteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	ownerID := authOwnerID(r)
	result, err := s.execService.Execute(r.Context(), execsvc.ExecuteRequest{
		Code:      req.Code,
		Timeout:   req.Timeout,
		SessionID: req.SessionID,
		OwnerID:   ownerID,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := serverapi.ExecuteResponse{
		Stdout:          result.Stdout,
		Stderr:          result.Stderr,
		ExitCode:        result.ExitCode,
		ExecutionID:     result.ExecutionID,
		OutputFiles:     result.OutputFiles,
		Metrics:         result.Metrics,
		DurationSeconds: result.DurationSeconds,
		SessionID:       result.SessionID,
		SessionFiles:    result.SessionFiles,
	}
	if result.SessionTTLRemaining > 0 {
		resp.SessionTTLRemaining = result.SessionTTLRemaining.Round(time.Second).String()
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *service) handleAPIListSessions(w http.ResponseWriter, r *http.Request) {
	if s.execService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "execute service is unavailable")
		return
	}

	if !s.execService.SessionsEnabled() {
		writeAPIError(w, http.StatusBadRequest, "sessions are disabled")
		return
	}

	sessions, maxSessions, err := s.execService.ListSessions(r.Context(), authOwnerID(r))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := serverapi.ListSessionsResponse{
		Sessions:    make([]serverapi.SessionResponse, 0, len(sessions)),
		Total:       len(sessions),
		MaxSessions: maxSessions,
	}
	for _, session := range sessions {
		resp.Sessions = append(resp.Sessions, serverapi.SessionResponse{
			SessionID:      session.ID,
			CreatedAt:      session.CreatedAt,
			LastUsed:       session.LastUsed,
			TTLRemaining:   session.TTLRemaining.Round(time.Second).String(),
			WorkspaceFiles: session.WorkspaceFiles,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *service) handleAPICreateSession(w http.ResponseWriter, r *http.Request) {
	if s.execService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "execute service is unavailable")
		return
	}

	if !s.execService.SessionsEnabled() {
		writeAPIError(w, http.StatusBadRequest, "sessions are disabled")
		return
	}

	ownerID := authOwnerID(r)
	sessionID, err := s.execService.CreateSession(r.Context(), ownerID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := serverapi.CreateSessionResponse{SessionID: sessionID}
	if sessions, _, err := s.execService.ListSessions(r.Context(), ownerID); err == nil {
		for _, session := range sessions {
			if session.ID == sessionID {
				resp.TTLRemaining = session.TTLRemaining.Round(time.Second).String()
				break
			}
		}
	}

	writeJSON(w, http.StatusCreated, resp)
}

func (s *service) handleAPIDestroySession(w http.ResponseWriter, r *http.Request) {
	if s.execService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "execute service is unavailable")
		return
	}

	sessionID := chi.URLParam(r, "sessionID")
	if strings.TrimSpace(sessionID) == "" {
		writeAPIError(w, http.StatusBadRequest, "sessionID is required")
		return
	}

	if err := s.execService.DestroySession(r.Context(), sessionID, authOwnerID(r)); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *service) handleAPIListResources(w http.ResponseWriter, _ *http.Request) {
	if s.resourceRegistry == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "resource registry is unavailable")
		return
	}

	static := s.resourceRegistry.ListStatic()
	resources := make([]serverapi.ResourceInfo, 0, len(static))

	for _, res := range static {
		resources = append(resources, serverapi.ResourceInfo{
			URI:         res.URI,
			Name:        res.Name,
			Description: res.Description,
			MIMEType:    res.MIMEType,
		})
	}

	templates := s.resourceRegistry.ListTemplates()
	templateInfos := make([]serverapi.ResourceTemplateInfo, 0, len(templates))

	for _, tmpl := range templates {
		uriTemplate := ""
		if tmpl.URITemplate != nil {
			uriTemplate = tmpl.URITemplate.Raw()
		}

		templateInfos = append(templateInfos, serverapi.ResourceTemplateInfo{
			URITemplate: uriTemplate,
			Name:        tmpl.Name,
			Description: tmpl.Description,
			MIMEType:    tmpl.MIMEType,
		})
	}

	writeJSON(w, http.StatusOK, serverapi.ListResourcesResponse{
		Resources: resources,
		Templates: templateInfos,
	})
}

func (s *service) handleAPIReadResource(w http.ResponseWriter, r *http.Request) {
	uri := strings.TrimSpace(r.URL.Query().Get("uri"))
	if uri == "" {
		writeAPIError(w, http.StatusBadRequest, "uri is required")
		return
	}

	ctx := r.Context()
	if cc := strings.TrimSpace(r.URL.Query().Get("client_context")); cc == types.ClientContextCLIParam {
		ctx = types.WithClientContext(ctx, types.ClientContextCLI)
	}

	content, mimeType, err := s.resourceRegistry.Read(ctx, uri)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	if mimeType == "" {
		mimeType = "text/plain; charset=utf-8"
	}

	w.Header().Set("Content-Type", mimeType)
	_, _ = io.WriteString(w, content)
}

func (s *service) handleAPIOperation(w http.ResponseWriter, r *http.Request) {
	if s.proxyService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "proxy service is unavailable")
		return
	}

	operationID := chi.URLParam(r, "operationID")
	if strings.TrimSpace(operationID) == "" {
		writeAPIError(w, http.StatusBadRequest, "operationID is required")
		return
	}

	if moduleName := operationExtensionName(operationID); moduleName != "" &&
		s.moduleRegistry != nil &&
		s.moduleRegistry.Get(moduleName) != nil {
		ext := s.moduleRegistry.Get(moduleName)
		if enabledAware, ok := ext.(module.EnabledAware); ok && !enabledAware.Enabled() {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("module %q is not enabled", moduleName))
			return
		}

		if !s.moduleRegistry.IsInitialized(moduleName) {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("module %q is not enabled", moduleName))
			return
		}
	}

	if !s.dispatchOperation(operationID, w, r) {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("unknown operation %q", operationID))
		return
	}
}

func (s *service) handleRuntimeStorageUpload(w http.ResponseWriter, r *http.Request) {
	if s.storageService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "storage is unavailable")
		return
	}

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	name = strings.TrimLeft(name, "/")
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "name is required")
		return
	}

	executionID := runtimeExecutionID(r.Context())
	if executionID == "" {
		writeAPIError(w, http.StatusUnauthorized, "runtime execution ID is missing")
		return
	}

	relativeKey, url, err := s.storageService.Upload(executionID, name, r.Body)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("upload failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, serverapi.RuntimeStorageUploadResponse{
		Key: relativeKey,
		URL: url,
	})
}

func (s *service) handleRuntimeStorageList(w http.ResponseWriter, r *http.Request) {
	if s.storageService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "storage is unavailable")
		return
	}

	executionID := runtimeExecutionID(r.Context())
	if executionID == "" {
		writeAPIError(w, http.StatusUnauthorized, "runtime execution ID is missing")
		return
	}

	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))

	storageFiles, err := s.storageService.List(executionID, prefix)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("listing files failed: %v", err))
		return
	}

	files := make([]serverapi.RuntimeStorageFile, 0, len(storageFiles))
	for _, f := range storageFiles {
		files = append(files, serverapi.RuntimeStorageFile{
			Key:          f.Key,
			Size:         f.Size,
			LastModified: f.LastModified,
			URL:          f.URL,
		})
	}

	writeJSON(w, http.StatusOK, serverapi.RuntimeStorageListResponse{Files: files})
}

func (s *service) handleRuntimeStorageURL(w http.ResponseWriter, r *http.Request) {
	if s.storageService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "storage is unavailable")
		return
	}

	executionID := runtimeExecutionID(r.Context())
	if executionID == "" {
		writeAPIError(w, http.StatusUnauthorized, "runtime execution ID is missing")
		return
	}

	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "key is required")
		return
	}

	writeJSON(w, http.StatusOK, serverapi.RuntimeStorageURLResponse{
		Key: key,
		URL: s.storageService.GetURL(executionID, key),
	})
}

func (s *service) handleStorageServeFile(w http.ResponseWriter, r *http.Request) {
	if s.storageService == nil {
		http.NotFound(w, r)
		return
	}

	filePath := strings.TrimPrefix(r.URL.Path, "/api/v1/storage/files/")
	if filePath == "" {
		http.NotFound(w, r)
		return
	}

	s.storageService.ServeFile(w, r, filePath)
}

func (s *service) proxyRequest(
	ctx context.Context,
	method string,
	requestPath string,
	body io.Reader,
	headers http.Header,
) ([]byte, int, http.Header, error) {
	if s.proxyService == nil {
		return nil, http.StatusServiceUnavailable, nil, fmt.Errorf("proxy service is unavailable")
	}

	targetURL := strings.TrimRight(s.proxyService.URL(), "/") + requestPath
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, fmt.Errorf("creating proxy request: %w", err)
	}

	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Del("Authorization")

	tokenID := fmt.Sprintf("server-api-%d", time.Now().UnixNano())
	token := s.proxyService.RegisterToken(tokenID)
	defer s.proxyService.RevokeToken(tokenID)

	if token != "" && token != "none" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header.Clone(), fmt.Errorf("reading proxy response: %w", err)
	}

	return data, resp.StatusCode, resp.Header.Clone(), nil
}

func runtimeExecutionID(ctx context.Context) string {
	value, _ := ctx.Value(runtimeExecutionIDKey).(string)
	return value
}

func authOwnerID(r *http.Request) string {
	user := auth.GetAuthUser(r.Context())
	if user == nil {
		return ""
	}

	return fmt.Sprintf("%d", user.GitHubID)
}

func parseOptionalInt(r *http.Request, key string) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return 0, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}

	return parsed, nil
}

func decodeJSON(r *http.Request, dst any) error {
	defer func() { _ = r.Body.Close() }()

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decoding request body: %w", err)
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func operationExtensionName(operationID string) string {
	prefix, _, ok := strings.Cut(operationID, ".")
	if !ok {
		return ""
	}

	return strings.TrimSpace(prefix)
}
