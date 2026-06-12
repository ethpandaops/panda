# Repository Guide

This file provides guidance to coding agents working in this repository.

## Project Overview

ethpandaops/panda is a server + proxy system for Ethereum analytics. The server is the only product API boundary, runs sandboxed Python locally, and delegates credentialed upstream access to a separate proxy.

The architecture is:
- `panda` talks to `server`
- `server` routes through `proxy`/`proxies`
- each `proxy` talks to the datasources it advertises

Modules provide integration-specific metadata and behavior for ClickHouse, Prometheus, Loki, Dora, Forky, Ethnode, CBT, and block archive.

See `docs/architecture.md` for the canonical boundary definition.

## Architectural Guardrails

- `server` is the only public/runtime API boundary for `panda`, MCP clients, and sandbox code
- sandboxed Python calls back into `server`, never directly into `proxy`
- `proxy` is a thin credentialed upstream gateway, not a product operations API
- module behavior is exposed through `execute_python`, resources, docs, and search; do not add per-module MCP tools
- datasource identity is owned by the proxy route that advertised it; modules initialize from proxy discovery
- rendered content never branches on caller identity: resource handlers receive a `surface.Dialect` and spell invocations through its vocabulary (`pkg/surface`); never add `if cli/mcp` conditionals to content code

## Supported Deployment Modes

Only two deployment modes are supported:

1. all local: `panda -> local server -> local proxy`
2. local server + hosted proxy: `panda -> local server -> hosted proxy`

In both modes, sandbox code still executes locally and calls back into local `server`.

## Commands

```bash
# Build
make build                    # Build panda-server + panda
make build-proxy             # Build standalone proxy binary
make docker                   # Build Docker image
make docker-sandbox           # Build sandbox container image
make install                  # Install panda-server + panda binaries into GOBIN

# Test
make test                     # Run tests with race detector
make test-coverage            # Run tests with coverage report
go test -race -v ./pkg/sandbox/...  # Run tests for a single package

# Lint and format
make lint                     # Run golangci-lint (v2 config)
make lint-fix                 # Run golangci-lint with auto-fix
make fmt                      # Format code (gofmt -s)
make vet                      # Run go vet

# Run
make run                      # Build + run server
docker compose up -d          # Full local stack: server + proxy

# CLI (requires a running server)
./panda datasources                          # List available datasources
./panda schema                               # Show ClickHouse table schemas
./panda docs                                 # Show Python API docs
./panda execute --code 'print("hello")'      # Execute Python in sandbox
./panda session list                         # Manage sandbox sessions
./panda search examples "block count"        # Semantic search examples
./panda search runbooks "finality delay"     # Semantic search runbooks

# Evaluation tests (in tests/eval/)
cd tests/eval && uv sync
uv run python -m scripts.eval --tags smoke                # single-pass eval (CI smoke uses this)
uv run python -m scripts.eval                             # full eval over every case in cases/*.yaml
uv run python -m scripts.harden                           # same harness, wrapped in the optimization loop
uv run python -m scripts.repl
```

## Architecture

### Key Components

- **App kernel** (`pkg/app/`): Shared server-side initialization for the module registry, proxy client, sandbox, cartographoor, and search indices
- **Server** (`pkg/server/`, `cmd/server/`): Control plane for MCP (SSE + streamable-http), product HTTP API, resource/tool registration, and sandbox orchestration
- **CLI** (`pkg/cli/`, `cmd/panda/`): HTTP client for the server API with human-friendly output
- **Credential proxy** (`pkg/proxy/`, `cmd/proxy/`): Trust boundary that holds datasource credentials and executes raw upstream requests on behalf of the server
- **Storage** (`pkg/storage/`): Local file storage for sandbox outputs, backed by afero filesystem
- **Sandbox** (`pkg/sandbox/`): Data plane that executes Python in isolated containers (Docker for dev, gVisor for production)
- **Modules** (`modules/`): Per-integration packages that provide config, examples, docs, resources, and server-side operation behavior

### Data Flow

1. `panda` or an MCP client connects to `server`
2. `server` builds a credential-free sandbox environment with server runtime tokens and datasource metadata
3. sandbox code calls back into `server` for operations and storage
4. `server` stores uploaded files locally via the storage service (`~/.panda/data/storage/`)
5. `server` routes through the proxy client/router for credentialed upstream access to ClickHouse, Prometheus, Loki, Ethnode, and Benchmarkoor

### Module System

Eleven compiled-in modules are registered in `pkg/app/app.go`:
- `clickhouse`
- `prometheus`
- `loki`
- `dora`
- `forky`
- `ethnode`
- `cbt`
- `benchmarkoor`
- `tracoor`
- `block_archive`
- `datasets` (dataset knowledge packs, lives in `datasets/` at the repo root)

Each module implements `module.Module` in `pkg/module/module.go`. Optional capability interfaces live alongside it in `pkg/module/module.go`.
- `ProxyAware` — receives proxy client for proxy-backed operations
- `ProxyDiscoverable` — initializes from discovered datasources
- `CartographoorAware` — receives network discovery client
- `DefaultEnabled` — activates without explicit config (e.g., dora)
- provider interfaces such as sandbox env, datasource info, examples, Python docs, and resources are optional and capability-based

Datasource identity is owned by the proxy that advertised it. Modules that implement `ProxyDiscoverable` initialize from discovered datasources. The proxy client refreshes datasource info every 60 seconds by default (the embedded local proxy polls every 5 seconds).

