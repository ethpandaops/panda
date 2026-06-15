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
// service port becomes reachable at a stable, owner-scoped, dotted hostname:
// <service>.<enclave>.<owner>.<base_domain> for the primary port and
// <port>-<service>.<enclave>.<owner>.<base_domain> for the rest, so a per-enclave
// wildcard certificate (*.<enclave>.<owner>.<base_domain>) covers every host.
// The owner's default devnet additionally gets a short alias
// <service>.<owner>.<base_domain> (covered by a per-owner wildcard).
type IngressConfig struct {
	// Enabled turns ingress creation on. When false, panda creates no Ingress
	// objects and the endpoints operation still computes hostnames for display.
	Enabled bool `yaml:"enabled"`

	// BaseDomain is the apex the per-owner subdomains hang off, e.g. "k3s.bruno"
	// (bruno) or "devnet.ethpandaops.io" (prod).
	BaseDomain string `yaml:"base_domain"`

	// IngressClass is the spec.ingressClassName set on created Ingresses, e.g.
	// "traefik" (bruno) or "ingress-nginx-devnets" (prod). Defaults to "traefik".
	IngressClass string `yaml:"ingress_class"`

	// Annotations are applied verbatim to every Ingress panda creates. This is
	// the controller-agnostic hook for routing, TLS issuance and edge auth — set
	// whatever the chosen ingress controller / cert-manager / auth layer needs,
	// e.g.
	//   traefik: {"traefik.ingress.kubernetes.io/router.entrypoints": "web"}
	//   nginx:   {"nginx.ingress.kubernetes.io/auth-url": "...", "cert-manager.io/cluster-issuer": "zerossl-devnet"}
	Annotations map[string]string `yaml:"annotations"`

	// TLS, when true, makes panda emit a TLS section on every Ingress so the edge
	// (or cert-manager) serves https and computed URLs use https. With TLSSecret
	// empty, a per-Ingress secret name is derived (cert-manager issues it via the
	// issuer named in Annotations). Empty/false means plain http (bruno).
	TLS bool `yaml:"tls"`

	// TLSSecret optionally pins a fixed (e.g. pre-provisioned wildcard) secret for
	// the canonical hosts instead of a per-Ingress cert-manager secret. Setting it
	// implies TLS.
	TLSSecret string `yaml:"tls_secret"`

	// AliasTLSSecret optionally pins a fixed secret (e.g. a per-owner wildcard
	// *.<owner>.<base>) for the short default-devnet alias hosts. The alias hangs
	// one label higher than the canonical hosts, so it may need a different cert.
	// Setting it implies TLS for the alias.
	AliasTLSSecret string `yaml:"alias_tls_secret"`

	// LocalOwner is the owner label used when the request carries no
	// authenticated identity (bruno/lean dev). Never sourced from client args.
	LocalOwner string `yaml:"local_owner"`
}
