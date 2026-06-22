"""chartkit — the PUBLIC interface an agent calls from sandbox code.

The agent passes DATA + plain labels. chartkit derives bins, domains, ticks,
formatting, scales, layout and SVG. The agent never writes coordinates, ticks,
polyline points, or SVG — those live in the engine (this file's imports).

    from ethpandaops import chartkit as ck
    from ethpandaops.chartkit.sources.datasets.xatu import xatu   # source libraries referenced directly
    ck.histogram(df["arrival_s"], x="Time into slot", unit="s",
        title="Most blocks land inside three seconds",
        subtitle="First-seen arrival of each block, across all sentries",
        chart_title="Block arrival distribution",
        source=xatu("mainnet.fct_block_first_seen_by_node"),
        stats=[("MEDIAN","1.46s","good"), ("WITHIN 2s","78%","good")],
        notes="Excludes blocks with no sentry coverage (<0.1%).",
    ).save("arrival.png")           # or .url() to upload from the sandbox
"""
import math, datetime
import numpy as np, pandas as pd
from ._engine import plot, render, make_scale, txt, tw, GREEN, ACC   # the engine (never agent-facing)

PALETTE=[GREEN,ACC,"#2f6db0","#8e44ad","#1f9b7a","#b8860b"]

# ---- derivation helpers (agents never call these) ----
def _step(span,n):
    raw=max(span,1e-9)/n; mag=10**math.floor(math.log10(raw))
    return next((m*mag for m in (1,2,2.5,5,10) if raw<=m*mag),10*mag)
def nice_ticks(lo,hi,n=5):
    if hi<=lo: hi=lo+1
    st=_step(hi-lo,n); v=math.floor(lo/st)*st; out=[]
    while v<=hi+st*0.5: out.append(round(v,6)); v+=st
    return out
def log_ticks(lo,hi):
    a=int(math.floor(math.log10(max(lo,1e-9)))); b=int(math.ceil(math.log10(max(hi,1e-9))))
    return [10**i for i in range(a,b+1)]
def _ax(vals,scale,n):                              # -> (domain, ticks) for linear OR log
    if scale=="log":
        pos=[v for v in vals if v>0]
        if not pos: raise ValueError("chartkit: a log scale needs positive values (all values were <= 0).")
        tk=log_ticks(min(pos),max(pos))
        if len(tk)<2: tk=[tk[0],tk[0]*10]           # non-degenerate domain so the log scale doesn't divide by zero
        return (tk[0],tk[-1]),tk
    tk=nice_ticks(0,max(vals),n); return (0,tk[-1]),tk
_UNIT={"s":lambda v:f"{v:g}s","ms":lambda v:f"{int(v)}ms","%":lambda v:f"{v:g}%"}
def _fmt(u): return _UNIT.get(u,lambda v:f"{v:,.0f}")
def _atitle(label,unit): return f"{label} ({unit})" if unit else label

# chartkit knows NOTHING about specific sources. Agents reference source libraries directly
# (from sources.datasources.prometheus import prometheus) and pass the result as source=.
# A source is any self-describing dict {name, ref, logo, color, source} — see sources/base.py.

# reference lines (thresholds / targets / deadlines) — pass in markers=[...]
def hline(value,label="",color="deadline"): return {"axis":"y","value":value,"label":label,"color":color}
def vline(value,label="",color="deadline",dash=True): return {"axis":"x","value":value,"label":label,"color":color,"dash":dash}
def _srcs(source,sources): return sources or ([source] if source else [])

class Chart:
    def __init__(self,a): self._a=a
    def save(self,out): render(out,**self._a); return out
    # in the sandbox: def url(self): render("/workspace/_c.png",**self._a); return storage.upload("/workspace/_c.png")

