package devnet

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

// kurtosisClusterSettingPath returns the path to Kurtosis's cluster-setting
// file, which holds the name of the currently selected cluster. Kurtosis stores
// it under its XDG data dir as a plain one-line cluster name.
func kurtosisClusterSettingPath() string {
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".local", "share")
		} else {
			base = filepath.Join(".local", "share")
		}
	}

	return filepath.Join(base, "kurtosis", "cluster-setting")
}

// ActiveCluster returns the Kurtosis cluster currently selected, or "" if none
// has been selected yet.
func ActiveCluster() (string, error) {
	data, err := os.ReadFile(kurtosisClusterSettingPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}

		return "", fmt.Errorf("reading Kurtosis cluster setting: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

// EnsureCluster makes Kurtosis target the cluster named by target, which selects
// the backend (Docker vs Kubernetes). An empty target is a no-op.
//
// If no cluster has been selected yet it writes the setting. If the same cluster
// is already selected it does nothing. If a *different* cluster is already
// selected it refuses to switch silently — a running engine is bound to that
// cluster, so switching needs an engine restart — and returns actionable
// guidance instead.
func EnsureCluster(target string, out io.Writer) error {
	if target == "" {
		return nil
	}

	active, err := ActiveCluster()
	if err != nil {
		return err
	}

	switch active {
	case target:
		fmt.Fprintf(out, "Kurtosis cluster: %s\n", target)

		return nil
	case "":
		if err := writeClusterSetting(target); err != nil {
			return err
		}
		fmt.Fprintf(out, "Set Kurtosis cluster to %q.\n", target)

		return nil
	default:
		return fmt.Errorf(
			"panda is configured for Kurtosis cluster %q but Kurtosis is currently set to %q.\n"+
				"Switch with: kurtosis cluster set %s && kurtosis engine restart\n"+
				"(for a Kubernetes backend, also keep `kurtosis gateway` running in another terminal)",
			target, active, target,
		)
	}
}

// EnsureKubeContext selects the given kubeconfig context as the current one, so
// Kurtosis (which connects through the kubeconfig's current context) targets the
// intended cluster. An empty context is a no-op.
//
// Note: a running engine/gateway is bound to the context that was current when
// it started; changing the context here takes effect on the next engine start.
func EnsureKubeContext(contextName string, out io.Writer) error {
	if contextName == "" {
		return nil
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := rules.Load()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}

	if _, ok := cfg.Contexts[contextName]; !ok {
		return fmt.Errorf("kubeconfig has no context %q", contextName)
	}

	if cfg.CurrentContext == contextName {
		return nil
	}

	cfg.CurrentContext = contextName
	if err := clientcmd.ModifyConfig(rules, *cfg, true); err != nil {
		return fmt.Errorf("selecting kubeconfig context %q: %w", contextName, err)
	}

	fmt.Fprintf(out, "Selected kubeconfig context %q (restart the engine if it was running on another).\n", contextName)

	return nil
}

func writeClusterSetting(name string) error {
	path := kurtosisClusterSettingPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating Kurtosis data dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
		return fmt.Errorf("writing Kurtosis cluster setting: %w", err)
	}

	return nil
}
