# Proposal: `panda upload` — shareable file URLs

Status: draft · Scope: one CLI command + the minimal plumbing behind it

## Problem

You have a file — a chart, a report, a parquet — and you want a link you can
paste into Discord / HackMD / an issue. Today the only URL panda mints is the
server-local `/api/v1/storage/files/...`, which dies with the process and isn't
reachable by anyone else.

## The feature

One command:

```console
$ panda upload timings.png
https://data.ethpandaops.io/uploads/9f3ca1/timings.png
```

That's it. Reads a file, pushes it to the ethpandaops R2 public bucket, prints a
durable URL. `--json` for the full object if a script needs it; `-` reads stdin
(with `--name` for the extension).

```console
$ cat gas.svg | panda upload - --name gas.svg
https://data.ethpandaops.io/uploads/c0de7f/gas.svg

$ panda upload report.md --json
{"url":"https://data.ethpandaops.io/uploads/2b77e4/report.md","size":18452,"content_type":"text/markdown; charset=utf-8"}
```

Key scheme: `uploads/<6-hex-of-content-sha256>/<filename>`. Collision-free
without any state to track, and the filename stays human-readable in the URL.

## Plumbing (the irreducible part)

R2 credentials must stay behind the proxy trust boundary, so the bytes flow
CLI → server → proxy → R2. Four small additions:

| Layer | File | What |
|-------|------|------|
| CLI | `pkg/cli/upload.go` | `panda upload <file>...` (`groupDirect`), streams to the server route |
| Server | `pkg/server/api.go` | `POST /api/v1/uploads` — streams body to the proxy, returns the URL (like the existing `/storage/files/*` route) |
| Proxy | `pkg/proxy/handlers/uploads.go` + `/uploads` route | R2 `PutObject`; holds the creds |
| Config | `proxy-config.yaml` | one R2 block (below) |

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

The client config needs nothing — uploads ride the existing `server.url` → proxy
path. Uploads carry the caller attribution panda already sends, so the proxy can
org-gate writes (`auth.allowed_orgs`) and cap size. No new MCP tool.

## Prerequisite (platform repo)

An R2 writer key scoped RW to just that bucket — terraform R2 + SOPS secret +
proxy deployment values. Small, self-contained.

## Deliberately out of scope

Parked until there's a real need — none are required to ship the above:

- `list` / `get` / `rm` — the URL is the receipt; download is `curl`.
- Private assets — the R2 public bucket has no per-object auth; a private bucket
  is a separate feature.
- Sandbox Python API (`assets.publish(fig)`) — natural Phase 2 once the CLI works,
  surfaced through `execute_python` (never a 4th MCP tool).
- Retention/GC — content-addressed uploads are immutable; add an R2 lifecycle
  policy only if growth becomes a cost problem.

## Open decision

Target the existing `-public` bucket under an `uploads/` prefix (ships now, but
`data.ethpandaops.io` reads as the xatu data surface), or the dedicated
`-dropbox` bucket behind a new `uploads.ethpandaops.io` CNAME (cleaner, one extra
`platform`-repo change). Prefix separation is fine to start.
