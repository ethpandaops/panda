"""Pure-SVG chart generator — NO browser. Self-contained SVG (computed layout + PIL
font-metric measurement) rasterized with rsvg-convert (librsvg). Sandbox-shippable.

Override layer:
  - theme=   : any colour/token (dict, deep-merged over defaults)
  - xscale=/yscale= : "linear" | "log" | a callable (domain,pxrange)->fn
  - slots=   : replace any frame band (header/kpis/plot/footer) with your own fn
  - @renderer("kind") OR body= : the plot body itself
Anything not overridden falls back to the built-in default."""
import base64, math, subprocess, html, re, tempfile
from types import SimpleNamespace
from pathlib import Path
from PIL import ImageFont

_ASSETS=Path(__file__).resolve().parent/"assets"   # fonts + the panda brand ship with the package

def b64(p,m): return f"data:{m};base64,"+base64.b64encode(Path(p).read_bytes()).decode()
FONTB={w:b64(_ASSETS/f"fonts/Inter-{w}.ttf","font/ttf") for w in (400,600,700)}
BRAND=b64(_ASSETS/"ethpandaops.png","image/png")   # the frame's OWN brand (panda) — the only logo the engine knows
# Datasource/dataset logos are NOT registered here. Each source library ships its own
# identity and hands the renderer a self-describing dict ({name, ref, logo, color, source}).

# ---- text measurement via font metrics (deterministic; no browser) ----
_F={}
def _font(size,w):
    k=(int(round(size)),w)
    if k not in _F: _F[k]=ImageFont.truetype(str(_ASSETS/f"fonts/Inter-{w}.ttf"),int(round(size)))
    return _F[k]
def tw(s,size,w=400): return _font(size,w).getlength(str(s))
def esc(s): return html.escape(str(s),quote=False)
# Colours and hrefs land in SVG ATTRIBUTES, where unescaped caller input could break out of
# the quotes or (for an href) point librsvg at an external resource. Element text is escaped by
# txt(); these guard the attribute path. A rejected value renders as a safe default, never markup.
_COLOR=re.compile(r"^(#[0-9a-fA-F]{3,8}|rgba?\([0-9 .,%]+\)|url\(#[\w-]+\)|[a-zA-Z]{1,20})$")
def safe_color(c,default="#000000"): c="" if c is None else str(c); return c if _COLOR.match(c) else default
def safe_href(u):
    """Only allow logos that are package-produced base64 data URIs (blocks http/file/SSRF)."""
    u="" if u is None else str(u)
    return u if u.startswith("data:image/") else ""
def txt(x,y,s,size,fill,w=400,anchor="start"):
    return f'<text x="{x:.1f}" y="{y:.1f}" font-size="{size}" font-weight="{w}" fill="{safe_color(fill)}" text-anchor="{anchor}">{esc(s)}</text>'
def wrap(s,size,maxw,w=400):
    out,cur=[],""
    for word in str(s).split():
        t=(cur+" "+word).strip()
        if tw(t,size,w)<=maxw or not cur: cur=t
        else: out.append(cur); cur=word
    if cur: out.append(cur)
    return out

# ===================== THEME (every token overridable) =====================
DEFAULT_THEME={
 "paper":"#fafaf7","card":"#ffffff","ink":"#15171c","ink2":"#0b0c0f","muted":"#5b6066","faint":"#9aa0a8",
 "line":"#e7e6de","grid":"#eae9e1","data":"#1c5e3a","accent":"#c2410c","deadline":"#c0392b",
 "sentiment":{"good":"#1a7f37","ok":"#b8860b","bad":"#c0392b","neutral":"#15171c"},
 "networks":{"mainnet":"#2f6db0","sepolia":"#8e44ad","holesky":"#cf6a1a","hoodi":"#1f9b7a"},
 "services":{"consensus":"#1c5e3a","execution":"#c2410c","network":"#2f6db0","storage":"#8e44ad"},
 "heat":[(238,243,239),(20,80,46)],
}
def resolve_theme(o):
    import copy; t=copy.deepcopy(DEFAULT_THEME)
    for k,v in (o or {}).items():
        if isinstance(v,dict) and isinstance(t.get(k),dict): t[k].update(v)
        else: t[k]=v
    return t
