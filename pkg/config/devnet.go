package config

// ClusterConfig identifies the single Kubernetes cluster panda uses. The
// cluster's engine-level settings (storage class, enclave size) live in
// Kurtosis's own config — they are fixed when that cluster's engine starts, so
// panda only needs to know which cluster to target. Switch between a local and
// a cloud cluster by editing this block (or pointing panda at another config).
type ClusterConfig struct {
	// Name is the Kurtosis cluster to activate — a key under "kurtosis-clusters"
	// in ~/.config/kurtosis/kurtosis-config.yml. Empty leaves Kurtosis's current
	// selection untouched.
	Name string `yaml:"name,omitempty"`

	// KubeconfigContext is the kubeconfig context Kurtosis connects through.
	// Empty uses the kubeconfig's current context.
	KubeconfigContext string `yaml:"kubeconfig_context,omitempty"`
}

// DevnetConfig is the `devnet:` section of the panda config. It configures the
// `panda devnet` commands. The target cluster lives in the top-level `cluster:`
// block (ClusterConfig), since clusters are a shared resource, not devnet-only.
type DevnetConfig struct {
	// Package overrides the ethereum-package reference to run. Empty uses the
	// built-in default (github.com/ethpandaops/ethereum-package).
	Package string `yaml:"package,omitempty"`

	// DockerCache is a pull-through registry cache host (e.g.
	// "docker.ethquokkaops.io"). When set, every package image is routed
	// through it, which avoids Docker Hub rate limits — especially important on
	// a multi-node Kubernetes backend. Empty disables it.
	DockerCache string `yaml:"docker_cache,omitempty"`

	// Ingress configures GitHub-user-scoped external access to a devnet's
	// services via Traefik Ingress objects panda creates on Kubernetes. Disabled
	// by default; see IngressConfig.
	Ingress IngressConfig `yaml:"ingress,omitempty"`
}

// IngressConfig configures how panda exposes a devnet's services externally by
// creating Traefik Ingress objects in the enclave's namespace. Each exposed
// service port becomes reachable at a stable, owner-scoped hostname of the form
// <port>--<service>--<enclave>.<owner>.<base_domain>, with a primary alias of
// <service>--<enclave>.<owner>.<base_domain>. The left-most segment is a single
// DNS label so one wildcard certificate (*.<owner>.<base_domain>) covers every
// host.
type IngressConfig struct {
	// Enabled turns ingress creation on. When false, panda creates no Ingress
	// objects and the endpoints operation still computes hostnames for display.
	Enabled bool `yaml:"enabled"`

	// BaseDomain is the apex the per-owner subdomains hang off, e.g. "k3s.bruno"
	// (bruno) or "devnet.ethpandaops.io" (prod).
	BaseDomain string `yaml:"base_domain"`

	// IngressClass is the spec.ingressClassName set on created Ingresses.
	// Defaults to "traefik" when empty.
	IngressClass string `yaml:"ingress_class"`

	// Entrypoint is the Traefik entrypoint routers attach to (the
	// traefik.ingress.kubernetes.io/router.entrypoints annotation), e.g. "web"
	// (bruno, plain http) or "websecure" (prod). Defaults to "web" when empty.
	Entrypoint string `yaml:"entrypoint"`

	// TLSSecret is the wildcard TLS secret name to attach to every host. Empty
	// means plain http (bruno); a non-empty value switches computed endpoint
	// URLs to https.
	TLSSecret string `yaml:"tls_secret"`

	// AuthMiddleware is an optional Traefik middleware reference (e.g.
	// "devnet-forward-auth@kubernetescrd") added via the
	// traefik.ingress.kubernetes.io/router.middlewares annotation. Empty on
	// bruno; set in prod to enforce auth at the edge.
	AuthMiddleware string `yaml:"auth_middleware"`

	// LocalOwner is the owner label used when the request carries no
	// authenticated identity (bruno/lean dev). Never sourced from client args.
	LocalOwner string `yaml:"local_owner"`
}
