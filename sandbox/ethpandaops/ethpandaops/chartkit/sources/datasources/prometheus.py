"""The Prometheus datasource. Ships prometheus.png next to this module."""
from pathlib import Path
from ..base import datasource, logo_uri

PROMETHEUS=datasource(id="prometheus", name="Prometheus", logo=logo_uri(Path(__file__).with_name("prometheus.png")), color="#c2410c")
prometheus=PROMETHEUS
