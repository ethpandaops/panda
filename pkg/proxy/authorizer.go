package proxy

import (
	"context"
	"net/http"

	"github.com/sirupsen/logrus"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

// Authorizer enforces per-datasource access control based on GitHub org membership.
// Rules are built from datasource configs at startup and checked on every request.
type Authorizer struct {
	log   logrus.FieldLogger
	rules map[string][]string // "type:name" -> allowed_orgs; "type" for type-level rules (ethnode)
}

// NewAuthorizer creates an Authorizer from the server config.
func NewAuthorizer(log logrus.FieldLogger, cfg ServerConfig) *Authorizer {
	a := &Authorizer{
		log:   log.WithField("component", "authorizer"),
		rules: make(map[string][]string, len(cfg.ClickHouse)+len(cfg.Prometheus)+len(cfg.Loki)+1),
	}

	for _, ds := range cfg.ClickHouse {
		if len(ds.AllowedOrgs) > 0 {
			a.rules[ruleKey("clickhouse", ds.Name)] = ds.AllowedOrgs
		}
	}

	for _, ds := range cfg.Prometheus {
		if len(ds.AllowedOrgs) > 0 {
			a.rules[ruleKey("prometheus", ds.Name)] = ds.AllowedOrgs
		}
	}

	for _, ds := range cfg.Loki {
		if len(ds.AllowedOrgs) > 0 {
			a.rules[ruleKey("loki", ds.Name)] = ds.AllowedOrgs
		}
	}

	if cfg.EthNode != nil && len(cfg.EthNode.AllowedOrgs) > 0 {
		a.rules[ruleKey("ethnode", "")] = cfg.EthNode.AllowedOrgs
	}

	return a
}

// Middleware returns an HTTP middleware that checks datasource access.
func (a *Authorizer) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dsType := extractDatasourceType(r.URL.Path)
			dsName := r.Header.Get(handlers.DatasourceHeader)

			if !a.isAllowed(r.Context(), dsType, dsName) {
				http.Error(w, "forbidden: insufficient org membership for this datasource", http.StatusForbidden)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// FilterDatasources returns a copy of the response with only the datasources
// the authenticated user is allowed to access.
func (a *Authorizer) FilterDatasources(ctx context.Context, resp DatasourcesResponse) DatasourcesResponse {
	userOrgs := getUserOrgs(ctx)
	if userOrgs == nil {
		return resp // no auth → return everything
	}

	filtered := DatasourcesResponse{
		EthNodeAvailable: resp.EthNodeAvailable && a.orgsMatch(userOrgs, ruleKey("ethnode", "")),
	}

	for i, name := range resp.ClickHouse {
		if a.orgsMatch(userOrgs, ruleKey("clickhouse", name)) {
			filtered.ClickHouse = append(filtered.ClickHouse, name)

			if i < len(resp.ClickHouseInfo) {
				filtered.ClickHouseInfo = append(filtered.ClickHouseInfo, resp.ClickHouseInfo[i])
			}
		}
	}

	for i, name := range resp.Prometheus {
		if a.orgsMatch(userOrgs, ruleKey("prometheus", name)) {
			filtered.Prometheus = append(filtered.Prometheus, name)

			if i < len(resp.PrometheusInfo) {
				filtered.PrometheusInfo = append(filtered.PrometheusInfo, resp.PrometheusInfo[i])
			}
		}
	}

	for i, name := range resp.Loki {
		if a.orgsMatch(userOrgs, ruleKey("loki", name)) {
			filtered.Loki = append(filtered.Loki, name)

			if i < len(resp.LokiInfo) {
				filtered.LokiInfo = append(filtered.LokiInfo, resp.LokiInfo[i])
			}
		}
	}

	return filtered
}

// isAllowed checks if the request context is authorized to access the datasource.
func (a *Authorizer) isAllowed(ctx context.Context, dsType, dsName string) bool {
	userOrgs := getUserOrgs(ctx)
	if userOrgs == nil {
		return true // no auth user in context (none mode) → allow
	}

	// For ethnode, check at type level (no per-name granularity).
	if dsType == "ethnode" {
		return a.orgsMatch(userOrgs, ruleKey("ethnode", ""))
	}

	// For datasources endpoint, skip middleware check (filtered in handler).
	if dsType == "datasources" || dsType == "unknown" {
		return true
	}

	return a.orgsMatch(userOrgs, ruleKey(dsType, dsName))
}

// orgsMatch returns true if the user has access based on the rule for the given key.
// If no rule exists for the key, access is allowed (open by default).
func (a *Authorizer) orgsMatch(userOrgs []string, key string) bool {
	allowedOrgs, exists := a.rules[key]
	if !exists {
		return true // no restriction configured
	}

	for _, allowed := range allowedOrgs {
		for _, userOrg := range userOrgs {
			if allowed == userOrg {
				return true
			}
		}
	}

	return false
}

// getUserOrgs extracts the user's org/group memberships from the request context.
// Works across both auth modes:
//   - OAuth mode: auth.AuthUser.Orgs
//   - OIDC mode: proxy.AuthUser.Groups
//   - None mode: returns nil (no restriction)
func getUserOrgs(ctx context.Context) []string {
	// Check proxy.AuthUser (OIDC mode).
	if user := GetAuthUser(ctx); user != nil {
		return user.Groups
	}

	// Check auth.AuthUser (OAuth mode).
	if user := simpleauth.GetAuthUser(ctx); user != nil {
		return user.Orgs
	}

	return nil
}

// ruleKey builds the map key for an authorization rule.
func ruleKey(dsType, dsName string) string {
	if dsName == "" {
		return dsType
	}

	return dsType + ":" + dsName
}
