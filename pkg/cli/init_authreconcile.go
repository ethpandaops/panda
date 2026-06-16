package cli

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// reconcileProxyAuth updates the proxy.auth block of an existing config file to
// match the auth settings discovered from the proxy, when they have drifted
// (e.g. the proxy switched OIDC issuers). Only issuer_url, client_id and mode
// are touched; all other config and comments are preserved. It returns true
// when the file was modified.
//
// This exists because 'panda init' does not overwrite an existing config.yaml
// without --force, so an issuer change on the proxy would otherwise leave the
// local config pinned to a stale issuer, and 'panda auth login' resolves the
// issuer from that config before consulting the proxy.
func reconcileProxyAuth(path string, auth initAuthConfig) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("reading config %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parsing config %s: %w", path, err)
	}

	authNode := proxyAuthNode(&doc)
	if authNode == nil {
		// No proxy.auth block to reconcile (unusual for an init-generated
		// config); leave the file untouched.
		return false, nil
	}

	// The config template omits mode when it is the legacy "oauth" default,
	// so treat "oauth" as "no mode key".
	desiredMode := strings.TrimSpace(auth.Mode)
	if desiredMode == "oauth" {
		desiredMode = ""
	}

	changed := setMappingScalar(authNode, "issuer_url", auth.IssuerURL)

	if setMappingScalar(authNode, "client_id", auth.ClientID) {
		changed = true
	}

	if desiredMode == "" {
		if removeMappingKey(authNode, "mode") {
			changed = true
		}
	} else if setMappingScalar(authNode, "mode", desiredMode) {
		changed = true
	}

	if !changed {
		return false, nil
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return false, fmt.Errorf("encoding config %s: %w", path, err)
	}

	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("writing config %s: %w", path, err)
	}

	return true, nil
}

// proxyAuthNode returns the mapping node for proxy.auth, or nil when it is
// absent or not a mapping.
func proxyAuthNode(doc *yaml.Node) *yaml.Node {
	root := doc
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}

	if root.Kind != yaml.MappingNode {
		return nil
	}

	proxyNode := mappingValue(root, "proxy")
	if proxyNode == nil || proxyNode.Kind != yaml.MappingNode {
		return nil
	}

	authNode := mappingValue(proxyNode, "auth")
	if authNode == nil || authNode.Kind != yaml.MappingNode {
		return nil
	}

	return authNode
}

// mappingValue returns the value node for key in a mapping node, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}

	return nil
}

// setMappingScalar sets key=value in a mapping, appending the key when absent.
// It returns true when the mapping changed.
func setMappingScalar(m *yaml.Node, key, value string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}

		if m.Content[i+1].Value == value {
			return false
		}

		m.Content[i+1].Value = value
		m.Content[i+1].Tag = "!!str"
		m.Content[i+1].Style = yaml.DoubleQuotedStyle

		return true
	}

	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle},
	)

	return true
}

// removeMappingKey deletes key (and its value) from a mapping. It returns true
// when a key was removed.
func removeMappingKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)

			return true
		}
	}

	return false
}
