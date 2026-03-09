# ethpandaops-mcp

An MCP server that provides AI assistants with Ethereum network analytics capabilities via [Xatu](https://github.com/ethpandaops/xatu) data.

Agents execute Python code in sandboxed containers with direct access to ClickHouse blockchain data, Prometheus metrics, Loki logs, and S3-compatible storage for outputs.

Read more: https://www.anthropic.com/engineering/code-execution-with-mcp

## Quick Start

```bash
# Configure the server + proxy runtime
cp config.example.yaml config.yaml
cp proxy-config.example.yaml proxy-config.yaml

# Configure the CLI client
ep init
# Edit ~/.config/ethpandaops/config.yaml if your server is not at localhost:2480

# Run the local stack
docker compose up -d
```

The local stack runs:
- `server` on port `2480`
- `proxy` on port `18081`
- `minio` on ports `31400` / `31401` by default

By default `docker compose` publishes those ports on `127.0.0.1` only. Override `MCP_SERVER_HOST`, `MCP_PROXY_HOST`, or `MINIO_HOST` if you intentionally want them exposed on another interface.

## Deployment Modes

See [docs/deployments.md](/Users/samcm/go/src/github.com/ethpandaops/mcp/docs/deployments.md) for the supported runtime shapes.

The intended topology is:
- `ep` talks to `server`
- `server` talks to `proxy`
- `proxy` talks to datasources

`ep` is a client. It does not embed the proxy or run sandboxes itself.

If the server enables HTTP auth, authenticate the CLI first:

```bash
mcp auth login --issuer http://localhost:2480 --client-id ep
```

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
make install         # Install mcp, ep, and local search assets to GOBIN
make build-proxy     # Build standalone proxy binary
make test            # Run tests
make lint            # Run linters
make docker          # Build Docker image
make docker-sandbox  # Build sandbox image
```

Installed binaries look for config in this order:
- `--config`
- `$ETHPANDAOPS_CONFIG` or `$EP_CONFIG`
- `~/.config/ethpandaops/config.yaml`
- `./config.yaml`

If no config is found, `ep` tells the user to run `ep init`.

## License

MIT
