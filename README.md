# ethpandaops-mcp

An MCP server that provides AI assistants with Ethereum network analytics capabilities via [Xatu](https://github.com/ethpandaops/xatu) data.

Agents execute Python code in sandboxed containers with direct access to ClickHouse blockchain data, Prometheus metrics, Loki logs, and S3-compatible storage for outputs.

Read more: https://www.anthropic.com/engineering/code-execution-with-mcp

## Quick Start

```bash
# Configure
cp config.example.yaml config.yaml
# Edit config.yaml with your datasource credentials (ClickHouse, Prometheus, Loki)

# Run (builds sandbox image, starts MinIO + MCP server)
docker-compose up -d
```

The server runs on port 2480 (SSE transport, configurable via `MCP_SERVER_PORT`) with MinIO on ports 2400/2401 (configurable via `MINIO_API_PORT`/`MINIO_CONSOLE_PORT`).

## Deployment Modes

See `docs/deployments.md` for dev, local-agent, and remote-agent deployment modes, plus separation-of-concerns notes.

## Claude Code

Add to `~/.claude.json` under `mcpServers`:

```json
{
  "ethpandaops-mcp": {
    "type": "http",
    "url": "http://localhost:2480/mcp"
  }
}
```

### Skills

Install skills to give Claude knowledge about querying Ethereum data:

```bash
npx skills add ethpandaops/mcp
```

This installs the `query` skill which provides background knowledge for using the MCP tools effectively (ClickHouse queries, Prometheus metrics, Loki logs, session management, etc.).

## Claude Desktop

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

## Tools & Resources

| Tool | Description |
|------|-------------|
| `execute_python` | Execute Python in a sandbox with the `ethpandaops` library |
| `search` | Semantic search over examples and runbooks (`type=examples|runbooks`) |

Resources are available for getting started (`mcp://getting-started`), datasource discovery (`datasources://`), network info (`networks://`), table schemas (`clickhouse://`), and Python API docs (`python://ethpandaops`).

## Development

```bash
make build           # Build mcp and ep
make install         # Install mcp and ep to GOPATH/bin
make build-proxy     # Build standalone proxy binary
make test            # Run tests
make lint            # Run linters
make docker          # Build Docker image
make docker-sandbox  # Build sandbox image
```

## License

MIT
