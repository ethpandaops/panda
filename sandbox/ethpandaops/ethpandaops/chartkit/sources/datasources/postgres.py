"""A datasource added with ZERO changes to chartkit or gen_svg. Ships postgres.png next to
this module; the source dict is self-describing so the renderer just picks up the logo."""
from pathlib import Path
from ..base import datasource, logo_uri

POSTGRES=datasource(id="postgres", name="Postgres", logo=logo_uri(Path(__file__).with_name("postgres.png")), color="#31648c")
postgres=POSTGRES
