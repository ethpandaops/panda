/* panda case studio — frontend */

const S = {
  meta: null,
  questions: [],
  selected: null,
  detail: null,
  tab: "all",
  open: new Set(),          // expanded tool-call keys
  collapsedRuns: new Set(), // explicitly collapsed run bodies
  expandedRuns: new Set(),  // explicitly re-opened verdict-ed runs
  forms: {},                // per-question review form state
  models: new Set(),
  allModels: [],
  exporting: false,
  reviewMode: {},           // per-question: "mint" | "fix"
  fixForms: {},             // per-question fix-dispatch form state
  fixLog: {},               // per-fix accumulated log text
  fixLogOffset: {},         // per-fix byte offset
  diffOpen: {},             // per-fix diff <details> open state
  selRound: {},             // per-fix selected round idx
  logOpen: {},              // per-fix pipeline-log <details> open state
  vrunOpen: new Set(),      // expanded verify-timeline cards
  dispatching: false,
};

/* ------------------------------------------------ url routing
   /q/<qid>?tab=<list-tab>&mode=mint|fix&round=N — refresh-safe. */

function parseUrl() {
  const m = location.pathname.match(/^\/q\/(q_[a-z0-9]+)/);
  const p = new URLSearchParams(location.search);
  return {
    qid: m ? m[1] : null,
    tab: p.get("tab") || "all",
    mode: p.get("mode"),
    round: p.get("round") ? parseInt(p.get("round"), 10) : null,
    step: p.get("step") != null && p.get("step") !== "" ? parseInt(p.get("step"), 10) : null,
  };
}

function syncUrl() {
  const path = S.selected ? `/q/${S.selected}` : "/";
  const p = new URLSearchParams();
  if (S.tab !== "all") p.set("tab", S.tab);
  if (S.selected) {
    const mode = S.reviewMode[S.selected];
    if (mode) p.set("mode", mode);
    const fix = (S.detail?.fixes || []).filter((f) => f.status !== "discarded").slice(-1)[0];
    if (fix && mode === "fix") {
      const round = S.selRound[fix.id] ?? fix.current_round;
      if (round) p.set("round", round);
    }
  }
  const url = path + (p.toString() ? `?${p}` : "");
  if (url === location.pathname + location.search) return;
  // New question = a history entry; everything else replaces in place.
  const samePath = path === location.pathname;
  history[samePath ? "replaceState" : "pushState"]({}, "", url);
}

function applyUrl(u) {
  S.tab = u.tab;
  document.querySelectorAll("#tabs button").forEach((b) =>
    b.classList.toggle("active", b.dataset.tab === u.tab));
  S.selected = u.qid;
  if (u.qid && u.mode) S.reviewMode[u.qid] = u.mode;
}

function applyUrlFixState(u) {
  const fix = (S.detail?.fixes || []).filter((f) => f.status !== "discarded").slice(-1)[0];
  if (!fix) return;
  if (u.round) S.selRound[fix.id] = u.round;
}

/* ------------------------------------------------ prefs (localStorage) */

function savePrefs() {
  localStorage.setItem(
    "studio_prefs",
    JSON.stringify({
      models: [...S.models],
      runs: $("#q-runs").value,
      route: $("#q-route").value,
    })
  );
}

function loadPrefs() {
  try {
    return JSON.parse(localStorage.getItem("studio_prefs")) || null;
  } catch {
    return null;
  }
}

const $ = (sel, el = document) => el.querySelector(sel);
const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

