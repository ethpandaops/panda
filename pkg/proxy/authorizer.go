package proxy

import (
	"context"
	"net/http"
	"sort"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/auth"
	"github.com/ethpandaops/panda/pkg/proxy/handlers"
	"github.com/ethpandaops/panda/pkg/types"
)

// AllowAllAuthenticated is the allowed_orgs entry that grants access to every
// authenticated user, including identities that carry no org group (e.g. Dex
// tokens with only org:team groups, or CI service accounts).
const AllowAllAuthenticated = "*"

// Authorizer enforces per-datasource access control based on GitHub org membership.
// Rules are built from datasource configs at startup and checked on every request.
//
// Authorization is default-deny for authenticated users: a datasource, variant or
// feature with an empty allowed_orgs list is unreachable, and a request for a
// datasource or route type without a rule is denied. "*" in allowed_orgs admits
// every authenticated user. Requests without an authenticated user (auth mode
// "none") are not restricted.
type Authorizer struct {
	log logrus.FieldLogger
	// authRequired is true when the proxy runs with authentication. A request
	// without a user in its context is then denied rather than treated as
	// auth mode "none", so a missing or group-less identity never fails open.
	authRequired bool
	rules        map[string][]datasourceVariantRule // "type:name" -> variants; "type" for type-level rules (ethnode, github, uploads, faucet)
}

type datasourceVariantRule struct {
	routeName   string
	allowedOrgs []string
	metadata    map[string]string
	isVariant   bool // built from a variants[] entry (for startup warnings)
}

// NewAuthorizer creates an Authorizer from the server config.
func NewAuthorizer(log logrus.FieldLogger, cfg ServerConfig) *Authorizer {
	a := &Authorizer{
		log:          log.WithField("component", "authorizer"),
		authRequired: cfg.Auth.Mode != AuthModeNone && cfg.Auth.Mode != "",
		rules:        make(map[string][]datasourceVariantRule, len(cfg.ClickHouse)+len(cfg.Prometheus)+len(cfg.Loki)+4),
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

	for _, ds := range cfg.Benchmarkoor {
		metadata := metadataValue("url", ds.URL)
		if ds.UIURL != "" {
			if metadata == nil {
				metadata = map[string]string{}
			}

			metadata["ui_url"] = ds.UIURL
		}

		a.rules[ruleKey("benchmarkoor", ds.Name)] = []datasourceVariantRule{{
			routeName:   ds.Name,
			allowedOrgs: append([]string(nil), ds.AllowedOrgs...),
			metadata:    metadata,
		}}
	}

	for _, ds := range cfg.Compute {
		// No url metadata: the compute backend address is private and must not
		// surface to callers (see ComputeDatasourceInfo).
		a.rules[ruleKey("compute", ds.Name)] = []datasourceVariantRule{{
			routeName:   ds.Name,
			allowedOrgs: append([]string(nil), ds.AllowedOrgs...),
		}}
	}

	// Type-level feature rules. Built whenever the feature is configured so an
	// empty allowed_orgs denies rather than leaving the route without a rule.
	if cfg.EthNode != nil {
		a.rules[ruleKey("ethnode", "")] = typeRule(cfg.EthNode.AllowedOrgs)
	}

	if cfg.GitHub != nil {
		a.rules[ruleKey("github", "")] = typeRule(cfg.GitHub.AllowedOrgs)
	}

	if cfg.Uploads != nil {
		a.rules[ruleKey("uploads", "")] = typeRule(cfg.Uploads.AllowedOrgs)
	}

	// The faucet route is also served on the ethnode credential when the faucet
	// block is omitted; it still needs faucet.allowed_orgs to be reachable.
	if cfg.ToFaucetHandlerConfig() != nil {
		var allowedOrgs []string
		if cfg.Faucet != nil {
			allowedOrgs = cfg.Faucet.AllowedOrgs
		}

		a.rules[ruleKey("faucet", "")] = typeRule(allowedOrgs)
	}

	// Without authentication there is no user to authorize, so empty
	// allowed_orgs only makes something unreachable in authenticated modes.
	if a.authRequired {
		a.warnUnreachable()
	}

	return a
}

// warnUnreachable logs every rule (datasource, variant or feature) that no
// authenticated user can satisfy because its allowed_orgs list is empty.
func (a *Authorizer) warnUnreachable() {
	keys := make([]string, 0, len(a.rules))
	for key := range a.rules {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		variants := a.rules[key]
		for i, variant := range variants {
			if len(variant.allowedOrgs) > 0 {
				continue
			}

			entry := a.log.WithField("rule", key)
			if variant.isVariant {
				entry = entry.WithField("variant", i)
			}

			entry.Warn(`allowed_orgs is empty: unreachable by authenticated users until allowed_orgs is set (use "*" to allow every authenticated user)`)
		}
	}
}

func typeRule(allowedOrgs []string) []datasourceVariantRule {
	return []datasourceVariantRule{{allowedOrgs: append([]string(nil), allowedOrgs...)}}
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
		if a.authRequired {
			return DatasourcesResponse{}
		}

		return resp // no auth → return everything
	}

	filtered := DatasourcesResponse{
		EthNodeAvailable:   resp.EthNodeAvailable && a.orgsMatch(userOrgs, hasUser, ruleKey("ethnode", "")),
		EmbeddingAvailable: resp.EmbeddingAvailable,
		EmbeddingModel:     resp.EmbeddingModel,
	}

	filtered.ClickHouseInfo = a.filterDatasourceList(userOrgs, hasUser, "clickhouse", resp.ClickHouseInfo)
	filtered.PrometheusInfo = a.filterDatasourceList(userOrgs, hasUser, "prometheus", resp.PrometheusInfo)
	filtered.LokiInfo = a.filterDatasourceList(userOrgs, hasUser, "loki", resp.LokiInfo)
	filtered.BenchmarkoorInfo = a.filterDatasourceList(userOrgs, hasUser, "benchmarkoor", resp.BenchmarkoorInfo)
	filtered.ComputeInfo = a.filterDatasourceList(userOrgs, hasUser, "compute", resp.ComputeInfo)

	return filtered
}

