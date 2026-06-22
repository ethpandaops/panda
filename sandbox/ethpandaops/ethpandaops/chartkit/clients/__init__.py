"""Ethereum client logos, shipped as a referenceable library (sourced from lab.ethpandaops.io).

    from clients import logo, CLIENTS
    logo("lighthouse")   # -> data-uri you can drop into any custom panel / draw call
    CLIENTS              # -> sorted list of available client names

Discoverable: the list comes from the logos shipped next to this module, so a new client
logo dropped in clients/logos/ is immediately usable — no code change here or in chartkit.
"""
import base64
from pathlib import Path

_DIR=Path(__file__).parent/"logos"
CLIENTS=sorted(p.stem for p in _DIR.glob("*.png"))

def logo(name):
    """data-uri for an Ethereum client logo (e.g. 'geth', 'lighthouse', 'reth')."""
    p=_DIR/f"{name}.png"
    if not p.exists():
        raise KeyError(f"no logo for client {name!r}; available: {', '.join(CLIENTS)}")
    return "data:image/png;base64,"+base64.b64encode(p.read_bytes()).decode()

def has(name): return (_DIR/f"{name}.png").exists()

# ---- visibility backing ----
# Most client logos render bare (like lab.ethpandaops.io). A few are near-monochrome and
# vanish on ONE side: we back those, and only on the surface that would hide them. Curated
# on purpose — auto-luminance over-tiled logos that read fine (Prysm, Teku, …).
NEEDS_BACKING={
    "lighthouse":"light",                              # ~white  -> back only on LIGHT surfaces
    "nethermind":"dark","nimbus":"dark","nimbusel":"dark",  # ~black -> back only on DARK surfaces
}

def _hexlum(h):
    h=h.lstrip("#")[:6]; r,g,b=(int(h[i:i+2],16) for i in (0,2,4))
    return 0.299*r+0.587*g+0.114*b

def badge(x, y, size, name, bg="#fafaf7"):
    """SVG for a client logo whose top-left is (x,y). Bare unless this logo is whitelisted
    as near-mono AND the surface `bg` is the tone that hides it — then a soft, centred,
    rounded backing in the opposite tone."""
    img=lambda X,Y,S:(f'<image x="{X:.2f}" y="{Y:.2f}" width="{S:.2f}" height="{S:.2f}" '
                      f'xlink:href="{logo(name)}"/>')
    surface="dark" if _hexlum(bg)<128 else "light"
    if NEEDS_BACKING.get(name)!=surface:
        return img(x,y,size)
    tile="#eef0ee" if surface=="dark" else "#2b3140"   # soft, not harsh
    pad=size*0.18
    return (f'<rect x="{x:.2f}" y="{y:.2f}" width="{size:.2f}" height="{size:.2f}" '
            f'rx="{size*0.28:.2f}" fill="{tile}"/>'+img(x+pad,y+pad,size-2*pad))