# Two titles, delineated by role, validated here so structural mistakes fail loudly:
#   title       (REQUIRED) — top headline: the finding/takeaway ("Most blocks land inside 3s")
#   subtitle    (optional) — top scope: what is measured + over what ("First-seen arrival, all sentries")
#   chart_title (REQUIRED) — label on the chart itself: the neutral plot name ("Block arrival distribution")
_SENT={"good","ok","bad","neutral"}
def _panel(*,title,subtitle="",chart_title="",pi,ph,stats=None,source=None,sources=None,notes="",window=None,legend=None,network="mainnet"):
    title=(title or "").strip(); subtitle=(subtitle or "").strip(); chart_title=(chart_title or "").strip()
    if not title:
        raise ValueError("chartkit: `title` is required — the top headline (the finding it shows), "
                         'e.g. title="Most blocks land inside three seconds".')
    if not chart_title:
        raise ValueError("chartkit: `chart_title` is required — the label on the chart itself (the neutral "
                         'plot name), e.g. chart_title="Block arrival distribution". It is distinct from `title`.')
    if chart_title.lower()==title.lower():
        raise ValueError("chartkit: `chart_title` repeats `title`. The top `title` is the takeaway; "
                         "`chart_title` names what the plot shows. Make them different.")
    if subtitle and subtitle.lower()==title.lower():
        raise ValueError("chartkit: `subtitle` repeats `title`. The title is the finding; the subtitle is the "
                         "scope (what is measured + over what). Make them say different things, or drop subtitle.")
    for k in (stats or []):
        if not (isinstance(k,(list,tuple)) and len(k)==3 and k[2] in _SENT):
            raise ValueError(f"chartkit: each stat must be (label, value, sentiment) with sentiment in "
                             f"{sorted(_SENT)} — got {k!r}.")
    srcs=_srcs(source,sources)
    for sd in srcs:
        if not (isinstance(sd,dict) and sd.get("name") and sd.get("ref") is not None):
            raise ValueError(f"chartkit: each source must come from a source library (a dict with name+ref) — got {sd!r}.")
        lg=sd.get("logo")                            # logos must be package-produced data URIs, never external URLs
        if lg is not None and not str(lg).startswith("data:image/"):
            raise ValueError("chartkit: a source `logo` must be a data:image/... URI from a source library, not a URL.")
    return Chart(dict(network=network,title=title,subtitle=subtitle,chart_title=chart_title,plot_inner=pi,plot_h=ph,
        kpis=stats,sources=srcs,notes=notes,legend=legend,window=window))

# ===================== public chart functions =====================
def histogram(values,*,x,unit="",title,subtitle="",chart_title="",source=None,sources=None,notes="",
              stats=None,network="mainnet",window=None,median=True,bins=80):
    v=np.asarray(list(values),float)
    if not len(v): raise ValueError("chartkit.histogram: `values` is empty — nothing to plot.")
    if v.min()<0: raise ValueError("chartkit.histogram: values must be non-negative (use scatter/line for signed data).")
    bw=(v.max() or 1)/bins
    ac,ae=np.histogram(v,bins=np.arange(0,v.max()+bw,bw))
    idx=np.where(ac>=max(3,len(v)*5e-4))[0]; nz=np.where(ac>0)[0]      # tolerate tiny / all-equal samples
    last=int(idx.max()) if len(idx) else (int(nz.max()) if len(nz) else 0)
    xmax=max(1,int(math.ceil(ae[last+1]))); hc,he=np.histogram(v,bins=np.arange(0,xmax+1e-9,bw)); yt=nice_ticks(0,hc.max())
    m=[{"axis":"x","value":float(np.median(v)),"label":f"median {np.median(v):.2f}{unit}"}] if median else []
    pi,ph=plot(kind="histogram",xdom=(0,xmax),ydom=(0,yt[-1]),xticks=list(range(0,xmax+1)),yticks=yt,
        xfmt=_fmt(unit),yfmt=_fmt(""),xtitle=_atitle(x,unit),ytitle="Count",
        bars=[(he[i],he[i+1],hc[i]) for i in range(len(hc))],markers=m)
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,chart_title=chart_title,network=network)

