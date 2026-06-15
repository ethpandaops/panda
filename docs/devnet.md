# Devnets on Kubernetes (`panda devnet`)

`panda devnet` spins up multi-client Ethereum devnets as Kurtosis enclaves on a
Kubernetes cluster. The CLI dispatches operations to the local panda server,
which holds the Kurtosis engine connection and drives the
[ethereum-package](https://github.com/ethpandaops/ethereum-package).

```
panda CLI  ──HTTP──▶  panda server (local)  ──Kurtosis SDK──▶  Kurtosis engine ──▶ k8s
```

## Two rails, one switch

panda uses **one cluster at a time**. You keep a local rail (a local k3s/k8s for
fast iteration) and a cloud rail, and switch by editing the top-level `cluster:`
block in the panda config — or by pointing panda at a different config file.

```yaml
cluster:
  name: bruno                 # the Kurtosis cluster to use
  kubeconfig_context: bruno   # kube context Kurtosis connects through

devnet:
  package: github.com/ethpandaops/ethereum-package
  docker_cache: docker.ethquokkaops.io   # avoids Docker Hub rate limits
```

Switch to the cloud rail:

```yaml
cluster:
  name: cloud
  kubeconfig_context: ethpandaops-cloud
```

On each devnet operation the server:

1. selects `kubeconfig_context` as the current kube context (so Kurtosis targets
   the right cluster), and
2. activates the Kurtosis cluster named `name`.

If you switch clusters while an engine is already running, restart it
(`kurtosis engine restart`) so it rebinds to the new context.

## Why there's no `storage_class` in panda config

The storage class (and enclave size) are **engine-level** settings: Kurtosis
fixes them when the engine starts and the SDK can't override them per run. They
therefore live in Kurtosis's own config, once per cluster, not in panda. A
cluster usually has several storage classes and the *binding mode* matters —
`WaitForFirstConsumer` classes (e.g. `local-path`, EBS `gp3`, GKE
`pd-balanced`) work, while `Immediate`-binding classes (e.g. `longhorn`) break
Kurtosis's pod-scheduling wait. Pick a `WaitForFirstConsumer` class.

## One-time per-cluster setup (Kurtosis side)

Each cluster needs a Kurtosis cluster entry (`~/.config/kurtosis/kurtosis-config.yml`)
and a reachable engine. For a local k3s named `bruno`:

```yaml
# ~/.config/kurtosis/kurtosis-config.yml
kurtosis-clusters:
  docker:
    type: "docker"
  bruno:
    type: "kubernetes"
    config:
      kubernetes-cluster-name: "bruno"
      storage-class: "local-path"        # a WaitForFirstConsumer class on this cluster
      enclave-size-in-megabytes: 1024
```

Then start the engine and a gateway (the gateway exposes the in-cluster engine
on `localhost:9710`, where the SDK connects):

```bash
kurtosis cluster set bruno
kurtosis engine start
kurtosis gateway        # keep running in a separate terminal
```

Add a `cloud` entry the same way for the cloud rail.

## Usage

```bash
panda devnet up my-devnet --args ./network_params.yaml
panda devnet ls
panda devnet inspect my-devnet
panda devnet services my-devnet                       # list services + ports
panda devnet logs my-devnet                           # recent logs, all services
panda devnet logs my-devnet el-1-geth-lighthouse --tail 500
panda devnet logs my-devnet -f                         # follow all services live
panda devnet logs my-devnet el-1-geth-lighthouse -f    # follow one (Ctrl-C to stop)
panda devnet endpoints my-devnet                       # external URLs per service
panda devnet use my-devnet                             # make it your default (short URLs)
panda devnet down my-devnet        # or: panda devnet down --all
```

`services` and `logs` go through the panda server (which holds the cluster
connection), so they work wherever `panda devnet ls` works — including remotely
through the cloud proxy — without needing the `kurtosis` CLI or a gateway
locally. Logs are read straight from the pods, so they're available even though
this fork ships container logs to OTel/ClickHouse (which leaves the engine's
log API empty). `-f` streams chunked text live; non-`-f` rides the plain
request/response operation path.

## External access to services (RPC, dora, …)

When `devnet.ingress.enabled` is set, `up` creates a Traefik `Ingress` per HTTP/WS
service port so each is reachable at a stable, **GitHub-user-scoped** hostname:

```
<service>.<enclave>.<owner>.<base>             # primary port, e.g. dora.my-devnet.qu0b.k3s.bruno
                                               #                    el-1-geth-lighthouse.my-devnet.qu0b.k3s.bruno (rpc)
<port>-<service>.<enclave>.<owner>.<base>      # other ports, e.g. ws-el-1-geth-lighthouse.my-devnet.qu0b.k3s.bruno
```

Names are clean dotted labels. This works because the devnet zone is served by a
self-hosted **authoritative DNS** (on bruno, dnsmasq's `*.k3s.bruno` wildcard
already resolves at any depth; in prod, NS-delegate the zone to a self-hosted DNS
like the `ethpandaops.general.dns_server` role) and certs come from **ZeroSSL
DNS-01** (no Let's Encrypt rate limits) via cert-manager — a per-enclave wildcard
`*.<enclave>.<owner>.<base>` covers every host above, since the left-most label is
the only variable part below it.

Your **default devnet** also gets short, enclave-less aliases
`<service>.<owner>.<base>` (e.g. `dora.qu0b.k3s.bruno`). The newest `panda devnet
up` becomes the default; `panda devnet use <enclave>` switches it back to an
earlier one. The enclave-qualified URLs keep working for every devnet regardless.
(The alias hangs one label higher, so in prod it uses a per-owner wildcard —
`alias_tls_secret` / `*.<owner>.<base>` — separate from the per-enclave cert.)

`panda devnet endpoints my-devnet` lists them (`--json` for scripting). Web UIs
(dora, grafana) load at the host root; EL JSON-RPC, WebSocket (`ws--…`) and the
CL beacon API are reached the same way — straight through Traefik, so there are
no proxy body/timeout limits on RPC or large responses.

The `<owner>` segment is **server-derived** (the authenticated GitHub login, never
client-supplied; `local_owner` is used in lean dev). That makes it the multi-tenant
boundary: a single per-user wildcard cert `*.<owner>.<base>` covers all of a user's
devnets, and a Traefik forward-auth middleware can enforce *authenticated user ==
`<owner>`* so users only reach their own services.

```yaml
devnet:
  ingress:
    enabled: true
    base_domain: k3s.bruno      # bruno (LAN; dnsmasq *.k3s.bruno wildcard already routes to Traefik)
    entrypoint: web             # plain HTTP on the trusted LAN
    ingress_class: traefik
    local_owner: qu0b           # owner when the request carries no identity (lean dev)
```

Flip to production by changing only this block — no code or hostname-scheme change:

```yaml
devnet:
  ingress:
    enabled: true
    base_domain: devnet.ethpandaops.io
    entrypoint: websecure
    tls_secret: devnet-wildcard-tls          # *.<owner>.devnet.ethpandaops.io (cert-manager DNS-01)
    auth_middleware: devnet-forward-auth@kubernetescrd
    # local_owner unset → owner comes from the authenticated identity
```

## Roadmap: cloud rail behind the proxy

Today both rails run the Kurtosis client in the local server. Because the local
server runs on a user's own host, it can't be the authorization boundary for a
shared cloud cluster. So the cloud rail will move behind the cloud **proxy**,
which holds the cloud kubeconfig and gates enclave creation on GitHub-org
membership; enclaves are owner-stamped and filtered per authenticated user. The
operation contract already derives the caller's identity server-side, so this is
an additive change — the CLI and the `cluster:` switch stay the same.

For the concrete production rollout on the ethpandaops platform (GitOps Kurtosis
engine, DNS, certs, Dex/OIDC auth, and the hosted panda-proxy), see
[`devnet-production.md`](./devnet-production.md).