func (a *Authorizer) filterDatasourceList(userOrgs []string, hasUser bool, dsType string, infos []types.DatasourceInfo) []types.DatasourceInfo {
	if len(infos) == 0 {
		return nil
	}

	filtered := make([]types.DatasourceInfo, 0, len(infos))

	for _, info := range infos {
		variant, ok := a.matchingVariant(userOrgs, hasUser, ruleKey(dsType, info.Name))
		if !ok {
			continue
		}

		filtered = append(filtered, datasourceInfoForVariant(info, variant))
	}

	return filtered
}

// isAllowed checks if the request context is authorized to access the datasource.
func (a *Authorizer) isAllowed(ctx context.Context, dsType, dsName string) bool {
	_, ok := a.routeName(ctx, dsType, dsName)

	return ok
}

func (a *Authorizer) routeName(ctx context.Context, dsType, dsName string) (string, bool) {
	userOrgs, hasUser := getUserOrgs(ctx)
	if !hasUser && a.authRequired {
		return "", false
	}

	switch dsType {
	case "datasources", "embed":
		// No datasource data: the listing is filtered in its handler and
		// embeddings are infrastructure. Open to any authenticated user.
		return "", true
	case "ethnode", "github", "uploads", "faucet":
		// Type-level rules (no per-name granularity).
		return "", a.orgsMatch(userOrgs, hasUser, ruleKey(dsType, ""))
	case "clickhouse", "prometheus", "loki", "benchmarkoor", "compute":
		variant, ok := a.matchingVariant(userOrgs, hasUser, ruleKey(dsType, dsName))
		if !ok {
			return "", false
		}

		if variant.routeName == "" {
			return dsName, true
		}

		return variant.routeName, true
	default:
		// Unrecognised route type: deny authenticated users. Without auth
		// (mode "none") nothing is restricted.
		return "", !hasUser
	}
}

// orgsMatch returns true if the user has access based on the rule for the given key.
// If no rule exists for the key, authenticated users are denied.
func (a *Authorizer) orgsMatch(userOrgs []string, hasUser bool, key string) bool {
	_, ok := a.matchingVariant(userOrgs, hasUser, key)

	return ok
}

func (a *Authorizer) matchingVariant(userOrgs []string, hasUser bool, key string) (datasourceVariantRule, bool) {
	variants, exists := a.rules[key]

	if !hasUser {
		// No auth user in context (none mode): no restriction; select the
		// first configured backend when there is one.
		if !exists {
			return datasourceVariantRule{}, true
		}

		if len(variants) == 0 {
			return datasourceVariantRule{}, false
		}

		return variants[0], true
	}

	// Default deny: an unknown datasource or unconfigured feature has no rule.
	for _, variant := range variants {
		if allowedOrgsMatch(userOrgs, variant.allowedOrgs) {
			return variant, true
		}
	}

	return datasourceVariantRule{}, false
}

// allowedOrgsMatch reports whether an authenticated user with userOrgs may use
// a rule. An empty allowedOrgs matches nobody; "*" matches every authenticated
// user; any other entry must equal one of the user's groups exactly.
func allowedOrgsMatch(userOrgs, allowedOrgs []string) bool {
	for _, allowed := range allowedOrgs {
		if allowed == AllowAllAuthenticated {
			return true
		}

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
	if user := auth.GetAuthUser(ctx); user != nil {
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
			isVariant:   true,
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
			isVariant:   true,
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
			isVariant:   true,
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
