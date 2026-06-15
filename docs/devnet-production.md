# Deploying `panda devnet` remote access to production

This guide takes the `panda devnet` feature (multi-client Kurtosis devnets +
owner-scoped external access to their services — RPC, dora, beacon API, …) from
the local **bruno** setup to the ethpandaops **platform**.

The guiding principle is **maintainability**: reuse the platform's existing
building blocks and add as few new, stateful components as possible. The design
below adds exactly **two** GitOps apps (the Kurtosis engine and `panda-server`)
and **nothing else** on the DNS/cert/tunnel side — devnet hosts ride the
platform's existing `*.ethpandaops.io` Cloudflare tunnel + edge cert + nginx.

It assumes the feature from PR #213 (`services`, `logs`/`-f`, `endpoints`,
user-scoped `Ingress`, `devnet use`, `host_style`). For the local design and the
bruno setup see [`devnet.md`](./devnet.md).

## The hostname decision (why flat, not a self-hosted zone)

Devnet hosts are user-scoped. The depth question is: how do we get TLS + routing
for them without standing up new infrastructure?

- A self-hosted authoritative DNS zone (RFC2136) + a ZeroSSL DNS-01 issuer would
  give clean **dotted** names at any depth — but it's a new stateful DNS server,
  a new ACME account, TSIG secrets, and an NS delegation to operate **forever**.
  That's a lot of maintenance surface for cosmetic hostnames. **Rejected.**
- The platform already routes `*.ethpandaops.io` through a Cloudflare tunnel to
  `ingress-nginx-devnets`, with TLS terminated at the Cloudflare edge by the
  universal-SSL cert. That wildcard covers exactly **one label** under the apex.

So in prod panda uses **`host_style: flat`** — it folds service/enclave/owner into
a single DNS label:

```
<service>--<enclave>--<owner>.ethpandaops.io          dora--bal3--qu0b.ethpandaops.io
<port>--<service>--<enclave>--<owner>.ethpandaops.io  ws--el-1-geth-lighthouse--bal3--qu0b.ethpandaops.io
<service>--<owner>.ethpandaops.io                     dora--qu0b.ethpandaops.io   (default-devnet alias)
```

One label ⇒ covered by the **existing** wildcard cert + tunnel rule + nginx. The
cost is `--`-separated names instead of dotted ones; the benefit is **zero new
DNS/cert/tunnel components**. Maintainability wins.

## TL;DR — what's already there vs net-new

| Concern | Platform-provided | Net-new for devnets |
|---|---|---|
| GitOps | ArgoCD (root-app + ApplicationSet) | one `Application` for the Kurtosis engine, one for `panda-server` |
| Ingress | ingress-nginx-devnets | — (panda creates per-service `Ingress` objects at runtime) |
| DNS | Cloudflare `*.ethpandaops.io` → tunnel | — (flat hosts fall under the existing wildcard) |
| Certs | Cloudflare edge universal-SSL on `*.ethpandaops.io` | — (TLS terminates at the edge; panda Ingresses serve HTTP) |
| Tunnel | cloudflare-tunnel-devnets `*.ethpandaops.io` → nginx | — (flat hosts match the existing rule) |
| Auth | Dex/OIDC; **hosted panda-proxy** | gate the create/manage path at panda-server; (optional) Cloudflare Access at the edge for service hosts |
| Kurtosis engine | — | GitOps engine on the analytics cluster (mirror the bruno engine) |

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
   browser / cast / wallet ──HTTPS──▶ Cloudflare edge ──tunnel──▶ ingress-nginx-devnets ──▶ service
        dora--bal3--qu0b.ethpandaops.io / el-1-…--bal3--qu0b.ethpandaops.io
