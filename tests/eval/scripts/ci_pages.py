"""Maintain the CI smoke report area of the gh-pages branch.

The layout under ``<pages>/eval/ci/``:

  index.html                 one copy of the report template (fetch mode: no
                             injected payload, so it loads runs as JSON)
  manifest.json              every published run, newest first — the viewer's
                             branch/commit walker reads this
  runs/<branch>/<sha>.json   one payload per measured commit (scripts.ci_report's
                             data.json)

Two subcommands, both run by the eval-smoke workflow against a gh-pages worktree:

  collect — before building a report: copy this branch's previous payloads (the
      branch walk) and the newest master payload (the baseline) out of the pages
      tree for scripts.ci_report to consume.

  publish — after building: drop the new payload in, refresh index.html from the
      current template, update the manifest, and prune old runs so the branch
      never grows without bound.
"""

from __future__ import annotations

import argparse
import json
import shutil
from datetime import datetime, timedelta
from pathlib import Path

from scripts.ci_report import sanitize_branch

_TEMPLATE = Path(__file__).with_name("report_template.html")

KEEP_MASTER = 300  # one entry per merge — years of history
KEEP_BRANCH = 50  # one entry per PR push
MAX_BRANCH_AGE_DAYS = 90  # drop non-master branches idle this long


def _parse_args() -> argparse.Namespace:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    sub = ap.add_subparsers(dest="cmd", required=True)

    col = sub.add_parser("collect", help="pull comparison records out of the pages tree")
    col.add_argument("--pages-dir", required=True, help="checkout of the gh-pages branch")
    col.add_argument("--branch", required=True, help="branch being measured")
    col.add_argument("--sha", required=True, help="commit being measured (excluded from history)")
    col.add_argument("--history-out", required=True, help="dir to copy branch payloads into")
    col.add_argument(
        "--master-baseline-out",
        default="",
        help="file to copy the newest master payload to (skipped when measuring master)",
    )

    pub = sub.add_parser("publish", help="add a payload to the pages tree")
    pub.add_argument("--pages-dir", required=True, help="checkout of the gh-pages branch")
    pub.add_argument("--payload", required=True, help="data.json from scripts.ci_report")
    return ap.parse_args()


def _read_json(path: Path) -> dict | None:
    try:
        data = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        print(f"skipping unreadable {path}: {exc}")
        return None
    return data if isinstance(data, dict) else None


def _manifest(ci_dir: Path) -> list[dict]:
    data = _read_json(ci_dir / "manifest.json") or {}
    return [e for e in data.get("runs", []) if e.get("id") and e.get("created_at")]


def collect(args: argparse.Namespace) -> None:
    ci_dir = Path(args.pages_dir) / "eval" / "ci"
    out = Path(args.history_out)
    out.mkdir(parents=True, exist_ok=True)

    branch_dir = ci_dir / "runs" / sanitize_branch(args.branch)
    copied = 0
    if branch_dir.is_dir():
        for path in sorted(branch_dir.glob("*.json")):
            if path.stem == args.sha:
                continue
            shutil.copy(path, out / path.name)
            copied += 1
    print(f"collected {copied} prior payload(s) for {args.branch}")

    if args.master_baseline_out and args.branch != "master":
        entries = [e for e in _manifest(ci_dir) if e.get("branch") == "master"]
        if entries:
            latest = max(entries, key=lambda e: e["created_at"])
            src = ci_dir / "runs" / f"{latest['id']}.json"
            if src.is_file():
                dst = Path(args.master_baseline_out)
                dst.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy(src, dst)
                print(f"master baseline: {latest['id']}")
                return
        print("no master baseline available yet")


def _entry(record: dict, run_id: str) -> dict:
    pcts = record.get("token_percentiles") or {}
    return {
        "id": run_id,
        "branch": record.get("branch", ""),
        "tag": record.get("tag", ""),
        "sha": record.get("commit", ""),
        "created_at": record.get("created_at", ""),
        "pass_rate": record.get("pass_rate", 0.0),
        "runs": record.get("runs", 0),
        "tokens_p50": pcts.get("p50") or record.get("mean_tokens_correct", 0.0),
        "event": record.get("event", ""),
        "run_url": record.get("run_url", ""),
        "pr": record.get("pr", ""),
    }


def _prune(entries: list[dict], now: datetime) -> tuple[list[dict], list[dict]]:
    """Newest-first entries -> (kept, dropped). Per-branch caps, plus an idle
    cutoff for non-master branches (merged PR branches stop publishing and decay
    out on their own)."""
    by_branch: dict[str, list[dict]] = {}
    for e in entries:
        by_branch.setdefault(e.get("branch", ""), []).append(e)
    kept, dropped = [], []
    cutoff = (now - timedelta(days=MAX_BRANCH_AGE_DAYS)).isoformat()
    for branch, group in by_branch.items():
        group.sort(key=lambda e: e["created_at"], reverse=True)
        durable = branch in ("master", "releases")
        if not durable and group[0]["created_at"] < cutoff:
            dropped += group
            continue
        cap = KEEP_MASTER if durable else KEEP_BRANCH
        kept += group[:cap]
        dropped += group[cap:]
    kept.sort(key=lambda e: e["created_at"], reverse=True)
    return kept, dropped


def publish(args: argparse.Namespace) -> None:
    payload = json.loads(Path(args.payload).read_text())
    record = payload["record"]
    # CI payloads key by branch/sha; release payloads form a "releases"
    # pseudo-branch keyed by tag, so the viewer walks them like commits.
    if record.get("kind") == "ci":
        run_id = f"{sanitize_branch(record['branch'])}/{record['commit']}"
    else:
        record.setdefault("branch", "releases")
        run_id = f"releases/{sanitize_branch(record['tag'])}"

    ci_dir = Path(args.pages_dir) / "eval" / "ci"
    dst = ci_dir / "runs" / f"{run_id}.json"
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy(Path(args.payload), dst)
    # The viewer is one shared page, refreshed on every publish so it always
    # matches the newest template (older payloads stay readable: the page only
    # expects the {record, runs, history} shape).
    shutil.copy(_TEMPLATE, ci_dir / "index.html")

    entries = [e for e in _manifest(ci_dir) if e["id"] != run_id]
    entries.append(_entry(record, run_id))
    now = datetime.fromisoformat(record["created_at"])
    kept, dropped = _prune(sorted(entries, key=lambda e: e["created_at"], reverse=True), now)
    for e in dropped:
        (ci_dir / "runs" / f"{e['id']}.json").unlink(missing_ok=True)
    for branch_dir in (ci_dir / "runs").iterdir():
        if branch_dir.is_dir() and not any(branch_dir.iterdir()):
            branch_dir.rmdir()

    (ci_dir / "manifest.json").write_text(
        json.dumps({"generated_at": record["created_at"], "runs": kept}, indent=1) + "\n"
    )
    print(
        f"published {run_id} ({len(kept)} run(s) in manifest"
        + (f", pruned {len(dropped)}" if dropped else "")
        + ")"
    )


def main() -> None:
    args = _parse_args()
    collect(args) if args.cmd == "collect" else publish(args)


if __name__ == "__main__":
    main()
