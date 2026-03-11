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
                              │ Proxy               │
                              │ (remote or local)   │
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

Code runs on your machine. Credentials stay in the proxy. Sandbox containers never receive datasource credentials.

## Getting Started

The server always runs locally on your machine. The two deployment modes differ only in where the credential proxy runs.

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/) with the Compose plugin
- A terminal

### Install

```bash
curl -sSfL https://raw.githubusercontent.com/ethpandaops/panda/master/scripts/install.sh | sh
```

This installs the `panda` CLI to `~/.local/bin/`.

### Mode 1: Hosted Proxy (recommended)

Use the ethpandaops-hosted proxy at `panda-proxy.ethpandaops.io`. You get access to shared Xatu ClickHouse data, Prometheus, and Loki without managing any credentials.

```bash
# Set up everything: pull images, write config, authenticate, start server
panda init

# Verify
panda server status
panda datasources
```

`panda init` walks you through:
1. Checking Docker and pulling the server + sandbox images
2. Writing config files to `~/.config/panda/`
3. Opening a browser for GitHub OAuth login against the hosted proxy
4. Starting the server container

### Mode 2: Local Proxy (bring your own credentials)

Run your own proxy when you have direct access to datasource credentials (ClickHouse, Prometheus, Loki, etc.).

```bash
# 1. Initialize with your local proxy URL, skip hosted auth
panda init --proxy-url http://host.docker.internal:18081 --skip-auth
```

> **Note:** The server runs inside a Docker container, so it cannot reach `localhost` on your host machine. Use `host.docker.internal` (macOS/Windows) or `172.17.0.1` (Linux) to point at a proxy running on your host.

```bash
# 2. Create a proxy config with your credentials
cat > proxy-config.yaml <<'EOF'
server:
  listen_addr: ":18081"
auth:
  mode: none
clickhouse:
  - name: my-cluster
    host: "clickhouse.example.com"
    port: 8443
    database: default
    username: "user"
    password: "pass"
    secure: true
EOF

# 3. Run the proxy (using the same Docker image)
docker run -d --name panda-proxy \
  -p 18081:18081 \
  -v $(pwd)/proxy-config.yaml:/config/proxy-config.yaml:ro \
  --entrypoint /app/panda-proxy \
  ethpandaops/panda:server-latest \
  --config /config/proxy-config.yaml

# 4. Verify
panda server status
panda datasources
```

See [proxy-config.example.yaml](proxy-config.example.yaml) for the full set of configurable datasources (Prometheus, Loki, Ethereum nodes, etc.).

### Verify it works

```bash
panda datasources                          # List available datasources
panda execute --code 'print("hello")'      # Run Python in the sandbox
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

### Auth

The hosted proxy requires authentication via GitHub OAuth. If you used `panda init` with the default hosted proxy, auth is handled during setup. To re-authenticate or refresh:

```bash
panda auth login
```

Local proxies with `auth.mode: none` do not require authentication.

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
make download-models    # Download embedding model for semantic search
```

See [docs/architecture.md](docs/architecture.md) for the full boundary definition and [docs/deployments.md](docs/deployments.md) for deployment modes.

## License

MIT
