package devnet

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/panda/pkg/config"
)

func TestSanitizeLabel(t *testing.T) {
	t.Run("lowercases and collapses bad chars", func(t *testing.T) {
		assert.Equal(t, "el-1-geth", sanitizeLabel("EL_1.Geth"))
		assert.Equal(t, "a-b", sanitizeLabel("a__b"))
		assert.Equal(t, "rpc", sanitizeLabel("--rpc--"))
	})

	t.Run("empty becomes x", func(t *testing.T) {
		assert.Equal(t, "x", sanitizeLabel(""))
		assert.Equal(t, "x", sanitizeLabel("___"))
	})

	t.Run("github numeric owner survives", func(t *testing.T) {
		assert.Equal(t, "1234567", sanitizeLabel("1234567"))
	})

	t.Run("shortens over-63-char labels deterministically", func(t *testing.T) {
		long := strings.Repeat("a", 200)
		got := sanitizeLabel(long)
		require.LessOrEqual(t, len(got), maxDNSLabel)
		// Deterministic: same input -> same output.
		assert.Equal(t, got, sanitizeLabel(long))
		// Distinct inputs -> distinct outputs (hash suffix).
		other := strings.Repeat("a", 199) + "b"
		assert.NotEqual(t, got, sanitizeLabel(other))
	})
}

func TestLeftLabel(t *testing.T) {
	t.Run("primary port -> bare service label", func(t *testing.T) {
		assert.Equal(t, "el-1-geth-lighthouse", leftLabel("rpc", "el-1-geth-lighthouse", true))
		assert.Equal(t, "dora", leftLabel("http", "dora", true))
	})

	t.Run("non-primary port -> <port>-<service>", func(t *testing.T) {
		assert.Equal(t, "ws-el-1-geth-lighthouse", leftLabel("ws", "el-1-geth-lighthouse", false))
		assert.Equal(t, "metrics-dora", leftLabel("metrics", "dora", false))
	})

	t.Run("over-long label is shortened to one DNS label", func(t *testing.T) {
		got := leftLabel("metrics", strings.Repeat("x", 80), false)
		require.LessOrEqual(t, len(got), maxDNSLabel)
		assert.NotContains(t, got, ".")
	})
}

func TestHost(t *testing.T) {
	// Clean dotted: <label>.<enclave>.<owner>.<base>.
	assert.Equal(t, "dora.bal3.qu0b.k3s.bruno", host("dora", "bal3", "qu0b", "k3s.bruno"))
	// enclave + owner sanitized as their own labels; base appended verbatim.
	assert.Equal(t, "x.my-dev.q-u-0b.devnet.ethpandaops.io",
		host("x", "my.dev", "q.u_0b", "devnet.ethpandaops.io"))
	// empty base.
	assert.Equal(t, "dora.bal3.qu0b", host("dora", "bal3", "qu0b", ""))
}

func TestIsExposed(t *testing.T) {
	cases := []struct {
		port Port
		want bool
	}{
		{Port{Name: "rpc"}, true},
		{Port{Name: "ws"}, true},
		{Port{Name: "http"}, true},
		{Port{Name: "api"}, true},
		{Port{Name: "metrics"}, true},
		{Port{Name: "custom", Application: "http"}, true},
		{Port{Name: "custom", Application: "ws"}, true},
		{Port{Name: "engine-rpc"}, false},
		{Port{Name: "tcp-discovery"}, false},
		{Port{Name: "udp-discovery"}, false},
		{Port{Name: "quic-discovery"}, false},
		{Port{Name: "p2p"}, false},
		{Port{Name: "udp"}, false},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, isExposed(c.port), "port %+v", c.port)
	}
}

func TestPrimaryPort(t *testing.T) {
	t.Run("rpc wins", func(t *testing.T) {
		p, ok := primaryPort([]Port{{Name: "metrics"}, {Name: "http"}, {Name: "rpc"}})
		require.True(t, ok)
		assert.Equal(t, "rpc", p.Name)
	})

	t.Run("http when no rpc", func(t *testing.T) {
		p, ok := primaryPort([]Port{{Name: "metrics"}, {Name: "http"}})
		require.True(t, ok)
		assert.Equal(t, "http", p.Name)
	})

	t.Run("first http application when no rpc/http names", func(t *testing.T) {
		p, ok := primaryPort([]Port{{Name: "metrics"}, {Name: "ui", Application: "http"}})
		require.True(t, ok)
		assert.Equal(t, "ui", p.Name)
	})

	t.Run("first exposed otherwise", func(t *testing.T) {
		p, ok := primaryPort([]Port{{Name: "metrics"}, {Name: "ws"}})
		require.True(t, ok)
		assert.Equal(t, "metrics", p.Name)
	})

	t.Run("none when empty", func(t *testing.T) {
		_, ok := primaryPort(nil)
		assert.False(t, ok)
	})
}

