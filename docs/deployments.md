# Deployments

See [architecture.md](architecture.md) for the source-of-truth responsibility split.

This repo has one intended product topology:

```text
ep -> server -> proxy -> datasources
```

- `ep` is a client.
- `server` owns MCP, HTTP API, sandbox execution, sessions, and search.
- `proxy` owns datasource and storage credentials.
- sandboxed Python talks to the local server using server-issued runtime tokens.

## Components

- `ep`: local CLI that talks to `server` over HTTP.
- `server`: runs MCP transports, the CLI-facing HTTP API, sandboxes, sessions, and search.
- `proxy`: credential boundary for ClickHouse, Prometheus, Loki, ethnode, and S3.
- `modules`: datasource integrations, docs, resources, examples, and schema discovery.

## Local Docker Compose

Use this for repo development and normal local use.

```text
ep -> localhost:2480 (server container)
server -> http://ethpandaops-mcp-proxy:18081
proxy -> datasources
```

- `docker compose up -d` runs `server`, `proxy`, and `minio`.
- `config.yaml` configures the server.
- `proxy-config.yaml` configures datasource credentials.
- `ep init` writes the client config with `server.url`.
- if the proxy is hosted and auth is enabled, run `ep auth login`

## Local Server + Hosted Proxy

Use this when users should execute code locally but access your hosted credential proxy.

```text
ep -> local server
local server -> hosted proxy
hosted proxy -> datasources
```

- the user runs `server` locally
- the user points `ep` at that local server
- the local server points `proxy.url` at the hosted proxy
- code still executes on the user’s machine

This is the recommended external-user shape when you do not want to execute code on your own servers.

## Configuration Split

- `ep` config:
  - `server.url` or `server.base_url`
- `server` config:
  - `server.sandbox_url`
  - sandbox settings
  - module config
  - `proxy.url`
  - optional `proxy.auth`
- `proxy` config:
  - optional hosted GitHub auth config
  - datasource credentials
  - storage credentials
  - proxy auth/rate-limit/audit settings

## Notes

- there is no client-side `proxy.mode`
- `ep` does not embed the proxy
- `ep` does not run sandboxes itself
- if the server is not running, `ep` cannot execute or query anything
