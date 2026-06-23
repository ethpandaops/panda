"""Xatu — the one dataset with its own brand. Lives in a ClickHouse datasource by default;
pass source= to point it at another ClickHouse instance (refined, raw, a devnet proxy).
Ships xatu.png next to this module."""
from pathlib import Path
from ..base import dataset, logo_uri
from ..datasources.clickhouse import CLICKHOUSE

_LOGO=logo_uri(Path(__file__).with_name("xatu.png"))
GREEN="#1c5e3a"

def xatu(ref, source=CLICKHOUSE):
    return dataset(ref=ref, source=source, name="Xatu", logo=_LOGO, color=GREEN)
