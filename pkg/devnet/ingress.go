package devnet

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/ethpandaops/panda/pkg/config"
)

// maxDNSLabel is the RFC 1035 limit on a single DNS label. Hosts are dotted —
// <service>.<enclave>.<owner>.<base> for a service's primary port and
// <port>-<service>.<enclave>.<owner>.<base> for the rest — so the enclave and
// owner are their own readable labels and a per-enclave wildcard certificate
// (*.<enclave>.<owner>.<base>) covers every host (the left-most label is the
// only variable part below it). The left-most label is shortened
// deterministically if it would exceed this limit (see shortenLabel).
const maxDNSLabel = 63

// PortEndpoint is a single exposed port's external URL.
type PortEndpoint struct {
	// Name is the port's logical name (e.g. "rpc", "ws", "metrics").
	Name string `json:"name"`
	// URL is the fully-qualified external URL for the port.
	URL string `json:"url"`
}

// ServiceEndpoints describes the external URLs panda assigns to one service:
// the primary URL (the headline one, e.g. an EL's rpc or dora's http) and the
// per-port URLs. It is computed purely from the service ports and ingress
// config — no cluster access — so it is unit-testable.
type ServiceEndpoints struct {
	// Service is the service name.
	Service string `json:"service"`
	// PrimaryURL points at the service's primary port (rpc > http > first http
	// app > first exposed). Empty if the service exposes no ports.
	PrimaryURL string `json:"primary_url"`
	// Ports are the per-port URLs, in the order the ports were exposed.
	Ports []PortEndpoint `json:"ports"`
}

// exposedPortNames is the set of port names panda exposes externally regardless
// of declared application protocol. Everything else (engine-rpc behind JWT, p2p,
// discovery, quic, raw udp) is deliberately left in-cluster only.
var exposedPortNames = map[string]bool{
	"rpc":     true,
	"ws":      true,
	"http":    true,
	"api":     true,
	"metrics": true,
}

// isExposed reports whether a port should be reachable from outside the cluster:
// any http/ws application port, or one of the well-known exposed names.
func isExposed(p Port) bool {
	if p.Application == "http" || p.Application == "ws" {
		return true
	}

	return exposedPortNames[p.Name]
}

// exposedPorts returns the service's externally-exposed ports, preserving the
// service's (name-sorted) port order.
func exposedPorts(svc Service) []Port {
	out := make([]Port, 0, len(svc.Ports))
	for _, p := range svc.Ports {
		if isExposed(p) {
			out = append(out, p)
		}
	}

	return out
}

// primaryPort picks the headline port among the exposed ones: a port named
// "rpc", else one named "http", else the first http application port, else the
// first exposed port. It returns false when there are no exposed ports.
func primaryPort(exposed []Port) (Port, bool) {
	if len(exposed) == 0 {
		return Port{}, false
	}

	for _, p := range exposed {
		if p.Name == "rpc" {
			return p, true
		}
	}
	for _, p := range exposed {
		if p.Name == "http" {
			return p, true
		}
	}
	for _, p := range exposed {
		if p.Application == "http" {
			return p, true
		}
	}

	return exposed[0], true
}

// sanitizeLabel reduces s to a valid DNS label: lowercased, with every run of
// disallowed characters collapsed to a single hyphen and leading/trailing
// hyphens stripped. A label that would exceed maxDNSLabel is shortened
// deterministically (see shortenLabel). An empty result becomes "x" so callers
// always get a usable label.
func sanitizeLabel(s string) string {
	s = strings.ToLower(s)

	var b strings.Builder
	lastHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		// Any other rune (including '-') becomes a single collapsed hyphen.
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	if len(out) > maxDNSLabel {
		out = shortenLabel(out)
	}

	return out
}

// shortenLabel deterministically compresses an over-long label to fit within
// maxDNSLabel by keeping a readable prefix and appending an fnv hash of the full
// label, so distinct long labels stay distinct. It uses hash/fnv (stable across
// runs), never math/rand.
func shortenLabel(label string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(label))
	suffix := fmt.Sprintf("-%08x", h.Sum32())

	prefix := label[:maxDNSLabel-len(suffix)]
	prefix = strings.TrimRight(prefix, "-")

	return prefix + suffix
}

// boundLabel shortens an over-long DNS label deterministically.
func boundLabel(label string) string {
	if len(label) > maxDNSLabel {
		return shortenLabel(label)
	}

	return label
}

// dotBase prefixes a non-empty base domain with a dot (base may itself be
// multi-label, e.g. "ethpandaops.io").
func dotBase(base string) string {
	if base == "" {
		return ""
	}

	return "." + base
}

