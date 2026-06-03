package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/types"
)

// Router is a proxy client that can resolve datasource ownership across
// multiple underlying proxy clients.
type Router interface {
	Client

	// Primary returns the first external proxy client. It is nil when only
	// local clients are configured.
	Primary() Client

	// OwnerForDatasource returns the proxy that owns a datasource by type/name.
	OwnerForDatasource(datasourceType, datasourceName string) (DatasourceOwner, bool)

	// ClientForDatasource returns the proxy client that owns a datasource by type/name.
	ClientForDatasource(datasourceType, datasourceName string) (Client, bool)
}

// DatasourceOwner identifies the proxy that owns a datasource.
type DatasourceOwner struct {
	ProxyName string
	URL       string
}

// ClientRoute is one proxy client in a router.
type ClientRoute struct {
	// Name is the configured proxy identifier.
	Name string
	// Client is the underlying proxy client.
	Client Client
	// Local marks an in-process/local proxy. Local routes are never primary.
	Local bool
}

type routerClient struct {
	log     logrus.FieldLogger
	routes  []clientRoute
	primary *clientRoute

	mu               sync.Mutex
	warnedCollisions map[string]struct{}
}

type clientRoute struct {
	name   string
	client Client
	local  bool
}

type datasourceKey struct {
	typ  string
	name string
}

// NewRouter creates a multi-proxy router.
func NewRouter(log logrus.FieldLogger, routes []ClientRoute) Router {
	r := &routerClient{
		log:              log.WithField("component", "proxy-router"),
		routes:           make([]clientRoute, 0, len(routes)),
		warnedCollisions: make(map[string]struct{}),
	}

	for i, route := range routes {
		if route.Client == nil {
			continue
		}

		name := strings.TrimSpace(route.Name)
		if name == "" {
			name = defaultRouteName(i)
		}

		r.routes = append(r.routes, clientRoute{
			name:   name,
			client: route.Client,
			local:  route.Local,
		})
	}

	for i := range r.routes {
		if !r.routes[i].local {
			r.primary = &r.routes[i]

			break
		}
	}

	return r
}

func defaultRouteName(index int) string {
	if index == 0 {
		return "primary"
	}

	return fmt.Sprintf("proxy-%d", index+1)
}

// Start starts every wrapped proxy client.
func (r *routerClient) Start(ctx context.Context) error {
	if len(r.routes) == 0 {
		r.log.Warn("No proxy clients configured")

		return nil
	}

	var errs []error
	for _, route := range r.routes {
		if err := route.client.Start(ctx); err != nil {
			errs = append(errs, fmt.Errorf("starting proxy %q: %w", route.name, err))
		}
	}

	return errors.Join(errs...)
}

// Stop stops every wrapped proxy client.
func (r *routerClient) Stop(ctx context.Context) error {
	var errs []error
	for _, route := range r.routes {
		if err := route.client.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping proxy %q: %w", route.name, err))
		}
	}

	return errors.Join(errs...)
}

// Discover refreshes datasource discovery on every wrapped proxy client.
func (r *routerClient) Discover(ctx context.Context) error {
	var errs []error
	for _, route := range r.routes {
		if err := route.client.Discover(ctx); err != nil {
			errs = append(errs, fmt.Errorf("discovering proxy %q: %w", route.name, err))
		}
	}

	return errors.Join(errs...)
}

// URL returns the primary proxy URL. It is empty when there is no external proxy.
func (r *routerClient) URL() string {
	primary := r.Primary()
	if primary == nil {
		return ""
	}

	return primary.URL()
}

// RegisterToken returns a primary-proxy token for primary-only proxy requests.
func (r *routerClient) RegisterToken(executionID string) string {
	primary := r.Primary()
	if primary == nil {
		return ""
	}

	return primary.RegisterToken(executionID)
}

// RevokeToken revokes a primary-proxy token for primary-only proxy requests.
func (r *routerClient) RevokeToken(executionID string) {
	primary := r.Primary()
	if primary == nil {
		return
	}

	primary.RevokeToken(executionID)
}

// EnsureAuthenticated checks authentication for the primary external proxy.
func (r *routerClient) EnsureAuthenticated(ctx context.Context) error {
	primary := r.Primary()
	if primary == nil {
		return nil
	}

	return primary.EnsureAuthenticated(ctx)
}

// Primary returns the first external proxy client.
func (r *routerClient) Primary() Client {
	if r.primary == nil {
		return nil
	}

	return r.primary.client
}

// ClickHouseDatasources returns merged ClickHouse datasource names.
func (r *routerClient) ClickHouseDatasources() []string {
	return namesFromInfo(r.ClickHouseDatasourceInfo())
}

// ClickHouseDatasourceInfo returns merged ClickHouse datasource info.
func (r *routerClient) ClickHouseDatasourceInfo() []types.DatasourceInfo {
	infos, _ := r.mergeDatasourceInfo("clickhouse")

	return infos
}

