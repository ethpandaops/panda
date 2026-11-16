package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

func (f *fakePublishedPortResolver) Resolve(_ context.Context, hostPort int) (string, int, error) {
	f.calls = append(f.calls, hostPort)

	return f.host, f.port, f.err
}

func TestFindPublishedPort(t *testing.T) {
	containers := []container.Summary{
		{
			Ports: []container.PortSummary{{PrivatePort: 8123, PublicPort: 18123, Type: "udp"}},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{
				"other": {NetworkID: "net-other", IPAddress: netip.MustParseAddr("172.20.0.9")},
			}},
		},
		{
			Ports: []container.PortSummary{
				{PrivatePort: 9000},
				{PrivatePort: 8123, PublicPort: 18123, Type: "tcp"},
			},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{
				"bridge": {NetworkID: "net-bridge", IPAddress: netip.MustParseAddr("172.17.0.5")},
			}},
		},
	}

	networkName, networkID, host, port, err := findPublishedPort(containers, 18123)
	if err != nil {
		t.Fatalf("findPublishedPort: %v", err)
	}

	if networkName != "bridge" || networkID != "net-bridge" || host != "172.17.0.5" || port != 8123 {
		t.Fatalf("got %s %s %s:%d", networkName, networkID, host, port)
	}

	if _, _, _, _, err := findPublishedPort(containers, 19999); !errors.Is(err, errPublishedPortNotFound) {
		t.Fatalf("err = %v, want errPublishedPortNotFound", err)
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
