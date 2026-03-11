"""Configuration module for ethpandaops-panda evaluation."""

from config.evaluator import get_evaluator_model, get_evaluator_model_name
from config.settings import (
    DEFAULT_AGENT_MODEL,
    DEFAULT_EVALUATOR_MODEL,
    EvalSettings,
)

__all__ = [
    "DEFAULT_AGENT_MODEL",
    "DEFAULT_EVALUATOR_MODEL",
    "EvalSettings",
    "get_evaluator_model",
    "get_evaluator_model_name",
]
