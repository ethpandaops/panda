# Repository Guide

This file provides guidance to coding agents working in this repository.

## Project Overview

ethpandaops/mcp is a server + proxy system for Ethereum analytics. The server is the only product API boundary, runs sandboxed Python locally, and delegates credentialed upstream access to a separate proxy.

The architecture is:
- `ep` talks to `server`
- `server` talks to `proxy`
- `proxy` talks to datasources

Modules provide integration-specific metadata and behavior for ClickHouse, Prometheus, Loki, Dora, and Ethnode.

See `docs/architecture.md` for the canonical boundary definition.

## Architectural Guardrails

- `server` is the only public/runtime API boundary for `ep`, MCP clients, and sandbox code
- sandboxed Python calls back into `server`, never directly into `proxy`
- `proxy` is a thin credentialed upstream gateway, not a product operations API
- module behavior is exposed through `execute_python`, resources, docs, and search; do not add per-module MCP tools
- datasource identity is owned by the proxy; modules initialize from proxy discovery

## Supported Deployment Modes

Only two deployment modes are supported:

1. all local: `ep -> local server -> local proxy`
2. local server + hosted proxy: `ep -> local server -> hosted proxy`

In both modes, sandbox code still executes locally and calls back into local `server`.

## Commands

```bash
# Build
make build                    # Build mcp + ep
make build-proxy             # Build standalone proxy binary
make docker                   # Build Docker image
make docker-sandbox           # Build sandbox container image
make download-models          # Download embedding model + libllama for local search
make install                  # Install mcp + ep binaries into GOBIN

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
make run                      # Build + download models + run server with stdio transport
make run-sse                  # Build + run server with SSE transport on port 2480
docker compose up -d          # Full local stack: server + proxy + MinIO

# CLI (requires a running server)
./ep datasources                          # List available datasources
./ep schema                               # Show ClickHouse table schemas
./ep docs                                 # Show Python API docs
./ep execute --code 'print("hello")'      # Execute Python in sandbox
./ep session list                         # Manage sandbox sessions
./ep search examples "block count"        # Semantic search examples
./ep search runbooks "finality delay"     # Semantic search runbooks

# Evaluation tests (in tests/eval/)
cd tests/eval && uv sync
uv run python -m scripts.run_eval
uv run python -m scripts.run_eval --category basic_queries
uv run python -m scripts.repl
```

## Architecture

### Key Components

- **App kernel** (`pkg/app/`): Shared server-side initialization for the module registry, proxy client, sandbox, cartographoor, and search indices
- **Server** (`pkg/server/`, `cmd/mcp/`): Control plane for MCP transports, product HTTP API, resource/tool registration, and sandbox orchestration
- **CLI** (`pkg/cli/`, `cmd/cli/`): HTTP client for the server API with human-friendly output
- **Credential proxy** (`pkg/proxy/`, `cmd/proxy/`): Trust boundary that holds datasource and S3 credentials and executes raw upstream requests on behalf of the server
- **Sandbox** (`pkg/sandbox/`): Data plane that executes Python in isolated containers (Docker for dev, gVisor for production)
- **Modules** (`modules/`): Per-integration packages that provide config, examples, docs, resources, and server-side operation behavior

### Data Flow

1. `ep` or an MCP client connects to `server`
2. `server` builds a credential-free sandbox environment with server runtime tokens and datasource metadata
3. sandbox code calls back into `server` for operations and storage
4. `server` calls `proxy` for credentialed upstream access
5. `proxy` validates proxy-scoped auth and forwards requests to ClickHouse, Prometheus, Loki, S3, or Ethnode upstreams

### Module System

Five compiled-in modules are registered in `pkg/app/app.go`:
- `clickhouse`
- `prometheus`
- `loki`
- `dora`
- `ethnode`

Each module implements `module.Module` in `pkg/module/module.go`. Optional capability interfaces live alongside it in `pkg/module/module.go`.
- `ProxyAware` — receives proxy client for proxy-backed operations
- `ProxyDiscoverable` — initializes from discovered datasources
- `CartographoorAware` — receives network discovery client
- `DefaultEnabled` — activates without explicit config (e.g., dora)
- provider interfaces such as sandbox env, datasource info, examples, Python docs, getting-started snippets, and resources are optional and capability-based

