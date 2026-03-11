# panda

An MCP server for Ethereum network analytics. Agents execute Python in sandboxed containers with access to ClickHouse, Prometheus, Loki, and Ethereum node data via [Xatu](https://github.com/ethpandaops/xatu).

Read more: https://www.anthropic.com/engineering/code-execution-with-mcp

## Architecture

```
┌─ Your Machine ─────────────────────────────────────────────────┐
│                                                                │
│   ┌───────────┐         ┌──────────────────────────────────┐   │
│   │           │  HTTP   │ Server (Docker)                  │   │
│   │  Claude / ├────────►│                                  │   │
│   │  panda    │   MCP   │  MCP tools ─ execute_python      │   │
│   │           │         │             ─ manage_session      │   │
│   └───────────┘         │             ─ search              │   │
│                         │                                  │   │
│                         │  ┌────────────────────────────┐  │   │
│                         │  │ Sandbox (Docker)           │  │   │
│                         │  │                            │  │   │
│                         │  │  Python code executes here │  │   │
│                         │  │  No credentials, only a    │  │   │
│                         │  │  server token + API URL    │  │   │
│                         │  └─────────┬──────────────────┘  │   │
│                         │            │ calls back           │   │
│                         │◄───────────┘                     │   │
│                         └──────────────┬───────────────────┘   │
│                                        │                       │
└────────────────────────────────────────┼───────────────────────┘
                                         │
                              ┌──────────▼──────────┐
                              │ Proxy (remote)      │
                              │                     │
                              │  Holds credentials  │
                              │  for all upstream    │
                              │  datasources         │
                              └──────────┬──────────┘
                                         │
                    ┌────────────────────┬┴───────────────────┐
                    ▼                    ▼                    ▼
             ┌────────────┐    ┌──────────────┐    ┌──────────────┐
             │ ClickHouse │    │  Prometheus  │    │  Loki / Eth  │
             │            │    │              │    │    nodes     │
             └────────────┘    └──────────────┘    └──────────────┘
```

Code runs on your machine. Credentials stay remote. Sandbox containers never receive datasource credentials.

## Quick Start

```bash
# Install the CLI
curl -sSfL https://raw.githubusercontent.com/ethpandaops/panda/master/scripts/install.sh | sh

# Set up everything: Docker check, image pull, config, auth, and server start
panda init

# Use it
panda datasources
panda execute --code 'print("hello")'
```

## Client Setup

**Claude Code** — add to `~/.claude.json`:

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

**Claude Desktop** — add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

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

Install [skills](https://github.com/anthropics/skills) for Claude Code: `npx skills add ethpandaops/panda`

## Server Management

```bash
panda server start      # Start the server container
panda server stop       # Stop the server container
panda server restart    # Restart the server container
panda server status     # Show container status and health
panda server logs       # Stream server logs
panda server update     # Pull latest images and restart
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `execute_python` | Run Python in a sandboxed container with the `ethpandaops` library |
| `manage_session` | List, create, or destroy persistent sandbox sessions |
| `search` | Semantic search over examples and runbooks |

## Development

```bash
cp config.example.yaml config.yaml
cp proxy-config.example.yaml proxy-config.yaml
make docker-sandbox
docker compose up -d
```

```bash
make build              # Build panda-server and panda
make build-proxy        # Build standalone proxy binary
make test               # Run tests with race detector
make lint               # Run golangci-lint
make docker             # Build server Docker image
make docker-sandbox     # Build sandbox image
```

See [docs/architecture.md](docs/architecture.md) for the full boundary definition and [docs/deployments.md](docs/deployments.md) for deployment modes.

## License

MIT
