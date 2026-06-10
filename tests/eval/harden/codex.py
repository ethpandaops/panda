"""Run ``codex exec`` with a FILTERED live log.

codex streams blocks: an ``exec`` block is a tool call — the command AND its full output
(file dumps, test logs: the firehose) — and a ``codex`` block is the assistant's message.
We show one line per tool call carrying only its NAME (``ran: rg``, ``edited <file>``)
plus the assistant's messages — no command content, no command output. The full raw
output is still returned (for the proposer's summary / an error tail); only what's
LOGGED is trimmed.
"""

from __future__ import annotations

import re
import subprocess
import threading
from collections.abc import Callable

from rich.markup import escape

_ANSI = re.compile(r"\x1b\[[0-9;]*m")
_MARKERS = {"exec", "codex", "user", "thinking"}
# A diff/patch body line — suppressed wherever it appears. codex repeats whole patches in
# its messages, which is the real firehose when the proposer edits many files.
_DIFF_BODY = re.compile(r"^(diff --git |index [0-9a-f]|--- |\+\+\+ |@@ |[+\- ]|/)")
# A shell wrapper prefix codex puts around most commands (`bash -lc '<cmd>'`).
_SHELL_WRAP = re.compile(r"""^(?:/\S+/)?(?:ba|z|da)?sh\s+-l?c\s+['"]?""")
# codex's token-usage meta lines ("tokens used" + a bare number) — not chat, not a tool.
_TOKENS_META = re.compile(r"^(tokens used:?|[\d,]+)$")


def _cmd_name(cmd: str) -> str:
    """Just the tool's NAME from a command line: unwrap the `bash -lc '...'` shell
    wrapper, then take the first token (`rg`, `go`, `sed`, ...). No arguments — the log
    shows WHAT ran, never its content."""
    inner = _SHELL_WRAP.sub("", cmd.strip())
    tok = inner.split()[0] if inner.split() else inner
    return tok.strip("'\"`()")


def _summarize(line: str, state: dict) -> str | None:
    """One filtered log line (or None to suppress) for a raw codex output line.

    Shows: one ``ran: <name>`` per tool call (the command's name only, no arguments),
    one ``edited <file>`` per patched file, and the assistant's prose messages.
    Suppresses command content/output, diff/patch bodies (deduped per file), token-usage
    meta, and header/prompt noise.
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
            return f"ran: {_cmd_name(bare)}"  # the tool's name only, no arguments
        return None  # suppress the command's output (the firehose)
    if state.get("mode") == "codex" and bare:
        if _TOKENS_META.match(bare):
            return None  # usage meta, not a chat message
        # codex repeats its final message (e.g. a structured verdict) as the run's
        # output — suppress exact repeats of long lines we already showed.
        if len(bare) > 60:
            seen = state.setdefault("shown_msgs", set())
            if bare in seen:
                return None
            seen.add(bare)
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

    # Drain stdout on a thread so the timeout below applies to the WHOLE run. Reading
    # inline would block until codex closes stdout — a hung codex with an open pipe
    # would make any timeout unreachable.
    def _drain() -> None:
        for line in proc.stdout:
            raw.append(line.rstrip("\n"))
            shown = _summarize(line, state)
            if shown and log:
                log(f"[dim]{prefix}{escape(shown)}[/dim]")

    reader = threading.Thread(target=_drain, daemon=True)
    reader.start()
    try:
        code = proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
        reader.join(timeout=5)
        return (-1, "\n".join(raw))
    reader.join(timeout=10)
    return (code, "\n".join(raw))