### Server Startup Order

1. Module registry (register all compiled-in modules, no init yet)
2. Sandbox service
3. Proxy client/router (initial datasource discovery + background refresh every 60s by default; embedded local proxy every 5s)
4. Module initialization (proxy discovery or DefaultEnabled)
5. Inject proxy into `ProxyAware` modules and start modules
6. Cartographoor client
7. Semantic search runtime
8. MCP tool registry: `execute_python`, `manage_session`, `search`
9. MCP resource registry
10. Background discovery refresh armed (module activation/resource registration for datasources that appear later; inert during build)
11. Product HTTP API

### Public Surfaces

MCP tools (exactly 3 — this is intentional and must not be expanded; do not add new MCP tools):
- `execute_python`
- `manage_session`
- `search`

All module functionality is exposed to MCP clients through `execute_python`. Modules that want to be usable in an MCP context must provide Python libraries, examples, and documentation so that the LLM can generate Python code that queries the module's datasources via the sandbox. There are no per-module MCP tools — the Python sandbox is the universal interface.

CLI commands and groups include:
- `auth`
- `benchmarkoor`
- `block-archive`
- `build` (GitHub Actions Docker image builder)
- `cbt`
- `clickhouse`
- `config`
- `datasets`
- `datasources`
- `docs`
- `dora`
- `ethnode`
- `execute`
- `forky`
- `getting-started`
- `init`
- `loki`
- `prometheus`
- `resources`
- `schema`
- `search`
- `server`
- `session`
- `tracoor`
- `upgrade`
- `version`

The proxy is a separate binary, built with `make build-proxy`.

## Configuration

Runtime config files:
- `config.yaml` — server config (copy from `config.example.yaml`)
- `proxy-config.yaml` — credential proxy config (copy from `proxy-config.example.yaml`)

Installed CLI config lookup order:
- `--config`
- `$PANDA_CONFIG` or `$ETHPANDAOPS_CONFIG`
- `~/.config/panda/config.yaml`
- `./config.yaml`

`panda init` creates `~/.config/panda/config.yaml` with `server.url`.

Key config sections:

```yaml
server:
  base_url: "http://localhost:2480"

proxies:
  - name: "hosted"
    url: "https://panda-proxy.ethpandaops.io"
    auth:
      mode: "oidc" # or "oauth"
      issuer_url: "https://panda-proxy.ethpandaops.io"
      client_id: "panda"

local_proxy:
  enabled: true
  clickhouse:
    - name: "local-kurtosis"
      host: "host.docker.internal"
      port: 18123
      database: "otel"
      autodiscover: true
      autodiscover_interval: 10s

storage:
  base_dir: "~/.panda/data/storage"

sandbox:
  backend: docker|gvisor
  image: "ethpandaops-panda-sandbox:latest"
  sessions:
    enabled: true
    ttl: 30m
    max_sessions: 50
```

Environment variables are substituted using `${VAR_NAME}` or `${VAR_NAME:-default}` syntax.

`local_proxy` is an in-process loopback proxy for local datasources. It is enabled by default and runs unauthenticated. The deprecated single `proxy:` form is still accepted when used alone and is promoted to `proxies[0]`; setting both `proxy:` and `proxies:` is an error.

## Project Layout

```text
cmd/server/        # Server binary entry point (panda-server)
cmd/panda/         # CLI binary entry point (panda)
cmd/proxy/         # Credential proxy binary entry point
pkg/
  app/             # Shared server-side application kernel
  attribution/     # Caller attribution (on-behalf-of) carried CLI -> server -> proxy
  cli/             # CLI command definitions
  module/          # Module interface and registry
  server/          # Server builder, HTTP API, MCP transport
  proxy/           # Proxy client/server, auth, handlers
  storage/         # Local file storage (afero-backed)
  sandbox/         # Sandboxed execution backends and sessions
  tool/            # MCP tool definitions and handlers
  resource/        # MCP resource definitions
  surface/         # Client surface dialects (MCP, CLI) for rendered content
  auth/            # OAuth/JWT client and storage
  embedding/       # Remote embedding client for semantic search
  config/          # Configuration loading and validation
  observability/   # Prometheus metrics
  types/           # Shared data types
datasets/          # Dataset knowledge packs (content-only module)
modules/
  clickhouse/      # ClickHouse module
  prometheus/      # Prometheus module
  loki/            # Loki module
  dora/            # Dora module
  forky/           # Forky module
  ethnode/         # Ethnode module
  cbt/             # CBT module
  benchmarkoor/    # Benchmarkoor module
  tracoor/         # Tracoor module
  block_archive/   # Block archive module
runbooks/          # Embedded markdown runbooks
sandbox/           # Sandbox Docker image
tests/eval/        # LLM evaluation harness
docs/              # Deployment architecture docs
```

## Local Development

1. `cp config.example.yaml config.yaml`
2. `cp proxy-config.example.yaml proxy-config.yaml`
3. `make docker-sandbox`
4. `docker compose up -d`

Main local stack (`docker-compose.yaml`):
- `server` on port `2480`
- `proxy` on port `18081`

By default docker compose publishes those ports on `127.0.0.1`.
File storage is local to the server process (`~/.panda/data/storage/` by default).

## Deployment

See `docs/deployments.md` for supported deployment shapes. The intended boundary stays the same in each mode:
- clients talk to `server`
- `server` routes through `proxy`/`proxies`
- each `proxy` talks to the datasources it advertises
