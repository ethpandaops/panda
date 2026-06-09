"""One-time hydration: paraphrase each case into `variations` with the proposer model (Codex).

Each variation is the SAME question asked differently (same intent, same answer). They run as
extra cases under the same id + asserts, so the harness is graded on intent across many
wordings — and the harden proposer can't score by overfitting to one phrasing. Variations are
written back into the cases file (comments preserved): static, traceable, reviewable. Commit
the diff.

    uv run python -m scripts.hydrate --cases coverage.yaml --n 5
    uv run python -m scripts.hydrate --cases smoke.yaml --question-id mainnet_block_arrival_p50 --dry-run

Uses Codex (gpt-5.5 @ xhigh by default) via your `codex` CLI — the same model the harden
proposer/auditor use, no OpenRouter/API. The model is answer-BLIND: it only ever sees the
question text (never the answer or rubric) and runs read-only in a throwaway empty repo, so a
variation can neither be shaped by nor leak the answer.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import tempfile
from pathlib import Path

from rich.markup import escape
from ruamel.yaml import YAML

from harden.logsetup import setup_logging

_SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "properties": {"variations": {"type": "array", "items": {"type": "string"}}},
    "required": ["variations"],
}

_PROMPT = """You are expanding ONE evaluation question into paraphrases for robustness testing.
Write {n} alternate phrasings of the question below that preserve the EXACT same intent and the
SAME answer. Vary register and structure — terse, verbose, casual, formal, "I'm trying to ...",
question vs imperative — but NEVER change what is asked: keep the same entity, metric, network,
and every number / time window. Do NOT answer the question; only rephrase it.

QUESTION:
{q}"""


def _codex(prompt: str, *, model: str, reasoning: str, timeout: float) -> dict:
    """Run a structured ``codex exec`` and return its JSON. Read-only in a throwaway empty
    git repo so the model has no access to the repo (it can't peek at cases/answers)."""
    with tempfile.TemporaryDirectory() as td:
        subprocess.run(["git", "init", "-q", td], check=False, capture_output=True)
        schema = Path(td) / "schema.json"
        out = Path(td) / "out.json"
        schema.write_text(json.dumps(_SCHEMA))
        cmd = [
            "codex", "exec",
            "-m", model,
            "-c", f"model_reasoning_effort={reasoning}",
            "-C", td,
            "--sandbox", "read-only",
            "--output-schema", str(schema),
            "-o", str(out),
            "-",
        ]
        proc = subprocess.run(cmd, input=prompt, text=True, capture_output=True, timeout=timeout)
        if proc.returncode != 0:
            raise RuntimeError(f"codex exited {proc.returncode}: {(proc.stderr or '')[-300:]}")
        raw = out.read_text() if out.exists() else (proc.stdout or "")
    return json.loads(raw)


def generate(question: str, *, n: int, model: str, reasoning: str, timeout: float) -> list[str]:
    """n distinct paraphrases of `question` (the model sees only the question text)."""
    data = _codex(_PROMPT.format(n=n, q=question), model=model, reasoning=reasoning, timeout=timeout)
    out: list[str] = []
    for s in data.get("variations", []):
        s = str(s).strip()
        if s and s != question and s not in out:
            out.append(s)
    return out[:n]


def main() -> None:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--cases", required=True, help="cases/*.yaml file to hydrate")
    ap.add_argument("--n", type=int, default=5, help="paraphrases per question")
    ap.add_argument("--model", default="gpt-5.5", help="codex model (the proposer model)")
    ap.add_argument("--reasoning-effort", default="xhigh")
    ap.add_argument("--timeout", type=float, default=600.0)
    ap.add_argument("--question-id", action="append", default=[], help="restrict to id(s)")
    ap.add_argument(
        "--overwrite", action="store_true", help="replace existing variations (default: fill empty)"
    )
    ap.add_argument("--dry-run", action="store_true", help="print proposed variations, don't write")
    args = ap.parse_args()
    log = setup_logging().info

    path = Path(__file__).resolve().parents[1] / "cases" / args.cases
    yaml = YAML()  # round-trip: preserves comments + formatting on dump
    yaml.preserve_quotes = True
    yaml.indent(mapping=2, sequence=4, offset=2)
    data = yaml.load(path.read_text())

    wanted = set(args.question_id)
    changed = 0
    for item in data:
        qid = item.get("id", "")
        if wanted and qid not in wanted:
            continue
        if item.get("variations") and not args.overwrite:
            log(f"{qid}: already has variations — skipping (use --overwrite)")
            continue
        steps = item.get("steps")
        question = item.get("input") or (steps[0].get("prompt", "") if steps else "")
        if not question:
            log(f"{qid}: no input/steps — skipping")
            continue
        log(f"[cyan]{qid}[/cyan] generating {args.n} variations via codex ({args.model})...")
        variations = generate(
            str(question),
            n=args.n,
            model=args.model,
            reasoning=args.reasoning_effort,
            timeout=args.timeout,
        )
        log(f"[green]{qid}[/green] +{len(variations)} variation(s)")
        for v in variations:
            log(f"[dim]    - {escape(v)}[/dim]")
        if variations and not args.dry_run:
            item["variations"] = variations
            changed += 1

    if args.dry_run:
        log("[yellow](dry run — nothing written)[/yellow]")
        return
    if not changed:
        log("nothing to write")
        return
    with path.open("w") as f:
        yaml.dump(data, f)
    log(f"[green]wrote variations for {changed} question(s)[/green] to {path} — review the git diff")


if __name__ == "__main__":
    main()