// serviceHost builds a service port's hostname per the configured host style.
//
//	dotted (default; dev/self-hosted DNS that resolves any depth):
//	  <service>.<enclave>.<owner>.<base>            (primary port)
//	  <port>-<service>.<enclave>.<owner>.<base>     (other ports)
//	flat (prod behind a single-label wildcard / cloudflare tunnel + edge cert):
//	  <service>--<enclave>--<owner>.<base>          (primary port)
//	  <port>--<service>--<enclave>--<owner>.<base>  (other ports)
//
// Flat folds everything into one DNS label so it sits one level under the apex
// (what a *.<base> wildcard cert / tunnel rule covers).
func serviceHost(portName, service, enclave, owner string, primary bool, cfg config.IngressConfig) string {
	if cfg.HostStyle == "flat" {
		parts := make([]string, 0, 4)
		if !primary {
			parts = append(parts, sanitizeLabel(portName))
		}
		parts = append(parts, sanitizeLabel(service), sanitizeLabel(enclave), sanitizeLabel(owner))

		return boundLabel(strings.Join(parts, "--")) + dotBase(cfg.BaseDomain)
	}

	left := sanitizeLabel(service)
	if !primary {
		left = boundLabel(sanitizeLabel(portName) + "-" + sanitizeLabel(service))
	}

	return left + "." + sanitizeLabel(enclave) + "." + sanitizeLabel(owner) + dotBase(cfg.BaseDomain)
}

// scheme returns the URL scheme implied by the ingress config for canonical
// hosts: https when TLS is enabled (or a fixed secret is pinned), http otherwise.
func scheme(cfg config.IngressConfig) string {
	if cfg.TLS || cfg.TLSSecret != "" {
		return "https"
	}

	return "http"
}

// copyAnnotations returns a fresh copy of the configured ingress annotations so
// callers never mutate the shared config map. These are applied verbatim to
// every Ingress, making panda controller-agnostic (Traefik, nginx, …) and the
// hook for cert-manager and edge auth.
func copyAnnotations(cfg config.IngressConfig) map[string]string {
	out := make(map[string]string, len(cfg.Annotations))
	for k, v := range cfg.Annotations {
		out[k] = v
	}

	return out
}

// Endpoints computes the external URLs for each service that exposes at least
// one port, given the enclave name, the resolved owner, and the ingress config.
// It performs no cluster access and is the source of truth shared by the
// endpoints operation and ingress reconciliation. Services with no exposed
// ports are omitted.
func Endpoints(services []Service, enclaveName, owner string, cfg config.IngressConfig) []ServiceEndpoints {
	sch := scheme(cfg)

	out := make([]ServiceEndpoints, 0, len(services))
	for _, svc := range services {
		exposed := exposedPorts(svc)
		if len(exposed) == 0 {
			continue
		}

		primary, hasPrimary := primaryPort(exposed)

		ports := make([]PortEndpoint, 0, len(exposed))
		primaryURL := ""
		for _, p := range exposed {
			isPrimary := hasPrimary && p.Name == primary.Name && p.Number == primary.Number
			h := serviceHost(p.Name, svc.Name, enclaveName, owner, isPrimary, cfg)
			url := sch + "://" + h
			ports = append(ports, PortEndpoint{Name: p.Name, URL: url})
			if isPrimary {
				primaryURL = url
			}
		}

		out = append(out, ServiceEndpoints{
			Service:    svc.Name,
			PrimaryURL: primaryURL,
			Ports:      ports,
		})
	}

	return out
}

// aliasLabel marks an ingress as a short default-devnet alias, so the alias can
// be moved atomically across a user's enclaves.
const aliasLabel = "panda.devnet/alias"

// aliasScheme returns the URL scheme for alias hosts: https when TLS is enabled
// (or a fixed alias secret is pinned), http otherwise.
func aliasScheme(cfg config.IngressConfig) string {
	if cfg.TLS || cfg.AliasTLSSecret != "" {
		return "https"
	}

	return "http"
}

// aliasHostname builds the short default-devnet alias host for a service per the
// configured host style: dotted "<service>.<owner>.<base>" (e.g.
// "dora.qu0b.k3s.bruno") or flat "<service>--<owner>.<base>" (one label, e.g.
// "dora--qu0b.ethpandaops.io").
func aliasHostname(service, owner string, cfg config.IngressConfig) string {
	if cfg.HostStyle == "flat" {
		return boundLabel(sanitizeLabel(service)+"--"+sanitizeLabel(owner)) + dotBase(cfg.BaseDomain)
	}

	return sanitizeLabel(service) + "." + sanitizeLabel(owner) + dotBase(cfg.BaseDomain)
}

