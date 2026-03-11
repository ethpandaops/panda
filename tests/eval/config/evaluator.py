"""Evaluator model configuration for LLM-judged metrics."""

from __future__ import annotations

import os
from functools import lru_cache
from typing import Any

from deepeval.models import DeepEvalBaseLLM

from config.settings import DEFAULT_EVALUATOR_MODEL


class OpenRouterModel(DeepEvalBaseLLM):
    """OpenRouter model adapter for DeepEval.

    This class wraps OpenRouter's API to work with DeepEval's LLM interface.
    OpenRouter provides access to many models through a unified OpenAI-compatible API.
    """

    def __init__(
        self,
        model: str | None = None,
        api_key: str | None = None,
        base_url: str = "https://openrouter.ai/api/v1",
    ) -> None:
        """Initialize the OpenRouter model.

        Args:
            model: OpenRouter model identifier. Defaults to DEFAULT_EVALUATOR_MODEL.
            api_key: OpenRouter API key. Defaults to OPENROUTER_API_KEY env var.
            base_url: OpenRouter API base URL.
        """
        model_name = model or DEFAULT_EVALUATOR_MODEL
        self._model_name = model_name
        self.api_key = api_key or os.environ.get("OPENROUTER_API_KEY")
        self.base_url = base_url

        if not self.api_key:
            raise ValueError(
                "OpenRouter API key required. Set OPENROUTER_API_KEY environment variable "
                "or pass api_key parameter."
            )

        # Initialize OpenAI client with OpenRouter base URL
        try:
            from openai import AsyncOpenAI, OpenAI
        except ImportError:
            raise ImportError(
                "OpenAI package required for OpenRouter. Install with: pip install openai"
            )

        self._client = OpenAI(
            api_key=self.api_key,
            base_url=self.base_url,
        )
        self._async_client = AsyncOpenAI(
            api_key=self.api_key,
            base_url=self.base_url,
        )

    def load_model(self) -> str:
        """Load the model - required by DeepEvalBaseLLM."""
        return self._model_name

    def generate(self, prompt: str, schema: Any = None) -> str:
        """Generate a response from the model.

        Args:
            prompt: The prompt to send to the model.
            schema: Optional schema for structured output (not used).

        Returns:
            The model's response text.
        """
        response = self._client.chat.completions.create(
            model=self._model_name,
            messages=[{"role": "user", "content": prompt}],
            extra_headers={
                "HTTP-Referer": "https://github.com/ethpandaops/panda",
                "X-Title": "panda-eval",
            },
        )
        return response.choices[0].message.content or ""

    async def a_generate(self, prompt: str, schema: Any = None) -> str:
        """Async generate a response from the model.

        Args:
            prompt: The prompt to send to the model.
            schema: Optional schema for structured output (not used).

        Returns:
            The model's response text.
        """
        response = await self._async_client.chat.completions.create(
            model=self._model_name,
            messages=[{"role": "user", "content": prompt}],
            extra_headers={
                "HTTP-Referer": "https://github.com/ethpandaops/panda",
                "X-Title": "panda-eval",
            },
        )
        return response.choices[0].message.content or ""

    def get_model_name(self) -> str:
        """Get the model name."""
        return self._model_name


@lru_cache(maxsize=4)
def get_evaluator_model(model_name: str) -> DeepEvalBaseLLM | str:
    """Get the evaluator model instance for DeepEval metrics.

    Args:
        model_name: Model identifier. Supported formats:
            - OpenAI: "gpt-4o", "gpt-4-turbo", "gpt-4o-mini"
            - Anthropic: "claude-sonnet-4-5", "claude-haiku-4-5", "claude-opus-4-5"
            - OpenRouter: Any model with "/" in name (e.g., "google/gemini-3-flash-preview")

    Returns:
        A model instance compatible with DeepEval metrics.
    """
    # OpenRouter models - identified by "/" in the name (provider/model format)
    if "/" in model_name:
        return OpenRouterModel(model=model_name)

    # OpenAI models - just return the string, DeepEval handles it natively
    if model_name.startswith("gpt-"):
        return model_name

    # Anthropic models - need to wrap in DeepEval's Anthropic adapter
    if model_name.startswith("claude-"):
        try:
            from deepeval.models import AnthropicModel

            # Map our model names to Anthropic's model IDs
            model_map = {
                "claude-sonnet-4-5": "claude-sonnet-4-5-20250514",
                "claude-haiku-4-5": "claude-haiku-4-5-20250514",
                "claude-opus-4-5": "claude-opus-4-5-20250514",
            }
            anthropic_model_id = model_map.get(model_name, model_name)
            return AnthropicModel(model=anthropic_model_id)
        except ImportError:
            raise ImportError(
                "To use Claude models as evaluators, ensure deepeval[anthropic] is installed: "
                "uv add 'deepeval[anthropic]'"
            )

    # Unknown model - try passing through as string (OpenAI-compatible)
    return model_name


def get_evaluator_model_name(model_name: str) -> str:
    """Get a display name for the evaluator model.

    Args:
        model_name: Model identifier.

    Returns:
        Human-readable model name.
    """
    display_names = {
        "gpt-4o": "GPT-4o",
        "gpt-4-turbo": "GPT-4 Turbo",
        "gpt-4o-mini": "GPT-4o Mini",
        "claude-sonnet-4-5": "Claude Sonnet 4.5",
        "claude-haiku-4-5": "Claude Haiku 4.5",
        "claude-opus-4-5": "Claude Opus 4.5",
    }
    # For OpenRouter models, show provider/model if not in display_names
    if "/" in model_name and model_name not in display_names:
        return f"{model_name} (OpenRouter)"
    return display_names.get(model_name, model_name)