const fmtMs = (ms) => {
  if (ms == null) return "—";
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
};
const fmtTokens = (t) => (t ? `${((t.input + t.output) / 1000).toFixed(1)}k tok` : "");
const ago = (iso) => {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${s | 0}s`;
  if (s < 3600) return `${(s / 60) | 0}m`;
  if (s < 86400) return `${(s / 3600) | 0}h`;
  return `${(s / 86400) | 0}d`;
};

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  if (!res.ok) {
    let msg = `${res.status}`;
    try { msg = (await res.json()).detail || msg; } catch {}
    throw new Error(msg);
  }
  return res.json();
}

function toast(msg, isErr = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = `toast show${isErr ? " err" : ""}`;
  clearTimeout(t._h);
  t._h = setTimeout(() => (t.className = "toast"), 4200);
}

/* ------------------------------------------------ composer */

function renderModels() {
  const box = $("#q-models");
  box.innerHTML = "";
  for (const m of S.models) {
    const chip = document.createElement("span");
    chip.className = "model-chip on";
    chip.textContent = m.replace("opencode-go/", "") + " ×";
    chip.title = `${m} — click to remove`;
    chip.onclick = () => {
      if (S.models.size > 1) S.models.delete(m);
      savePrefs();
      renderModels();
      renderModelDD();
    };
    box.appendChild(chip);
  }
  const add = document.createElement("span");
  add.className = "model-chip add";
  add.textContent = "+ models";
  add.onclick = () => {
    const dd = $("#model-dd");
    dd.hidden = !dd.hidden;
    if (!dd.hidden) {
      $("#model-search").focus();
      renderModelDD();
    }
  };
  box.appendChild(add);
}

function renderModelDD() {
  const list = $("#model-dd-list");
  const needle = ($("#model-search").value || "").toLowerCase();
  const items = S.allModels.filter((m) => m.toLowerCase().includes(needle)).slice(0, 250);
  list.innerHTML = items
    .map((m) => `<div class="model-dd-item${S.models.has(m) ? " on" : ""}" data-m="${esc(m)}">${esc(m)}</div>`)
    .join("") || `<div class="model-dd-item">no matches</div>`;
  list.querySelectorAll("[data-m]").forEach((el) => {
    el.onclick = () => {
      const m = el.dataset.m;
      S.models.has(m) ? (S.models.size > 1 && S.models.delete(m)) : S.models.add(m);
      savePrefs();
      renderModels();
      renderModelDD();
    };
  });
}

async function submitQuestion() {
  const input = $("#q-input");
  const btn = $("#q-submit");
  const msg = $("#q-msg");
  const question = input.value.trim();
  if (!question) { msg.textContent = "type a question first"; return; }
  btn.disabled = true;
  msg.textContent = "";
  try {
    const q = await api("/api/questions", {
      method: "POST",
      body: JSON.stringify({
        question,
        runs: parseInt($("#q-runs").value, 10) || 5,
        route: $("#q-route").value,
        expectation: $("#q-expect").value.trim(),
        models: [...S.models],
      }),
    });
    input.value = "";
    $("#q-expect").value = "";
    toast(`launched ${q.runs.length} agents on ${q.id}`);
    await refreshList();
    selectQuestion(q.id);
  } catch (e) {
    msg.textContent = e.message;
  } finally {
    btn.disabled = false;
  }
}

/* ------------------------------------------------ queue */

function tabMatch(q) {
  switch (S.tab) {
    case "running": return q.status === "running";
    case "needs_review": return q.status === "needs_review";
    case "done": return q.status === "reviewed" || q.status === "exported";
    case "archived": return q.status === "archived";
    default: return q.status !== "archived";
  }
}

/* Re-rendering on every poll tick makes the UI flash (entry animations replay,
   innerHTML rebuilds) — so every render function is keyed on its payload and
   skipped when nothing changed. */
const RENDER_KEYS = {};
function unchanged(slot, key) {
  if (RENDER_KEYS[slot] === key) return true;
  RENDER_KEYS[slot] = key;
  return false;
}

function renderStats() {
  const total = S.questions.length;
  const running = S.questions.filter((q) => q.status === "running").length;
  const review = S.questions.filter((q) => q.status === "needs_review").length;
  if (unchanged("stats", `${total}:${running}:${review}`)) return;
  $("#topstats").innerHTML =
    `<span><b>${total}</b> questions</span>` +
    `<span><b>${running}</b> running</span>` +
    `<span><b>${review}</b> awaiting review</span>`;
}

function renderQueue() {
  const box = $("#queue");
  const items = S.questions.filter(tabMatch);
  if (unchanged("queue", JSON.stringify([S.tab, S.selected, items]))) return;
  if (!items.length) {
    box.innerHTML = `<div style="padding:24px 16px;color:var(--faint);font-size:11px;">nothing here yet.</div>`;
    return;
  }
  box.innerHTML = items
    .map((q) => {
      const dots = q.runs
        .map((r) => {
          const v = r.verdict ? ` ${r.verdict}` : "";
          return `<span class="dot ${r.status}${v}" title="#${r.idx} ${esc(r.model)} — ${r.status}${v}"></span>`;
        })
        .join("");
      return `
      <div class="qcard${q.id === S.selected ? " selected" : ""}" data-qid="${q.id}">
        <div class="qcard-q">${esc(q.question)}</div>
        <div class="qcard-meta">
          <span class="qcard-dots">${dots}</span>
          <span class="badge ${q.status}">${q.status.replace("_", " ")}</span>
          ${q.fix && q.fix.status !== "discarded" ? `<span class="fix-badge ${fixBadgeCls(q.fix.status)}">fix</span>` : ""}
          <span class="qcard-time">${ago(q.created_at)}</span>
        </div>
      </div>`;
    })
    .join("");
  box.querySelectorAll(".qcard").forEach((el) => {
    el.onclick = () => selectQuestion(el.dataset.qid);
  });
}

/* ------------------------------------------------ detail */

async function selectQuestion(qid) {
  S.selected = qid;
  renderQueue();
  await refreshDetail(true);
  syncUrl();
}

async function refreshDetail(force = false) {
  if (!S.selected) return;
  const wasRunning = S.detail?.status === "running";
  if (!force && S.detail && S.detail.id === S.selected && S.detail.status !== "running") return;
  try {
    S.detail = await api(`/api/questions/${S.selected}`);
  } catch {
    S.detail = null;
    S.selected = null;
  }
  if (S.detail && wasRunning && S.detail.status !== "running") {
    toast(`all runs finished for ${S.detail.id}`);
  }
  if (S.detail && S.detail.status !== "running" && !S.forms[S.detail.id]) {
    await initForm(S.detail);
  }
  renderDetail();
}

async function initForm(q) {
  if (S.forms[q.id]?.touched) return;
  try {
    const d = await api(`/api/questions/${q.id}/draft`);
    if (S.forms[q.id]?.touched) return; // user edited while the draft was in flight
    S.forms[q.id] = {
      case_id: d.case_id,
      description: q.review?.description || "",
      tags: "",
      rubric: d.rubric,
      touched: false,
    };
  } catch {}
}

function verdictButtons(q, r) {
  const c = r.verdict === "correct" ? " on" : "";
  const i = r.verdict === "incorrect" ? " on" : "";
  return `
    <span class="verdict">
      <button class="vbtn correct${c}" data-v="correct" data-run="${r.idx}" title="mark correct">✓</button>
      <button class="vbtn incorrect${i}" data-v="incorrect" data-run="${r.idx}" title="mark incorrect">✗</button>
    </span>`;
}

function toolCallHtml(qid, runIdx, tc, ci) {
  const key = `${qid}:${runIdx}:${ci}`;
  const open = S.open.has(key) ? " open" : "";
  const status = tc.status === "completed" ? "" : ` ${tc.status}`;
  const preview = (tc.input || "").replace(/\s+/g, " ").slice(0, 110);
  return `
  <div class="tc${open}${status}" data-key="${key}">
    <div class="tc-head">
      <span class="tc-caret">▶</span>
      <span class="tc-name">${esc(tc.name || "tool")}</span>
      <span class="tc-preview">${esc(preview)}</span>
      ${tc.status === "error" ? `<span class="tc-err-flag">ERR</span>` : ""}
      ${tc.status === "pending" || tc.status === "running" ? `<span class="tc-err-flag" style="color:var(--amber)">LIVE</span>` : ""}
      ${tc.status === "interrupted" ? `<span class="tc-err-flag" style="color:var(--faint)" title="the run died while this call was in flight">CUT</span>` : ""}
      <span class="tc-dur">${fmtMs(tc.duration_ms)}</span>
    </div>
    <div class="tc-body">
      <div class="tc-section">
        <div class="tc-label">input</div>
        <pre class="tc-pre" data-sk="${key}:in">${esc(tc.input || "(empty)")}</pre>
      </div>
      <div class="tc-section">
        <div class="tc-label">output</div>
        <pre class="tc-pre" data-sk="${key}:out">${esc(tc.output || "(no output yet)")}</pre>
      </div>
    </div>
  </div>`;
}

function runCardHtml(q, r) {
  const key = `${q.id}:${r.idx}`;
  // Verdict-ed runs fold away (the review is done) unless explicitly re-opened.
  const collapsed =
    S.collapsedRuns.has(key) || (!!r.verdict && !S.expandedRuns.has(key));
  const verdictCls = r.verdict ? ` v-${r.verdict}` : "";
  const live = r.status === "running";
  const stats = [
    r.tool_calls?.length ? `<span><b>${r.tool_calls.length}</b> tools</span>` : "",
    r.tokens ? `<span><b>${fmtTokens(r.tokens)}</b></span>` : "",
    `<span><b>${fmtMs(live ? r.elapsed_ms : r.duration_ms)}</b>${live ? " elapsed" : ""}</span>`,
  ].join("");
  const trace = (r.tool_calls || [])
    .map((tc, ci) => toolCallHtml(q.id, r.idx, tc, ci))
    .join("");
  const answer = r.error
    ? `<div class="run-error">${esc(r.error)}</div>`
    : r.answer
      ? `<div class="answer-label">${live ? "answer (streaming)" : "final answer"}</div>
         <pre class="answer${live ? " live" : ""}" data-sk="ans:${q.id}:${r.idx}">${esc(r.answer)}</pre>`
      : live || r.status === "queued"
        ? `<pre class="answer live thinking">thinking…</pre>`
        : "";
  return `
  <div class="run-card${verdictCls}">
    <div class="run-head" data-run-toggle="${q.id}:${r.idx}">
      <span class="run-id">#${r.idx + 1}</span>
      <span class="run-model">${esc(r.model.replace("opencode-go/", ""))}</span>
      <span class="status-pill ${r.status}">${r.status}</span>
      <span class="run-stats">${stats}</span>
      ${live || r.status === "queued" ? `<button class="cancel-btn" data-cancel="${r.idx}" title="cancel this run and flag it as a failure">✕ cancel</button>` : ""}
      ${["interrupted", "error", "cancelled"].includes(r.status) ? `<button class="retry-btn" data-retry="${r.idx}" title="reset this run and execute it again">↻ retry</button>` : ""}
      ${r.auto_review?.verdict ? `<span class="auto-chip ${r.auto_review.verdict}${r.verdict_auto ? "" : " overridden"}" title="auto-triage vs expectation: ${esc(r.auto_review.reason)}${r.verdict_auto ? "" : " (overridden by reviewer)"}">auto ${r.auto_review.verdict === "correct" ? "✓" : "✗"}</span>` : ""}
      ${r.status === "complete" ? verdictButtons(q, r) : ""}
    </div>
    <div class="run-body${collapsed ? " collapsed" : ""}">
      ${trace ? `<div class="trace${live ? " trace-live" : ""}" data-sk="tr:${q.id}:${r.idx}">${trace}</div>` : ""}
      ${answer}
    </div>
  </div>`;
}

/* ------------------------------------------------ fix pipeline panel */

const FIX_STAGES = ["worktree", "codex", "audit", "build", "verify", "awaiting_review", "pr_open"];
const FIX_STAGE_ALIAS = { queued: "worktree", amend: "audit", opening_pr: "pr_open", failed: null, discarded: null };
const FIX_ACTIVE = ["queued", "worktree", "codex", "audit", "amend", "build", "verify", "opening_pr"];

function fixBadgeCls(status) {
  if (FIX_ACTIVE.includes(status)) return "active";
  return status;
}

function progressStripHtml(f) {
  const cur = FIX_STAGE_ALIAS[f.status] !== undefined ? FIX_STAGE_ALIAS[f.status] : f.status;
  const curIdx = FIX_STAGES.indexOf(cur);
  return `<div class="fix-progress">${FIX_STAGES.slice(0, 5).map((s, i) => {
    let cls = "";
    if (i < curIdx) cls = "done";
    else if (s === cur) cls = "now";
    return `<span class="fix-progress-seg ${cls}" title="${s}"></span>`;
  }).join("")}</div>`;
}

function roundPillsHtml(f, selIdx) {
  const rounds = f.rounds || [];
  if (rounds.length < 2) return "";
  const chain = new Set(f.chain || []);
  return `<span class="round-pills">${rounds.map((r) => {
    const cls = [
      "round-pill",
      roundStatusCls(r),
      chain.has(r.idx) ? "" : "abandoned",
      r.idx === selIdx ? "sel" : "",
    ].join(" ");
    return `<button class="${cls}" data-round="${r.idx}"
      title="round ${r.idx} · ${r.kind} · ${r.status}${r.parent ? ` (from R${r.parent})` : ""}${chain.has(r.idx) ? "" : " · off the current branch"}">R${r.idx}${f.pr_round === r.idx ? "·PR" : ""}</button>`;
  }).join("")}</span>`;
}

function fixForm(qid) {
  return (S.fixForms[qid] ||= {
    problem: "", hints: "", expected: "", runs: 3, model: "", amendHints: "", touched: false,
  });
}

function fixFormHtml(q) {
  const f = fixForm(q.id);
  const models = [...new Set(q.runs.map((r) => r.model))];
  const badModel = q.runs.find((r) => r.verdict === "incorrect")?.model || models[0];
  return `
  <div class="fix-form">
    <div class="review-hint">describe what's actually broken — codex gets the ✗ runs as evidence,
      finds the root cause in a fresh panda worktree, the diff is adversarially audited
      (answer-leakage / misplacement / overfit guards), rebuilt, and re-verified against a scratch
      server before you decide on a PR.</div>
    <label class="field"><span>what's wrong (required)</span>
      <textarea id="fx-problem" rows="2" placeholder="e.g. agent counted only the last N rows instead of the full hour window">${esc(f.problem)}</textarea></label>
    <label class="field"><span>fix hints — where to look, how you'd fix it</span>
      <textarea id="fx-hints" rows="3" placeholder="optional: suspect docs/examples/module, suggested approach…">${esc(f.hints)}</textarea></label>
    <div class="fix-form-row">
      <label class="field"><span>expected behavior / answer</span>
        <input id="fx-expected" value="${esc(f.expected)}" placeholder="optional"></label>
      <label class="field"><span>verify runs</span>
        <input id="fx-runs" type="number" min="1" max="6" value="${f.runs}"></label>
      <label class="field"><span>verify model</span>
        <select id="fx-model">${models.map((m) =>
          `<option value="${esc(m)}"${(f.model || badModel) === m ? " selected" : ""}>${esc(m.replace("opencode-go/", ""))}</option>`).join("")}
        </select></label>
    </div>
    <div class="review-actions">
      <button class="export-btn" id="fx-dispatch" ${S.dispatching ? "disabled" : ""}>dispatch codex fix ▸</button>
      <span class="review-msg">runs in a worktree of ../panda — your checkout is untouched</span>
    </div>
  </div>`;
}

function diffHtml(diff) {
  return diff.split("\n").map((l) => {
    let cls = "";
    if (/^(diff --git|index |new file|deleted file|\+\+\+ |--- )/.test(l)) cls = " d-meta";
    else if (l.startsWith("@@")) cls = " d-hunk";
    else if (l.startsWith("+")) cls = " d-add";
    else if (l.startsWith("-")) cls = " d-del";
    return `<div class="d-line${cls}">${esc(l) || " "}</div>`;
  }).join("");
}

function roundStatusCls(r) {
  if (r.status === "running") return "running";
  if (r.status === "verified") return "verified";
  return "bad";
}

function roundShortSummary(rnd) {
  if (rnd.summary) return rnd.summary;
  const first = (rnd.codex_summary || "").split("\n").find((l) => l.trim());
  return first ? first.trim().slice(0, 200) : "(no summary)";
}

function roundOutcome(rnd) {
  if (rnd.status === "verified") return { label: "audit ✓ build ✓", cls: "ok" };
  if (rnd.status === "audit_blocked") return { label: "audit ✗", cls: "bad" };
  if (rnd.status === "build_failed") return { label: "build ✗", cls: "bad" };
  if (rnd.status === "running") return { label: "running", cls: "live" };
  return { label: rnd.status, cls: "bad" };
}

function fixTimelineHtml(q, f, selRoundIdx) {
  const cards = [];
  for (const rnd of f.rounds || []) {
    const rkey = `${f.id}:r${rnd.idx}`;
    const ropen = S.vrunOpen.has(rkey);
    const dim = selRoundIdx != null && rnd.idx !== selRoundIdx;
    const out = roundOutcome(rnd);
    cards.push(`
    <div class="vrun-card round${dim ? " dim" : ""}">
      <div class="vrun-head" data-vrun="${rkey}">
        <span class="tc-caret" style="transform:rotate(${ropen ? 90 : 0}deg)">▶</span>
        <span class="vrun-round">R${rnd.idx}</span>
        <span class="vrun-n">codex · ${esc(rnd.kind)}</span>
        <span class="vrun-ans sans">${esc(roundShortSummary(rnd))}</span>
        <span class="vrun-outcome ${out.cls}">${out.label}</span>
      </div>
      ${ropen ? `
      <div class="vrun-body">
        ${rnd.hints ? `<div class="round-hints-line">hints: “${esc(rnd.hints)}”</div>` : ""}
        ${rnd.codex_summary ? `<pre class="tc-pre" data-sk="rs:${rkey}" style="max-height:420px">${esc(rnd.codex_summary)}</pre>` : `<div class="review-hint">no codex narrative captured for this round.</div>`}
      </div>` : ""}
    </div>`);
    (rnd.verify || []).forEach((v, i) => {
      const key = `${f.id}:${rnd.idx}:${i}`;
      const open = S.vrunOpen.has(key);
      const ans = (v.error || v.answer || "(no answer)").replace(/\s+/g, " ");
      const trace = open
        ? (v.tool_calls || []).map((tc, ci) => toolCallHtml(q.id, `vf-${rnd.idx}-${i}`, tc, ci)).join("")
        : "";
      cards.push(`
      <div class="vrun-card${v.error ? " err" : ""}${dim ? " dim" : ""}">
        <div class="vrun-head" data-vrun="${key}">
          <span class="tc-caret" style="transform:rotate(${open ? 90 : 0}deg)">▶</span>
          <span class="vrun-round">R${rnd.idx}</span>
          <span class="vrun-n">verify ${i + 1}</span>
          <span class="vrun-ans">${esc(ans.slice(0, 200))}</span>
          <span class="vrun-meta">${(v.tool_calls || []).length}t · ${fmtMs(v.duration_ms)}</span>
        </div>
        ${open ? `
        <div class="vrun-body">
          ${v.error ? `<div class="run-error">${esc(v.error)}</div>` : ""}
          ${v.answer ? `<pre class="answer" data-sk="va:${key}">${esc(v.answer)}</pre>` : ""}
          ${trace ? `<div class="trace" style="margin-top:10px" data-sk="vt:${key}">${trace}</div>` : ""}
        </div>` : ""}
      </div>`);
    });
  }
  if (!cards.length) return "";
  return `
  <div class="glance">
    <div class="tc-label">timeline — each codex round + its verification runs (baselines are above, in the question's runs)</div>
    ${cards.join("")}
  </div>`;
}

function fixStatusHtml(q, f) {
  const form = fixForm(q.id);
  const active = FIX_ACTIVE.includes(f.status);
  const rounds = f.rounds || [];
  const selIdx = S.selRound[f.id] ?? f.current_round ?? (rounds[rounds.length - 1] || {}).idx;
  const rnd = rounds.find((r) => r.idx === selIdx) || rounds[rounds.length - 1];
  const canAct = !active && f.status !== "discarded";
  const files = rnd ? (rnd.diff.match(/^diff --git/gm) || []).length : 0;
  const adds = rnd ? rnd.diff.split("\n").filter((l) => /^\+[^+]/.test(l)).length : 0;
  const dels = rnd ? rnd.diff.split("\n").filter((l) => /^-[^-]/.test(l)).length : 0;
  const auditOk = rnd?.audit && !rnd.audit.blocked;
  const logOpen = S.logOpen[f.id] ?? active;
  return `
  <div class="fix-status" data-fid="${f.id}">
    <div class="fix-bar">
      <div class="fix-bar-row">
        <span class="fix-badge ${fixBadgeCls(f.status)}">${f.status.replace("_", " ")}</span>
        ${rnd ? `<span class="fix-head-fact">R${rnd.idx} ${esc(rnd.kind)}</span>` : ""}
        ${rnd?.diff ? `<span class="fix-head-fact">${files} file${files === 1 ? "" : "s"} <b class="plus">+${adds}</b> <b class="minus">−${dels}</b></span>` : ""}
        ${rnd?.audit ? `<span class="fix-head-fact ${auditOk ? "ok" : "bad"}">${auditOk ? "audit ✓" : "audit ✗"}</span>` : ""}
        ${roundPillsHtml(f, rnd?.idx)}
        <button class="log-toggle${logOpen ? " on" : ""}" id="fix-log-toggle" data-fid="${f.id}">log</button>
        <span class="fix-head-id" title="branch ${esc(f.branch)}">${f.id}</span>
      </div>
      ${active ? progressStripHtml(f) : ""}
    </div>
    ${f.error ? `<div class="fix-error">${esc(f.error)}</div>` : ""}
    ${f.pr_url ? `<div class="exported-note">PR (round ${f.pr_round ?? "?"}): <a class="pr-link" href="${esc(f.pr_url)}" target="_blank">${esc(f.pr_url)}</a></div>` : ""}
    ${logOpen ? `<pre class="fix-log" id="fix-log">${esc(S.fixLog[f.id] || "")}</pre>` : ""}
    ${fixTimelineHtml(q, f, rounds.length > 1 ? rnd?.idx : null)}
    ${rnd ? `
      ${rnd.audit?.blocked ? `
        <div class="audit-card blocked">
          <b>audit BLOCKED (round ${rnd.idx})</b> — ${esc(rnd.audit.summary || "")}
          ${(rnd.audit.findings || []).map((x) =>
            `<div class="audit-finding">[${esc(x.severity)}/${esc(x.kind)}] ${esc(x.file)}: ${esc(x.issue)}</div>`).join("")}
        </div>` : ""}
      ${rnd.build_error ? `<div class="fix-error">${esc(rnd.build_error)}</div>` : ""}
      ${rnd.diff ? `
        <details class="diff-details" data-fid="${f.id}" ${S.diffOpen[f.id] ?? f.status === "awaiting_review" ? "open" : ""}>
          <summary>diff @ round ${rnd.idx}${esc(rnd.commit ? ` · ${rnd.commit.slice(0, 10)}` : "")}</summary>
          <div class="diff-view" data-sk="diff:${f.id}:${rnd.idx}">${diffHtml(rnd.diff)}</div>
        </details>` : ""}
      ${auditOk ? `
        <details class="aux-details">
          <summary>audit verdict (clean)</summary>
          <div class="audit-card clean" style="margin:8px 0 0">${esc(rnd.audit.summary || "")}</div>
        </details>` : ""}` : ""}
    ${canAct && rnd ? `
      <label class="field" style="margin-top:12px"><span>fresh hints — forks a new round off round ${rnd.idx}</span>
        <textarea id="fx-fork-hints" rows="2" placeholder="e.g. wrong layer — move the guidance into the dataset pack; or: also cover the prometheus case">${esc(form.amendHints)}</textarea></label>` : ""}
    <div class="review-actions" style="margin-top:10px">
      ${canAct && rnd?.status === "verified" ? `<button class="export-btn" id="fx-pr">open PR from round ${rnd.idx} ▸</button>` : ""}
      ${f.status === "failed" ? `<button class="export-btn" id="fx-resume" title="adopt the preserved worktree changes and re-run audit → build → verify (no new codex pass)">resume pipeline ▸</button>` : ""}
      ${canAct && rnd ? `<button class="ghost-btn" id="fx-fork-btn">fork from round ${rnd.idx} ▸</button>
        <button class="danger-btn" id="fx-discard">discard fix + worktree</button>` : ""}
      ${active ? `<span class="review-msg">pipeline running — safe to navigate away</span>` : ""}
    </div>
  </div>`;
}

function fixPanelHtml(q) {
  const fixes = (q.fixes || []).filter((f) => f.status !== "discarded");
  const latest = fixes[fixes.length - 1];
  return latest ? fixStatusHtml(q, latest) : fixFormHtml(q);
}

function bottomPanelHtml(q) {
  if (q.status === "running") return "";
  const hasFix = (q.fixes || []).some((x) => x.status !== "discarded");
  const mode = S.reviewMode[q.id] || (hasFix ? "fix" : "mint");
  const latest = (q.fixes || []).filter((x) => x.status !== "discarded").slice(-1)[0];
  const fixTag = latest ? ` · <span class="fix-badge ${fixBadgeCls(latest.status)}">${latest.status.replace("_", " ")}</span>` : "";
  return `
  <div class="review-panel">
    <div class="mode-tabs">
      <button data-mode="mint" class="${mode === "mint" ? "active" : ""}">mint test case</button>
      <button data-mode="fix" class="${mode === "fix" ? "active" : ""}">work on fix${fixTag}</button>
    </div>
    ${mode === "mint" ? mintPanelHtml(q) : fixPanelHtml(q)}
  </div>`;
}

function mintPanelHtml(q) {
  const f = S.forms[q.id] || { case_id: "", description: "", tags: "", rubric: "" };
  const nCorrect = q.runs.filter((r) => r.verdict === "correct").length;
  const exported = q.review?.exported_at;
  return `
    <div class="review-hint">
      tick ✓ on the runs whose answers are correct, then export a draft case.
      <b style="color:var(--green)">${nCorrect}</b> approved so far.
    </div>
    <div class="review-grid four">
      <label class="field"><span>case id</span>
        <input id="rv-case-id" value="${esc(f.case_id)}"></label>
      <label class="field"><span>description</span>
        <input id="rv-desc" value="${esc(f.description)}" placeholder="(defaults to the question)"></label>
      <label class="field"><span>tags (comma sep)</span>
        <input id="rv-tags" value="${esc(f.tags)}" placeholder="clickhouse, blocks"></label>
      <label class="field"><span>cases file</span>
        <select id="rv-cases-file">
          ${(S.meta.cases_files || []).map((cf) =>
            `<option value="${cf}"${f.cases_file === cf ? " selected" : ""}>${cf}</option>`).join("")}
        </select></label>
    </div>
    <label class="field"><span>rubric draft</span>
      <textarea id="rv-rubric" rows="6">${esc(f.rubric)}</textarea></label>
    <label class="smoke-check">
      <input type="checkbox" id="rv-smoke" ${f.smoke ? "checked" : ""}>
      include in smoke tests <span class="review-msg">(adds the <code>smoke</code> tag — runs on every PR, keep it fast &amp; rock-solid)</span>
    </label>
    <div class="review-actions">
      <button class="export-btn" id="rv-export-pr" ${nCorrect === 0 || S.exporting ? "disabled" : ""}>
        mint case → open PR ▸
      </button>
      <button class="ghost-btn" id="rv-export" ${nCorrect === 0 || S.exporting ? "disabled" : ""}>
        ${exported ? "re-export draft only" : "export draft only"}
      </button>
      <button class="ghost-btn" id="rv-auto-draft" ${nCorrect === 0 || S.exporting ? "disabled" : ""}>✨ auto-draft</button>
      <button class="ghost-btn" id="rv-redraft">redraft rubric from ticks</button>
      <span class="review-msg" id="rv-msg"></span>
    </div>
    ${q.review?.pr_url ? `<div class="exported-note">case PR: <a class="pr-link" href="${esc(q.review.pr_url)}" target="_blank">${esc(q.review.pr_url)}</a></div>` : ""}
    ${exported ? `<div class="exported-note">exported ${esc(q.review.case_id)} → studio_data/approved/ (${esc(q.review.exported_at)})</div>` : ""}`;
}

function renderDetail(force = false) {
  const box = $("#detail");
  const q = S.detail;
  const key = q
    ? JSON.stringify([
        q.id, q.status, q.review, q.runs, q.fixes, S.exporting, S.dispatching,
        S.forms[q.id], S.reviewMode[q.id], S.fixForms[q.id], S.selRound, [...S.vrunOpen],
      ])
    : "empty";
  if (!force && unchanged("detail", key)) return;
  if (force) RENDER_KEYS.detail = key;
  // A re-render destroys every inner scrollable — capture their positions (and
  // the page scroll) by stable key and restore after the swap, so reading a
  // long tool output isn't yanked back to the top when live data lands.
  const scrolls = {};
  const atBottom = {};
  box.querySelectorAll("[data-sk]").forEach((el) => {
    if (el.scrollTop) scrolls[el.dataset.sk] = el.scrollTop;
    atBottom[el.dataset.sk] = el.scrollHeight - el.scrollTop - el.clientHeight < 30;
  });
  const pageScroll = box.scrollTop;
  if (!q) {
    box.innerHTML = `
      <div class="empty-state">
        <div class="empty-mark">🐼</div>
        <p>submit a question, then pick it from the queue.<br>five agents will chew on it via the local panda.</p>
      </div>`;
    return;
  }
  const runs = q.runs.map((r) => runCardHtml(q, r)).join("");
  box.innerHTML = `
    <div class="detail-head">
      <h2 class="detail-q">${esc(q.question)}</h2>
      ${q.expectation ? `<div class="expect-line">expects: ${esc(q.expectation)}</div>` : ""}
      ${q.archived_at ? `<div class="exported-note">archived ${esc(q.archived_at)} — ${esc(q.archived_reason || "")} <button class="ghost-btn" id="q-unarchive" style="margin-left:8px;padding:2px 10px;font-size:10px">unarchive</button></div>` : ""}
      <div class="detail-meta">
        <span class="chip">id <b>${q.id}</b></span>
        <span class="chip">network <b>${esc(q.network)}</b></span>
        <span class="chip">route <b>${esc(q.route)}</b></span>
        <span class="chip" title="${q.sandbox ? "agent shell runs in a docker container with no host filesystem" : "agent shell runs directly on the host"}">sandbox <b>${q.sandbox ? "on" : "off"}</b></span>
        <span class="chip">runs <b>${q.runs.length}</b></span>
        <span class="badge ${q.status}">${q.status.replace("_", " ")}</span>
        ${q.runs.some((r) => ["interrupted", "error", "cancelled"].includes(r.status))
          ? `<button class="retry-btn" id="q-retry-failed" title="reset and re-execute every interrupted/error/cancelled run">↻ retry failed runs</button>` : ""}
        ${!q.archived_at ? `<button class="del-btn" id="q-archive" title="hide from the working queues (auto-happens when a PR from this question merges)">archive</button>` : ""}
        <button class="del-btn" id="q-delete">delete</button>
      </div>
    </div>
    ${runs}
    ${bottomPanelHtml(q)}`;
  bindDetail(q);
  box.querySelectorAll("[data-sk]").forEach((el) => {
    const sk = el.dataset.sk;
    // Live feeds (streaming trace/answer) follow the tail — unless the reader
    // scrolled away from the bottom, in which case their position wins.
    if (el.matches(".trace-live, .answer.live") && atBottom[sk] !== false) {
      el.scrollTop = el.scrollHeight;
    } else if (scrolls[sk]) {
      el.scrollTop = scrolls[sk];
    }
  });
  if (pageScroll) box.scrollTop = pageScroll;
  const dd = $(".diff-details", box);
  if (dd)
    dd.addEventListener("toggle", () => {
      S.diffOpen[dd.dataset.fid] = dd.open;
    });
  const lt = $("#fix-log-toggle", box);
  if (lt)
    lt.onclick = () => {
      S.logOpen[lt.dataset.fid] = !(S.logOpen[lt.dataset.fid] ?? lt.classList.contains("on"));
      renderDetail(true);
    };
  const logEl = $("#fix-log", box);
  if (logEl) {
    const fid = logEl.closest(".fix-status")?.dataset.fid;
    logEl.textContent = S.fixLog[fid] || "";
    logEl.scrollTop = logEl.scrollHeight;
  }
}

function bindDetail(q) {
  const box = $("#detail");

  box.querySelectorAll(".tc-head").forEach((el) => {
    el.onclick = () => {
      const key = el.parentElement.dataset.key;
      S.open.has(key) ? S.open.delete(key) : S.open.add(key);
      el.parentElement.classList.toggle("open");
    };
  });

  box.querySelectorAll("[data-run-toggle]").forEach((el) => {
    el.onclick = (ev) => {
      if (ev.target.closest(".vbtn, .cancel-btn, .retry-btn")) return;
      const key = el.dataset.runToggle;
      if (el.nextElementSibling.classList.contains("collapsed")) {
        S.collapsedRuns.delete(key);
        S.expandedRuns.add(key);
      } else {
        S.collapsedRuns.add(key);
        S.expandedRuns.delete(key);
      }
      renderDetail(true);
    };
  });

  box.querySelectorAll(".vbtn").forEach((el) => {
    el.onclick = async (ev) => {
      ev.stopPropagation();
      const idx = parseInt(el.dataset.run, 10);
      const run = q.runs[idx];
      const verdict = run.verdict === el.dataset.v ? null : el.dataset.v;
      run.verdict = verdict;
      run.verdict_auto = false;
      if (verdict) S.expandedRuns.delete(`${q.id}:${idx}`); // fold the reviewed card
      try {
        await api(`/api/questions/${q.id}/runs/${idx}/verdict`, {
          method: "POST",
          body: JSON.stringify({ verdict }),
        });
      } catch (e) {
        toast(`verdict failed: ${e.message}`, true);
      }
      // ticks change the approved set — re-draft the rubric unless hand-edited
      if (!S.forms[q.id]?.touched) {
        delete S.forms[q.id];
        await initForm(q);
      }
      renderDetail(true);
      refreshList();
    };
  });

  const arch = $("#q-archive", box);
  if (arch)
    arch.onclick = async () => {
      await api(`/api/questions/${q.id}/archive`, { method: "POST" });
      toast("question archived");
      await refreshDetail(true);
      refreshList();
    };

  const unarch = $("#q-unarchive", box);
  if (unarch)
    unarch.onclick = async () => {
      await api(`/api/questions/${q.id}/unarchive`, { method: "POST" });
      toast("question unarchived");
      await refreshDetail(true);
      refreshList();
    };

  const del = $("#q-delete", box);
  if (del)
    del.onclick = async () => {
      if (!confirm(`delete ${q.id} and its runs?`)) return;
      await api(`/api/questions/${q.id}`, { method: "DELETE" });
      S.selected = null;
      S.detail = null;
      renderDetail();
      refreshList();
      syncUrl();
    };

  // review form bindings — keep edits in S.forms so re-renders don't eat them.
  // Resolve the form at EVENT time: redraft swaps the object in S.forms, and a
  // captured reference would silently write into the orphaned one.
  const form = () =>
    S.forms[q.id] || (S.forms[q.id] = { case_id: "", description: "", tags: "", rubric: "", touched: false });
  const bindField = (sel, key) => {
    const el = $(sel, box);
    if (el)
      el.oninput = () => {
        const f = form();
        f[key] = el.value;
        f.touched = true;
      };
  };
  bindField("#rv-case-id", "case_id");
  bindField("#rv-desc", "description");
  bindField("#rv-tags", "tags");
  bindField("#rv-rubric", "rubric");
  bindField("#rv-cases-file", "cases_file");
  const smoke = $("#rv-smoke", box);
  if (smoke)
    smoke.onchange = () => {
      const fm = form();
      fm.smoke = smoke.checked;
      fm.touched = true;
    };

  const autoDraft = $("#rv-auto-draft", box);
  if (autoDraft)
    autoDraft.onclick = async () => {
      autoDraft.disabled = true;
      autoDraft.textContent = "✨ drafting…";
      try {
        const d = await api(`/api/questions/${q.id}/auto-draft`, { method: "POST" });
        S.forms[q.id] = {
          case_id: d.case_id,
          description: d.description || "",
          tags: (d.tags || []).join(", "),
          rubric: d.rubric || "",
          cases_file: d.cases_file || S.forms[q.id]?.cases_file || "",
          touched: true,
        };
        toast("case drafted — review before minting");
        renderDetail(true);
      } catch (e) {
        toast(`auto-draft failed: ${e.message}`, true);
        renderDetail(true);
      }
    };

  const redraft = $("#rv-redraft", box);
  if (redraft)
    redraft.onclick = async () => {
      delete S.forms[q.id];
      await initForm(q);
      renderDetail(true);
    };

  // review-mode tabs
  box.querySelectorAll(".mode-tabs button").forEach((b) => {
    b.onclick = () => {
      S.reviewMode[q.id] = b.dataset.mode;
      renderDetail(true);
      syncUrl();
    };
  });

  // fix-dispatch form
  const ff = fixForm(q.id);
  const bindFix = (sel, key, isNum = false) => {
    const el = $(sel, box);
    if (el)
      el.oninput = () => {
        ff[key] = isNum ? parseInt(el.value, 10) || 3 : el.value;
        ff.touched = true;
      };
  };
  bindFix("#fx-problem", "problem");
  bindFix("#fx-hints", "hints");
  bindFix("#fx-expected", "expected");
  bindFix("#fx-runs", "runs", true);
  bindFix("#fx-model", "model");
  bindFix("#fx-fork-hints", "amendHints");

  const dispatch = $("#fx-dispatch", box);
  if (dispatch)
    dispatch.onclick = async () => {
      if (!ff.problem.trim()) return toast("describe the problem first", true);
      S.dispatching = true;
      renderDetail();
      try {
        const out = await api(`/api/questions/${q.id}/fixes`, {
          method: "POST",
          body: JSON.stringify({
            problem: ff.problem,
            hints: ff.hints,
            expected: ff.expected,
            verify_runs: ff.runs,
            verify_model: ff.model || undefined,
          }),
        });
        toast(`fix ${out.id} dispatched — codex is on it`);
        await refreshDetail(true);
      } catch (e) {
        toast(`dispatch failed: ${e.message}`, true);
      } finally {
        S.dispatching = false;
        renderDetail();
      }
    };

  const latestFix = (q.fixes || []).filter((x) => x.status !== "discarded").slice(-1)[0];
  const selRound = latestFix ? (S.selRound[latestFix.id] ?? latestFix.current_round) : null;

  // round tree: click a node to inspect that round
  box.querySelectorAll(".round-pill").forEach((el) => {
    el.onclick = () => {
      S.selRound[latestFix.id] = parseInt(el.dataset.round, 10);
      renderDetail(true);
      syncUrl();
    };
  });

  // verification timeline: collapsed cards, one per verify run
  box.querySelectorAll("[data-vrun]").forEach((el) => {
    el.onclick = (ev) => {
      if (ev.target.closest(".tc")) return;
      const key = el.dataset.vrun;
      S.vrunOpen.has(key) ? S.vrunOpen.delete(key) : S.vrunOpen.add(key);
      renderDetail(true);
    };
  });

  // retry dead runs (interrupted / error / cancelled)
  box.querySelectorAll("[data-retry]").forEach((el) => {
    el.onclick = async (ev) => {
      ev.stopPropagation();
      const idx = parseInt(el.dataset.retry, 10);
      try {
        await api(`/api/questions/${q.id}/runs/${idx}/retry`, { method: "POST" });
        toast(`run #${idx + 1} re-dispatched`);
        await refreshDetail(true);
        refreshList();
      } catch (e) {
        toast(`retry failed: ${e.message}`, true);
      }
    };
  });
  const retryAll = $("#q-retry-failed", box);
  if (retryAll)
    retryAll.onclick = async () => {
      try {
        const out = await api(`/api/questions/${q.id}/retry-failed`, { method: "POST" });
        toast(`re-dispatched ${out.retried.length} runs`);
        await refreshDetail(true);
        refreshList();
      } catch (e) {
        toast(`retry failed: ${e.message}`, true);
      }
    };

  // cancel a running agent (flags it as a failure)
  box.querySelectorAll(".cancel-btn").forEach((el) => {
    el.onclick = async (ev) => {
      ev.stopPropagation();
      const idx = parseInt(el.dataset.cancel, 10);
      try {
        await api(`/api/questions/${q.id}/runs/${idx}/cancel`, { method: "POST" });
        toast(`run #${idx + 1} cancelled and flagged ✗`);
        await refreshDetail(true);
      } catch (e) {
        toast(`cancel failed: ${e.message}`, true);
      }
    };
  });

  const prBtn = $("#fx-pr", box);
  if (prBtn && latestFix)
    prBtn.onclick = async () => {
      if (!confirm(`open a real PR on ethpandaops/panda from ${latestFix.branch} @ round ${selRound}?`)) return;
      prBtn.disabled = true;
      try {
        const out = await api(`/api/fixes/${latestFix.id}/pr`, {
          method: "POST",
          body: JSON.stringify({ round: selRound }),
        });
        toast(`PR opened: ${out.pr_url}`);
        await refreshDetail(true);
      } catch (e) {
        toast(`PR failed: ${e.message}`, true);
        prBtn.disabled = false;
      }
    };

  const resumeBtn = $("#fx-resume", box);
  if (resumeBtn && latestFix)
    resumeBtn.onclick = async () => {
      resumeBtn.disabled = true;
      try {
        await api(`/api/fixes/${latestFix.id}/resume`, { method: "POST" });
        toast("resuming pipeline from preserved worktree");
        await refreshDetail(true);
      } catch (e) {
        toast(`resume failed: ${e.message}`, true);
        resumeBtn.disabled = false;
      }
    };

  const forkBtn = $("#fx-fork-btn", box);
  if (forkBtn && latestFix)
    forkBtn.onclick = async () => {
      const hints = ($("#fx-fork-hints", box)?.value || "").trim();
      if (!hints) return toast("write some hints for codex first", true);
      try {
        await api(`/api/fixes/${latestFix.id}/fork`, {
          method: "POST",
          body: JSON.stringify({ round: selRound, hints }),
        });
        ff.amendHints = "";
        toast(`forking from round ${selRound}`);
        await refreshDetail(true);
      } catch (e) {
        toast(`fork failed: ${e.message}`, true);
      }
    };

  const discardBtn = $("#fx-discard", box);
  if (discardBtn && latestFix)
    discardBtn.onclick = async () => {
      if (!confirm(`discard ${latestFix.id} — removes the worktree and branch?`)) return;
      try {
        await api(`/api/fixes/${latestFix.id}/discard`, { method: "POST" });
        toast("fix discarded");
        await refreshDetail(true);
      } catch (e) {
        toast(`discard failed: ${e.message}`, true);
      }
    };

  const doExport = async (openPr) => {
    if (openPr && !confirm("mint this case and open a real PR on ethpandaops/panda?")) return;
    S.exporting = true;
    renderDetail();
    try {
      const f = form();
      const tags = f.tags.split(",").map((t) => t.trim()).filter(Boolean);
      if (f.smoke && !tags.includes("smoke")) tags.push("smoke");
      const out = await api(`/api/questions/${q.id}/export`, {
        method: "POST",
        body: JSON.stringify({
          case_id: f.case_id,
          description: f.description,
          tags,
          rubric: f.rubric,
          open_pr: openPr,
          cases_file: f.cases_file || $("#rv-cases-file", box)?.value || "",
        }),
      });
      S.forms[q.id] = { ...f, case_id: out.case_id, touched: true };
      toast(openPr ? `case PR opened: ${out.pr_url}` : `exported → ${out.yaml_path}`);
      await refreshDetail(true);
      refreshList();
    } catch (e) {
      toast(`export failed: ${e.message}`, true);
    } finally {
      S.exporting = false;
      renderDetail();
    }
  };
  const exportBtn = $("#rv-export", box);
  if (exportBtn) exportBtn.onclick = () => doExport(false);
  const exportPrBtn = $("#rv-export-pr", box);
  if (exportPrBtn) exportPrBtn.onclick = () => doExport(true);
}

