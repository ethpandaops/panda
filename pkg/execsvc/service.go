package execsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/mcp/pkg/config"
	"github.com/ethpandaops/mcp/pkg/plugin"
	"github.com/ethpandaops/mcp/pkg/proxy"
	"github.com/ethpandaops/mcp/pkg/sandbox"
	"github.com/ethpandaops/mcp/pkg/types"
)

const (
	MinTimeout = 1
	MaxTimeout = 600
)

type ExecuteRequest struct {
	Code      string
	Timeout   int
	SessionID string
	OwnerID   string
}

type Service struct {
	log        logrus.FieldLogger
	sandboxSvc sandbox.Service
	cfg        *config.Config
	pluginReg  *plugin.Registry
	proxySvc   proxy.Service
}

func New(
	log logrus.FieldLogger,
	sandboxSvc sandbox.Service,
	cfg *config.Config,
	pluginReg *plugin.Registry,
	proxySvc proxy.Service,
) *Service {
	return &Service{
		log:        log.WithField("component", "exec-service"),
		sandboxSvc: sandboxSvc,
		cfg:        cfg,
		pluginReg:  pluginReg,
		proxySvc:   proxySvc,
	}
}

func (s *Service) Execute(ctx context.Context, req ExecuteRequest) (*sandbox.ExecutionResult, error) {
	if req.Code == "" {
		return nil, fmt.Errorf("code is required")
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = s.cfg.Sandbox.Timeout
	}

	if timeout < MinTimeout || timeout > MaxTimeout {
		return nil, fmt.Errorf("timeout must be between %d and %d seconds", MinTimeout, MaxTimeout)
	}

	env, err := s.BuildSandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to configure sandbox: %w", err)
	}

	executionID := uuid.New().String()
	proxyToken := s.proxySvc.RegisterToken(executionID)
	env["ETHPANDAOPS_PROXY_TOKEN"] = proxyToken
	defer s.proxySvc.RevokeToken(executionID)

	if req.SessionID == "" && s.sandboxSvc.SessionsEnabled() {
		canCreate, count, maxAllowed := s.sandboxSvc.CanCreateSession(ctx, req.OwnerID)
		if !canCreate {
			return nil, fmt.Errorf(
				"maximum sessions limit reached (%d/%d). Use manage_session with operation 'list' to see sessions, then 'destroy' to free up a slot",
				count,
				maxAllowed,
			)
		}
	}

	return s.sandboxSvc.Execute(ctx, sandbox.ExecuteRequest{
		Code:      req.Code,
		Env:       env,
		Timeout:   time.Duration(timeout) * time.Second,
		SessionID: req.SessionID,
		OwnerID:   req.OwnerID,
	})
}

func (s *Service) SessionsEnabled() bool {
	return s.sandboxSvc.SessionsEnabled()
}

func (s *Service) ListSessions(ctx context.Context, ownerID string) ([]sandbox.SessionInfo, int, error) {
	sessions, err := s.sandboxSvc.ListSessions(ctx, ownerID)
	if err != nil {
		return nil, 0, err
	}

	_, _, maxSessions := s.sandboxSvc.CanCreateSession(ctx, ownerID)

	return sessions, maxSessions, nil
}

func (s *Service) CreateSession(ctx context.Context, ownerID string) (string, error) {
	env, err := s.BuildSandboxEnv()
	if err != nil {
		return "", fmt.Errorf("building sandbox env: %w", err)
	}

	return s.sandboxSvc.CreateSession(ctx, ownerID, env)
}

func (s *Service) DestroySession(ctx context.Context, sessionID, ownerID string) error {
	return s.sandboxSvc.DestroySession(ctx, sessionID, ownerID)
}

func (s *Service) BuildSandboxEnv() (map[string]string, error) {
	env, err := s.pluginReg.SandboxEnv()
	if err != nil {
		return nil, fmt.Errorf("collecting sandbox env: %w", err)
	}

	env["ETHPANDAOPS_PROXY_URL"] = s.proxySvc.URL()

	if bucket := s.proxySvc.S3Bucket(); bucket != "" {
		env["ETHPANDAOPS_S3_BUCKET"] = bucket
	}

	if prefix := s.proxySvc.S3PublicURLPrefix(); prefix != "" {
		env["ETHPANDAOPS_S3_PUBLIC_URL_PREFIX"] = prefix
	}

	delete(env, "ETHPANDAOPS_CLICKHOUSE_DATASOURCES")
	delete(env, "ETHPANDAOPS_PROMETHEUS_DATASOURCES")
	delete(env, "ETHPANDAOPS_LOKI_DATASOURCES")

	if ds := s.proxySvc.ClickHouseDatasourceInfo(); len(ds) > 0 {
		env["ETHPANDAOPS_CLICKHOUSE_DATASOURCES"] = buildClickhouseDatasourceJSON(ds)
	}

	if ds := s.proxySvc.PrometheusDatasourceInfo(); len(ds) > 0 {
		env["ETHPANDAOPS_PROMETHEUS_DATASOURCES"] = buildPrometheusDatasourceJSON(ds)
	}

	if ds := s.proxySvc.LokiDatasourceInfo(); len(ds) > 0 {
		env["ETHPANDAOPS_LOKI_DATASOURCES"] = buildLokiDatasourceJSON(ds)
	}

	return env, nil
}

func buildClickhouseDatasourceJSON(infos []types.DatasourceInfo) string {
	type dsInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Database    string `json:"database"`
	}

	result := make([]dsInfo, 0, len(infos))
	for _, info := range infos {
		result = append(result, dsInfo{
			Name:        info.Name,
			Description: info.Description,
			Database:    info.Metadata["database"],
		})
	}

	return marshalDatasourceJSON(result)
}

func buildPrometheusDatasourceJSON(infos []types.DatasourceInfo) string {
	type dsInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
	}

	result := make([]dsInfo, 0, len(infos))
	for _, info := range infos {
		result = append(result, dsInfo{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
		})
	}

	return marshalDatasourceJSON(result)
}

func buildLokiDatasourceJSON(infos []types.DatasourceInfo) string {
	type dsInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		URL         string `json:"url"`
	}

	result := make([]dsInfo, 0, len(infos))
	for _, info := range infos {
		result = append(result, dsInfo{
			Name:        info.Name,
			Description: info.Description,
			URL:         info.Metadata["url"],
		})
	}

	return marshalDatasourceJSON(result)
}

func marshalDatasourceJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}

	return string(data)
}