GREEN=DEFAULT_THEME["data"]; ACC=DEFAULT_THEME["accent"]; DEADLINE=DEFAULT_THEME["deadline"]   # default aliases
# Named theme presets (partial overrides; resolve_theme deep-merges them over DEFAULT_THEME).
WARM={"paper":"#f4ecdd","card":"#fbf7ee","ink":"#2c2519","ink2":"#1d1810","muted":"#6f6450","faint":"#a3977f",
      "line":"#e6dcc8","grid":"#ece2d0","data":"#1c5e3a","accent":"#b8480f","deadline":"#b3402a",
      "sentiment":{"good":"#1a7f37","ok":"#a9791a","bad":"#b3402a","neutral":"#2c2519"},"heat":[(236,226,206),(28,80,46)]}
DIM ={"paper":"#2b2f36","card":"#333842","ink":"#e8e5dd","ink2":"#f5f2ea","muted":"#a8a499","faint":"#7a766b",
      "line":"#3e444e","grid":"#383e48","data":"#5fae7e","accent":"#dd9356","deadline":"#d77f6b",
      "sentiment":{"good":"#5fae7e","ok":"#cfa84e","bad":"#d77f6b","neutral":"#e8e5dd"},"heat":[(56,62,72),(95,174,126)]}
THEMES={"light":None,"warm":WARM,"dim":DIM}                # named presets agents can pass as theme=
def _lerp(a,b,t): return tuple(round(a[i]+(b[i]-a[i])*t) for i in range(3))
def heatcolor(t01,t): r,g,bl=_lerp(t["heat"][0],t["heat"][1],max(0,min(1,t01))); return f"#{r:02x}{g:02x}{bl:02x}"

# ---- value gradients: map a normalised value t in [0,1] to a colour. A "rainbow" is just one
# such ramp. Used to colour marks by magnitude (box/bar) the way a heatmap colours cells. ----
def _hsv2hex(h,s,v):
    i=int(h*6)%6; f=h*6-int(h*6); p,q,w=v*(1-s),v*(1-s*f),v*(1-s*(1-f))
    r,g,b=[(v,w,p),(q,v,p),(p,v,w),(p,q,v),(w,p,v),(v,p,q)][i]
    return f"#{int(r*255):02x}{int(g*255):02x}{int(b*255):02x}"
def _stops_at(stops,t):
    t=max(0.0,min(1.0,t))
    for i in range(len(stops)-1):
        o0,c0=stops[i]; o1,c1=stops[i+1]
        if t<=o1:
            f=(t-o0)/(o1-o0) if o1>o0 else 0.0
            return "#%02x%02x%02x"%tuple(round(c0[j]+(c1[j]-c0[j])*f) for j in range(3))
    return "#%02x%02x%02x"%tuple(stops[-1][1])
_VIRIDIS=[(0,(68,1,84)),(.25,(59,82,139)),(.5,(33,145,140)),(.75,(94,201,98)),(1,(253,231,37))]
RAMPS=("gradient","rainbow","viridis")              # names agents pass as a chart `color=`
def ramp_color(name,t,theme):
    t=max(0.0,min(1.0,t))
    if name=="gradient": return heatcolor(t,theme)  # the theme's own light->brand ramp
    if name=="rainbow":  return _hsv2hex(0.66*(1-t),0.62,0.82)   # blue (low) -> red (high)
    if name=="viridis":  return _stops_at(_VIRIDIS,t)
    return None
def ramp_stops(name,theme,n=9): return [(round(i/(n-1),3),ramp_color(name,i/(n-1),theme)) for i in range(n)]

# ===================== SCALES (hookable) =====================
def _sclin(d,r): (a,b),(c,e)=d,r; return lambda v: c+(v-a)/(b-a)*(e-c)
def _sclog(d,r):
    (a,b),(c,e)=d,r; la,lb=math.log10(max(a,1e-9)),math.log10(b)
    return lambda v: c+(math.log10(max(v,1e-9))-la)/(lb-la)*(e-c)
def make_scale(s,d,r): return s(d,r) if callable(s) else {"log":_sclog,"linear":_sclin}.get(s,_sclin)(d,r)