// AliasEndpoints computes the short default-devnet alias URLs — one per service
// with a primary port: <service>.<owner>.<base>. These resolve to the owner's
// current default devnet (the most recent 'up', or one pinned with 'devnet use').
func AliasEndpoints(services []Service, owner string, cfg config.IngressConfig) []ServiceEndpoints {
	sch := aliasScheme(cfg)

	out := make([]ServiceEndpoints, 0, len(services))
	for _, svc := range services {
		if _, ok := primaryPort(exposedPorts(svc)); !ok {
			continue
		}
		url := sch + "://" + aliasHostname(svc.Name, owner, cfg)
		out = append(out, ServiceEndpoints{
			Service:    svc.Name,
			PrimaryURL: url,
			Ports:      []PortEndpoint{{Name: "primary", URL: url}},
		})
	}

	return out
}

// SetDefaultAlias makes the given enclave the owner's default devnet: it removes
// any existing short-alias ingresses for the owner (across all namespaces), then
// creates <service>.<owner>.<base> alias ingresses (primary port) in this
// enclave's namespace. This is how 'up' (newest wins) and 'panda devnet use'
// point an owner's bare hostnames at exactly one devnet.
func SetDefaultAlias(ctx context.Context, enclaveUUID, owner string, services []Service, cfg config.IngressConfig) error {
	clientset, err := newK8sClient()
	if err != nil {
		return err
	}

	namespace, err := enclaveNamespace(ctx, clientset, enclaveUUID)
	if err != nil {
		return err
	}

	if err := clearAliasIngresses(ctx, clientset, owner); err != nil {
		return err
	}

	for _, svc := range services {
		ingress := buildAliasIngress(namespace, enclaveUUID, owner, svc, cfg)
		if ingress == nil {
			continue
		}
		if err := upsertIngress(ctx, clientset, namespace, ingress); err != nil {
			return fmt.Errorf("creating alias ingress for service %q: %w", svc.Name, err)
		}
	}

	return nil
}

// clearAliasIngresses deletes every panda alias ingress for the owner across all
// namespaces, so the default alias only ever resolves to one devnet.
func clearAliasIngresses(ctx context.Context, clientset kubernetes.Interface, owner string) error {
	selector := fmt.Sprintf("app.kubernetes.io/managed-by=panda,%s=true,panda.devnet/owner=%s",
		aliasLabel, sanitizeLabel(owner))

	list, err := clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("listing alias ingresses for owner %q: %w", owner, err)
	}

	for i := range list.Items {
		ing := &list.Items[i]
		if err := clientset.NetworkingV1().Ingresses(ing.Namespace).Delete(ctx, ing.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting alias ingress %s/%s: %w", ing.Namespace, ing.Name, err)
		}
	}

	return nil
}

// buildAliasIngress constructs the short-alias Ingress for one service (host
// <service>.<owner>.<base> -> primary port), or nil when the service has no
// primary port.
func buildAliasIngress(namespace, enclaveUUID, owner string, svc Service, cfg config.IngressConfig) *networkingv1.Ingress {
	primary, ok := primaryPort(exposedPorts(svc))
	if !ok {
		return nil
	}

	pathType := networkingv1.PathTypePrefix
	ingressClass := cfg.IngressClass
	h := aliasHostname(svc.Name, owner, cfg)

	annotations := copyAnnotations(cfg)

	spec := networkingv1.IngressSpec{
		IngressClassName: &ingressClass,
		Rules: []networkingv1.IngressRule{{
			Host: h,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     "/",
						PathType: &pathType,
						Backend: networkingv1.IngressBackend{
							Service: &networkingv1.IngressServiceBackend{
								Name: svc.Name,
								Port: networkingv1.ServiceBackendPort{Number: int32(primary.Number)},
							},
						},
					}},
				},
			},
		}},
	}
	if cfg.TLS || cfg.AliasTLSSecret != "" {
		secret := cfg.AliasTLSSecret
		if secret == "" {
			secret = "panda-alias-" + sanitizeLabel(svc.Name) + "-tls"
		}
		spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{h}, SecretName: secret}}
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "panda-alias-" + sanitizeLabel(svc.Name),
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "panda",
				"panda.devnet/owner":           sanitizeLabel(owner),
				"panda.devnet/enclave":         enclaveUUID,
				aliasLabel:                     "true",
			},
			Annotations: annotations,
		},
		Spec: spec,
	}
}

