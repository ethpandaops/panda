# Architecture

This document defines the supported architecture and responsibility boundaries for the repo.

## Product Boundary

```text
panda / MCP client -> server -> proxy -> upstream datasources
                     |
                     -> sandbox -> server
```

- `panda` and MCP clients are the only user or agent entry points.
- `server` is the only product API boundary.
- `proxy` is not a product API. It is an internal credentialed gateway.
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

`proxy` is intentionally thin. It owns:

- datasource identity and credentials
- datasource discovery via `GET /datasources` (authenticated, returns metadata without credentials)
- hosted auth control plane for remote users
- proxy-scoped bearer token validation
- raw upstream relay to ClickHouse, Prometheus, Loki, S3, and Ethereum nodes
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

### 2. Local server + hosted proxy

```text
panda -> local server -> hosted proxy -> upstreams
              |
              -> local sandbox -> local server
```

- code still executes on the user's machine
- hosted proxy keeps credentials remote
- `panda auth login` bootstraps access to the hosted proxy

There is no supported hosted-server product topology in this repo.

## Module Model

Integrations are called modules and live under `modules/`.

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
- getting-started snippets
- custom resources
- proxy-aware startup
- proxy-discoverable
- cartographoor-aware startup

Modules are server-side integrations. They do not define new MCP tools.

### Datasource Discovery

Datasource identity (name, description, metadata) is owned by the proxy.
Modules that implement `ProxyDiscoverable` initialize from discovered datasources.
The proxy client refreshes datasource info every 5 minutes.

## Guardrails

- do not add new MCP tools for modules
- do not make sandbox code talk to `proxy`
- do not move product semantics back into `proxy`
- do not make `panda` reconstruct server state locally
- the proxy owns datasource identity; modules must not define their own datasource config
