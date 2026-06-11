OpenTelemetry logs stored in ClickHouse, table `{db}.otel_logs`. The database differs per deployment: resolve `{db}` from the datasource's otel-logs dataset params (`database: ...` in `datasources://` or the sandbox datasource env), falling back to the datasource's default database.

How the rows are keyed depends on the deployment shape (the live placement
section above shows which datasources carry this dataset here):

- **Kurtosis enclaves:** multiple enclaves can share one table — filter on `EnclaveName` (list distinct values first).
- **Hosted/central stores:** keyed by `ResourceAttributes['network']` (network name) and `ResourceAttributes['host.name']` (node).

Tips:
- **Always filter `Timestamp`** (e.g. `Timestamp >= now() - INTERVAL 1 HOUR`).
- `SeverityText` is often empty for raw Docker logs — match severity on `Body`, e.g. `match(Body, '(?i)(crit|err|error|fatal|warn)')`.
- A node VM mixes CL/EL/validator/sidecar containers — use `LogAttributes['log.file.name']` to tell them apart.
