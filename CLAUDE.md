# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ethpandaops/mcp is a server + proxy system for Ethereum analytics. The server exposes MCP tools plus a product HTTP API, runs sandboxed Python locally or in deployment, and delegates datasource access to a separate credential proxy.

The architecture is:
- `ep` talks to `server`
- `server` talks to `proxy`
- `proxy` talks to datasources

Extensions provide integration-specific metadata and behavior for ClickHouse, Prometheus, Loki, Dora, and Ethnode.

## Commands

```bash
# Build
make build                    # Build mcp + ep
make build-proxy             # Build standalone proxy binary
make docker                   # Build Docker image
make docker-sandbox           # Build sandbox container image
make download-models          # Download embedding model + libllama for local search
make install                  # Install mcp + ep and search assets into GOBIN

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

- **App kernel** (`pkg/app/`): Shared server-side initialization for the extension registry, proxy client, sandbox, cartographoor, and search indices
- **Server** (`pkg/server/`, `cmd/mcp/`): Control plane for MCP transports, product HTTP API, auth, resource/tool registration, and sandbox orchestration
- **CLI** (`pkg/cli/`, `cmd/cli/`): HTTP client for the server API with human-friendly output
- **Credential proxy** (`pkg/proxy/`, `cmd/proxy/`): Trust boundary that holds datasource and S3 credentials and executes extension operations against upstream systems
- **Sandbox** (`pkg/sandbox/`): Data plane that executes Python in isolated containers (Docker for dev, gVisor for production)
- **Extensions** (`extensions/`): Per-integration packages that provide config, examples, docs, resources, and operation behavior

### Data Flow

1. `ep` or an MCP client connects to `server`
2. `server` builds a credential-free sandbox environment with proxy URL, auth token, and datasource metadata
3. Sandbox code and server-side operations access data through `proxy`
4. `proxy` validates auth and forwards requests to ClickHouse, Prometheus, Loki, S3, Dora, or Ethnode upstreams

### Extension System

Five compiled-in extensions are registered in `pkg/app/app.go`:
- `clickhouse`
- `prometheus`
- `loki`
- `dora`
- `ethnode`

Each extension implements `extension.Extension` in [pkg/extension/extension.go](/Users/samcm/go/src/github.com/ethpandaops/mcp/pkg/extension/extension.go). Optional interfaces:
- `ProxyAware` — receives proxy client for proxy-backed operations
- `CartographoorAware` — receives network discovery client
- `DefaultEnabled` — activates without explicit config

### Server Startup Order

1. Extension registry (register + init all configured/default-enabled extensions)
2. Sandbox service
3. Proxy client
4. Inject proxy into `ProxyAware` extensions and start extensions
5. Cartographoor client
6. Auth service
7. Semantic search runtime
8. MCP tool registry: `execute_python`, `manage_session`, `search`
9. MCP resource registry
10. Product HTTP API

### Public Surfaces

MCP tools:
- `execute_python`
- `manage_session`
- `search`

CLI commands:
- `datasources`
- `schema`
- `docs`
- `execute`
- `session`
- `search`
- extension command groups such as `clickhouse`, `prometheus`, `loki`, `dora`, and `ethnode`

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

extensions:
  clickhouse:
    schema_discovery:
      datasources: [...]

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
  extension/       # Extension interface and registry
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
extensions/
  clickhouse/      # ClickHouse extension
  prometheus/      # Prometheus extension
  loki/            # Loki extension
  dora/            # Dora extension
  ethnode/         # Ethnode extension
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