def box(rows,*,x_label,x_unit="",title,subtitle="",chart_title="",source=None,sources=None,notes="",stats=None,
        network="mainnet",window=None,sort="med",color="data",x_max=None,x_min=0):
    """Horizontal box plot. `rows`: dicts with label,p05,q1,med,q3,p95 (box=q1..q3, whiskers=p05..p95)."""
    rows=[dict(r) for r in rows]
    if not rows: raise ValueError("chartkit.box: `rows` is empty — nothing to plot.")
    for r in rows:
        miss=[k for k in ("label","p05","q1","med","q3","p95") if k not in r]
        if miss: raise ValueError(f"chartkit.box: row {r!r} is missing {miss} (need label,p05,q1,med,q3,p95).")
    if sort: rows=sorted(rows,key=lambda r:r[sort])
    n=len(rows); labels=[str(r["label"]) for r in rows]
    hi=x_max or max(r["p95"] for r in rows); xt=nice_ticks(x_min,hi,6)
    xhi=max(xt[-1],hi*1.03)                                       # domain must cover the longest whisker
    lp=max(58,int(max(tw(l,11) for l in labels))+18)            # gutter fits the longest label
    h=0.30                                                       # box half-thickness in data units
    def body(c):
        for i,r in enumerate(rows):
            y=n-1-i                                                             # first sorted row at the TOP
            c.line([(r["p05"],y),(r["p95"],y)],color="muted",width=1.5)         # whisker
            for wx in (r["p05"],r["p95"]): c.line([(wx,y-0.14),(wx,y+0.14)],color="muted",width=1.5)
            c.rect(r["q1"],y-h,r["q3"],y+h,color=color,rx=2.5,opacity=0.85)     # IQR box
            c.line([(r["med"],y-h),(r["med"],y+h)],color="paper",width=2.4)     # median
    pi,ph=plot(body=body,xdom=(x_min,xhi),ydom=(-0.6,n-0.4),xticks=xt,yticks=list(range(n)),
        xfmt=_fmt(x_unit),yfmt=lambda y:labels[n-1-int(round(y))],xtitle=_atitle(x_label,x_unit),ytitle=None,
        lpad=lp,ph=max(220,n*30))
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,chart_title=chart_title,network=network)

def bar(items,*,value_label="",unit="",title,subtitle="",chart_title="",sort=True,color="data",
        source=None,sources=None,notes="",stats=None,network="mainnet",window=None):
    """Horizontal bars for named categories. `items`: (label, value) pairs."""
    items=list(items)
    if not items: raise ValueError("chartkit.bar: `items` is empty — nothing to plot.")
    if sort: items=sorted(items,key=lambda kv:kv[1],reverse=True)
    n=len(items); labels=[str(k) for k,_ in items]; vals=[float(v) for _,v in items]
    hi=max(vals); xt=nice_ticks(0,hi,6); xhi=max(xt[-1],hi*1.14)   # headroom for the value labels
    lp=max(58,int(max(tw(l,11) for l in labels))+18); vf=_fmt(unit)
    def body(c):
        h=0.34
        for i,(k,v) in enumerate(items):
            y=n-1-i; c.rect(0,y-h,float(v),y+h,color=color,rx=2.5)
            c.label(float(v),y,"  "+vf(float(v)),size=11.5,color="ink",weight=700,dy=4)
    pi,ph=plot(body=body,xdom=(0,xhi),ydom=(-0.6,n-0.4),xticks=xt,yticks=list(range(n)),
        xfmt=vf,yfmt=lambda y:labels[n-1-int(round(y))],xtitle=_atitle(value_label,unit),ytitle=None,
        lpad=lp,ph=max(200,n*34))
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,chart_title=chart_title,network=network)

def area(df,*,x,y,unit="",y_label=None,color=GREEN,title,subtitle="",chart_title="",
         source=None,sources=None,notes="",stats=None,network="mainnet",window=None):
    """Filled time/numeric series (single)."""
    if not len(df): raise ValueError("chartkit.area: dataframe is empty — nothing to plot.")
    xv=pd.Series(list(df[x]))
    if np.issubdtype(np.asarray(xv).dtype,np.datetime64) or isinstance(xv.iloc[0],(pd.Timestamp,datetime.datetime)):
        tt=pd.to_datetime(xv); t0=tt.min(); xs=[(t-t0).total_seconds()/3600 for t in tt]; xmax=max(xs)
        xticks=nice_ticks(0,xmax,6); xfmt=lambda h:(t0+pd.Timedelta(hours=h)).strftime("%-d %b %H:%M"); xtitle="Time (UTC)"
        window=window or f"{t0:%-d %b %H:%M} → {(t0+pd.Timedelta(hours=xmax)):%-d %b %H:%M} UTC"
        xdom=(0,xmax)
    else:
        xs=list(xv); xdom=(min(xs),max(xs)); xticks=nice_ticks(min(xs),max(xs),6); xfmt=_fmt(""); xtitle=x
    yv=[float(v) for v in df[y]]; yt=nice_ticks(0,max(yv),5)
    pi,ph=plot(kind="area",series=[{"pts":list(zip(xs,yv)),"color":color}],xdom=xdom,ydom=(0,yt[-1]),
        xticks=xticks,yticks=yt,xfmt=xfmt,yfmt=_fmt(unit),xtitle=xtitle,ytitle=_atitle(y_label or y,unit))
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,chart_title=chart_title,network=network)