# ===================== Draw — the friendly context handed to chart bodies =====================
# Work in DATA coordinates with primitives; never touch SVG strings. Registered renderers
# AND custom bodies (the "escape hatch") both use this, so a bespoke chart is still high-level.
class Draw:
    def __init__(self,kind,sx,sy,yb,PW,PT,PH,GL,t,bars,series,spans,cells):
        self.kind,self.sx,self.sy,self.yb,self.PW,self.PT,self.PH,self.GL,self.t=kind,sx,sy,yb,PW,PT,PH,GL,t
        self.bars,self.series,self.spans,self.cells,self._buf=bars,series,spans,cells,[]
    def add(self,svg): self._buf.append(svg); return svg
    def col(self,c): return safe_color(self.t.get(c,c))           # theme name or raw hex, attribute-safe
    def line(self,points,color="data",width=2.4,scale_y=None):
        f=scale_y or self.sy; pts=" ".join(f"{self.sx(x):.1f},{f(v):.1f}" for x,v in points)
        return self.add(f'<polyline points="{pts}" fill="none" stroke="{self.col(color)}" stroke-width="{width}" stroke-linejoin="round"/>')
    def area(self,points,color="data",opacity=0.12,scale_y=None):
        f=scale_y or self.sy; pts=" ".join(f"{self.sx(x):.1f},{f(v):.1f}" for x,v in points)
        self.add(f'<polygon points="{self.sx(points[0][0]):.1f},{self.yb:.1f} {pts} {self.sx(points[-1][0]):.1f},{self.yb:.1f}" fill="{self.col(color)}" fill-opacity="{opacity}"/>')
        self.line(points,color,2.6,scale_y)
    def bars_(self,bars,color="data",gap=1):
        for x0,x1,v in bars:
            X0,X1,Y=self.sx(x0),self.sx(x1),self.sy(v); self.add(f'<rect x="{X0:.1f}" y="{Y:.1f}" width="{max(1,X1-X0-gap):.1f}" height="{self.yb-Y:.1f}" rx="1.2" fill="{self.col(color)}"/>')
    def rect(self,x0,y0,x1,y1,color="data",rx=2,opacity=1):
        X0,X1,Y0,Y1=self.sx(x0),self.sx(x1),self.sy(y0),self.sy(y1)
        self.add(f'<rect x="{min(X0,X1):.1f}" y="{min(Y0,Y1):.1f}" width="{abs(X1-X0):.1f}" height="{abs(Y1-Y0):.1f}" rx="{rx}" fill="{self.col(color)}" fill-opacity="{opacity}"/>')
    def dot(self,x,y,r=3.2,color="data",opacity=1):
        self.add(f'<circle cx="{self.sx(x):.1f}" cy="{self.sy(y):.1f}" r="{r}" fill="{self.col(color)}" fill-opacity="{opacity}"/>')
    def band(self,x0,x1,color="accent",opacity=0.08):                       # shaded x-range across the plot
        X0,X1=self.sx(x0),self.sx(x1); self.add(f'<rect x="{X0:.1f}" y="{self.PT}" width="{abs(X1-X0):.1f}" height="{self.PH}" fill="{self.col(color)}" fill-opacity="{opacity}"/>')
    def label(self,x,y,s,size=11,color="ink",weight=400,anchor="start",dx=0,dy=0):
        self.add(txt(self.sx(x)+dx,self.sy(y)+dy,s,size,self.col(color),weight,anchor))
    def vline(self,value,color="accent",label=None,dash=False):
        X=self.sx(value); d=' stroke-dasharray="5 3"' if dash else ''
        self.add(f'<line x1="{X:.1f}" x2="{X:.1f}" y1="{self.PT}" y2="{self.PT+self.PH}" stroke="{self.col(color)}" stroke-width="2"{d}/>')
        if label: self.add(txt(X+8,self.PT+11,label,11.5,self.col(color),700))
    def hline(self,value,color="deadline",label=None):
        Y=self.sy(value); self.add(f'<line x1="{self.GL}" x2="{self.GL+self.PW}" y1="{Y:.1f}" y2="{Y:.1f}" stroke="{self.col(color)}" stroke-width="1.8" stroke-dasharray="5 3"/>')
        if label: ly=Y-7 if Y>self.PT+18 else Y+16; self.add(txt(self.GL+self.PW-4,ly,label,11.5,self.col(color),700,anchor="end"))
    def right_axis(self,domain,ticks,unit="",label="",color="accent"):
        sy2=make_scale("linear",domain,(self.PT+self.PH,self.PT)); Rx=self.GL+self.PW; c=self.col(color)
        for tk in ticks: self.add(txt(Rx+8,sy2(tk)+4,f"{tk:g}{unit}",11,c))
        if label: self.add(f'<text transform="rotate(90)" x="{self.PT+self.PH/2:.1f}" y="{-(Rx+42):.1f}" text-anchor="middle" font-size="12" font-weight="600" fill="{c}">{esc(label)}</text>')
        return sy2                                                # use as scale_y for the right-hand series

