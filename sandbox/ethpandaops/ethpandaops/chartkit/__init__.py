"""chartkit — the sandbox charting library.

Browser-free, deterministic charts: pure SVG (computed layout + PIL font metrics)
rasterised with librsvg. Agents pass DATA + plain labels; chartkit derives bins,
domains, ticks, scales, layout and SVG. The agent never writes coordinates or SVG.

    from ethpandaops import chartkit as ck
    from ethpandaops.chartkit.sources.datasets.xatu import xatu

    ck.histogram(values, x="Time into slot", unit="s",
        title="Most blocks land inside three seconds",   # top headline (the finding)
        chart_title="Block arrival distribution",        # label on the chart itself
        source=xatu("mainnet.fct_block_first_seen_by_node"), scope="mainnet",
    ).save("arrival.png")

Read the usage rules an agent must follow with `ck.guide()` (the bundled GUIDE.md).
"""
from ._api import (
    histogram, bar, box, line, area, scatter, heatmap, waterfall, custom,
    hline, vline, events, Chart, nice_ticks, log_ticks,
)
from ._engine import WARM, DIM, THEMES   # theme presets: pass theme="warm"/"dim" or theme=ck.WARM
from . import sources, clients

def guide():
    """The agent usage guide (GUIDE.md) as text."""
    from pathlib import Path
    return (Path(__file__).resolve().parent/"GUIDE.md").read_text()

__all__ = [
    "histogram", "bar", "box", "line", "area", "scatter", "heatmap", "waterfall", "custom",
    "hline", "vline", "events", "Chart", "nice_ticks", "log_ticks", "sources", "clients", "guide",
    "WARM", "DIM", "THEMES",
]
