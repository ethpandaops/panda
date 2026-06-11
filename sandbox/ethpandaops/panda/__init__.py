"""Compatibility alias for the ethpandaops sandbox library.

The CLI is named ``panda``, but the Python package is ``ethpandaops``. This
module keeps common imports such as ``from panda import clickhouse`` working
without changing the canonical API surface.
"""

import importlib

from ethpandaops import storage

__version__ = "0.1.0"
__all__ = ["storage"]


def __getattr__(name):
    mod = importlib.import_module(f"ethpandaops.{name}")
    globals()[name] = mod
    return mod
