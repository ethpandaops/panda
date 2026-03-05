# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ethpandaops/mcp is an MCP (Model Context Protocol) server that provides AI assistants with Ethereum network analytics capabilities. It enables agents to execute Python code in sandboxed containers with access to ClickHouse blockchain data, Prometheus metrics, Loki logs, and S3-compatible storage for outputs.

The server uses a **plugin architecture** where each datasource (ClickHouse, Prometheus, Loki, Dora) is a self-contained plugin. All datasource access flows through a separate **credential proxy** — the MCP server never holds datasource credentials directly.

## Commands

```bash
# Build
make build                    # Build binary
make docker                   # Build Docker image
make docker-sandbox           # Build sandbox container image
make download-models          # Download embedding model + libllama (required before first run)

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
make run                      # Build + download models + run with stdio transport
make run-sse                  # Build + run with SSE transport on port 2480
docker-compose up -d          # Full stack: MCP server + MinIO + sandbox builder

# Evaluation tests (in tests/eval/)
cd tests/eval && uv sync                          # Install Python dependencies
uv run python -m scripts.run_eval                  # Run all eval tests
uv run python -m scripts.run_eval --category basic_queries  # Run specific category
uv run python -m scripts.repl                      # Interactive REPL
```

## Architecture

### Key Components

- **MCP server** (`pkg/server/`): Control plane — transport, auth, tool/resource registration, sandbox orchestration
- **Credential proxy** (`pkg/proxy/`, `cmd/proxy/`): Trust boundary — holds all datasource + S3 credentials, proxies all data access. Runs as a separate binary (`cmd/proxy/`)
- **Sandbox** (`pkg/sandbox/`): Data plane — executes Python in isolated containers (Docker for dev, gVisor for production)
- **Plugins** (`plugins/`): Per-datasource packages — config, schema discovery, resources, examples, Python API docs

### Data Flow

1. Client connects to MCP server (stdio/SSE/streamable-http)
2. MCP server builds a **credential-free** sandbox environment with proxy URL + auth token + datasource metadata
3. Sandbox runs Python; all data access flows through the credential proxy
4. Proxy validates auth, rate-limits, audits, and forwards to ClickHouse/Prometheus/Loki/S3

### Plugin System

Four compiled-in plugins registered in `pkg/server/builder.go`:
- `clickhouse` — schema discovery via proxy, `clickhouse://tables` resources
- `prometheus` — Prometheus metrics queries
- `loki` — Loki log queries
- `dora` — Beacon chain explorer (auto-enabled via `DefaultEnabled` interface, needs no config)

Each plugin implements `plugin.Plugin` (`pkg/plugin/plugin.go`). Optional interfaces:
- `ProxyAware` — receives proxy client for credential-proxied operations
- `CartographoorAware` — receives network discovery client
- `DefaultEnabled` — activates without explicit config

### Builder Startup Order (`pkg/server/builder.go`)

1. Plugin registry (register + init all plugins)
2. Sandbox service (Docker/gVisor)
3. Proxy client (connects to separate proxy server, discovers datasources)
4. Inject proxy into `ProxyAware` plugins → start all plugins
5. Cartographoor client (Ethereum network discovery) → inject into `CartographoorAware` plugins
6. Auth service (GitHub OAuth for HTTP transports)
7. Example index (GGUF embedding model for semantic search)
8. Runbook index (embedded markdown runbooks with semantic search)
9. Tool registry: `execute_python`, `manage_session`, `search_examples`, `search_runbooks`
10. Resource registry

### MCP Tools

| Tool | Description |
|------|-------------|
| `execute_python` | Execute Python in sandbox with `ethpandaops` library |
| `manage_session` | List/create/destroy persistent sandbox sessions |
| `search_examples` | Semantic search over query examples |
| `search_runbooks` | Semantic search over procedural runbooks |

### CLI Subcommands (`cmd/mcp/`)

- `serve` — start MCP server (`--transport/-t`: stdio/sse/streamable-http, `--port/-p`)
- `test` — run a sandbox test without full server (`--code`, `--timeout/-t`)
- `auth login` — OAuth PKCE login (`--issuer`, `--client-id`)
- `auth logout` / `auth status` — manage stored tokens
- `version`

