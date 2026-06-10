# Deployments

See [architecture.md](architecture.md) for the source-of-truth responsibility split.

This repo has one intended product topology:

```text
panda -> server -> proxy/proxies -> datasources
```

- `panda` is a client.
- `server` owns MCP, HTTP API, sandbox execution, sessions, and search.
- `proxy` routes own datasource credentials.
- sandboxed Python talks to the local server using server-issued runtime tokens.

## Components

- `panda`: local CLI that talks to `server` over HTTP.
- `server`: runs MCP transports, the CLI-facing HTTP API, sandboxes, sessions, and search.
- `proxy`: credential boundary for ClickHouse, Prometheus, Loki, and ethnode. The server can route across multiple configured proxies plus its embedded local proxy.
- `modules`: datasource integrations, docs, resources, examples, and schema discovery.

## Local Docker Compose

Use this for repo development and normal local use.

```text
panda -> localhost:2480 (server container)
server -> http://ethpandaops-panda-proxy:18081
proxy -> datasources
```

- `docker compose up -d` runs `server` and `proxy`.
- `config.yaml` configures the server.
- `proxy-config.yaml` configures datasource credentials.
- `panda init` writes the client config with `server.url`.
- if the proxy is hosted and auth is enabled, run `panda auth login`

## Local Server + Hosted Proxy

Use this when users should execute code locally but access your hosted credential proxy.

```text
panda -> local server
local server -> hosted proxy
hosted proxy -> datasources
```

- the user runs `server` locally
- the user points `panda` at that local server
- the local server points a `proxies:` entry at the hosted proxy
- code still executes on the user’s machine

This is the recommended external-user shape when you do not want to execute code on your own servers.

## Configuration Split

- `panda` config:
  - `server.url` or `server.base_url`
- `server` config:
  - `server.sandbox_url`
  - sandbox settings
  - `proxies:` entries (`name`, `url`)
  - optional `proxies[].auth` (`mode`, `issuer_url`, `client_id`; for
    `mode: client_credentials` also `username` + `password` — service
    accounts mint tokens in memory, no seeded credential files)
  - optional `local_proxy` settings for the embedded loopback proxy
  - deprecated `proxy:` single-proxy form, promoted to `proxies[0]` when used alone
- `proxy` config:
  - optional hosted GitHub auth config
  - datasource credentials
  - proxy auth/rate-limit/audit settings

## Notes

- there is no client-side `proxy.mode`
- `panda` does not embed the proxy
- `panda` does not run sandboxes itself
- if the server is not running, `panda` cannot execute or query anything