# ===================== pluggable chart-type registry =====================
GL=58; PW0=844; PH=250; PT=12
RENDERERS={}
def renderer(*kinds):
    def deco(fn):
        for k in kinds: RENDERERS[k]=fn
        return fn
    return deco

@renderer("histogram","bar")
def _bars(c):
    gap=1 if c.kind=="histogram" else 8
    return [f'<rect x="{c.sx(x0):.1f}" y="{c.sy(v):.1f}" width="{max(1,c.sx(x1)-c.sx(x0)-gap):.1f}" height="{c.yb-c.sy(v):.1f}" rx="1.2" fill="{c.t["data"]}"/>' for x0,x1,v in c.bars]

@renderer("area")
def _area(c):
    o=[]
    for s in c.series:
        pts=" ".join(f"{c.sx(x):.1f},{c.sy(y):.1f}" for x,y in s["pts"]); col=safe_color(s.get("color",c.t["data"]))
        o.append(f'<polygon points="{c.sx(s["pts"][0][0]):.1f},{c.yb:.1f} {pts} {c.sx(s["pts"][-1][0]):.1f},{c.yb:.1f}" fill="{col}" fill-opacity="0.12"/>')
        o.append(f'<polyline points="{pts}" fill="none" stroke="{col}" stroke-width="2.6"/>')
    return o

@renderer("line")
def _line(c):
    o=[]
    for s in c.series:
        pts=" ".join(f"{c.sx(x):.1f},{c.sy(y):.1f}" for x,y in s["pts"]); col=safe_color(s.get("color",c.t["data"]))
        o.append(f'<polyline points="{pts}" fill="none" stroke="{col}" stroke-width="{s.get("w",2.6)}" stroke-linejoin="round"/>')
        if s.get("label"): lx,ly=s["pts"][-1]; o.append(txt(c.sx(lx)+8,c.sy(ly)+4,s["label"],11.5,col,700))
    return o

@renderer("waterfall")
def _waterfall(c):
    o=[]; n=len(c.spans); rh=c.PH/n; R=c.GL+c.PW; bh=min(12,rh*0.36); fs=min(10.5,max(8.5,rh*0.46))
    for i,sp in enumerate(c.spans):
        cy=c.PT+i*rh; col=safe_color(sp.get("color") or c.t["services"].get(sp.get("service"),c.t["data"]))
        x0=c.sx(sp["start"]); x1=c.sx(sp["start"]+sp["dur"]); bw=max(2,x1-x0)
        if i: o.append(f'<line x1="{c.GL}" x2="{R}" y1="{cy:.1f}" y2="{cy:.1f}" stroke="#f0efe8"/>')
        ny=cy+rh*0.40; by=cy+rh*0.46; nm=sp["name"]; ind=sp.get("depth",0)*14
        if x0+ind+tw(nm,fs,600)>R-2: o.append(txt(min(x1,R-2),ny,nm,fs,c.t["ink"],600,anchor="end"))
        else: o.append(txt(x0+ind,ny,nm,fs,c.t["ink"],600))
        o.append(f'<rect x="{x0:.1f}" y="{by:.1f}" width="{bw:.1f}" height="{bh:.1f}" rx="2.5" fill="{col}"/>')
        dl=f'{sp["dur"]}ms'; dy=by+bh*0.5+3.2
        if x1+6+tw(dl,9)>R-2: o.append(txt(x0-6,dy,dl,9,c.t["muted"],anchor="end"))
        else: o.append(txt(x1+6,dy,dl,9,c.t["muted"]))
    return o

@renderer("scatter")
def _scatter(c):
    for x,y in c.points: c.dot(x,y,r=3.4,color="data",opacity=0.45)
    return []