// PrometheusDatasources returns merged Prometheus datasource names.
func (r *routerClient) PrometheusDatasources() []string {
	return namesFromInfo(r.PrometheusDatasourceInfo())
}

// PrometheusDatasourceInfo returns merged Prometheus datasource info.
func (r *routerClient) PrometheusDatasourceInfo() []types.DatasourceInfo {
	infos, _ := r.mergeDatasourceInfo("prometheus")

	return infos
}

// LokiDatasources returns merged Loki datasource names.
func (r *routerClient) LokiDatasources() []string {
	return namesFromInfo(r.LokiDatasourceInfo())
}

// LokiDatasourceInfo returns merged Loki datasource info.
func (r *routerClient) LokiDatasourceInfo() []types.DatasourceInfo {
	infos, _ := r.mergeDatasourceInfo("loki")

	return infos
}

// EthNodeAvailable returns primary-proxy ethnode availability.
func (r *routerClient) EthNodeAvailable() bool {
	primary := r.Primary()
	if primary == nil {
		return false
	}

	return primary.EthNodeAvailable()
}

// EmbeddingAvailable returns primary-proxy embedding availability.
func (r *routerClient) EmbeddingAvailable() bool {
	primary := r.Primary()
	if primary == nil {
		return false
	}

	return primary.EmbeddingAvailable()
}

// EmbeddingModel returns the primary-proxy embedding model.
func (r *routerClient) EmbeddingModel() string {
	primary := r.Primary()
	if primary == nil {
		return ""
	}

	return primary.EmbeddingModel()
}

// OwnerForDatasource returns the proxy that owns a datasource by type/name.
func (r *routerClient) OwnerForDatasource(datasourceType, datasourceName string) (DatasourceOwner, bool) {
	_, owners := r.mergeDatasourceInfo(datasourceType)

	route, ok := owners[datasourceKey{
		typ:  normalizeDatasourceType(datasourceType),
		name: strings.TrimSpace(datasourceName),
	}]
	if !ok {
		return DatasourceOwner{}, false
	}

	return DatasourceOwner{
		ProxyName: route.name,
		URL:       route.client.URL(),
	}, true
}

// ClientForDatasource returns the proxy client that owns a datasource by type/name.
func (r *routerClient) ClientForDatasource(datasourceType, datasourceName string) (Client, bool) {
	_, owners := r.mergeDatasourceInfo(datasourceType)

	route, ok := owners[datasourceKey{
		typ:  normalizeDatasourceType(datasourceType),
		name: strings.TrimSpace(datasourceName),
	}]
	if !ok {
		return nil, false
	}

	return route.client, true
}

func (r *routerClient) mergeDatasourceInfo(datasourceType string) ([]types.DatasourceInfo, map[datasourceKey]clientRoute) {
	datasourceType = normalizeDatasourceType(datasourceType)

	infos := make([]types.DatasourceInfo, 0)
	owners := make(map[datasourceKey]clientRoute)

	for _, route := range r.routes {
		for _, info := range routeDatasourceInfo(route.client, datasourceType) {
			info.Name = strings.TrimSpace(info.Name)
			if info.Name == "" {
				continue
			}

			info.Type = datasourceType
			info.ProxyName = route.name

			key := datasourceKey{typ: info.Type, name: info.Name}
			existing, exists := owners[key]
			if exists {
				if existing.name != route.name {
					r.warnCollision(info.Type, info.Name, existing.name, route.name)
				}

				continue
			}

			owners[key] = route
			infos = append(infos, info)
		}
	}

	return infos, owners
}

func normalizeDatasourceType(datasourceType string) string {
	return strings.ToLower(strings.TrimSpace(datasourceType))
}

func routeDatasourceInfo(client Client, datasourceType string) []types.DatasourceInfo {
	switch datasourceType {
	case "clickhouse":
		return client.ClickHouseDatasourceInfo()
	case "prometheus":
		return client.PrometheusDatasourceInfo()
	case "loki":
		return client.LokiDatasourceInfo()
	default:
		return nil
	}
}

func (r *routerClient) warnCollision(datasourceType, datasourceName, winnerProxy, ignoredProxy string) {
	key := strings.Join([]string{datasourceType, datasourceName, winnerProxy, ignoredProxy}, "\x00")

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.warnedCollisions[key]; exists {
		return
	}

	r.warnedCollisions[key] = struct{}{}

	r.log.WithFields(logrus.Fields{
		"datasource_type": datasourceType,
		"datasource_name": datasourceName,
		"winner_proxy":    winnerProxy,
		"ignored_proxy":   ignoredProxy,
	}).Warn("Datasource collision across proxies; keeping first")
}

var (
	_ Client  = (*routerClient)(nil)
	_ Router  = (*routerClient)(nil)
	_ Service = (*routerClient)(nil)
)
