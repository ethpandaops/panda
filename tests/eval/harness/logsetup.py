"""Colored, structured logging for the eval scripts (via rich).

Call ``setup_logging()`` once in an entrypoint; everything else just does
``logging.getLogger("eval")`` (or a child like ``eval.build``) and logs normally. Messages
may use rich markup — e.g. ``log.info("[green]ACCEPT[/green] ...")``. We attach the handler to
the ``eval`` logger only (not root), so noisy third-party libraries aren't reformatted.
"""

from __future__ import annotations

import logging

from rich.logging import RichHandler

_NAME = "eval"


def setup_logging(level: int = logging.INFO) -> logging.Logger:
    """Configure (idempotently) and return the ``eval`` logger with a rich handler."""
    logger = logging.getLogger(_NAME)
    if not logger.handlers:
        handler = RichHandler(
            show_path=False,
            markup=True,
            rich_tracebacks=True,
            omit_repeated_times=True,  # don't reprint the timestamp on same-second lines
            log_time_format="[%H:%M:%S]",
        )
        logger.addHandler(handler)
        logger.setLevel(level)
        logger.propagate = False  # don't double-log via root
    return logger


def get_logger(child: str | None = None) -> logging.Logger:
    """The ``eval`` logger, or a child (``eval.<child>``) that inherits its handler."""
    return logging.getLogger(f"{_NAME}.{child}") if child else logging.getLogger(_NAME)