```

Key alignment points:
- **panda-server runs in the analytics cluster** (Deployment + ServiceAccount),
  with a `kurtosis gateway` sidecar to the in-cluster engine (`:9710`). That gives
  it the Kurtosis SDK connection *and* the k8s API access to create `Ingress`
  objects. No kubeconfig file — an in-cluster SA + RBAC is enough.
- **Identity is server-derived from the hosted proxy** (`AuthUser` → GitHub
  login). The owner is never client-supplied; it's the multi-tenant namespace.
- panda creates Ingresses **at runtime** per devnet; the existing tunnel + nginx
  pick them up by Host match — **no per-devnet GitOps, DNS or cert changes**.

## Required code changes — done (on PR #213)

- **Controller-agnostic ingress.** `ingress_class` + a verbatim `annotations` map
  + a `tls` toggle, so prod uses nginx with `tls: false` (edge TLS).
- **`host_style: flat`.** Single-label hosts that fall under `*.ethpandaops.io`.
- **Owner label = GitHub login** (`resolveOwner` prefers `GitHubLogin`), so hosts
  read `dora--…--qu0b` not `dora--…--583231`. Numeric ID kept for authz.

## Auth

- **Create / manage path (the important one):** gated at **panda-server** via the
  hosted proxy's OIDC (GitHub-org membership) — only authorized members can spin
  up enclaves. This is the "only authorized people" boundary.
- **Service hosts (dora/RPC/beacon):** carry **no per-Ingress auth**, so `cast`,
  wallets and scripts reach RPC without an interactive browser SSO flow. If the
  org wants these gated too, do it **uniformly at the edge with Cloudflare Access**
  (which supports service tokens for programmatic RPC) — not a bespoke per-owner
  forward-auth service to build and maintain. The `<owner>` label remains the
  namespace; edge access policy is a platform-level config, not panda's concern.

## Kurtosis engine on the analytics cluster

Mirror the bruno GitOps engine (`applications/kurtosis-engine/`): an ArgoCD
`Application` deploying the engine (+ logs components) into a `kurtosis-engine`
namespace, carrying the fork's bruno learnings — grace-period fast teardown, the
enclave warm-pool self-heal, the v9 `SERIALIZED_ARGS` with `poolSize: 2`, and a
`WaitForFirstConsumer` storage class.

**The one real platform dependency:** the engine + APIC + files-artifacts-expander
+ logs images are a **fork** (they carry the patches above). They must be hosted
where the analytics cluster can pull them — build them through the platform's
existing image pipeline and set `engine.image` / `engine.imageOrg` accordingly.
The bruno build lives on a private registry the platform can't reach.

`panda-server` reaches the engine via the `kurtosis gateway` sidecar; the engine
itself is never publicly exposed.

## Config (prod `devnet.ingress`)

bruno → prod is config-only — no code or hostname-scheme change:

```yaml
cluster:
  name: ""                      # in-cluster SA + gateway sidecar; leave empty
  kubeconfig_context: ""

devnet:
  package: github.com/ethpandaops/ethereum-package
  docker_cache: docker.ethquokkaops.io
  ingress:
    enabled: true
    host_style: flat                           # single label fits *.ethpandaops.io
    base_domain: ethpandaops.io
    ingress_class: ingress-nginx-devnets       # the platform's devnet ingress controller
    tls: false                                 # TLS terminates at the Cloudflare edge
    # local_owner unset → owner = authenticated GitHub login
```

> No `cert-manager.io/cluster-issuer`, no DNS-01 issuer, no TSIG, no NS
> delegation, no `tls_secret`. The Cloudflare edge cert on `*.ethpandaops.io`
> serves TLS; the existing tunnel rule routes to nginx; nginx matches the flat
> Host. That's the whole maintainability payoff.

## Rollout

1. **Code:** merge PR #213; tag a release → goreleaser builds the `panda` CLI and
   the `panda-server`/`panda-proxy` images.
2. **Fork images:** build the kurtosis-engine fork images via the platform image
   pipeline; set `engine.image`/`engine.imageOrg` in `applications/kurtosis-engine/`.
3. **Infra (GitOps):** the two ArgoCD `Application`s — `kurtosis-engine` and
   `panda-server` (Deployment + SA + RBAC for Ingress + gateway sidecar) — are in
   the platform PR. Nothing to add on DNS/cert/tunnel.
4. **Proxy:** point the hosted panda-proxy's devnet routes at the in-cluster
   `panda-server`.
5. **CLI:** users install `panda` from the release and target the prod proxy URL.

## Validation

As a real GitHub user, through the proxy:
- `panda devnet up smoke --args ...` → `panda devnet endpoints smoke`
- `https://dora--smoke--qu0b.ethpandaops.io` loads in a browser with a valid cert
- `https://el-1-geth-lighthouse--smoke--qu0b.ethpandaops.io` serves
  `eth_blockNumber`; WS upgrades; a large `eth_getLogs` returns
- `panda devnet down smoke` removes the enclave (and its Ingresses with it)

## Rollback

`devnet.ingress.enabled: false` disables Ingress creation (the devnets still run;
only external access stops). Removing the engine/`panda-server` ArgoCD apps tears
the rest down; nothing is shared with the hosted proxy's existing datasource role.