func TestEndpoints(t *testing.T) {
	services := []Service{
		{
			Name: "el-1-geth-lighthouse",
			Ports: []Port{
				{Name: "engine-rpc", Number: 8551},
				{Name: "metrics", Number: 9001},
				{Name: "rpc", Number: 8545, Application: "http"},
			},
		},
		{
			Name:  "dora",
			Ports: []Port{{Name: "http", Number: 8080, Application: "http"}},
		},
		{
			// No exposed ports -> omitted.
			Name:  "validator-key-generation",
			Ports: []Port{{Name: "p2p", Number: 30303}},
		},
	}

	t.Run("http when no TLS secret", func(t *testing.T) {
		cfg := config.IngressConfig{BaseDomain: "k3s.bruno"}
		eps := Endpoints(services, "mydevnet", "qu0b", cfg)

		require.Len(t, eps, 2)

		el := eps[0]
		assert.Equal(t, "el-1-geth-lighthouse", el.Service)
		// rpc is primary -> clean bare host.
		assert.Equal(t, "http://el-1-geth-lighthouse.mydevnet.qu0b.k3s.bruno", el.PrimaryURL)
		// engine-rpc is skipped; rpc + metrics exposed.
		var names []string
		for _, p := range el.Ports {
			names = append(names, p.Name)
		}
		assert.ElementsMatch(t, []string{"rpc", "metrics"}, names)
		for _, p := range el.Ports {
			switch p.Name {
			case "rpc": // primary -> bare host, same as PrimaryURL
				assert.Equal(t, "http://el-1-geth-lighthouse.mydevnet.qu0b.k3s.bruno", p.URL)
			case "metrics": // non-primary -> <port>-<service> left label
				assert.Equal(t, "http://metrics-el-1-geth-lighthouse.mydevnet.qu0b.k3s.bruno", p.URL)
			}
		}

		dora := eps[1]
		assert.Equal(t, "http://dora.mydevnet.qu0b.k3s.bruno", dora.PrimaryURL)
	})

	t.Run("https when TLS secret set", func(t *testing.T) {
		cfg := config.IngressConfig{BaseDomain: "devnet.ethpandaops.io", TLSSecret: "wildcard"}
		eps := Endpoints(services, "mydevnet", "42", cfg)

		require.Len(t, eps, 2)
		assert.Equal(t, "https://el-1-geth-lighthouse.mydevnet.42.devnet.ethpandaops.io", eps[0].PrimaryURL)
		for _, p := range eps[0].Ports {
			assert.True(t, strings.HasPrefix(p.URL, "https://"))
		}
	})
}

