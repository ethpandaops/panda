"""Scripts module for ethpandaops-panda evaluation.

Line-buffer stdout/stderr the moment any entrypoint is imported (``python -m scripts.X`` or
the installed console scripts import this package first), so long-running scripts STREAM
their logs in real time. Python block-buffers a non-TTY by default, which means piping/tee
or watching a log file otherwise shows nothing until the buffer fills or the process exits.
"""

import sys

for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(line_buffering=True)
    except (AttributeError, ValueError):  # not a TextIOWrapper (e.g. already wrapped) — skip
        pass