@renderer("heatmap")
def _heatmap(c):
    vmax=max((v for *_,v in c.cells),default=1) or 1; o=[]
    for col,row,v in c.cells:
        x0=c.sx(col); x1=c.sx(col+1); y1=c.sy(row); y0=c.sy(row+1)
        o.append(f'<rect x="{x0:.1f}" y="{y0:.1f}" width="{x1-x0-1:.1f}" height="{y1-y0-1:.1f}" rx="1.5" fill="{heatcolor(v/vmax,c.t)}"/>')
    return o

def plot(*, kind=None, xdom, ydom, xticks, yticks, xfmt, yfmt, xtitle, ytitle,
         bars=None, series=None, markers=None, spans=None, cells=None, points=None, ph=None, body=None,
         theme=None, xscale="linear", yscale="linear", rpad=None, lpad=None):
    t=resolve_theme(theme); H=ph or PH; gl=lpad or GL    # lpad widens the left gutter for long y labels
    _rp=max(16, tw(xfmt(xticks[-1]),11)/2+6)
    if series: _rp=max([_rp]+[10+tw(s.get("label",""),11.5,700) for s in series if s.get("label")])
    if rpad: _rp=max(_rp,rpad)                    # caller can reserve extra right margin (e.g. a 2nd axis)
    PW=PW0-gl-_rp; sx=make_scale(xscale,xdom,(gl,gl+PW)); sy=make_scale(yscale,ydom,(PT+H,PT)); yb=sy(ydom[0]); p=[]
    for tk in yticks:
        y=sy(tk); p.append(f'<line x1="{gl}" x2="{gl+PW}" y1="{y:.1f}" y2="{y:.1f}" stroke="{t["grid"]}"/>')
        p.append(txt(gl-10,y+4,yfmt(tk),11,t["faint"],anchor="end"))
    c=Draw(kind,sx,sy,yb,PW,PT,H,gl,t,bars,series,spans,cells); c.points=points
    ret=(body or RENDERERS[kind])(c); p+=(ret or [])+c._buf
    for m in markers or []:
        col=safe_color(m.get("color",t["accent"])); dash=' stroke-dasharray="5 3"' if m.get("dash") else ''
        if m["axis"]=="x":
            X=sx(m["value"]); p.append(f'<line x1="{X:.1f}" x2="{X:.1f}" y1="{PT}" y2="{PT+H}" stroke="{col}" stroke-width="2"{dash}/>')
            if X+8+tw(m["label"],11.5,700)>PW0-2: p.append(txt(X-8,PT+11,m["label"],11.5,col,700,anchor="end"))
            else: p.append(txt(X+8,PT+11,m["label"],11.5,col,700))
        else:
            Y=sy(m["value"]); p.append(f'<line x1="{gl}" x2="{gl+PW}" y1="{Y:.1f}" y2="{Y:.1f}" stroke="{col}" stroke-width="1.8" stroke-dasharray="5 3"/>')
            ly=Y-7 if Y>PT+18 else Y+16; p.append(txt(gl+PW-4,ly,m["label"],11.5,col,700,anchor="end"))
    for tk in xticks: p.append(txt(sx(tk),PT+H+20,xfmt(tk),11,t["faint"],anchor="middle"))
    if ytitle: p.append(f'<text transform="rotate(-90)" x="{-(PT+H/2):.1f}" y="15" text-anchor="middle" font-size="12" font-weight="600" fill="{t["muted"]}">{esc(ytitle)}</text>')
    p.append(txt(gl+PW/2,PT+H+44,xtitle,12,t["muted"],600,anchor="middle"))
    return "".join(p), PT+H+52

# ===================== FRAME — replaceable slots =====================
def slot_header(s):
    t=s.t; y=s.y; CL=s.CL; CR=s.CR
    logo=52; gap=8; pillh=21; cluster=logo+gap+pillh        # brand stack height
    nh=t["networks"][s.network]; pillw=tw(s.network.capitalize(),10.5,600)+22
    avail=CR-max(logo,pillw)-24-CL                           # title must clear the brand cluster (no bleed)
    tlines=wrap(s.title,28,avail,700); lh=32
    tbh=(len(tlines)-1)*lh+(46 if s.subtitle else 24)        # title (+subtitle) text block
    band=max(cluster,tbh)
    ty=y+(band-tbh)/2                                        # title block, vertically centred in band
    for i,ln in enumerate(tlines): s.E.append(txt(CL,ty+22+i*lh,ln,28,t["ink2"],700))
    if s.subtitle: s.E.append(txt(CL,ty+22+(len(tlines)-1)*lh+22,s.subtitle,14,t["muted"]))
    cy=y+(band-cluster)/2                                    # brand cluster, vertically centred in band
    s.E.append(f'<image x="{CR-logo}" y="{cy:.1f}" width="{logo}" height="{logo}" xlink:href="{safe_href(BRAND)}"/>')
    pcx=CR-logo/2; py=cy+logo+gap
    s.E.append(f'<rect x="{pcx-pillw/2:.1f}" y="{py:.1f}" width="{pillw:.1f}" height="{pillh}" rx="6" fill="{nh}1a"/>')
    s.E.append(txt(pcx,py+14.5,s.network.capitalize(),10.5,nh,600,anchor="middle"))
    return band+18

