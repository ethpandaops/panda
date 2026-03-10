---
name: create-module
description: "Add a new datasource module to ethpandaops/mcp. Triggers on: add module, new module, create module, add plugin, new plugin, create plugin, add datasource."
argument-hint: <module-name>
disable-model-invocation: true
---

# Create New Module

Use this when adding a new integration like ClickHouse, Prometheus, Loki, Dora, or Ethnode.
The architecture is **module + server operations**, not plugins + bespoke proxy endpoints.

## Files To Create

Create the module folder:

```text
modules/{name}/
├── config.go
├── module.go
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

- `modules/prometheus/` or `modules/loki/` for simple JSON passthrough APIs
- `modules/clickhouse/` for streamed table results and datasource discovery
- `modules/dora/` or `modules/ethnode/` for external HTTP APIs with curated helpers

## Required Wiring

1. `pkg/app/app.go`
   Add `reg.Add({name}module.New())` in `buildModuleRegistry()`.
2. `pkg/server/operations_<domain>.go`
   Add new server-owned operation handling for the module.
3. `pkg/server/operations_dispatch.go`
   Wire the module's handler into dispatch.
4. `pkg/proxy/server_config.go`
   Add proxy server config translation only if the module needs proxy-held credentials or typed proxy config.
5. `sandbox/ethpandaops/ethpandaops/__init__.py`
   Add the lazy import if the module exposes a Python module.
6. `sandbox/Dockerfile`
   Copy `modules/{name}/python/{name}.py` into the installed `ethpandaops` package.

## Architecture Rules

- Do **not** add a new MCP tool. MCP stays limited to platform primitives.
- Module semantics live in Go.
- Python wrappers stay thin and go through `sandbox/ethpandaops/ethpandaops/_runtime.py`.
- Default remote execution path is `POST /api/v1/operations/{module.operation}`.
- Server operation handlers own validation, defaults, routing, and error mapping.
- Upstream-backed bulk data must stay passthrough when possible.
  Do not materialize large tables into row-object JSON in the server.
- Small synthetic/object operations can still return the JSON envelope.
- Credentials never go into the sandbox. Only metadata and server runtime tokens do.

## Module Contract

Implement `module.Module` from `pkg/module/module.go` and only the optional capability interfaces you need:
- base lifecycle: `Name`, `Init`, `ApplyDefaults`, `Validate`, `Start`, `Stop`
- optional capabilities: `SandboxEnvProvider`, `DatasourceInfoProvider`, `ExamplesProvider`, `PythonAPIDocsProvider`, `GettingStartedSnippetProvider`, `ResourceProvider`

## Python Rules

- Keep Python ergonomic, not semantic.
- `clickhouse.query(...)` style wrappers should call `_runtime` helpers and parse only at the edge.
- If the operation returns bulk tables, prefer streaming formats like TSV instead of normalized JSON rows.
- If the operation already returns useful upstream JSON, return that JSON rather than reshaping it in Python.

## CLI Rules

- Module-specific CLI commands should be thin adapters over server operations.
- Do not duplicate validation/default logic already implemented in the server operation handler.
- Pretty-printing belongs in CLI; semantic behavior does not.

## Checklist

- [ ] New module implements `module.Module`
- [ ] Examples and Python API docs are added
- [ ] Server operation handler is wired into `pkg/server/operations_dispatch.go`
- [ ] No new MCP tool was added
- [ ] Python module is thin and copied via `sandbox/Dockerfile`
- [ ] CLI, if added, calls operations instead of bespoke transport logic
- [ ] `go test ./...` passes
- [ ] `python3 -m py_compile ...` passes for touched Python files
- [ ] `make docker-sandbox` builds if sandbox files changed
