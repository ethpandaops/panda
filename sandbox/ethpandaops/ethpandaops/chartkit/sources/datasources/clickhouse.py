"""The ClickHouse datasource. Ships clickhouse.png next to this module. Call it with a ref
to get a dataset that lives in it: clickhouse("internal.otel_logs")."""
from pathlib import Path
from ..base import datasource, logo_uri

CLICKHOUSE=datasource(id="clickhouse", name="ClickHouse", logo=logo_uri(Path(__file__).with_name("clickhouse.png")))
clickhouse=CLICKHOUSE
