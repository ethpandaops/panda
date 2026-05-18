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
		EthNodeAvailable:   resp.EthNodeAvailable && a.orgsMatch(userOrgs, ruleKey("ethnode", "")),
		EmbeddingAvailable: resp.EmbeddingAvailable,
		EmbeddingModel:     resp.EmbeddingModel,
	}

	for i, name := range resp.ClickHouse {
		if variant, ok := a.matchingVariant(userOrgs, ruleKey("clickhouse", name)); ok {
			filtered.ClickHouse = append(filtered.ClickHouse, name)

			if i < len(resp.ClickHouseInfo) {
				filtered.ClickHouseInfo = append(filtered.ClickHouseInfo, datasourceInfoForVariant(resp.ClickHouseInfo[i], variant))
			}
		}
	}

	for i, name := range resp.Prometheus {
		if variant, ok := a.matchingVariant(userOrgs, ruleKey("prometheus", name)); ok {
			filtered.Prometheus = append(filtered.Prometheus, name)

			if i < len(resp.PrometheusInfo) {
				filtered.PrometheusInfo = append(filtered.PrometheusInfo, datasourceInfoForVariant(resp.PrometheusInfo[i], variant))
			}
		}
	}

	for i, name := range resp.Loki {
		if variant, ok := a.matchingVariant(userOrgs, ruleKey("loki", name)); ok {
			filtered.Loki = append(filtered.Loki, name)

			if i < len(resp.LokiInfo) {
				filtered.LokiInfo = append(filtered.LokiInfo, datasourceInfoForVariant(resp.LokiInfo[i], variant))
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

// RouteName returns the internal backend route selected for the datasource.
func (a *Authorizer) RouteName(ctx context.Context, dsType, dsName string) (string, bool) {
	userOrgs := getUserOrgs(ctx)
	variant, ok := a.matchingVariant(userOrgs, ruleKey(dsType, dsName))
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
func (a *Authorizer) orgsMatch(userOrgs []string, key string) bool {
	_, ok := a.matchingVariant(userOrgs, key)

	return ok
}

func (a *Authorizer) matchingVariant(userOrgs []string, key string) (datasourceVariantRule, bool) {
	variants, exists := a.rules[key]
	if !exists {
		return datasourceVariantRule{}, true // no restriction configured
	}

	if len(variants) == 0 {
		return datasourceVariantRule{}, false
	}

	if userOrgs == nil {
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

	info.Metadata = cloneMetadata(variant.metadata)

	return info
}
