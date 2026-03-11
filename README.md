# ethpandaops-panda

An MCP server that provides AI assistants with Ethereum network analytics capabilities via [Xatu](https://github.com/ethpandaops/xatu) data.

Agents execute Python code in sandboxed containers with direct access to ClickHouse blockchain data, Prometheus metrics, Loki logs, and S3-compatible storage for outputs.

Read more: https://www.anthropic.com/engineering/code-execution-with-mcp

## Architecture

Three components with a strict trust boundary:

```
panda (CLI) ──→ server ──→ proxy ──→ datasources
                 │
                 └──→ sandbox containers (credential-free)
```

- **Server** (`panda-server`) — MCP server + HTTP API. Runs sandboxed Python, registers tools/resources, and manages sessions. Runs locally in Docker.
- **Proxy** (`panda-proxy`) — Credential boundary. Holds all datasource and S3 credentials. The only component that talks directly to credentialed upstream systems (ClickHouse, Prometheus, Loki, S3, Ethereum nodes). Runs remotely at `panda-proxy.ethpandaops.io`.
- **CLI** (`panda`) — HTTP client for the server API and Docker lifecycle manager. Does not embed the proxy or run sandboxes.

Sandbox containers receive a local server API URL and short-lived runtime tokens — credentials never reach the sandbox.

The canonical boundary definition lives in [docs/architecture.md](docs/architecture.md).

## Quick Start

```bash
# 1. Install the CLI
curl -sSfL https://raw.githubusercontent.com/ethpandaops/panda/master/scripts/install.sh | sh

# 2. Set up: check Docker, pull images, write config + compose file
panda init

# 3. Authenticate against the hosted proxy
panda auth login

# 4. Start the server (runs in Docker)
panda server start

# 5. Use it
panda datasources
panda execute --code 'print("hello")'
```

The server runs locally at `http://localhost:2480`. MCP clients connect via SSE transport.

## Server Management

The `panda server` commands manage the local Docker container:

```bash
panda server start     # Start the server container
panda server stop      # Stop the server container
panda server restart   # Restart the server container
panda server status    # Show container status, health, and auth
panda server logs      # Stream server logs
panda server update    # Pull latest images and restart
```

## Client Setup

### Claude Code

Add to `~/.claude.json` under `mcpServers`:

```json
{
  "ethpandaops-panda": {
    "type": "http",
    "url": "http://localhost:2480/mcp"
  }
}
```

#### Skills

Install skills to give Claude knowledge about querying Ethereum data:

```bash
npx skills add ethpandaops/panda
```

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "ethpandaops-panda": {
      "type": "http",
      "url": "http://localhost:2480/mcp"
    }
  }
}
```

### Auth

If your configured proxy requires auth, authenticate first:

```bash
panda auth login
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `execute_python` | Execute Python in a sandboxed container with the `ethpandaops` library |
| `manage_session` | List, create, or destroy persistent sandbox sessions |
| `search` | Semantic search over examples and runbooks |

Resources are available for getting started (`ethpandaops://getting-started`), datasource discovery (`datasources://`), network info (`networks://`), table schemas (`clickhouse://`), and Python API docs (`python://ethpandaops`).

## Development

For local development (building from source instead of Docker images):

```bash
# Configure the server + proxy runtime
cp config.example.yaml config.yaml
cp proxy-config.example.yaml proxy-config.yaml

# Build and run the local stack
make docker-sandbox
docker compose up -d
```

```bash
make build              # Build panda-server and panda
make build-proxy        # Build standalone proxy binary
make install            # Install panda-server and panda binaries to GOBIN
make test               # Run tests with race detector
make lint               # Run golangci-lint (v2)
make docker             # Build server Docker image
make docker-sandbox     # Build sandbox image
make download-models    # Download embedding model + build libllama
make run                # Build + download models + run server (stdio)
make run-sse            # Build + run server with SSE on port 2480
```

Config lookup order for `panda`: `--config` → `$PANDA_CONFIG` → `~/.config/panda/config.yaml` → `./config.yaml`

## License

MIT
