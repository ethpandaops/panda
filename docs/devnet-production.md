# Deploying `panda devnet` remote access to production

This guide takes the `panda devnet` feature (multi-client Kurtosis devnets +
owner-scoped external access to their services — RPC, dora, beacon API, …) from
the local **bruno** setup to the ethpandaops **platform**, reusing the platform's
existing building blocks instead of standing up new ones.

It assumes the feature from PR #213 (`services`, `logs`/`-f`, `endpoints`,
user-scoped Traefik `Ingress`, `devnet use`). For the local design and the
bruno setup see [`devnet.md`](./devnet.md).

## TL;DR — what's already there vs net-new

The platform already provides everything in the "Platform-provided" column; we
only add the "Net-new" pieces.

| Concern | Platform-provided | Net-new for devnets |
|---|---|---|
| GitOps | ArgoCD (root-app + ApplicationSet) | one ArgoCD `Application` for the Kurtosis engine, one for `panda-server` |
| Ingress | Traefik | — (panda creates the per-service `Ingress` objects at runtime) |
| DNS | Cloudflare (+ external-dns / terraform wildcards) | a record/wildcard for the devnet subdomain |
| Certs | cert-manager + a DNS-01 `ClusterIssuer` | reuse it (per-host, or a ZeroSSL issuer for churn — see TLS) |
| Auth | Dex/Keycloak OIDC; **hosted panda-proxy** at `panda-proxy.analytics.production.platform.ethpandaops.io` | a Traefik forward-auth middleware that ties the request to the `<owner>` host label |
| Kurtosis engine | — | GitOps engine on the prod cluster (mirror the bruno engine) |

## Architecture in production

```
panda CLI ──HTTPS──▶ panda-proxy (hosted, OIDC/Dex)         identity (GitHub login/ID)
                          │  derives AuthUser, forwards op
                          ▼
                    panda-server  (in-cluster: SA + `kurtosis gateway` sidecar)
                          │  Kurtosis SDK  +  k8s API (creates Ingress)
                          ▼
                 Kurtosis engine (GitOps)  ──▶  enclave namespace (EL/CL/VC/dora …)
                          ▲
   browser / cast / wallet ──HTTPS──▶ Traefik ──▶ forward-auth ──▶ Ingress ──▶ service
        dora.<owner>.<base> / el-1-….<enclave>.<owner>.<base>
```

Key alignment points:
- **panda-server runs in the prod cluster** (Deployment + ServiceAccount), with a
  `kurtosis gateway` sidecar to the in-cluster engine (`:9710`). That gives it the
  Kurtosis SDK connection *and* the k8s API access it needs to create `Ingress`
  objects. It does **not** need a kubeconfig file — an in-cluster SA + RBAC is
  enough.
- **Identity is server-derived from the hosted proxy** (`AuthUser` →
  GitHub login). The owner is never client-supplied; it's the multi-tenant
  boundary for both hostnames and authz.
- panda creates Ingresses **at runtime** per devnet; the platform's
  Traefik/cert-manager/DNS pick them up — no per-devnet GitOps changes.

## Required code changes (before prod)

