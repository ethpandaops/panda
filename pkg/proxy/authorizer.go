package proxy

import (
	"context"
	"net/http"

	"github.com/sirupsen/logrus"

	simpleauth "github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

// Authorizer enforces per-datasource access control based on GitHub org membership.
// Rules are built from datasource configs at startup and checked on every request.
type Authorizer struct {
	log   logrus.FieldLogger
	rules map[string][]datasourceVariantRule // "type:name" -> variants; "type" for type-level rules (ethnode)
}

type datasourceVariantRule struct {
	routeName   string
	allowedOrgs []string
	metadata    map[string]string
}

// NewAuthorizer creates an Authorizer from the server config.
func NewAuthorizer(log logrus.FieldLogger, cfg ServerConfig) *Authorizer {
	a := &Authorizer{
		log:   log.WithField("component", "authorizer"),
		rules: make(map[string][]datasourceVariantRule, len(cfg.ClickHouse)+len(cfg.Prometheus)+len(cfg.Loki)+1),
	}

	for _, ds := range cfg.ClickHouse {
		a.rules[ruleKey("clickhouse", ds.Name)] = clickHouseVariantRules(ds)
	}

	for _, ds := range cfg.Prometheus {
		a.rules[ruleKey("prometheus", ds.Name)] = prometheusVariantRules(ds)
	}

	for _, ds := range cfg.Loki {
		a.rules[ruleKey("loki", ds.Name)] = lokiVariantRules(ds)
	}

	if cfg.EthNode != nil && len(cfg.EthNode.AllowedOrgs) > 0 {
		a.rules[ruleKey("ethnode", "")] = []datasourceVariantRule{{
			allowedOrgs: append([]string(nil), cfg.EthNode.AllowedOrgs...),
		}}
	}

	return a
}

// Middleware returns an HTTP middleware that checks datasource access.
func (a *Authorizer) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dsType := extractDatasourceType(r.URL.Path)
			dsName := r.Header.Get(handlers.DatasourceHeader)

			routeName, ok := a.routeName(r.Context(), dsType, dsName)
			if !ok {
				http.Error(w, "forbidden: insufficient org membership for this datasource", http.StatusForbidden)

				return
			}

			if routeName != "" {
				r = r.WithContext(handlers.WithDatasourceRoute(r.Context(), routeName))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// FilterDatasources returns a copy of the response with only the datasources
// the authenticated user is allowed to access.
func (a *Authorizer) FilterDatasources(ctx context.Context, resp DatasourcesResponse) DatasourcesResponse {
	userOrgs, hasUser := getUserOrgs(ctx)
	if !hasUser {
		return resp // no auth → return everything
	}

	filtered := DatasourcesResponse{
		EthNodeAvailable:   resp.EthNodeAvailable && a.orgsMatch(userOrgs, hasUser, ruleKey("ethnode", "")),
		EmbeddingAvailable: resp.EmbeddingAvailable,
		EmbeddingModel:     resp.EmbeddingModel,
	}

	filtered.ClickHouse, filtered.ClickHouseInfo = a.filterDatasourceList(userOrgs, hasUser, "clickhouse", resp.ClickHouse, resp.ClickHouseInfo)
	filtered.Prometheus, filtered.PrometheusInfo = a.filterDatasourceList(userOrgs, hasUser, "prometheus", resp.Prometheus, resp.PrometheusInfo)
	filtered.Loki, filtered.LokiInfo = a.filterDatasourceList(userOrgs, hasUser, "loki", resp.Loki, resp.LokiInfo)

	return filtered
}

func (a *Authorizer) filterDatasourceList(userOrgs []string, hasUser bool, dsType string, names []string, infos []types.DatasourceInfo) ([]string, []types.DatasourceInfo) {
	filteredNames := make([]string, 0, len(names))
	filteredInfos := make([]types.DatasourceInfo, 0, len(infos))

	for i, name := range names {
		variant, ok := a.matchingVariant(userOrgs, hasUser, ruleKey(dsType, name))
		if !ok {
			continue
		}

		filteredNames = append(filteredNames, name)
		if i < len(infos) {
			filteredInfos = append(filteredInfos, datasourceInfoForVariant(infos[i], variant))
		}
	}

	return filteredNames, filteredInfos
}

// isAllowed checks if the request context is authorized to access the datasource.
func (a *Authorizer) isAllowed(ctx context.Context, dsType, dsName string) bool {
	_, ok := a.routeName(ctx, dsType, dsName)

	return ok
}

func (a *Authorizer) routeName(ctx context.Context, dsType, dsName string) (string, bool) {
	userOrgs, hasUser := getUserOrgs(ctx)

	// For ethnode, check at type level (no per-name granularity).
	if dsType == "ethnode" {
		if a.orgsMatch(userOrgs, hasUser, ruleKey("ethnode", "")) {
			return "", true
		}

		return "", false
	}

	// For datasources endpoint, skip middleware check (filtered in handler).
	if dsType == "datasources" || dsType == "unknown" {
		return "", true
	}

	variant, ok := a.matchingVariant(userOrgs, hasUser, ruleKey(dsType, dsName))
	if !ok {
		return "", false
	}

	if variant.routeName == "" {
		return dsName, true
	}

	return variant.routeName, true
}

// orgsMatch returns true if the user has access based on the rule for the given key.
// If no rule exists for the key, access is allowed (open by default).
func (a *Authorizer) orgsMatch(userOrgs []string, hasUser bool, key string) bool {
	_, ok := a.matchingVariant(userOrgs, hasUser, key)

	return ok
}

func (a *Authorizer) matchingVariant(userOrgs []string, hasUser bool, key string) (datasourceVariantRule, bool) {
	variants, exists := a.rules[key]
	if !exists {
		return datasourceVariantRule{}, true // no restriction configured
	}

	if len(variants) == 0 {
		return datasourceVariantRule{}, false
	}

	if !hasUser {
		return variants[0], true // no auth user in context (none mode) → select first configured backend
	}

	for _, variant := range variants {
		if allowedOrgsMatch(userOrgs, variant.allowedOrgs) {
			return variant, true
		}
	}

	return datasourceVariantRule{}, false
}

func allowedOrgsMatch(userOrgs, allowedOrgs []string) bool {
	if len(allowedOrgs) == 0 {
		return true
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
//   - None mode: returns false (no restriction)
func getUserOrgs(ctx context.Context) ([]string, bool) {
	// Check proxy.AuthUser (OIDC mode).
	if user := GetAuthUser(ctx); user != nil {
		return user.Groups, true
	}

	// Check auth.AuthUser (OAuth mode).
	if user := simpleauth.GetAuthUser(ctx); user != nil {
		return user.Orgs, true
	}

	return nil, false
}

// ruleKey builds the map key for an authorization rule.
func ruleKey(dsType, dsName string) string {
	if dsName == "" {
		return dsType
	}

	return dsType + ":" + dsName
}

func clickHouseVariantRules(ds ClickHouseClusterConfig) []datasourceVariantRule {
	if len(ds.Variants) == 0 {
		return []datasourceVariantRule{{
			routeName:   ds.Name,
			allowedOrgs: append([]string(nil), ds.AllowedOrgs...),
			metadata:    metadataValue("database", ds.Database),
		}}
	}

	rules := make([]datasourceVariantRule, 0, len(ds.Variants))
	for i, variant := range ds.Variants {
		rules = append(rules, datasourceVariantRule{
			routeName:   datasourceVariantRouteName(ds.Name, i),
			allowedOrgs: append([]string(nil), variant.AllowedOrgs...),
			metadata:    metadataValue("database", variant.Database),
		})
	}

	return rules
}

func prometheusVariantRules(ds PrometheusInstanceConfig) []datasourceVariantRule {
	if len(ds.Variants) == 0 {
		return []datasourceVariantRule{{
			routeName:   ds.Name,
			allowedOrgs: append([]string(nil), ds.AllowedOrgs...),
			metadata:    metadataValue("url", ds.URL),
		}}
	}

	rules := make([]datasourceVariantRule, 0, len(ds.Variants))
	for i, variant := range ds.Variants {
		rules = append(rules, datasourceVariantRule{
			routeName:   datasourceVariantRouteName(ds.Name, i),
			allowedOrgs: append([]string(nil), variant.AllowedOrgs...),
			metadata:    metadataValue("url", variant.URL),
		})
	}

	return rules
}

func lokiVariantRules(ds LokiInstanceConfig) []datasourceVariantRule {
	if len(ds.Variants) == 0 {
		return []datasourceVariantRule{{
			routeName:   ds.Name,
			allowedOrgs: append([]string(nil), ds.AllowedOrgs...),
			metadata:    metadataValue("url", ds.URL),
		}}
	}

	rules := make([]datasourceVariantRule, 0, len(ds.Variants))
	for i, variant := range ds.Variants {
		rules = append(rules, datasourceVariantRule{
			routeName:   datasourceVariantRouteName(ds.Name, i),
			allowedOrgs: append([]string(nil), variant.AllowedOrgs...),
			metadata:    metadataValue("url", variant.URL),
		})
	}

	return rules
}

func datasourceInfoForVariant(info types.DatasourceInfo, variant datasourceVariantRule) types.DatasourceInfo {
	if len(variant.metadata) == 0 {
		return info
	}

	info.Metadata = variant.metadata

	return info
}
