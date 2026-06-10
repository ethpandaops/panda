// Pre-merge smoke test: exercise the client_credentials proxy client against
// the real staging Authentik + panda-proxy. Run with:
//
//	PANDA_SMOKE_USERNAME=panda-ci-svc PANDA_SMOKE_PASSWORD=<app password> go run ./scripts/ccsmoke
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/proxy"
)

func main() {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	c := proxy.NewClient(log, proxy.ClientConfig{
		Name:              "staging",
		URL:               "https://panda-proxy.analytics.staging.platform.ethpandaops.io",
		IssuerURL:         "https://authentik.analytics.staging.platform.ethpandaops.io/application/o/panda-proxy/",
		ClientID:          "panda-proxy",
		AuthMode:          proxy.AuthModeClientCredentials,
		Username:          os.Getenv("PANDA_SMOKE_USERNAME"),
		Password:          os.Getenv("PANDA_SMOKE_PASSWORD"),
		DiscoveryInterval: 0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := c.Start(ctx); err != nil {
		log.WithError(err).Fatal("start failed")
	}

	fmt.Println("clickhouse datasources:", c.ClickHouseDatasources())

	// Token mint + cache: two RegisterTokens should reuse the cached token.
	t1 := c.RegisterToken()
	c.RevokeToken()
	t2 := c.RegisterToken()
	c.RevokeToken()
	fmt.Println("token minted:", len(t1) > 0, "| cached reuse:", t1 == t2)

	// Real query through the proxy as the service account.
	out, err := c.ClickHouseQuery(ctx, "xatu-experimental", "SELECT 1", nil)
	if err != nil {
		log.WithError(err).Fatal("query failed")
	}
	fmt.Printf("clickhouse SELECT 1 -> %q\n", string(out))
}
