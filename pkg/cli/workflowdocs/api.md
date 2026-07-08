# Workflow API cheat-sheet

The workflow engine's REST endpoints `panda workflow` wraps. Every command is
exactly one call.

**Transport.** Requests travel `panda → server (/api/v1/workflow/*) → proxy
(/workflow/*) → engine (/api/v1/*)`. The proxy holds the credential and injects
it (its configured `api_token`, or the caller's own bearer under passthrough
auth) — the CLI and server never handle a token. The engine's paths are under
`/api/v1`; `panda workflow api <METHOD> <path>` reaches any of them directly
(path relative to the engine's `/api/v1`, e.g. `panda workflow api GET whiteboards`,
which the server roots at `/api/v1/workflow/whiteboards`).

## CLI ↔ API resources

Each CLI noun maps to one engine resource (this is why the server paths double
the word — `/api/v1/workflow/` + the engine's `/workflows` resource):

```
| CLI noun   | API resource                                |
|------------|---------------------------------------------|
| whiteboard | /whiteboards/{wb}                           |
| session    | /whiteboards/{wb}/sessions/{ses}            |
| draft      | /whiteboards/{wb}/drafts/{draft}            |
| (base)     | /workflows/{wf}, /workflows/{wf}/releases   |
| run        | /workflows/{wf}/runs/{run}  (+ /state SSE)  |
| dispatch   | /agents, /workers, /dispatch/*              |
```

## Basics

- **Base path:** `/api/v1` (on the engine). Runs are always workflow-scoped
  (`/workflows/{wf}/runs/…`); there is no top-level `/runs`.
- **Auth:** the proxy injects the credential; you never handle a token. A
  misconfigured or unavailable path returns `401`/`403`/`502`/`503`.
- **Frontend links:** panda-server (not the engine) serves `GET /api/v1/workflow-info`
  → `{enabled, web_base_url}` — the web origin for human-facing links
  (`/whiteboards/{wb}`, `/workflows/{wf}/runs/{run}`); `panda workflow url` wraps
  it. Users log in there themselves; no token is exposed.
- **Errors:** RFC 7807 `application/problem+json` — `{type, title, status,
  detail}`. `type` is always `about:blank` (don't branch on it). An **unknown
  `/api/v1` route** returns `text/plain` "404 page not found" instead of JSON, so
  read `detail`/`title` when present and fall back to the raw body.
- **Spec fields:** `authoredSpecYaml` / `compiledSpecJson` are large embedded
  strings on draft/release objects. For the **DAG** read `graph`; for **declared
  inputs/defaults** parse `compiledSpecJson` (a JSON string) and read
  `.inputs.values`: `jq -r '.compiledSpecJson | fromjson | .inputs.values'`.
  Defaults live at `.schema.default`. Never parse `authoredSpecYaml`.
- **Idempotency:** `POST …/sessions/{sid}/items` requires an `Idempotency-Key`;
  `panda workflow session send` mints one per invocation (`--idempotency-key` to
  reuse for cross-retry dedup). Same key + same body → replay; same key +
  different body → `409`.
- **ID prefixes:** `wb_` whiteboard · `dr_` draft · `ses_` session · `wf_`
  workflow · `wfr_` release · `run_` run · `wart_` artifact · `wop_` worker
  operation · `dep_` deployment · `trg_` trigger · `tlitem1.<base64>` queue/steer
  items.

## SSE streams

`…/stream` endpoints return `Content-Type: text/event-stream` with
`X-Accel-Buffering: no`. Framing is `id:` / `event:` / `data:` per event,
blank-line separated. The engine also sends `:`-comment heartbeats (`: ping`) and an
initial `event: sync` frame after backfill — treat neither as data. Resume via
`Last-Event-ID` (allow-listed) or a query cursor.

- **State streams** (`…/state/stream`) resume with a numeric `?afterSeq=`; refetch
  the state snapshot when an event arrives (events are notifications, not state).
- **Worker-log streams** (`…/worker-log/stream`) resume with an opaque base64
  `?cursor=`.
- Streams stay open and are **not** guaranteed to close at a terminal state —
  detect terminal `run.status` / turn-end events client-side and disconnect.

## Whiteboards

| Method | Path | Notes |
|--------|------|-------|
| POST | `/whiteboards` | body `{name, requirements, inputs?}` → `{id: wb_…, name, status, …}` |
| GET | `/whiteboards` | → `{items:[{id,name,requirements,status,…}]}` |
| GET | `/whiteboards/{wb}/state` | snapshot: `whiteboard.latestDraftId`, `whiteboard.latestSessionId`, `drafts[]` (each carries `graph`, DAG at `graph.nodes`), `sessions[]`, `operations[]`, `cursor` |
| GET | `/whiteboards/{wb}/state/stream?afterSeq=` | SSE: `draft.updated`, `session.updated`, `operation.updated`, `whiteboard.updated`, `state.updated` → refetch state |

## Sessions

| Method | Path | Notes |
|--------|------|-------|
| POST | `/whiteboards/{wb}/sessions` | body `{title?, initialItem?:{type:"message",mode:"queue",content}}` → `201` `{sessionId: ses_…}`. `initialItem` is optional; omit it entirely when there is no initial content (a partial `initialItem` is rejected). |
| POST | `/whiteboards/{wb}/sessions/{sid}/items` | body `{type:"message",mode:"queue"\|"stop_and_send",content}`; `mode` is REQUIRED. `queue` starts a turn when idle and enqueues behind a live turn; `stop_and_send` interrupts a live turn. `Idempotency-Key` required. → the queued item (`id: tlitem1.<base64>`) |
| GET | `/whiteboards/{wb}/sessions/{sid}/worker-log?limit=` | history; `{items:[…], liveCursor}` |
| GET | `/whiteboards/{wb}/sessions/{sid}/worker-log/stream?cursor=` | SSE worker typing/turn events |
| GET | `…/turns` | cursor-paginated `{items, liveCursor, beforeCursor, hasMoreBefore, lastEventSeq}` |
| GET | `…/queue` | `{parked, pending}` |
| POST | `…/resume` · `…/skip` · `…/interrupt` | session control: `interrupt` → `404` if no active worker op; `skip` → `409` if no pending elicitation |
| POST/DELETE | `…/queue/{itemId}/skip` · `…/queue/{itemId}/retry` · `DELETE …/queue/{itemId}` | queue-item control: `skip`/delete → `200`; `retry` valid only on a parked item |

**Session worker-log events:** typing `worker.message.delta`; final text
`worker.message.final` / `output.text`; structured
`worker.structured_output.updated` / `output.structured`; end-of-turn
`worker.operation.{completed,failed,cancelled,interrupted}`,
`turn.{completed,failed,interrupted}`, `whiteboard.turn.completed`,
`whiteboard.session.failed`. An `event: sync` frame means replay caught up (not
turn done).

## Drafts

| Method | Path | Notes |
|--------|------|-------|
| GET | `/whiteboards/{wb}/drafts` | → `{items:[{id:dr_…,revision,status,graph{nodes[],edges[]},…}]}` (full draft objects; DAG at `graph.nodes`) |
| GET | `/whiteboards/{wb}/drafts/{dr}` | full draft |
| POST | `/whiteboards/{wb}/drafts` · `…/{dr}/revisions` | manual draft/revision `{authoredSpecYaml, inputs?}` → `201` with a new draft (`revision`+1, `status:candidate`) |
| POST | `/whiteboards/{wb}/drafts/{dr}/publish` | → `201` `{workflow, release, deployment, trigger}` |
| POST | `/whiteboards/{wb}/drafts/{dr}/run` | publish-and-run → `202` `{workflow, release, deployment, trigger, run}`; optional body `{inputs, dispatchPolicy}` |

**Draft top-level keys:** `id`, `whiteboardId`, `revision`, `status`
(`candidate`|`published`), `authoredSpecYaml`, `compiledSpecJson`, `inputs`,
`graph`, timestamps. `inputs` is the provided-inputs *override* (usually `{}`),
NOT the declared schema — read the schema from `compiledSpecJson`. `graph`
carries `nodes` and `edges`; each node is
`{id, name, path, phase, kind, needs?, qualityGate, hasRetry}`.

## Workflows & runs

| Method | Path | Notes |
|--------|------|-------|
| GET | `/workflows` | → `{items:[{id: wf_…, name, status, …}]}` |
| GET | `/workflows/{wf}` | `{id,name,status,currentReleaseId,…}` |
| GET | `/workflows/{wf}/releases/{rel}` | `{id,releaseNumber,authoredSpecYaml,compiledSpecJson,graph,dispatchPolicy,…}` |
| POST | `/workflows/{wf}/runs` | body `{inputs:{values,artifacts,secrets}?, dispatchPolicy?}` → `202` `{id: run_…, status}` |
| GET | `/workflows/{wf}/runs` | → `{items:[…]}` — each item is a full run object |
| GET | `/workflows/{wf}/runs/{run}` | full run: `id, status, inputs, outputs, dispatchPolicy, error, startedAt, finishedAt, …` |
| GET | `/workflows/{wf}/runs/{run}/state` | `run.status`, `run.outputs`, `tasks[].outputs`; top-level `operations[]`, `resources[]` |
| GET | `/workflows/{wf}/runs/{run}/state/stream?afterSeq=` | SSE: `run.updated`, `task.updated`, `operation.updated`, `resource.updated`, `state.updated` |
| GET | `/workflows/{wf}/runs/{run}/tasks` | `{items:[{id(=specNodeKey), specNodeKey, taskKey, workerOperationId, status, steerable}]}` (`workflowTaskExecutionId` may be absent/null on live runs) |
| GET | `/workflows/{wf}/runs/{run}/worker-log[?limit=,specNodeIds[]=,taskExecutionIds[]=]` + `/worker-log/stream?cursor=` | run logs; filter by spec node or task execution |
| POST | `/workflows/{wf}/runs/{run}/cancel` | cancel → `204` no body; status → `cancelled` |

**Terminal `run.status`:** `completed`, `failed`, `cancelled`. The worker-log
stream carries no `run.status`, so `run logs -f` reads terminal status from a
separate `/state` poll; `run watch` reads it from the `/state` snapshot it
refetches per event.

## Steering (mid-run task control)

Redirect a running task without cancelling; it resumes with the new direction and
still finishes with validated output.

| Method | Path | Notes |
|--------|------|-------|
| POST | `…/runs/{run}/task-executions/{task}/steer` | body `{message}` → `202` `{steerId, disposition}` |
| GET | `…/task-executions/{task}/queue` | `{consumed, dismissed, parked, pending, retracted}` |
| GET | `…/task-executions/{task}/turns` | cursor-paginated turns |
| POST | `…/queue/{itemId}/dismiss` · `/retry` | `dismiss` → `200` (status → `retracted`); `retry` valid only on a parked item |

`{task}` is the task's `specNodeKey` (its `id` in `run tasks`, e.g.
`tasks.fetch_weather`). For a loop task the steerable unit is the inner iteration
(`tasks.<loop>.<child>[iter=NNNN]`) — the loop parent is not steerable. URL-encode
the `[ ] =` in the key (the CLI does this for you). `disposition` ∈
`interrupt_requested`, `queued_behind_turn`, `queued_no_live_operation`. A steer
after the task has settled returns `409` "workflow task execution is terminal".

## Outputs & artifacts

- Scalar outputs: `state.run.outputs` and `state.tasks[].outputs`.
- Artifact/secret outputs arrive as `$resource` envelopes:
  ```json
  { "report": { "$resource": { "kind": "artifact", "ref": "tasks.write.outputs.artifacts.report" } } }
  ```

| Method | Path | Notes |
|--------|------|-------|
| GET | `…/runs/{run}/resources` | → rows `{id: wart_…, slotName, mediaType, sizeBytes, ref, taskKey}` |
| GET | `…/runs/{run}/artifacts/{wart_…}/content` | raw bytes (honor `Content-Type`; may be binary) |

Choose the concrete row by `slotName` / `mediaType` (matrix/loop outputs return
one row per producing task instance).

## Dispatch / agents / workers (placement)

| Method | Path | Notes |
|--------|------|-------|
| GET | `/agents` | → `{agents:{<name>:{name,type,models,workerIds}}}` |
| GET | `/dispatch/inventory` | → `{entries:[{agent, model?, workerCount, availableSlots}]}` (`model` optional) |
| GET | `/dispatch/effective[?scope=me\|org]` | `scope` is optional; omit for the server default. → `{mode, principal, scope, …}` |
| GET | `/dispatch/health` | `{cooldowns}` |
| POST | `/dispatch/simulate` | body `{kind:"workflow_task", runtime:{agent,model}, …}` → `{candidates, empty, wouldSelect}` |
| GET | `/worker-identities` | `{workerIdentities:[{id,name,…}]}` |
| GET | `/workers/operations` | `{items}` — queued/running worker operations |

`dispatchPolicy` on a run body is `WorkflowDispatchOverrides` (`defaults` +
per-task overrides keyed by spec node key like `tasks.review`). Supply only to
pin an agent/model/worker.

Read the **default** from `/dispatch/effective` `.workflowTask.policy.agent` /
`.model`; a `policy.stages[]` array is an escalation/fallback ladder, **not**
the default. For `simulate`, always pass the exact `runtime` you intend — a
bare `{"kind":"workflow_task"}` can report a stage candidate. After starting a
pinned run, verify the operation's `runtime.resolved.agent` (via
`/workers/operations`) matches the request; cancel on a mismatch.

## Uncurated endpoints (raw `api` hatch)

Reach these via `panda workflow api <METHOD> <path>`:

| Method | Path | Notes |
|--------|------|-------|
| GET | `/auth/me` (+ `/principals/me`) | the token's principal identity |
| GET | `/health` | liveness |
| GET · PATCH | `/whiteboards/{wb}` | fetch / update a whiteboard |
| POST | `/whiteboards/{wb}/archive` | archive (there is no hard `DELETE`) |
| GET | `/workflows/{wf}/releases` | release list |

The raw hatch reaches the full `/api/v1` with the injected token's authority —
including `secrets/*`, principal admin, `auth/tokens`, `templates/*`,
`deployments/*`, `workspaces/*` — so the proxy should inject a least-privilege
engine principal.
