# Proposal: `panda upload` — private preview, publish on click

Status: draft · Scope: `panda upload` + the plumbing behind it

## Problem

You have a file — a chart, an HTML report, a parquet — and you want a link you
can paste into Discord / HackMD / an issue. Today the only URL panda mints is the
server-local `/api/v1/storage/files/...`, which dies with the process and isn't
reachable by anyone else. And you don't want every upload to be world-readable by
accident — publishing should be a deliberate act.

## The flow

```console
$ panda upload report.html
http://localhost:2480/u/6f1c…            # private preview, opens in your browser
private preview (session-only) — click "Make public" to publish
```

The preview page renders the file and shows a **Make public** button. Clicking it
promotes the file to the public bucket and shows the durable link:

```
https://data.ethpandaops.io/uploads/9f3ca1/report.html   (expires in 60 days)
```

- **Private by default.** The upload is held **in memory** in the local server
  for the session only — nothing hits disk or leaves your machine until you
  publish. Restart the server and unpublished previews are gone.
- **Publishing is a conscious choice** — the button (or `--public` for scripts).
- `-` reads stdin (with `--name`); `--public` skips the preview and prints the
  public URL; `--no-open` suppresses the browser.

```console
$ panda upload chart.png --public
https://data.ethpandaops.io/uploads/71aa9c/chart.png

$ cat gas.svg | panda upload - --name gas.svg
http://localhost:2480/u/2b77e4
```

## Layers

| Layer | File | What |
|-------|------|------|
| CLI | `pkg/cli/upload.go` | `panda upload` (`groupDirect`); prints preview URL + opens browser, or `--public` to publish |
| Server (private) | `pkg/server/uploads.go` | in-memory session store; `POST /api/v1/uploads`, preview page `GET /u/{id}`, raw `GET /u/{id}/raw` |
| Server (publish) | `pkg/server/uploads.go` | `POST /api/v1/uploads/publish` streams the stored bytes to the proxy |
| Proxy | `pkg/proxy/handlers/uploads.go` + `/uploads` route | R2 `PutObject`; holds the creds |
| Config | `proxy-config.yaml` | one R2 block (below) |

R2 credentials stay behind the proxy trust boundary. The private side never
touches the proxy; only **publish** does. No new MCP tool.

```yaml
# proxy-config.yaml
uploads:
  bucket: "ethpandaops-platform-production-public"
  key_prefix: "uploads/"
  public_base_url: "https://data.ethpandaops.io"
  endpoint: "https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com"
  access_key_id: "${R2_UPLOADS_ACCESS_KEY_ID}"
  secret_access_key: "${R2_UPLOADS_SECRET_ACCESS_KEY}"
  max_object_bytes: 104857600   # 100 MiB
```

Published objects are content-addressed (`uploads/<6-hex-sha256>/<filename>`) —
identical bytes dedup and every public URL is immutable.

## Retention

- **Private previews** live only in the local server's memory (session lifetime),
  bounded to the most recent `uploadMaxItems` (32) uploads.
- **Public objects** expire **60 days** after publication via an R2 lifecycle
  rule on the bucket (configured in the platform repo).

## Abuse guards

The publish path (`/uploads` on the proxy) is authenticated (org-gated at token
issuance) and, on top of the generic per-user request limiter, has:

- a **dedicated per-user rate limit** (60/min, burst 20);
- a **size cap** (`max_object_bytes`, default 100 MiB) and an equal cap on the
  in-memory private upload;
- **filename sanitization** — path components stripped, restricted to
  `[A-Za-z0-9._-]`, length-capped, extension preserved;
- **forced download for scriptable types** on the public bucket — `text/html`,
  `image/svg+xml`, JS get `Content-Disposition: attachment` so it can't serve
  phishing/XSS inline; images, PDFs and text still render.

The private preview serves uploaded bytes with a `sandbox` CSP and a sandboxed
`<iframe>`, so previewing a malicious HTML upload can't execute scripts in the
local server's origin.

## Prerequisite (platform repo)

An R2 writer key scoped RW to just the target bucket + the 60-day lifecycle rule
+ the proxy `uploads:` config. See ethpandaops/platform#365.

## Deliberately out of scope

- `list` / `get` / `rm` — the URL is the receipt; download is `curl`.
- Sandbox Python API (`assets.publish(fig)`) — natural next step, surfaced
  through `execute_python` (never a 4th MCP tool).
- Persisting private previews across sessions (they're intentionally ephemeral).

## Open decision

Target the existing `-public` bucket under an `uploads/` prefix (ships now, but
`data.ethpandaops.io` reads as the xatu data surface), or the dedicated
`-dropbox` bucket behind a new `uploads.ethpandaops.io` CNAME (cleaner, one extra
`platform`-repo change). Prefix separation is fine to start.
