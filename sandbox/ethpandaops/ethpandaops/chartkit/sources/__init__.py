"""Source libraries. Each datasource/dataset is its own referenceable library that ships
its own logo and identity. There is no central registry — chartkit and gen_svg know about
none of these, and neither do the docs. Discover what's installed at runtime:

    import sources
    sources.available()        # {'datasources': [...], 'datasets': [...]} — reflects reality
    src = sources.load("datasources", name)   # import a source library by name

Adding/removing a source is a module under datasources/ or datasets/. Nothing else changes,
and no doc or example needs editing — they describe the mechanism, not a fixed list.
"""
import importlib
from pathlib import Path

_BASE=Path(__file__).parent

def available():
    def mods(sub): return sorted(p.stem for p in (_BASE/sub).glob("*.py") if p.stem!="__init__")
    return {"datasources":mods("datasources"),"datasets":mods("datasets")}

def load(kind, name):
    """Import a source factory by kind ('datasources'|'datasets') and name."""
    mod=importlib.import_module(f"{__name__}.{kind}.{name}")
    return getattr(mod,name)