- **Controller-agnostic ingress + cert-manager TLS — done (on PR #213).** The
  ingress is configured by `ingress_class` + a verbatim `annotations` map + a `tls`
  toggle; with `tls: true` and no fixed `tls_secret`, panda derives a per-Ingress
  secret name and cert-manager issues it from the `cluster-issuer` annotation. This
  is what lets prod use nginx + cert-manager instead of bruno's Traefik.
- **Owner label = GitHub login, not numeric ID — still TODO.** `authOwnerID`
  returns `user.GitHubID`; `AuthUser` also has `GitHubLogin`. Resolve the devnet
  owner to the login (in `resolveOwner`, leaving the global `authOwnerID` untouched)
  so hostnames read `dora.qu0b.<base>` not `dora.583231.<base>`. Keep the ID for
  authz comparisons.

## DNS + TLS: the one decision to make

Devnet hostnames are dotted and **user-scoped**:

```
<service>.<enclave>.<owner>.<base>          el-1-geth-lighthouse.bal3.qu0b.devnets.ethpandaops.io
<port>-<service>.<enclave>.<owner>.<base>   ws-el-1-geth-lighthouse.bal3.qu0b.devnets.ethpandaops.io
<service>.<owner>.<base>                    dora.qu0b.devnets.ethpandaops.io   (default-devnet alias)
```

`<base>` is platform-chosen (e.g. `devnets.ethpandaops.io`, or a `devnet.`
subdomain of the panda-proxy zone). A wildcard at any *single* level covers only
one label below it, so there are two ways to satisfy the dotted depth — pick per
how the platform already does DNS/certs:

- **Per-host (recommended, fully clean dotted, any depth):** let **external-dns**
  create an `A`/`CNAME` per Ingress host (→ Traefik LB) and **cert-manager**
  (ingress-shim) issue a cert per host. No flattening, no per-enclave wildcard
  management. Because devnets are ephemeral and churn fast, point the issuer at
  **ZeroSSL** (no Let's Encrypt 50/week limit) — the platform already runs this
  ACME flow for template-devnets (`ethpandaops.general.wildcard_cert_issuer`,
  ZeroSSL EAB + RFC2136). DNS-01 over Cloudflare works too if churn is low.
- **Per-owner wildcard (fewer certs, flattens multi-devnet):** one
  `*.<owner>.<base>` record + cert per user (cert-manager DNS-01/Cloudflare). This
  covers the clean default alias `dora.qu0b.<base>` and `<service>.<owner>`, but the
  enclave-qualified form must collapse to one label (`<service>--<enclave>.<owner>`)
  to stay under the wildcard. Cheapest on stock Cloudflare DNS-01 + Let's Encrypt.

Either way, keep the devnet records **DNS-only / grey-cloud** (not proxied) so
RPC, WebSocket and large `eth_getLogs`/trace responses bypass Cloudflare's body
and timeout limits.

## Auth: owner enforcement at the edge

The hostname carries `<owner>` and every Ingress is labeled
`panda.devnet/owner=<owner>`. Enforce that only that user reaches it with a
Traefik **forward-auth middleware** (referenced via `devnet.ingress.auth_middleware`,
e.g. `devnet-forward-auth@kubernetescrd`) that:
1. authenticates the caller through the platform's Dex/OIDC (same IdP as the proxy), and
2. checks the authenticated GitHub identity equals the `<owner>` segment of the host.

This closes the only residual gap from the feature review (the operation layer
derives the owner correctly but does not itself check enclave ownership — the
edge middleware is where prod enforces it).

## Kurtosis engine on the prod cluster

Mirror the bruno GitOps engine: an ArgoCD `Application` that deploys the Kurtosis
engine (+ logs aggregator/collector) into a `kurtosis-engine` namespace. Carry
forward the bruno learnings already in the fork:
- the grace-period patches (fast teardown) and the **enclave warm-pool self-heal**
  (so the pool refills after the logs components bootstrap),
- `KURTOSIS_IMAGE_ORG` / image cache pointed at the platform registry,
- a `WaitForFirstConsumer` storage class.

`panda-server` reaches it via the `kurtosis gateway` sidecar; no public exposure
of the engine itself.

## Config (prod `devnet.ingress`)

bruno → prod is config-only — no code or hostname-scheme change:

```yaml
cluster:
  name: <prod-kurtosis-cluster>
  kubeconfig_context: ""        # in-cluster SA; leave empty

devnet:
  package: github.com/ethpandaops/ethereum-package
  docker_cache: docker.ethquokkaops.io
  ingress:
    enabled: true
    base_domain: devnet.ethpandaops.io         # platform-chosen (delegated zone, option B)
    ingress_class: ingress-nginx-devnets       # the platform's devnet ingress controller
    tls: true                                  # per-Ingress secret, cert-manager issues it
    annotations:
      cert-manager.io/cluster-issuer: zerossl-devnet         # ZeroSSL DNS-01, no LE rate limits
      nginx.ingress.kubernetes.io/auth-url: https://…        # edge auth: authed user == <owner>
    # local_owner unset → owner = authenticated GitHub login
    # tls_secret / alias_tls_secret only if pre-provisioning wildcards instead
```

> The ingress is **controller-agnostic**: `ingress_class` + an `annotations` map
> (applied verbatim) + a `tls` toggle. On the platform that's nginx
> (`ingress-nginx-devnets`) rather than bruno's Traefik. With `tls: true` and no
> fixed `tls_secret`, panda derives a per-Ingress secret name and cert-manager
> issues the cert from the `cluster-issuer` annotation.

## Rollout

1. **Code:** merge PR #213 + the two changes above; tag a release → goreleaser
   builds the `panda` CLI and the `panda-server`/`panda-proxy` images.
2. **Infra (GitOps):** add ArgoCD `Application`s for the Kurtosis engine and
   `panda-server` (Deployment + SA + RBAC for Ingress + gateway sidecar). Add the
   cert-manager `ClusterIssuer` (ZeroSSL or reuse the Cloudflare DNS-01 one), the
   external-dns config (or the Cloudflare wildcard), and the Traefik forward-auth
   `Middleware`.
3. **Proxy:** point the hosted panda-proxy's devnet routes at the in-cluster
   `panda-server` (it already fronts panda for the org).
4. **CLI:** users install `panda` from the release and target the prod proxy URL.

## Validation

As a real GitHub user, through the proxy:
- `panda devnet up smoke --args ...` → `panda devnet endpoints smoke`
- `https://dora.qu0b.devnets.ethpandaops.io` loads in a browser with a valid cert
- `https://el-1-geth-lighthouse.smoke.qu0b.devnets.ethpandaops.io` serves
  `eth_blockNumber`; WS upgrades; a large `eth_getLogs` returns (no proxy limits)
- a **different** GitHub user gets `403` from the forward-auth middleware
- `panda devnet down smoke` removes the enclave (and its Ingresses with it)

## Rollback

`devnet.ingress.enabled: false` disables Ingress creation (the devnets still run;
only external access stops). Removing the engine/`panda-server` ArgoCD apps tears
the rest down; nothing is shared with the hosted proxy's existing datasource role.
