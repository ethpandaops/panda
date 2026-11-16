package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy/handlers"
)

type fakePublishedPortResolver struct {
	host  string
	port  int
	err   error
	calls []int
}

func (f *fakePublishedPortResolver) Resolve(ctx context.Context, hostPort int, probe publishedPortProbe) (string, int, error) {
	f.calls = append(f.calls, hostPort)

	if f.err != nil {
		return "", 0, f.err
	}

	if err := probe(ctx, f.host, f.port); err != nil {
		return "", 0, err
	}

	return f.host, f.port, nil
}

func TestPublishedPortCandidates(t *testing.T) {
	endpoint := func(id, ip string) *network.EndpointSettings {
		return &network.EndpointSettings{NetworkID: id, IPAddress: netip.MustParseAddr(ip)}
	}

	containers := []container.Summary{
		{
			ID:    "udp-only",
			Ports: []container.PortSummary{{PrivatePort: 8123, PublicPort: 18123, Type: "udp"}},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{
				"other": endpoint("net-other", "172.20.0.9"),
			}},
		},
		{
			ID:    "self",
			Ports: []container.PortSummary{{PrivatePort: 8123, PublicPort: 18123, Type: "tcp"}},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{
				"internal": endpoint("net-internal", "172.30.0.2"),
			}},
		},
		{
			ID: "clickhouse",
			Ports: []container.PortSummary{
				{PrivatePort: 9000},
				{PrivatePort: 8123, PublicPort: 18123, Type: "tcp"},
			},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{
				"bridge":   endpoint("net-bridge", "172.17.0.5"),
				"alpha":    endpoint("net-alpha", "172.19.0.5"),
				"internal": endpoint("net-internal", "172.30.0.5"),
				"no-ip":    {NetworkID: "net-no-ip"},
			}},
		},
	}

	want := []publishedPortCandidate{
		{networkName: "internal", networkID: "net-internal", host: "172.30.0.5", port: 8123},
		{networkName: "alpha", networkID: "net-alpha", host: "172.19.0.5", port: 8123},
		{networkName: "bridge", networkID: "net-bridge", host: "172.17.0.5", port: 8123},
	}

	for range 20 {
		got := publishedPortCandidates(containers, 18123, "self", map[string]struct{}{"internal": {}})
		if !slices.Equal(got, want) {
			t.Fatalf("candidates = %+v, want %+v", got, want)
		}
	}

	if got := publishedPortCandidates(containers, 19999, "self", nil); len(got) != 0 {
		t.Fatalf("candidates = %+v, want none", got)
	}
}

func TestAutodiscoverFallsBackToPublishedPortResolver(t *testing.T) {
	fakeClickHouse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = w.Write([]byte("Ok."))

			return
		}

		_, _ = w.Write([]byte("1\n"))
	}))
	defer fakeClickHouse.Close()

	wantHost, wantPort := clickHouseAutodiscoverTestAddress(t, fakeClickHouse)

	entry := ClickHouseClusterConfig{
		BaseDatasourceConfig: BaseDatasourceConfig{Name: "local-kurtosis"},
		Host:                 "127.0.0.1",
		Port:                 1,
		Database:             "otel",
		Autodiscover:         true,
	}

	resolver := &fakePublishedPortResolver{err: errPublishedPortNotFound}
	srv := &server{
		log:                      logrus.New(),
		clickhouseHandler:        handlers.NewClickHouseHandler(logrus.New(), nil),
		autodiscoverHTTPClient:   fakeClickHouse.Client(),
		autodiscoverPortResolver: resolver,
		staticClickHouseNames:    map[string]struct{}{},
		dynamicClickHouseNames:   map[string]struct{}{},
		staticAutodiscoverWarns:  map[string]struct{}{},
	}

	present := false

	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if present {
		t.Fatal("expected datasource to stay absent while nothing publishes the port")
	}

	resolver.host, resolver.port, resolver.err = wantHost, wantPort, nil

	srv.reconcileClickHouseAutodiscover(t.Context(), entry, &present)
	if !present {
		t.Fatal("expected datasource to become present through the resolver")
	}

	if len(resolver.calls) != 2 || resolver.calls[1] != 1 {
		t.Fatalf("resolver calls = %v", resolver.calls)
	}

	if got := srv.autodiscoveredClickHouseTarget("local-kurtosis"); got != fakeClickHouse.URL {
		t.Fatalf("target = %q, want %q", got, fakeClickHouse.URL)
	}
}
