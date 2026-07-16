# Driving workflows with `panda workflow`

`panda workflow` is a thin client for the **workflow engine**. The engine designs,
publishes, and runs multi-step agent workflows. You use it to turn a plain-
language request into a completed workflow run.

**Three roles, strictly separated:**

- **The workflow engine owns drafting.** It has templates and knows about devnets/networks and
  inputs. You describe what is wanted in plain language; the engine writes the
  workflow spec. You never hand-author specs and never rewrite the user's words.
- **The user owns the publish/run decision.** A request like "use the workflow engine to get
  the status of X" is a *drafting brief* — it authorizes creating a whiteboard
  and drafting, **not** publishing or running. Publishing/running is a side
  effect the user must approve after seeing the draft. See "The publish/run
  gate" below; it defines exactly what counts as approval.
- **You (the operating agent) own the sequence.** `panda workflow` gives you raw
  primitives, one workflow-engine REST call per command. The checkpoints below are yours
  to enforce (the CLI's `--approved` tripwires backstop them, nothing more).
  Use `--json` and parse the output.

**Fresh by default.** Every new request gets a **new** whiteboard → draft →
review → run. Reuse an existing whiteboard/workflow/run **only** when the user
explicitly named its id (`wb_…`/`wf_…`/`run_…`) or said in so many words to
re-run an existing one. A leftover workflow from an earlier session that looks
like it matches the request is **not** an invitation to run it —
`workflow list` / `whiteboard list` exist to inspect state and resolve
something the user named, never to find something to run.

## The resource model

```
whiteboard  ── the planning space (holds sessions + drafts)     wb_…
  └─ session ── your chat with the worker that writes drafts    ses_…
       └─ draft ── a candidate workflow spec (iterate → revise)
            └─ publish ─▶ workflow ── the executable object     wf_…
                              └─ run ── one execution           run_…
```

## The operator loop

### 1. Create a whiteboard
```
panda workflow whiteboard create --requirements "<what the user wants>" --json
# → .id = wb_…
```

### 2. Ask the engine to draft — the user's words, verbatim
```
panda workflow session create <wb> --content "<what the user wants>" --json
# → .sessionId = ses_…
```
Pass the request through cleanly. Do **not** prepend your analysis, guess at a
spec, or expand the request — the engine drafts it.

### 3. Wait for the draft (watch two signals)
The draft can appear before the chat turn fully ends. Watch both:
```
panda workflow session logs <wb> <ses> -f --json   # exits on the FIRST turn/operation terminal
panda workflow whiteboard get <wb> --json          # → .whiteboard.latestDraftId once present
```
`session logs -f` exits on the **first** turn-terminal event; a queued item starts a
**new** turn that is *not* followed — re-invoke `session logs -f` to follow it.

### 4. Show the draft to the user
Render the draft **to the user**. The review is for the human, not for you —
fetching the draft and reading it silently does not satisfy this step.
```
panda workflow draft show <wb> <draft>          # the review, pre-rendered
panda workflow draft show <wb> <draft> --json   # same review, structured
```
`draft show` is the plan output: id/revision/status, declared inputs (with
defaults), scalar + artifact outputs, and the task DAG in dependency order
(with `[quality-gate]` / loop / matrix annotations). Present it verbatim or
reformatted — but present it. Add brief notes on anything notable (loops,
`if` guards, `uses:` templates) and possible tweaks — only ones genuinely
worth asking about (missing inputs/outputs, weak verification, risky
assumptions). Do not pad the list.

For the raw draft object use `draft get <wb> <draft> --json`: the DAG is
`.graph.nodes[]`; declared inputs live in `.compiledSpecJson`, a JSON
**string** (`jq -r '.compiledSpecJson | fromjson | .inputs.values'`, defaults
at `.schema.default`). The top-level `.inputs` is the *provided-inputs
override* (usually `{}`), not the schema. **Never parse `authoredSpecYaml`.**

### 5. The review checkpoint — hard stop
After presenting the draft, **stop and ask**. Use a blocking question tool when
your harness has one (`AskUserQuestion` in Claude Code; an `elicitation/create`
request in MCP clients); otherwise ask in plain text and treat silence or an
ambiguous reply as *not* approved. Never auto-resolve this checkpoint. Offer
exactly these choices and follow the branch the user picks:

- **Publish and run** — default dispatch, no placement question (skip step 7);
  ask the follow preference (step 8), then run.
- **Publish and run with selected workers and/or agents** — do the dispatch
  placement elicitation (step 7), then the follow preference (step 8), then run.
- **Iterate** — ask what to change (offer the tweaks you spotted), send it per
  step 6, and re-review the **new** draft at this same checkpoint.
- **Stop** — leave the draft unpublished, report the captured IDs, and hand
  control back.

### 6. Iterate — send the change verbatim
```
panda workflow session send <wb> <ses> --content "Make it a loop." --json
```
Send only the user's change, in direct imperative form; no commentary, review
notes, or inferred rationale. If the user picked one of your suggested tweaks,
send only that tweak's text. If the feedback is ambiguous enough that rewriting
it would change its meaning, ask the user first. This starts a new turn —
re-invoke `session logs -f`, wait for the new draft (new revision / new
`latestDraftId`), and go back to step 4.

### 7. Dispatch placement (only on the "selected workers/agents" branch)
Skip this entirely for a plain "Publish and run" — default dispatch needs no
placement question. Otherwise, query the live options first — never guess
agent/model names:
```
panda workflow dispatch agents --json            # agent types, models, workers
panda workflow dispatch inventory --json    # (agent, model) pairs with healthy workers NOW
panda workflow dispatch workers --json           # worker ids/names for workerId pins
panda workflow dispatch effective --scope me --json   # what "default" resolves to
```
Read "default" from `.workflowTask.policy.agent` / `.model` — the top-level
resolved policy. If `policy.stages[]` is present it is an escalation/fallback
ladder, **not** the default. Offer only agents that appear in the inventory.
Ask one concise placement question (default vs a specific agent, or per-task
assignments). For per-task pins, build overrides keyed by spec node key:
```json
{"defaults": {"agent": "codex"}, "tasks": {"tasks.review": {"agent": "claude"}}}
```
Sanity-check the pin before running — always passing the exact runtime you
intend (a bare `{"kind":"workflow_task"}` can report a stage candidate instead):
```
panda workflow dispatch simulate --data '{"kind":"workflow_task","runtime":{"agent":"codex"}}'
```
If it comes back empty for the requested agent, tell the user the pin will not
place instead of running blind.

### 8. Ask the follow preference, then publish + run
Before running, ask one more question — how should the run be followed?

- **Follow live** — stream progress in the foreground (`run watch` /
  `run logs -f`); the conversation waits on the run.
- **Follow in the background** (best default when your harness runs commands
  in the background) — launch `run follow` as a background task and keep
  working with the user; relay notable transitions when you check in, and
  report the summary when the task's completion notification arrives.
- **Wait quietly** — no live progress; check at the end and report once.

Whichever they pick, tell the user they can steer any running task mid-flight —
redirect it without cancelling (step 10); steering works from the foreground
even while a background follow is watching. Then run:
```
panda workflow draft run <wb> <draft> --approved <draft> --json   # publish-and-run
# → .workflow.id = wf_… , .run.id = run_…
```
`--approved` must re-type the exact draft id the user approved — the CLI
refuses without it, and refuses a stale id after a newer revision. It is proof
the checkpoint happened, **not** the approval itself: passing it without the
user's explicit approval violates the gate. Inputs are usually optional —
the engine defaults them. Override with
`--inputs '{"values":{…}}'` only when needed (read what it accepts from the
draft's `compiledSpecJson`, step 4). Pass the assembled placement, if any, as
`--dispatch '{…}'`. If an agent was pinned, verify placement after the run
starts — check the run's operations resolve to the requested agent
(`panda workflow dispatch operations --json`, `runtime.resolved.agent`); on a
mismatch, cancel and report rather than letting it run on the wrong agent.

### 9. Follow the run
```
panda workflow run tasks  <wf> <run> --json         # task keys (for steering / narrow logs)
panda workflow run follow <wf> <run>                # background-friendly: deltas → stderr, one summary JSON → stdout
panda workflow run watch  <wf> <run> --json         # foreground: full snapshot stream
panda workflow run logs   <wf> <run> -f --json      # raw in-task detail, including caught errors (see below)
```
All three exit at terminal `run.status` (`completed|failed|cancelled`) with the
status exit code (0 completed, 1 failed/cancelled). `run watch` emits a full
`/state` snapshot per event — right for a foreground tail. `run follow` is the
one to background: stderr carries one short line per *change* (task/run
transitions, failures), stdout carries exactly **one** JSON summary at terminal
(status, duration, task counts, failed-task errors, outputs, artifacts), and a
dropped stream reconnects automatically — so reading its output later costs a
few lines, not an archive of snapshots. `run logs -f` streams pure worker-log
text and polls `/state` out-of-band for the terminal status.

Honor the user's follow preference. **Follow live:** report task transitions,
quality-gate outcomes, and errors as they happen; on a long quiet stretch, say
the run is still going and name the active task(s). **Background:** launch
`run follow` as a background task, keep working with the user, and when the
task exits read its stdout summary (the exit code already says
completed-vs-not) and report; don't rely solely on the completion notification
— check the task on your next natural turn if none arrived. When you hand the
run to the background, tell the user they can steer any running task mid-flight
(step 10) — redirect it without cancelling and without disturbing the follow. **Wait quietly:**
check at terminal and report once — but still look at failures rather than
assuming success. Do not disappear mid-run in any mode.

**Answering "what's happening" mid-run — inspect the worker log, don't just
re-read task states.** `run follow` renders run/task-status deltas; `run watch`
renders full `/state` snapshots, including operation and resource updates. But
neither includes raw worker-log events. An error a task handles internally need
not become a task or operation failure, so it can be absent from both views
while `run tasks` still shows the task plainly `running`. So when the user asks
what's happening, wants progress detail, or the run's purpose is to surface
breakage, do **not** answer from `run tasks` state alone. Read the current
history with `panda workflow run logs <wf> <run> --json`; add
`--spec-node <specNodeKey>` to scope it to the in-flight task. For continuous
monitoring, use `run logs <wf> <run> -f --json` and report what the worker is
actually doing, including caught-and-retried errors. Treat `run logs` as
required for status-on-demand and any breakage-hunting run; for the latter,
prefer a worker-log tail over a bare background `run follow` from the start.

### 10. Steer a running task (optional)
Redirect a task mid-flight without cancelling it:
```
panda workflow steer send <wf> <run> <task> --message "<new direction>" --json
```
When the user asks you to steer, **read the current run state first** — don't
steer blind. Pull `run tasks <wf> <run> --json` and confirm the task you mean to
target is actually in flight and `steerable`; a steer to a task that has already
settled returns `409`, and the loop parent silently accepts nothing (see below).
If more than one task is running, don't guess which one they mean: name the
in-flight tasks back to the user and confirm the target — and the exact
direction — before sending.

`<task>` is the task's `specNodeKey` from `run tasks` (e.g. `tasks.analyze`) — use
the real key you just read, never a guessed name. The task interrupts, applies your
direction, and still finishes with valid output. Steer to redirect; cancel
(`run cancel`) only to stop a task entirely.

Pass the user's steer direction through as close to verbatim as you can — it goes
straight to the running task. Don't paraphrase, expand, or "improve" it; only touch
the wording when it genuinely needs it (resolve a "that"/"it" that the task can't
see, or drop conversational filler), and keep their intent and specifics intact. If
their direction is ambiguous, ask them rather than guessing a rewrite.

For a **loop** task, steer the **inner iteration** node that `run tasks` shows
(`tasks.<loop>.<child>[iter=NNNN]`), not the loop parent — the parent isn't
steerable, so steering it does nothing.

### 11. Read outputs + artifacts
```
panda workflow run get <wf> <run> --json            # → .run.outputs (scalars)
```
Artifact outputs appear as `$resource` envelopes. Resolve them:
```
panda workflow artifact list <wf> <run> --json                 # find by slotName / mediaType
panda workflow artifact get  <wf> <run> <wart_…> --out out.md  # fetch bytes
```

### 12. Report back
Answer the original request from the scalar outputs and artifact content, and
include: the IDs (`wb_…`, approved `dr_…`, `wf_…`, `run_…`) so the side effect
is auditable; **frontend links** so the user can open things in a browser —
`panda workflow url whiteboard <wb>` / `url run <wf> <run>` (also on
`draft show`'s `whiteboardUrl` and `run follow`'s summary `links`; access is
the user's own workflow-engine login, never a panda or proxy token — omit links silently if the
server exposes no web origin); the final status; a task summary
(completed/failed counts, any failed-task errors); and the artifact list
(`slotName`, `mediaType`, size). If
artifacts exist and the user hasn't said what to do with them, ask once: print
inline, save to a directory, or leave it on the engine. For a failed or cancelled run,
still list produced artifacts and include the most relevant worker-log/error
excerpt. Never print credentials or secret values.

## The publish/run gate

Publishing/running is the side-effect boundary, and `draft run` /
`draft publish` / `run create` cross it. The CLI backstops all three —
`draft publish` and `draft run` refuse without `--approved <draftId>`, and
`run create` refuses without `--approved <workflowId>` — but the flag is a
tripwire, not the gate itself. It proves *you* completed the checkpoint; the
approval must come from the user. Never pass `--approved` on the user's
behalf.

`run create` carries a second condition on top of approval: it reuses an
existing workflow, which per "Fresh by default" is only legitimate when the
user explicitly named that workflow or asked to re-run one. "The user's goal
matches what some existing workflow does" satisfies neither condition.

**Approval that counts** (explicit, about publish/run, for the reviewed draft):

- "publish and run" / "approved to publish and run" / "run this exact draft";
- an initial request that *explicitly* says to draft, publish, and run without
  stopping for review (e.g. "draft and run it, don't ask").

**Not approval — do not cross the gate on these:**

- the original task request itself ("use the workflow engine to get the status of X" ≠ "run
  whatever you drafted") — never self-authorize by reading intent into the goal;
- an existing whiteboard/workflow you **found** via `workflow list` /
  `whiteboard list` that looks like it fits the request — discovery is not
  designation; only the user can name a thing to reuse;
- silence, or "ok" / "continue" / "looks good" when your question did not
  clearly ask for publish/run approval;
- approval given for an **older** draft after a newer revision appeared —
  approval binds to a specific `draftId`; re-review the new one.

If approval is missing, stop at the step-5 checkpoint and ask. When in doubt,
it is not approval — asking again is cheap; an unwanted run is not.

## Definition of done

Do not claim completion until:

- the run came from a **fresh** whiteboard → draft → review loop, or from a
  workflow/whiteboard the **user explicitly named** — never one you discovered;
- `whiteboardId`, `sessionId`, and the final `draftId` were captured;
- the draft was **shown to the user** (`draft show`: DAG +
  inputs/outputs/artifacts), not just fetched;
- the user explicitly approved publish/run at the checkpoint (or had
  pre-authorized it in so many words), any requested iterations produced a
  newer reviewed draft, and `--approved` carried the id of that reviewed draft;
- the placement branch matched the user's choice — default dispatch ran with no
  placement question; a pinned run was verified to resolve to the requested
  agent;
- the user was offered follow live / follow in the background / wait quietly,
  told about steering, and progress was followed per their choice — not
  silently abandoned mid-run;
- the run reached a terminal status, outputs were read from run state, and
  artifacts (if any) were resolved via `artifact list` / `artifact get`;
- the report includes IDs, status, outputs, artifacts, and frontend links
  (whiteboard + run, via `panda workflow url`) when the server exposes the engine's
  web origin; any failure names the failing command, the response, and the
  next diagnostic step.

## Gotchas

- **Draft-ready lag.** `latestDraftId` can trail the actual draft; if
  `whiteboard get` lags, check `draft list <wb>` / `session logs`. Watch both
  signals.
- **Streams may not close at terminal.** Don't treat stream EOF as the signal.
  `run watch` and `run follow` read terminal `run.status`
  (`completed|failed|cancelled`) from their `/state` snapshots (`follow` also
  reconnects on a dropped stream); `run logs -f` polls `/state` on the side for
  the same; session streams end on turn/operation-terminal events. All built in.
- **Caught in-task failures can be absent from state streams.** `run follow`
  renders run/task-status deltas and `run watch` renders full `/state`
  snapshots, but neither includes raw worker-log events. For status-on-demand,
  inspect `run logs`; for a breakage-hunting run, tail `run logs -f` from the
  start (`--spec-node <specNodeKey>` scopes either form) — see step 9.
- **Inputs are optional.** the engine defaults declared inputs — only pass `--inputs`
  to override.
- **`session send` always makes progress.** It defaults to `mode:"queue"`, which
  starts a new turn when the session is idle and enqueues behind a live turn — so a
  plain `session send` after a completed turn kicks off the next revision. Use
  `--interrupt` only to cut off a turn that is still running.
- **`draft run` is publish-and-run in one shot** — the highest-blast-radius
  command here. It belongs *after* the step-5 checkpoint, never before, and
  requires `--approved <draftId>` (so does `draft publish`).

## When to use `panda workflow` (and when not)

- **Use it** to create/inspect/run/steer a workflow or read its outputs.
- **Do not use it** to query Ethereum data — that's the panda data workflow
  (`panda datasets` → `panda search examples` → `panda execute`).

See `panda workflow docs api` for the full endpoint cheat-sheet, or
`panda workflow --help`.
