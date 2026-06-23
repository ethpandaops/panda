"""The source protocol shared by every datasource/dataset library.

Two distinct entities, split across sources/datasources/ and sources/datasets/:
  - datasource : a system that stores/serves data (ClickHouse, Prometheus, Tempo).
  - dataset    : a specific thing you read (a Xatu table, OTel logs, a metric) that
                 LIVES IN a datasource — passed in as `source=` — and may carry its own
                 brand or inherit the store's.

A library ships its own identity by calling these and exporting the result. chartkit
imports those factories; it never registers a logo. Add a source = add a module.
"""
import base64
from pathlib import Path

def logo_uri(path):
    """Encode a logo the provider library ships alongside its own code."""
    return "data:image/png;base64,"+base64.b64encode(Path(path).read_bytes()).decode()

class Source(dict):
    """Marker base for any chartkit source produced by a source library. chartkit accepts
    a chart `source=` only if it is a `Source` instance, so provenance can't be hand-faked
    with a bare dict — `available()`/`load()` and the source modules are the only way in."""

class Datasource(Source):
    """A place that stores/serves data. Call it with a ref to get a dataset in it:
    PROMETHEUS("up") == dataset(ref="up", source=PROMETHEUS)."""
    def __call__(self, ref, **kw):
        return dataset(ref=ref, source=self, **kw)

def datasource(*, id, name, logo, color=None):
    return Datasource(kind="datasource", id=id, name=name, logo=logo, color=color)

def dataset(*, ref, source, name=None, logo=None, color=None):
    """A dataset reference that lives in `source`. `name`/`logo`/`color` default to the
    datasource's identity when the dataset has no brand of its own (e.g. a raw metric)."""
    return Source(name=name or source["name"], ref=ref,
                  logo=logo if logo is not None else source["logo"],
                  color=color or source.get("color"), source=source)