// ReconcileIngresses upserts one Traefik Ingress per service (that exposes
// ports) in the enclave's Kubernetes namespace, making each devnet service
// reachable at its stable owner-scoped hostname. It is idempotent: existing
// Ingresses are updated in place. Teardown is handled by namespace deletion on
// 'down' — the Ingresses live in the enclave namespace — so there is no explicit
// delete; the owner/enclave labels let them be selected later if needed.
func ReconcileIngresses(ctx context.Context, enclaveUUID, enclaveName, owner string, services []Service, cfg config.IngressConfig) error {
	clientset, err := newK8sClient()
	if err != nil {
		return err
	}

	namespace, err := enclaveNamespace(ctx, clientset, enclaveUUID)
	if err != nil {
		return err
	}

	for _, svc := range services {
		ingress := buildIngress(namespace, enclaveUUID, enclaveName, owner, svc, cfg)
		if ingress == nil {
			// No exposed ports — nothing to route.
			continue
		}

		if err := upsertIngress(ctx, clientset, namespace, ingress); err != nil {
			return fmt.Errorf("reconciling ingress for service %q: %w", svc.Name, err)
		}
	}

	return nil
}

// buildIngress constructs the Ingress object for one service, or nil when the
// service exposes no ports. It creates one rule per exposed port plus a primary
// alias rule, attaches the configured Traefik entrypoint (and optional auth
// middleware), and adds a single TLS entry covering every host when a TLS
// secret is configured.
func buildIngress(namespace, enclaveUUID, enclaveName, owner string, svc Service, cfg config.IngressConfig) *networkingv1.Ingress {
	exposed := exposedPorts(svc)
	if len(exposed) == 0 {
		return nil
	}

	pathType := networkingv1.PathTypePrefix
	ingressClass := cfg.IngressClass

	rules := make([]networkingv1.IngressRule, 0, len(exposed)+1)
	hosts := make([]string, 0, len(exposed)+1)

	rule := func(h string, portNumber uint16) networkingv1.IngressRule {
		return networkingv1.IngressRule{
			Host: h,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:     "/",
						PathType: &pathType,
						Backend: networkingv1.IngressBackend{
							Service: &networkingv1.IngressServiceBackend{
								Name: svc.Name,
								Port: networkingv1.ServiceBackendPort{Number: int32(portNumber)},
							},
						},
					}},
				},
			},
		}
	}

	primary, hasPrimary := primaryPort(exposed)
	for _, p := range exposed {
		// The primary port gets the clean <service>.<enclave>.<owner>.<base>
		// host; other ports get a <port>-<service> left label.
		isPrimary := hasPrimary && p.Name == primary.Name && p.Number == primary.Number
		h := serviceHost(p.Name, svc.Name, enclaveName, owner, isPrimary, cfg)
		hosts = append(hosts, h)
		rules = append(rules, rule(h, p.Number))
	}

	annotations := copyAnnotations(cfg)

	spec := networkingv1.IngressSpec{
		IngressClassName: &ingressClass,
		Rules:            rules,
	}
	if cfg.TLS || cfg.TLSSecret != "" {
		secret := cfg.TLSSecret
		if secret == "" {
			secret = "panda-" + sanitizeLabel(svc.Name) + "-tls"
		}
		spec.TLS = []networkingv1.IngressTLS{{Hosts: hosts, SecretName: secret}}
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "panda-" + sanitizeLabel(svc.Name),
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "panda",
				"panda.devnet/owner":           sanitizeLabel(owner),
				"panda.devnet/enclave":         enclaveUUID,
			},
			Annotations: annotations,
		},
		Spec: spec,
	}
}

// upsertIngress creates the Ingress, or updates it in place when it already
// exists (carrying over the live ResourceVersion so the update is accepted).
func upsertIngress(ctx context.Context, clientset kubernetes.Interface, namespace string, ingress *networkingv1.Ingress) error {
	api := clientset.NetworkingV1().Ingresses(namespace)

	existing, err := api.Get(ctx, ingress.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := api.Create(ctx, ingress, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating ingress %q: %w", ingress.Name, err)
		}

		return nil
	}
	if err != nil {
		return fmt.Errorf("getting ingress %q: %w", ingress.Name, err)
	}

	ingress.ResourceVersion = existing.ResourceVersion
	if _, err := api.Update(ctx, ingress, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating ingress %q: %w", ingress.Name, err)
	}

	return nil
}