def waterfall(spans,*,x_label="",title,subtitle="",chart_title="",legend=None,
              source=None,sources=None,notes="",stats=None,network="mainnet",window=None):
    """Span timeline (Jaeger-style). `spans`: dicts with name,start,dur (ms) and optional color/depth."""
    if not spans: raise ValueError("chartkit.waterfall: `spans` is empty — nothing to plot.")
    lo=min(s["start"] for s in spans); hi=max(s["start"]+s["dur"] for s in spans); pad=(hi-lo)*0.08 or 1
    xdom=(lo-pad,hi+pad); xt=nice_ticks(xdom[0],xdom[1],6)
    pi,ph=plot(kind="waterfall",spans=spans,xdom=xdom,ydom=(0,1),xticks=xt,yticks=[],
        xfmt=lambda v:f"{v/1000:.2f}s",yfmt=lambda v:"",xtitle=_atitle(x_label,"s"),ytitle=None,
        ph=max(180,len(spans)*42))
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,legend=legend,chart_title=chart_title,network=network)

def heatmap(cells,*,x_labels,y_labels,x_title="",y_title="",lo="",hi="",title,subtitle="",chart_title="",x_step=1,
            source=None,sources=None,notes="",stats=None,network="mainnet",window=None):
    """2D density. `cells`: (col_index, row_index, value); row 0 is the bottom row."""
    if not cells: raise ValueError("chartkit.heatmap: `cells` is empty — nothing to plot.")
    nx=len(x_labels); ny=len(y_labels)
    pi,ph=plot(kind="heatmap",cells=cells,xdom=(0,nx),ydom=(0,ny),
        xticks=[i+0.5 for i in range(0,nx,x_step)],yticks=[i+0.5 for i in range(ny)],
        xfmt=lambda v:x_labels[int(v-0.5)],yfmt=lambda v:y_labels[int(v-0.5)],
        xtitle=x_title,ytitle=y_title,ph=max(200,ny*28))
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,legend={"type":"gradient","lo":lo,"hi":hi},chart_title=chart_title,network=network)

def line(df,*,x,left,right=None,y_scale="linear",y_max=None,markers=None,title,subtitle="",chart_title="",source=None,sources=None,notes="",
         stats=None,network="mainnet",window=None):
    if not len(df): raise ValueError("chartkit.line: dataframe is empty — nothing to plot.")
    if not left: raise ValueError("chartkit.line: `left` needs at least one (label, column, unit) series.")
    xv=pd.Series(list(df[x]))
    if np.issubdtype(np.asarray(xv).dtype,np.datetime64) or isinstance(xv.iloc[0],(pd.Timestamp,datetime.datetime)):
        tt=pd.to_datetime(xv); t0=tt.min(); xs=[(t-t0).total_seconds()/60 for t in tt]; xmax=math.ceil(max(xs))
        xticks=nice_ticks(0,xmax,6); xfmt=lambda m:(t0+pd.Timedelta(minutes=m)).strftime("%H:%M"); xtitle="Time (UTC)"
        window=window or f"{t0:%-d %b %Y %H:%M} → {(t0+pd.Timedelta(minutes=max(xs))):%H:%M} UTC"
    else:
        xs=list(xv); xmax=max(xs); xticks=nice_ticks(min(xs),xmax,6); xfmt=_fmt(""); xtitle=x
    # each series spec is (label, column, unit[, color]) — the optional 4th element overrides the colour
    lcol=lambda i: left[i][3] if len(left[i])>3 else PALETTE[i]
    def dom(cols): hi=max(df[sp[1]].max() for sp in cols); tk=nice_ticks(0,hi,5); return (0,tk[-1]),tk
    ldom,lt=_ax([v for sp in left for v in df[sp[1]]],y_scale,5); lunit=left[0][2]
    if y_max is not None: lt=nice_ticks(0,y_max,5); ldom=(0,y_max)   # force range / leave headroom
    lseries=[{"pts":list(zip(xs,list(df[left[i][1]]))),"color":lcol(i),"label":left[i][0]} for i in range(len(left))]
    legend=None
    if right:
        rcol=lambda i: right[i][3] if len(right[i])>3 else ACC
        rdom,rt=dom(right); runit=right[0][2]
        rseries=[{"pts":list(zip(xs,list(df[right[i][1]]))),"color":rcol(i)} for i in range(len(right))]
        def body(c):                                  # friendly context — no SVG strings
            for sr in lseries: c.line(sr["pts"], color=sr["color"])
            ax2=c.right_axis(rdom, rt, unit=runit, label=_atitle(right[0][0],runit), color=rcol(0))
            for sr in rseries: c.line(sr["pts"], color=sr["color"], scale_y=ax2)
        pi,ph=plot(body=body,xdom=(0,xmax),ydom=ldom,xticks=xticks,yticks=lt,xfmt=xfmt,yfmt=_fmt(lunit),
            xtitle=xtitle,ytitle=_atitle(left[0][0],lunit),yscale=y_scale,markers=(markers or []),rpad=60)
        legend=[(f"{left[0][0]} (left)",lcol(0)),(f"{right[0][0]} (right)",rcol(0))]
    else:
        pi,ph=plot(kind="line",xdom=(0,xmax),ydom=ldom,xticks=xticks,yticks=lt,xfmt=xfmt,yfmt=_fmt(lunit),
            xtitle=xtitle,ytitle=_atitle(left[0][0] if len(left)==1 else "Value",lunit),yscale=y_scale,markers=(markers or []),series=lseries)
        if len(left)>1: legend=[(left[i][0],lcol(i)) for i in range(len(left))]
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,legend=legend,chart_title=chart_title,network=network)

