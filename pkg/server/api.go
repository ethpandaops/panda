package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ethpandaops/mcp/pkg/auth"
	"github.com/ethpandaops/mcp/pkg/execsvc"
	"github.com/ethpandaops/mcp/pkg/extension"
	"github.com/ethpandaops/mcp/pkg/serverapi"
	"github.com/ethpandaops/mcp/pkg/types"
)

func (s *service) mountAPIRoutes(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/datasources", s.handleAPIDatasources)
		r.Get("/search/examples", s.handleAPISearchExamples)
		r.Get("/search/runbooks", s.handleAPISearchRunbooks)
		r.Post("/execute", s.handleAPIExecute)
		r.Get("/sessions", s.handleAPIListSessions)
		r.Post("/sessions", s.handleAPICreateSession)
		r.Delete("/sessions/{sessionID}", s.handleAPIDestroySession)
		r.Get("/resources/read", s.handleAPIReadResource)
		r.HandleFunc("/operations/{operationID}", s.handleAPIOperation)
	})
}

func (s *service) handleAPIDatasources(w http.ResponseWriter, r *http.Request) {
	if s.proxyService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "proxy service is unavailable")
		return
	}

	filterType := strings.TrimSpace(r.URL.Query().Get("type"))
	all := make([]types.DatasourceInfo, 0)
	all = append(all, s.proxyService.ClickHouseDatasourceInfo()...)
	all = append(all, s.proxyService.PrometheusDatasourceInfo()...)
	all = append(all, s.proxyService.LokiDatasourceInfo()...)

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

func (s *service) handleAPIReadResource(w http.ResponseWriter, r *http.Request) {
	uri := strings.TrimSpace(r.URL.Query().Get("uri"))
	if uri == "" {
		writeAPIError(w, http.StatusBadRequest, "uri is required")
		return
	}

	content, mimeType, err := s.resourceRegistry.Read(r.Context(), uri)
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

	if extensionName := operationExtensionName(operationID); extensionName != "" &&
		s.extensionRegistry != nil &&
		s.extensionRegistry.Get(extensionName) != nil {
		ext := s.extensionRegistry.Get(extensionName)
		if enabledAware, ok := ext.(extension.EnabledAware); ok && !enabledAware.Enabled() {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("extension %q is not enabled", extensionName))
			return
		}

		if !s.extensionRegistry.IsInitialized(extensionName) {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("extension %q is not enabled", extensionName))
			return
		}
	}

	targetURL := strings.TrimRight(s.proxyService.URL(), "/") + "/api/v1/operations/" + operationID
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, fmt.Sprintf("creating proxy request: %v", err))
		return
	}

	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	tokenID := fmt.Sprintf("server-api-%d", time.Now().UnixNano())
	token := s.proxyService.RegisterToken(tokenID)
	defer s.proxyService.RevokeToken(tokenID)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("proxy request failed: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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

func copyHeaders(dst, src http.Header) {
	for key := range dst {
		dst.Del(key)
	}

	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func operationExtensionName(operationID string) string {
	prefix, _, ok := strings.Cut(operationID, ".")
	if !ok {
		return ""
	}

	return strings.TrimSpace(prefix)
}