def slot_kpis(s):
    if not s.kpis: return 6                       # no stats -> no card, no wasted row
    t=s.t; y=s.y; kh=78; cw=s.CW/len(s.kpis)
    s.E.append(f'<rect x="{s.CL}" y="{y}" width="{s.CW}" height="{kh}" rx="12" fill="{t["card"]}" stroke="{t["line"]}"/>')
    for i,(lab,val,sent) in enumerate(s.kpis):
        cx=s.CL+i*cw
        if i: s.E.append(f'<line x1="{cx:.1f}" x2="{cx:.1f}" y1="{y}" y2="{y+kh}" stroke="{t["line"]}"/>')
        s.E.append(txt(cx+22,y+26,lab,10.5,t["faint"],600)); s.E.append(txt(cx+22,y+56,val,24,t["sentiment"][sent],700))
    return kh+24

def slot_plot(s):
    # the main content sits in its own card (matching the KPI card); title/legend/plot live inside it
    t=s.t; y0=s.y; pad=16; ix=s.CL+pad; py=y0+pad+4; inner=[]
    if s.chart_title:                                        # wrap so a long chart title never overflows the card
        ctl=wrap(s.chart_title,14,s.CW-2*pad,700)
        for j,ln in enumerate(ctl): inner.append(txt(s.CCX,y0+pad+10+j*18,ln,14,t["ink2"],700,anchor="middle"))
        py=y0+pad+10+len(ctl)*18
    leg=s.legend
    if isinstance(leg,dict) and leg.get("type")=="gradient":
        gy=py+18; gx=ix; gw=170                              # below the chart title, not over it
        stops="".join(f'<stop offset="{o}" stop-color="{c}"/>' for o,c in ramp_stops(leg.get("ramp","gradient"),t))
        inner.append(f'<defs><linearGradient id="hg">{stops}</linearGradient></defs>')
        inner.append(txt(gx,gy,leg["lo"],11,t["muted"])); gx+=tw(leg["lo"],11)+8
        inner.append(f'<rect x="{gx:.1f}" y="{gy-9:.1f}" width="{gw}" height="10" rx="2" fill="url(#hg)" stroke="{t["line"]}"/>'); gx+=gw+8
        inner.append(txt(gx,gy,leg["hi"],11,t["muted"])); py=gy+24
    elif leg:
        lx=ix; ly=py+22; lh=20                               # below the chart title, not over it
        for label,color in leg:
            iw=15+tw(label,11.5,600)+22
            if lx+iw>s.CR-pad and lx>ix: ly+=lh; lx=ix
            inner.append(f'<rect x="{lx:.1f}" y="{ly-9}" width="11" height="11" rx="2.5" fill="{safe_color(color)}"/>'); inner.append(txt(lx+17,ly,label,11.5,t["ink"],600)); lx+=iw
        py=ly+26
    H=(py-y0)+s.plot_h+pad-6                                  # card height: content + bottom padding
    s.E.append(f'<rect x="{s.CL}" y="{y0:.1f}" width="{s.CW}" height="{H:.1f}" rx="14" fill="{t["card"]}" stroke="{t["line"]}"/>')
    s.E.extend(inner)
    s.E.append(f'<g transform="translate({s.CL},{py})">{s.plot_inner}</g>')  # full width; plot gutters pad it
    return H+18

