"""The codex log filter shows tool names and assistant prose ONLY — patch bodies
must never reach the terminal, including patches whose `diff --git` header was
never seen and patches containing metadata lines (new file mode, renames)."""

from __future__ import annotations

from harden.codex import _summarize


def _feed(lines: list[str]) -> list[str]:
    state: dict = {"mode": None, "await_cmd": False}
    out = []
    for line in lines:
        shown = _summarize(line, state)
        if shown is not None:
            out.append(shown)
    return out


def test_new_file_patch_fully_suppressed():
    # The exact leak shape observed live: metadata line ended diff mode mid-patch
    # and the whole body poured into the log.
    out = _feed(
        [
            "codex",
            "diff --git a/pkg/cli/datasources_test.go b/pkg/cli/datasources_test.go",
            "new file mode 100644",
            "--- /dev/null",
            "+++ b/pkg/cli/datasources_test.go",
            "@@ -0,0 +1,94 @@",
            "+package cli",
            "+",
            "+import (",
            '+  "encoding/json"',
            "+)",
        ]
    )
    assert out == ["edited pkg/cli/datasources_test.go"]


def test_headerless_patch_chunk_suppressed():
    # A patch quoted mid-message without its diff --git opener.
    out = _feed(
        [
            "codex",
            "I added a test file:",
            "+++ b/pkg/cli/output_test.go",
            "@@ -1,4 +1,9 @@",
            "+func TestX(t *testing.T) {}",
            "Done — the suite passes.",
        ]
    )
    assert out == [
        "I added a test file:",
        "edited pkg/cli/output_test.go",
        "Done — the suite passes.",
    ]


def test_prose_and_tools_still_shown():
    out = _feed(
        [
            "exec",
            "bash -lc 'go test ./pkg/cli'",
            "ok  github.com/ethpandaops/panda/pkg/cli 1.2s",
            "codex",
            "The full suite passes when the environment is cleared.",
        ]
    )
    assert out == ["ran: go", "The full suite passes when the environment is cleared."]


def test_assistant_prose_extracts_messages_not_diff_tails():
    # A transcript whose TAIL is a printed patch: the old raw-tail summary captured the
    # diff; assistant_prose must return only the chat messages.
    raw = "\n".join(
        [
            "codex",
            "I will adjust the error hints to be class-specific.",
            "exec",
            "bash -lc go test ./...",
            "ok   github.com/x  0.5s",
            "codex",
            "Done: hints are now class-specific and the tests pass.",
            "apply patch",
            "*** Update File: pkg/cli/x.go",
            "+new line of code",
            "-old line of code",
        ]
    )
    from harden.codex import assistant_prose

    prose = assistant_prose(raw)
    assert "class-specific" in prose
    assert "new line of code" not in prose
    assert "go test" not in prose
    assert "edited" not in prose
