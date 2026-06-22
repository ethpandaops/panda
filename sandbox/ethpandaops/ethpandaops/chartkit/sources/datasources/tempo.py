"""The Tempo tracing datasource. Ships tempo.png next to this module."""
from pathlib import Path
from ..base import datasource, logo_uri

TEMPO=datasource(id="tempo", name="Tempo", logo=logo_uri(Path(__file__).with_name("tempo.png")), color="#7c3aed")
tempo=TEMPO
