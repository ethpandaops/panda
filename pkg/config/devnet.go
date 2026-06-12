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
}
