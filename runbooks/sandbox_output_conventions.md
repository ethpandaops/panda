---
name: Produce Sandbox Charts, Files, and Sessions
description: Conventions for sandbox output — chart with the ethpandaops chartkit library, upload results with the storage library to get a shareable URL plus host path, and reuse a session across turns for multi-step analysis. Use whenever a task must produce an image, a downloadable file, or span multiple turns.
tags: [sandbox, chartkit, storage, session, visualization, output]
triggers:
  - make a chart or plot of query results
  - save results to a shareable file or url
  - publish an html report from the sandbox
  - storage upload url and workspace host path
  - keep dataframes between execution turns
  - which plotting library to use in the sandbox
---

Owns how to return charts, files, and multi-turn state from the sandbox. Reference when
a task must produce an image, share a file, or continue across turns.

## Inputs
Required: the data (or query) to render or save, and whether the task spans multiple turns.

## Output
A chart image artifact, an uploaded file with a shareable URL and host path, and/or a
reused session — as the task requires.

## Rules

- **Charts:** use the ethpandaops **chartkit** charting library and produce a real
  saved image artifact (chartkit replaces matplotlib, plotly, and ASCII art here).
  Look up the exact API by searching the examples index for "chartkit <chart type>";
  if the index misses, read the chartkit docs surface.
- **Files / sharing:** upload with the ethpandaops **storage** library —
  `storage.upload(path, remote_name=...)` returns `.url` and `.host_path`; surface
  both. Search the examples index for "storage upload file url"; if the index
  misses, read the storage docs surface.
- **Sessions:** for multi-step work, reuse one session across turns — create/attach
  via the session surface (the CLI spells it `panda session create` and
  `panda execute --session <id>`; there is no `--session-id` flag).
  What persists is the `/workspace` FILESYSTEM, not Python variables — each call is
  a fresh process, so save intermediate dataframes to `/workspace` (parquet/CSV/
  pickle) and reload them in later calls.
- Reference an example or the module's docs surface instead of guessing an API
  signature.

## Notes

- A saved image and a shareable URL are the deliverables users and graders expect —
  code or an inline table in their place is usually a miss.
