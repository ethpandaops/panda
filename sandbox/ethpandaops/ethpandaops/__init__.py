"""ethpandaops data access library for Ethereum network analytics.

This library provides direct access to Ethereum network data:
- ClickHouse: Raw and aggregated blockchain data
- Prometheus: Infrastructure metrics
- Loki: Log data
- Storage: server-managed file storage for outputs

Use list_datasources() on each module to discover available datasources or
check the datasources://list MCP resource.

Example usage:
    from ethpandaops import clickhouse, prometheus, loki, storage

    # List available ClickHouse datasources (incl. extra.datasets placement)
    datasources = clickhouse.list_datasources()
    name = datasources[0]['name']

    # Query ClickHouse using the datasource name
    df = clickhouse.query(name, "SELECT 1")

    # Query Prometheus using instance name
    result = prometheus.query("<datasource>", "up")

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
