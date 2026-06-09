"""Run ``codex exec`` with a FILTERED live log.

codex streams blocks: an ``exec`` block is a tool call — the command AND its full output
(file dumps, test logs: the firehose) — and a ``codex`` block is the assistant's message. We
show one concise line per command (``ran: <cmd>``) and the assistant messages, and drop the
command output + the header noise. The full raw output is still returned (for the proposer's
summary / an error tail); only what's LOGGED is trimmed.
"""

from __future__ import annotations

import re
import subprocess
from collections.abc import Callable

_ANSI = re.compile(r"\x1b\[[0-9;]*m")
_MARKERS = {"exec", "codex", "user", "thinking"}
# A diff/patch body line — suppressed wherever it appears. codex repeats whole patches in
# its messages, which is the real firehose when the proposer edits many files.
_DIFF_BODY = re.compile(r"^(diff --git |index [0-9a-f]|--- |\+\+\+ |@@ |[+\- ]|/)")


def _summarize(line: str, state: dict) -> str | None:
    """One filtered log line (or None to suppress) for a raw codex output line.

    Shows: one ``ran: <cmd>`` per command, one ``edited <file>`` per patched file, and the
    assistant's prose messages. Suppresses command output, diff/patch bodies (deduped per
    file), and header/prompt noise.
    """
    clean = _ANSI.sub("", line).rstrip()
    bare = clean.strip()
    if bare in _MARKERS:
        state.update(mode=bare, await_cmd=(bare == "exec"), in_diff=False)
        return None  # the marker itself isn't shown
    # File-edit blocks: announce one "edited <file>" (deduped) and swallow the diff body.
    if bare == "apply patch" or clean.startswith("patch:"):
        state["in_diff"] = True
        return None
    m = re.match(r"diff --git a/(\S+)", clean)
    if m:
        state["in_diff"] = True
        f = m.group(1)
        if f not in state.setdefault("edited", set()):
            state["edited"].add(f)
            return f"edited {f}"
        return None
    if state.get("in_diff"):
        if not bare or _DIFF_BODY.match(clean):
            return None  # still inside the diff
        state["in_diff"] = False  # diff ended — fall through to normal handling
    if state.get("mode") == "exec":
        if state.get("await_cmd") and bare:
            state["await_cmd"] = False
            return f"ran: {' '.join(bare.split())[:140]}"  # the command, one line
        return None  # suppress the command's output (the firehose)
    if state.get("mode") == "codex" and bare:
        return clean  # the assistant message — the summary we want
    return None  # header / prompt echo / pre-first-marker noise


def run_codex(
    cmd: list[str],
    prompt: str,
    *,
    timeout: float,
    log: Callable[[str], None] | None = None,
    prefix: str = "      codex| ",
) -> tuple[int, str]:
    """Run ``codex exec`` (prompt via stdin), streaming a FILTERED summary through ``log``.

    Returns ``(returncode, full_raw_output)``. returncode -1 = timed out, 127 = codex missing.
    """
    try:
        proc = subprocess.Popen(
            cmd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
        )
    except FileNotFoundError:
        return (127, "codex CLI not found on PATH")

    assert proc.stdin and proc.stdout
    proc.stdin.write(prompt)
    proc.stdin.close()
    state: dict = {"mode": None, "await_cmd": False}
    raw: list[str] = []
    try:
        for line in proc.stdout:
            raw.append(line.rstrip("\n"))
            shown = _summarize(line, state)
            if shown and log:
                log(f"{prefix}{shown}")
        code = proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        return (-1, "\n".join(raw))
    return (code, "\n".join(raw))