Datasource identity is owned by the proxy. Modules that implement `ProxyDiscoverable` initialize from discovered datasources. The proxy client refreshes every 5 minutes.

### Server Startup Order

1. Module registry (register all compiled-in modules, no init yet)
2. Sandbox service
3. Proxy client (initial datasource discovery + background refresh every 5m)
4. Module initialization (proxy discovery or DefaultEnabled)
5. Inject proxy into `ProxyAware` modules and start modules
6. Cartographoor client
7. Semantic search runtime
8. MCP tool registry: `execute_python`, `manage_session`, `search`
9. MCP resource registry
10. Product HTTP API

### Public Surfaces

MCP tools (exactly 3 — this is intentional and must not be expanded; do not add new MCP tools):
- `execute_python`
- `manage_session`
- `search`

All module functionality is exposed to MCP clients through `execute_python`. Modules that want to be usable in an MCP context must provide Python libraries, examples, and documentation so that the LLM can generate Python code that queries the module's datasources via the sandbox. There are no per-module MCP tools — the Python sandbox is the universal interface.

CLI commands:
- `datasources`
- `schema`
- `docs`
- `execute`
- `session`
- `search`
- module command groups such as `clickhouse`, `prometheus`, `loki`, `dora`, and `ethnode`

The proxy is a separate binary, built with `make build-proxy`.

## Configuration

Runtime config files:
- `config.yaml` — server config (copy from `config.example.yaml`)
- `proxy-config.yaml` — credential proxy config (copy from `proxy-config.example.yaml`)

Installed CLI config lookup order:
- `--config`
- `$ETHPANDAOPS_CONFIG` or `$EP_CONFIG`
- `~/.config/ethpandaops/config.yaml`
- `./config.yaml`

`ep init` creates `~/.config/ethpandaops/config.yaml` with `server.url`.

Key config sections:

```yaml
server:
  transport: stdio|sse|streamable-http
  base_url: "http://localhost:2480"

proxy:
  url: "http://localhost:18081"
  auth:
    issuer_url: "..."
    client_id: "..."

sandbox:
  backend: docker|gvisor
  image: "ethpandaops-mcp-sandbox:latest"
  sessions:
    enabled: true
    ttl: 30m
    max_sessions: 10

semantic_search:
  model_path: "models/MiniLM-L6-v2.Q8_0.gguf"
```

Environment variables are substituted using `${VAR_NAME}` or `${VAR_NAME:-default}` syntax.

## Project Layout

```text
cmd/mcp/           # Server binary entry point
cmd/cli/           # CLI binary entry point (ep)
cmd/proxy/         # Credential proxy binary entry point
pkg/
  app/             # Shared server-side application kernel
  cli/             # CLI command definitions
  module/          # Module interface and registry
  server/          # Server builder, HTTP API, MCP transport
  proxy/           # Proxy client/server, auth, handlers
  sandbox/         # Sandboxed execution backends and sessions
  tool/            # MCP tool definitions and handlers
  resource/        # MCP resource definitions
  auth/            # OAuth/JWT client and storage
  embedding/       # GGUF embedding wrapper for semantic search
  config/          # Configuration loading and validation
  observability/   # Prometheus metrics
  types/           # Shared data types
modules/
  clickhouse/      # ClickHouse module
  prometheus/      # Prometheus module
  loki/            # Loki module
  dora/            # Dora module
  ethnode/         # Ethnode module
runbooks/          # Embedded markdown runbooks
sandbox/           # Sandbox Docker image
tests/eval/        # LLM evaluation harness
docs/              # Deployment architecture docs
```

## Local Development

1. `cp config.example.yaml config.yaml`
2. `cp proxy-config.example.yaml proxy-config.yaml`
3. `make docker-sandbox`
4. `make download-models`
5. `docker compose up -d`

Main local stack (`docker-compose.yaml`):
- `server` on port `2480`
- `proxy` on port `18081`
- `minio` on ports `31400` / `31401`

By default docker compose publishes those ports on `127.0.0.1`.

## Deployment

See `docs/deployments.md` for supported deployment shapes. The intended boundary stays the same in each mode:
- clients talk to `server`
- `server` talks to `proxy`
- `proxy` talks to datasources
