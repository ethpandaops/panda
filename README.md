# ethpandaops-mcp

An MCP server that provides AI assistants with Ethereum network analytics capabilities via [Xatu](https://github.com/ethpandaops/xatu) data.

Agents execute Python code in sandboxed containers with direct access to ClickHouse blockchain data, Prometheus metrics, Loki logs, and S3-compatible storage for outputs.

Read more: https://www.anthropic.com/engineering/code-execution-with-mcp

## Architecture

Three components with a strict trust boundary:

```
ep (CLI) ──→ server ──→ proxy ──→ datasources
              │
              └──→ sandbox containers (credential-free)
```

- **Server** (`mcp`) — MCP server + HTTP API. Runs sandboxed Python, registers tools/resources, manages auth and sessions.
- **Proxy** (`proxy`) — Credential boundary. Holds all datasource and S3 credentials. The only component that talks directly to upstream systems (ClickHouse, Prometheus, Loki, S3, Dora, Ethereum nodes).
- **CLI** (`ep`) — HTTP client for the server API. Does not embed the proxy or run sandboxes.

Sandbox containers receive a proxy URL and short-lived tokens — credentials never reach the sandbox.

Extensions (e.g. `clickhouse`, `prometheus`, `dora`) provide integration-specific behavior: examples, Python API docs, MCP resources, proxy operations, and CLI commands.

## Quick Start

```bash
# Configure the server + proxy runtime
cp config.example.yaml config.yaml
cp proxy-config.example.yaml proxy-config.yaml

# Build the sandbox image
make docker-sandbox

# Run the local stack
docker compose up -d

# Configure the CLI client
ep init
# Edit ~/.config/ethpandaops/config.yaml if your server is not at localhost:2480
```

The local stack runs:
- `server` on port `2480`
- `proxy` on port `18081`
- `minio` on ports `31400` / `31401`

By default `docker compose` publishes those ports on `127.0.0.1` only. Override `MCP_SERVER_HOST`, `MCP_PROXY_HOST`, or `MINIO_HOST` to expose on another interface.

## Deployment Modes

See [docs/deployments.md](docs/deployments.md) for the supported runtime shapes:

1. **Local Docker Compose** — Everything on one machine. Good for development.
2. **Local Server + Hosted Proxy** — Run the server locally, point it at a hosted proxy. Code executes locally; credentials stay remote.
3. **Hosted Server + Hosted Proxy** — Fully managed. Use `gvisor` sandbox backend and enable HTTP auth.

## Client Setup

### Claude Code

Add to `~/.claude.json` under `mcpServers`:

```json
{
  "ethpandaops-mcp": {
    "type": "http",
    "url": "http://localhost:2480/mcp"
  }
}
```

#### Skills

Install skills to give Claude knowledge about querying Ethereum data:

```bash
npx skills add ethpandaops/mcp
```

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "ethpandaops-mcp": {
      "type": "http",
      "url": "http://localhost:2480/mcp"
    }
  }
}
```

### Auth

If the server enables HTTP auth, authenticate first:

```bash
mcp auth login --issuer http://localhost:2480 --client-id ep
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `execute_python` | Execute Python in a sandboxed container with the `ethpandaops` library |
| `manage_session` | List, create, or destroy persistent sandbox sessions |
| `search` | Semantic search over examples and runbooks |

Resources are available for getting started (`ethpandaops://getting-started`), datasource discovery (`datasources://`), network info (`networks://`), table schemas (`clickhouse://`), and Python API docs (`python://ethpandaops`).

## Development

```bash
make build              # Build mcp and ep
make build-proxy        # Build standalone proxy binary
make install            # Install mcp, ep, and search assets to GOBIN
make test               # Run tests with race detector
make lint               # Run golangci-lint (v2)
make docker             # Build server Docker image
make docker-sandbox     # Build sandbox image
make download-models    # Download embedding model + build libllama
make run                # Build + download models + run server (stdio)
make run-sse            # Build + run server with SSE on port 2480
```

Config lookup order for `ep`: `--config` → `$ETHPANDAOPS_CONFIG` / `$EP_CONFIG` → `~/.config/ethpandaops/config.yaml` → `./config.yaml`

## License

MIT
