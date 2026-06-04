"""ethpandaops data access library for Ethereum network analytics.

This library provides direct access to Ethereum network data:
- ClickHouse: Raw and aggregated blockchain data — including container logs
  (hosted devnet / platform logs live in external.otel_logs)
- Prometheus: Infrastructure metrics
- Ethnode: Direct Ethereum node RPC (beacon + execution)
- Storage: S3-compatible file storage for outputs
- Loki: log datasource, present only when a deployment advertises one
  (on ethpandaops infra, devnet logs are in ClickHouse external.otel_logs, not Loki —
  check list_datasources() to see what's actually available)

Use list_datasources() on each module to discover available datasources or
check the datasources://list MCP resource.

Example usage:
    from ethpandaops import clickhouse, prometheus, ethnode, storage

    # List available ClickHouse clusters
    clusters = clickhouse.list_datasources()
    cluster_name = clusters[0]['name']  # e.g., "clickhouse-raw"

    # Query ClickHouse using cluster name
    df = clickhouse.query(cluster_name, "SELECT * FROM beacon_api_eth_v1_events_block LIMIT 10")

    # Query Prometheus using instance name
    result = prometheus.query("ethpandaops", "up")

    # Upload output file
    url = storage.upload("/workspace/chart.png")
"""

from . import storage

# Integration modules are assembled at Docker build time
# and can be imported as: from ethpandaops import clickhouse, prometheus, loki
__all__ = ["storage"]
__version__ = "0.1.0"


def __getattr__(name):
    """Lazy import for integration modules (clickhouse, prometheus, loki, dora)."""
    if name in ("block_archive", "cbt", "clickhouse", "prometheus", "loki", "dora", "ethnode", "specs"):
        import importlib

        mod = importlib.import_module(f".{name}", __name__)
        globals()[name] = mod
        return mod
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
