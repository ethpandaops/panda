---
name: create-extension
description: "Add a new datasource extension to ethpandaops/mcp. Triggers on: add extension, new extension, create extension, add plugin, new plugin, create plugin, add datasource."
argument-hint: <extension-name>
disable-model-invocation: true
---

# Create New Extension

Use this when adding a new integration like ClickHouse, Prometheus, Loki, Dora, or Ethnode.
The architecture is **extension + operations**, not plugins + bespoke proxy endpoints.

## Files To Create

Create the extension folder:

```text
extensions/{name}/
├── config.go
├── extension.go
├── examples.go
├── examples.yaml
└── python/{name}.py
```

Add these when needed:
- `resources.go` for custom MCP resources
- helper files for schemas, discovery, or API clients
- `pkg/proxy/handlers/{name}_operations.go`
- `pkg/proxy/handlers/{name}_operations_test.go`

## Copy From

- `extensions/prometheus/` or `extensions/loki/` for simple JSON passthrough APIs
- `extensions/clickhouse/` for streamed table results and datasource discovery
- `extensions/dora/` or `extensions/ethnode/` for external HTTP APIs with curated helpers

## Required Wiring

1. `pkg/app/app.go`
   Add `reg.Add({name}extension.New())` in `buildExtensionRegistry()`.
2. `pkg/proxy/handlers/operations.go`
   Add the new operations handler field, constructor wiring, and dispatch.
3. `pkg/proxy/server.go`
   Ensure the operations handler is created with any config the new extension needs.
4. `pkg/proxy/server_config.go`
   Add proxy server config translation if the extension needs proxy-held credentials or typed proxy config.
5. `sandbox/ethpandaops/ethpandaops/__init__.py`
   Add the lazy import if the extension exposes a Python module.
6. `sandbox/Dockerfile`
   Copy `extensions/{name}/python/{name}.py` into the installed `ethpandaops` package.

## Architecture Rules

- Do **not** add a new MCP tool. MCP stays limited to platform primitives.
- Extension semantics live in Go.
- Python wrappers stay thin and go through `sandbox/ethpandaops/ethpandaops/_runtime.py`.
- Default remote execution path is `POST /api/v1/operations/{extension.operation}`.
- Operation handlers own validation, defaults, routing, and error mapping.
- Upstream-backed bulk data must stay passthrough when possible.
  Do not materialize large tables into row-object JSON in the proxy.
- Small synthetic/object operations can still return the JSON envelope.
- Credentials never go into the sandbox. Only metadata and proxy URL/token do.

## Extension Contract

Implement `extension.Extension` from `pkg/extension/extension.go`:
- `Name`
- `Init`
- `ApplyDefaults`
- `Validate`
- `SandboxEnv`
- `DatasourceInfo`
- `Examples`
- `PythonAPIDocs`
- `GettingStartedSnippet`
- `RegisterResources`
- `Start`
- `Stop`

## Python Rules

- Keep Python ergonomic, not semantic.
- `clickhouse.query(...)` style wrappers should call `_runtime` helpers and parse only at the edge.
- If the operation returns bulk tables, prefer streaming formats like TSV instead of normalized JSON rows.
- If the operation already returns useful upstream JSON, return that JSON rather than reshaping it in Python.

## CLI Rules

- Extension-specific CLI commands should be thin adapters over operations.
- Do not duplicate validation/default logic already implemented in the proxy operation handler.
- Pretty-printing belongs in CLI; semantic behavior does not.

## Checklist

- [ ] New extension implements `extension.Extension`
- [ ] Examples and Python API docs are added
- [ ] Proxy operation handler is wired into `pkg/proxy/handlers/operations.go`
- [ ] No new MCP tool was added
- [ ] Python module is thin and copied via `sandbox/Dockerfile`
- [ ] CLI, if added, calls operations instead of bespoke transport logic
- [ ] `go test ./...` passes
- [ ] `python3 -m py_compile ...` passes for touched Python files
- [ ] `make docker-sandbox` builds if sandbox files changed