The proxy is a **separate binary**: `cmd/proxy/main.go` (built with `go build -o proxy ./cmd/proxy`)

## Configuration

Two config files:
- `config.yaml` — MCP server (copy from `config.example.yaml`)
- `proxy-config.yaml` — Credential proxy (copy from `proxy-config.example.yaml`)

Key config sections:
```yaml
server:
  transport: stdio|sse|streamable-http
  base_url: "http://localhost:2480"    # Required for SSE/HTTP

proxy:
  url: "http://localhost:18081"        # Proxy server URL
  auth:                                # Optional, for production JWT auth
    issuer_url: "..."
    client_id: "..."

plugins:
  clickhouse:
    schema_discovery:                  # References proxy datasource names, NOT direct credentials
      datasources: [...]
  # dora: enabled by default, no config needed

sandbox:
  backend: docker|gvisor
  image: "ethpandaops-mcp-sandbox:latest"
  sessions:                            # Persistent containers between calls
    enabled: true
    ttl: 30m
    max_sessions: 10

semantic_search:
  model_path: "models/MiniLM-L6-v2.Q8_0.gguf"  # Required; run make download-models

storage: { endpoint, access_key, secret_key, bucket, public_url_prefix }
```

Environment variables are substituted using `${VAR_NAME}` or `${VAR_NAME:-default}` syntax.

## Linting

Uses golangci-lint v2 (`.golangci.yml`):
- Linters: errcheck, govet, staticcheck, unused, misspell, unconvert, gocritic
- Formatters: gofmt, goimports (local prefix: `github.com/ethpandaops/mcp`)

## Project Layout

```
cmd/mcp/           # MCP server binary entry point
cmd/proxy/         # Credential proxy binary entry point
pkg/
  server/          # MCP server + builder (dependency injection)
  plugin/          # Plugin interface and registry
  proxy/           # Proxy client and server (auth, handlers, rate limiting, audit)
  sandbox/         # Sandboxed execution (Docker/gVisor backends, sessions)
  tool/            # MCP tool definitions and handlers
  resource/        # MCP resource definitions (datasources, networks, examples, API docs)
  auth/            # GitHub OAuth + JWT (client/, store/ sub-packages)
  embedding/       # GGUF embedding model wrapper for semantic search
  config/          # Configuration loading and validation
  observability/   # Prometheus metrics
  types/           # Shared types (DatasourceInfo, ExampleCategory, ModuleDoc)
plugins/
  clickhouse/      # ClickHouse plugin (schema discovery, examples, Python module)
  prometheus/      # Prometheus plugin
  loki/            # Loki plugin
  dora/            # Beacon chain explorer plugin (auto-enabled)
runbooks/          # Embedded markdown runbooks with YAML frontmatter
sandbox/           # Sandbox Docker image (Python 3.11, ethpandaops package)
tests/eval/        # LLM evaluation harness (Claude Agent SDK + DeepEval)
docs/              # Deployment architecture docs
```

## Local Development

1. `cp config.example.yaml config.yaml` — edit with datasource details
2. `make docker-sandbox` — build the Python sandbox image
3. `make download-models` — download embedding model for semantic search
4. `docker-compose up -d` — start full stack (MCP server + proxy + MinIO)

### Docker Compose

Main stack (`docker-compose.yaml`): MCP server, MinIO (S3), sandbox builder.
- MCP server: port 2480, metrics: port 31490
- MinIO: API port 31400, console port 31401
- Networks: `ethpandaops-mcp-external` (host-exposed), `ethpandaops-mcp-internal` (sandbox → MinIO/datasources)

Langfuse stack (`tests/eval/docker-compose.langfuse.yaml`): self-hosted tracing for eval tests.
- Web UI: http://localhost:31700 (admin@mcp.local / adminadmin)
- Enable with: `export MCP_EVAL_LANGFUSE_ENABLED=true`

## Deployment Modes

See `docs/deployments.md` for details:
- **Dev**: proxy + MCP on localhost, `auth.mode: none`
- **Local-agent**: production proxy with JWT, local MCP + sandboxes
- **Remote-agent**: all in production (K8s), gVisor backend, GitHub OAuth for clients