func TestBuildIngress(t *testing.T) {
	cfg := config.IngressConfig{
		BaseDomain:   "k3s.bruno",
		IngressClass: "traefik",
		Annotations: map[string]string{
			"traefik.ingress.kubernetes.io/router.entrypoints": "web",
			"cert-manager.io/cluster-issuer":                   "zerossl-devnet",
		},
		TLSSecret: "wildcard",
	}
	svc := Service{
		Name: "el-1",
		Ports: []Port{
			{Name: "engine-rpc", Number: 8551},
			{Name: "rpc", Number: 8545, Application: "http"},
			{Name: "ws", Number: 8546, Application: "ws"},
		},
	}

	ing := buildIngress("ns", "uuid-1", "dev", "qu0b", svc, cfg)
	require.NotNil(t, ing)

	assert.Equal(t, "panda-el-1", ing.Name)
	assert.Equal(t, "ns", ing.Namespace)
	assert.Equal(t, "panda", ing.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "qu0b", ing.Labels["panda.devnet/owner"])
	assert.Equal(t, "uuid-1", ing.Labels["panda.devnet/enclave"])
	// Configured annotations are applied verbatim (controller-agnostic).
	assert.Equal(t, "web", ing.Annotations["traefik.ingress.kubernetes.io/router.entrypoints"])
	assert.Equal(t, "zerossl-devnet", ing.Annotations["cert-manager.io/cluster-issuer"])
	require.NotNil(t, ing.Spec.IngressClassName)
	assert.Equal(t, "traefik", *ing.Spec.IngressClassName)

	// engine-rpc skipped; rpc (primary -> bare host) + ws (<port>-<service>) = 2 rules.
	require.Len(t, ing.Spec.Rules, 2)
	assert.Equal(t, "el-1.dev.qu0b.k3s.bruno", ing.Spec.Rules[0].Host)
	assert.Equal(t, int32(8545), ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number)
	assert.Equal(t, "ws-el-1.dev.qu0b.k3s.bruno", ing.Spec.Rules[1].Host)
	assert.Equal(t, int32(8546), ing.Spec.Rules[1].HTTP.Paths[0].Backend.Service.Port.Number)

	// TLS covers all hosts.
	require.Len(t, ing.Spec.TLS, 1)
	assert.Equal(t, "wildcard", ing.Spec.TLS[0].SecretName)
	assert.ElementsMatch(t,
		[]string{"el-1.dev.qu0b.k3s.bruno", "ws-el-1.dev.qu0b.k3s.bruno"},
		ing.Spec.TLS[0].Hosts)

	t.Run("no exposed ports yields nil", func(t *testing.T) {
		nilIng := buildIngress("ns", "uuid", "dev", "qu0b",
			Service{Name: "x", Ports: []Port{{Name: "p2p"}}}, cfg)
		assert.Nil(t, nilIng)
	})

	t.Run("no TLS disables TLS", func(t *testing.T) {
		plain := buildIngress("ns", "uuid", "dev", "qu0b", svc,
			config.IngressConfig{BaseDomain: "k3s.bruno", IngressClass: "traefik"})
		require.NotNil(t, plain)
		assert.Empty(t, plain.Spec.TLS)
		assert.Empty(t, plain.Annotations)
	})

	t.Run("TLS without a fixed secret derives a per-ingress secret", func(t *testing.T) {
		auto := buildIngress("ns", "uuid", "dev", "qu0b", svc,
			config.IngressConfig{BaseDomain: "k3s.bruno", IngressClass: "nginx", TLS: true})
		require.NotNil(t, auto)
		require.Len(t, auto.Spec.TLS, 1)
		assert.Equal(t, "panda-el-1-tls", auto.Spec.TLS[0].SecretName)
	})
}

func TestAliasHostname(t *testing.T) {
	assert.Equal(t, "dora.qu0b.k3s.bruno", aliasHostname("dora", "qu0b", "k3s.bruno"))
	assert.Equal(t, "el-1-geth.q-u-0b.devnet.ethpandaops.io",
		aliasHostname("el-1-geth", "q.u_0b", "devnet.ethpandaops.io"))
	assert.Equal(t, "dora.qu0b", aliasHostname("dora", "qu0b", ""))
}

func TestAliasEndpoints(t *testing.T) {
	services := []Service{
		{Name: "dora", Ports: []Port{{Name: "http", Number: 8080, Application: "http"}}},
		{Name: "novel", Ports: []Port{{Name: "p2p", Number: 30303}}}, // no primary -> omitted
	}

	t.Run("http when no alias TLS", func(t *testing.T) {
		eps := AliasEndpoints(services, "qu0b", config.IngressConfig{BaseDomain: "k3s.bruno"})
		require.Len(t, eps, 1)
		assert.Equal(t, "dora", eps[0].Service)
		assert.Equal(t, "http://dora.qu0b.k3s.bruno", eps[0].PrimaryURL)
	})

	t.Run("https when alias TLS set", func(t *testing.T) {
		eps := AliasEndpoints(services, "qu0b",
			config.IngressConfig{BaseDomain: "devnet.ethpandaops.io", AliasTLSSecret: "w"})
		require.Len(t, eps, 1)
		assert.Equal(t, "https://dora.qu0b.devnet.ethpandaops.io", eps[0].PrimaryURL)
	})
}

func TestBuildAliasIngress(t *testing.T) {
	cfg := config.IngressConfig{BaseDomain: "k3s.bruno", IngressClass: "traefik"}
	svc := Service{Name: "el-1", Ports: []Port{
		{Name: "engine-rpc", Number: 8551},
		{Name: "rpc", Number: 8545, Application: "http"},
	}}

	ing := buildAliasIngress("ns", "uuid-1", "qu0b", svc, cfg)
	require.NotNil(t, ing)
	assert.Equal(t, "panda-alias-el-1", ing.Name)
	assert.Equal(t, "true", ing.Labels[aliasLabel])
	assert.Equal(t, "qu0b", ing.Labels["panda.devnet/owner"])
	require.Len(t, ing.Spec.Rules, 1)
	assert.Equal(t, "el-1.qu0b.k3s.bruno", ing.Spec.Rules[0].Host)
	assert.Equal(t, int32(8545), ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number)

	nilIng := buildAliasIngress("ns", "u", "qu0b",
		Service{Name: "x", Ports: []Port{{Name: "p2p"}}}, cfg)
	assert.Nil(t, nilIng)
}
