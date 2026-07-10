# Architecture

This document defines the supported architecture and responsibility boundaries for the repo.

## Product Boundary

```text
panda / MCP client -> server -> proxy router -> proxy/proxies -> upstream datasources
                     |                 |
                     |                 -> embedded local proxy -> local datasources
                     -> sandbox -> server
```

- `panda` and MCP clients are the only user or agent entry points.
- `server` is the only product API boundary.
- `proxy` is not a product API. It is an internal credentialed gateway, and the server may route across multiple configured proxies plus an embedded local proxy.
- sandboxed Python never talks to `proxy` directly.

## Responsibilities

### `server`

`server` owns all product behavior:

- MCP transports and HTTP API
- `execute_python`, `manage_session`, and `search`
- sandbox lifecycle and execution routing
- runtime session tokens for sandbox-to-server calls
- server-owned operation semantics and response shaping
- module lifecycle, docs, examples, resources, and availability
- semantic search runtime and indices
- auth bootstrap metadata for hosted proxy access

If a change affects product semantics, defaults, validation, output shape, or the user-facing contract, it belongs in `server`.

### `proxy`

`proxy` is intentionally thin. Each proxy route owns:

- datasource identity and credentials for the datasources it advertises
- datasource discovery via `GET /datasources` (authenticated, returns metadata without credentials)
- hosted auth control plane for remote users
- proxy-scoped bearer token validation
- raw upstream relay to ClickHouse, Prometheus, Loki, Ethereum nodes, and the workflow engine
- rate limiting and audit logging

`proxy` must not own user-facing operation semantics.

### `panda`

`panda` is a thin client over the server API:

- config lookup
- local auth bootstrap UX (`panda auth ...`)
- output formatting
- no local module bootstrapping
- no sandbox ownership
- no direct datasource or proxy logic

### sandbox runtime

The Python runtime inside the sandbox is server-facing only:

- uses `ETHPANDAOPS_API_URL`
- uses `ETHPANDAOPS_API_TOKEN`
- calls `server` runtime endpoints for operations and storage
- never receives datasource credentials
- never receives proxy auth tokens

Executed code is LLM-generated and untrusted, so these guarantees must hold
against code actively trying to read the server's secrets — not just against
what the server chooses to pass in.

#### Sandbox backends

- `docker` / `gvisor`: code runs in a container. A separate mount + PID
  namespace and a non-`root` uid keep the server's config, credentials, and
  process environment out of reach by construction.
- `direct`: code runs in-process as a subprocess of `server`, for pods where a
  container-per-execution is undesirable. Because there is no container, the
  server env withholding alone is **not** sufficient — an untrusted script could
  otherwise read the on-disk config/credential files (`config.yaml`,
  `~/.config/panda/credentials/*`) and the server's `/proc/<pid>/environ`/`mem`
  at the shared uid. The direct backend closes those channels with four layers,
  applied by a re-exec trampoline before the script runs:
  1. **uid/gid drop** to a dedicated unprivileged id (`sandbox.exec_uid` /
     `exec_gid`, distinct from the server uid) — the `0600` credential files and
     the server's `/proc` become unreadable.
  2. **PID namespace** — the server process is not even visible in the sandbox's
     `/proc`.
  3. **mount namespace + fresh `/proc`** — private mounts bound to that PID
     namespace.
  4. **Landlock** — the filesystem is restricted to the execution's workspace
     plus the minimal read/exec paths Python needs; secret-bearing paths are
     simply absent.

  It additionally marks the server process non-dumpable (defense in depth for
  the `/proc` channel) and **fails closed**: if any layer is unavailable (no
  Landlock, missing capabilities, `exec_uid` unset or equal to the server uid)
  the backend refuses to start. The uid drop + namespaces require the server to
  hold ambient `CAP_SETUID` / `CAP_SETGID` / `CAP_SYS_ADMIN`; grant these via the
  pod `securityContext` (see `docker-entrypoint.sh` and
  `config.direct.example.yaml`). In hosted-proxy mode the pod still holds the
  service token used to authenticate to the proxy, so this isolation is what
  keeps that token away from executed code.

## Deployment Modes

Only two deployment modes are supported.

### 1. All local

```text
panda -> local server -> local proxy -> upstreams
              |
              -> local sandbox -> local server
```

- intended for trusted local operation
- no hosted auth requirement
- the sandbox-to-`proxy` isolation is a convention here, not a network boundary: in the default `docker compose` stack the sandbox shares a network with the proxy and proxy auth defaults to `none`. Sandbox code is expected to reach `server` only; if the local proxy holds privileged or shared credentials, enforce the boundary with a sandbox-only network or proxy auth.

### 2. Local server + hosted proxy

```text
panda -> local server -> hosted proxy -> upstreams
              |
              -> local sandbox -> local server
```

- code still executes on the user's machine
- hosted proxy keeps credentials remote
- `panda auth login` bootstraps access to the hosted proxy
- the sandbox cannot reach the hosted proxy (remote and authenticated), so the sandbox-to-`proxy` boundary is enforced by topology in this mode

There is no supported hosted-server product topology in this repo.

## Module Model

Integrations are called modules and live under `modules/` (plus the content-only `datasets/` module at the repo root).

Base contract:

- `Name`
- `Init`
- `ApplyDefaults`
- `Validate`
- `Start`
- `Stop`

Optional capabilities are declared explicitly in `pkg/module/module.go`, for example:

- sandbox env
- datasource metadata
- examples
- Python API docs
- custom resources
- proxy-aware startup
- proxy-discoverable
- cartographoor-aware startup

Modules are server-side integrations. They do not define new MCP tools.

### Datasource Discovery

Datasource identity (name, description, metadata) is owned by whichever proxy advertised it.
The server builds a proxy router from every configured `proxies:` entry and, when enabled, an embedded loopback `local_proxy` route.
Queries are routed to the proxy that owns the requested datasource.
Modules that implement `ProxyDiscoverable` initialize from discovered datasources.
The proxy client refreshes datasource info every 60 seconds by default (the embedded local proxy polls every 5 seconds).

## Guardrails

- do not add new MCP tools for modules
- do not make sandbox code talk to `proxy`
- do not move product semantics back into `proxy`
- do not make `panda` reconstruct server state locally
- datasource identity belongs to the proxy route that advertised it; modules must not define their own datasource config