def slot_footer(s):
    t=s.t; y0=s.y+8; s.E.append(f'<line x1="{s.CL}" x2="{s.CR}" y1="{y0}" y2="{y0}" stroke="{t["line"]}"/>'); fc=y0+22
    # top: data provenance (sources left) + notes (pinned right). The two columns have
    # hard bounds so a long ref/note can never overlap the other column.
    mid=s.CL+s.CW/2; gap=24                        # split the footer 50/50: sources left, notes right
    colr=(mid-gap/2) if s.notes else s.CR         # right bound of the DATASOURCES column
    notes_x=mid+gap/2; notes_w=s.CR-notes_x
    s.E.append(txt(s.CL,fc,"DATASOURCES",10,t["faint"],700)); sy=fc+22; srcb=fc
    img=lambda x,uri: f'<image x="{x:.1f}" y="{sy-14}" width="20" height="20" xlink:href="{safe_href(uri)}"/>'
    for src in s.sources:
        tx=s.CL; nm=src["name"]; st=src.get("source")   # st = the datasource this dataset lives in
        stacked=bool(st and st.get("logo") and st.get("name")!=nm)
        if stacked:                               # dataset-in-datasource -> store icon then dataset icon, no overlap
            s.E.append(img(s.CL,st["logo"])); s.E.append(img(s.CL+25,src["logo"])); tx=s.CL+25+20+8
        elif src.get("logo"):                     # logo is provider-owned; engine just embeds whatever it's handed
            s.E.append(img(s.CL,src["logo"])); tx=s.CL+27
        s.E.append(txt(tx,sy,nm,13,src.get("color") or t["data"],700)); aftn=tx+tw(nm,13,700)+8
        ref=src["ref"]
        if tw("· "+ref,12)<=colr-aftn:            # fits inline after the name
            s.E.append(txt(aftn,sy,"· "+ref,12,t["muted"])); srcb=sy; sy+=26
        else:                                     # too long -> wrap under the name, bounded by colr
            srcb=sy; sy+=18
            for ln in wrap(ref,12,colr-tx): s.E.append(txt(tx,sy,ln,12,t["muted"])); srcb=sy; sy+=17
            sy+=9
    noteb=fc
    if s.notes:                                   # no notes -> the whole NOTES column is skipped
        s.E.append(txt(notes_x,fc,"NOTES",10,t["faint"],700)); ny=fc+22
        for ln in wrap(s.notes,12.5,notes_w): s.E.append(txt(notes_x,ny,ln,12.5,t["muted"])); noteb=ny; ny+=18
    # bottom: generation meta centred on its own row (window lives here, with the credit)
    my=max(srcb,noteb)+28
    meta="Generated with github.com/ethpandaops/panda"+(f"   ·   {s.window}" if s.window else "")
    s.E.append(txt(s.CL+s.CW/2,my,meta,11,t["faint"],anchor="middle"))
    return my-s.y+14

DEFAULT_SLOTS={"header":slot_header,"kpis":slot_kpis,"plot":slot_plot,"footer":slot_footer}

def render(out, network, title, subtitle, chart_title, plot_inner, plot_h, kpis, sources, notes,
           legend=None, theme=None, slots=None, window=None):
    t=resolve_theme(theme); SL={**DEFAULT_SLOTS,**(slots or {})}
    s=SimpleNamespace(E=[],y=34,CL=34,CR=878,CW=844,CCX=456,t=t,network=network,title=title,subtitle=subtitle,
        chart_title=chart_title,plot_inner=plot_inner,plot_h=plot_h,kpis=kpis,sources=sources,notes=notes,legend=legend,window=window)
    for name in ("header","kpis","plot","footer"): s.y+=SL[name](s)
    H=int(s.y+22)
    style="".join(f'@font-face{{font-family:Inter;font-weight:{w};src:url({FONTB[w]})}}' for w in (400,600,700))+'text{font-family:Inter}'
    svg=(f'<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="912" height="{H}" '
         f'viewBox="0 0 912 {H}"><style>{style}</style><rect width="912" height="{H}" fill="{t["paper"]}"/>'+"".join(s.E)+'</svg>')
    out=Path(out)
    if out.suffix.lower()==".svg": raise ValueError("chartkit: save() writes a raster image (e.g. .png); got an .svg path.")
    with tempfile.NamedTemporaryFile("w",dir=str(out.parent or "."),suffix=".svg",delete=False) as f:
        f.write(svg); sp=f.name                          # unique temp -> no collision on concurrent renders
    try: subprocess.run(["rsvg-convert","-w","1824",sp,"-o",str(out)],check=True)
    finally: Path(sp).unlink(missing_ok=True)
    return str(out)