/* ------------------------------------------------ polling */

async function refreshList() {
  try {
    S.questions = await api("/api/questions");
  } catch {
    return;
  }
  renderStats();
  renderQueue();
}

async function pollFixLog(fid) {
  try {
    const out = await api(`/api/fixes/${fid}/log?offset=${S.fixLogOffset[fid] || 0}`);
    if (out.text) {
      S.fixLog[fid] = (S.fixLog[fid] || "") + out.text;
      const el = $("#fix-log");
      if (el && el.closest(".fix-status")?.dataset.fid === fid) {
        const stick = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        el.textContent = S.fixLog[fid];
        if (stick) el.scrollTop = el.scrollHeight;
      }
    }
    S.fixLogOffset[fid] = out.offset;
  } catch {}
}

function activeFixOf(q) {
  return (q?.fixes || []).find((f) => FIX_ACTIVE.includes(f.status));
}

async function tick() {
  await refreshList();
  if (!S.selected) return;
  const fix = activeFixOf(S.detail);
  if (S.detail?.status === "running" || fix) await refreshDetail(true);
  const shown = $("#fix-log")?.closest(".fix-status")?.dataset.fid;
  if (shown) await pollFixLog(shown);
}

/* ------------------------------------------------ boot */

(async function boot() {
  S.meta = await api("/api/meta");
  const prefs = loadPrefs();
  for (const m of prefs?.models?.length ? prefs.models : S.meta.default_models) S.models.add(m);
  $("#q-runs").value = prefs?.runs || S.meta.default_runs;
  if (prefs?.route) $("#q-route").value = prefs.route;
  S.allModels = S.meta.models;
  renderModels();
  ["#q-runs", "#q-route"].forEach((sel) => {
    $(sel).addEventListener("change", savePrefs);
  });
  $("#model-search").addEventListener("input", renderModelDD);
  document.addEventListener("click", (e) => {
    const dd = $("#model-dd");
    if (!dd.hidden && !e.target.closest("#model-dd") && !e.target.closest(".model-chip")) dd.hidden = true;
  });
  api("/api/models")
    .then((out) => {
      S.allModels = out.models;
      renderModelDD();
    })
    .catch(() => {});

  $("#q-submit").onclick = submitQuestion;
  $("#q-input").addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") submitQuestion();
  });
  document.querySelectorAll("#tabs button").forEach((b) => {
    b.onclick = () => {
      S.tab = b.dataset.tab;
      document.querySelectorAll("#tabs button").forEach((x) => x.classList.toggle("active", x === b));
      renderQueue();
      syncUrl();
    };
  });

  // restore from URL (refresh-safe deep link), and handle back/forward
  const u = parseUrl();
  applyUrl(u);
  await refreshList();
  if (S.selected) {
    await refreshDetail(true);
    applyUrlFixState(u);
    renderDetail(true);
  }
  window.addEventListener("popstate", async () => {
    const v = parseUrl();
    applyUrl(v);
    renderQueue();
    if (S.selected) {
      await refreshDetail(true);
      applyUrlFixState(v);
    }
    renderDetail(true);
  });

  // pre-init review forms for anything already awaiting review
  for (const q of S.questions) if (q.status === "needs_review") initForm(q);
  setInterval(tick, 1800);
})();