def scatter(df,*,x,y,x_label=None,y_label=None,x_unit="",y_unit="",x_scale="linear",y_scale="linear",
            trend=False,title,subtitle="",chart_title="",source=None,sources=None,notes="",stats=None,network="mainnet",window=None):
    xs=list(df[x]); ys=list(df[y])
    if not xs: raise ValueError("chartkit.scatter: dataframe is empty — nothing to plot.")
    xdom,xt=_ax(xs,x_scale,6); ydom,yt=_ax(ys,y_scale,5)
    if trend:                                          # least-squares fit drawn via the friendly context
        m,b=np.polyfit(xs,ys,1); r2=np.corrcoef(xs,ys)[0,1]**2; x0,x1=min(xs),max(xs)
        def body(c):
            for px,py in zip(xs,ys): c.dot(px,py,r=3.4,color="data",opacity=0.4)
            c.line([(x0,m*x0+b),(x1,m*x1+b)],color="accent",width=2.6)
            c.label(x1,m*x1+b,f"R²={r2:.2f}  ",size=11.5,color="accent",weight=700,anchor="end",dy=-8)
        pi,ph=plot(body=body,xdom=xdom,ydom=ydom,xticks=xt,yticks=yt,xscale=x_scale,yscale=y_scale,
            xfmt=_fmt(x_unit),yfmt=_fmt(y_unit),xtitle=_atitle(x_label or x,x_unit),ytitle=_atitle(y_label or y,y_unit))
    else:
        pi,ph=plot(kind="scatter",points=list(zip(xs,ys)),xdom=xdom,ydom=ydom,xticks=xt,yticks=yt,
            xscale=x_scale,yscale=y_scale,xfmt=_fmt(x_unit),yfmt=_fmt(y_unit),
            xtitle=_atitle(x_label or x,x_unit),ytitle=_atitle(y_label or y,y_unit))
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,chart_title=chart_title,network=network)

def custom(*,draw,xdom,ydom,xticks,yticks,x_label="",y_label="",x_unit="",y_unit="",xfmt=None,yfmt=None,
           x_scale="linear",y_scale="linear",height=None,title,subtitle="",chart_title="",source=None,sources=None,
           notes="",stats=None,legend=None,network="mainnet",window=None):
    """First-class escape hatch for ANY chart the named functions don't cover.
    `draw(c)` receives the friendly context (c.line/c.dot/c.rect/c.band/c.hline/
    c.vline/c.label/c.right_axis, plus c.sx/c.sy scales) and works in DATA coordinates.
    The frame, axes, ticks, theme, footer, anti-clip all still apply. No SVG."""
    pi,ph=plot(body=draw,xdom=xdom,ydom=ydom,xticks=xticks,yticks=yticks,xscale=x_scale,yscale=y_scale,
        xfmt=xfmt or _fmt(x_unit),yfmt=yfmt or _fmt(y_unit),xtitle=_atitle(x_label,x_unit),ytitle=_atitle(y_label,y_unit),ph=height)
    return _panel(title=title,subtitle=subtitle,pi=pi,ph=ph,stats=stats,source=source,sources=sources,
        notes=notes,window=window,legend=legend,chart_title=chart_title,network=network)
