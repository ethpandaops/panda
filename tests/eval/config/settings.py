"""Pydantic settings for ethpandaops-panda evaluation harness."""

from pathlib import Path
from typing import Literal

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

# Default values - single source of truth
DEFAULT_AGENT_MODEL = "claude-opus-4-5"
DEFAULT_EVALUATOR_MODEL = "google/gemini-3-flash-preview"


class EvalSettings(BaseSettings):
    """Configuration for the ethpandaops-panda evaluation harness."""

    model_config = SettingsConfigDict(
        env_prefix="MCP_EVAL_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # Model selection
    model: Literal["claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5"] = Field(
        default=DEFAULT_AGENT_MODEL,
        description="Claude model to use for evaluation",
    )

    # ethpandaops-panda connection (external server, auth disabled)
    mcp_url: str = Field(
        default="http://localhost:2480",
        description="URL of the ethpandaops-panda server",
    )

    # Evaluation settings
    max_turns: int = Field(
        default=15,
        description="Maximum number of conversation turns per test",
    )
    permission_mode: str = Field(
        default="bypassPermissions",
        description="Permission mode for the agent",
    )

    # Metric thresholds
    tool_correctness_threshold: float = Field(
        default=0.5,
        description="Minimum threshold for tool correctness metric",
    )
    task_completion_threshold: float = Field(
        default=0.5,
        description="Minimum threshold for task completion metric",
    )
    resource_discovery_threshold: float = Field(
        default=0.7,
        description="Minimum threshold for resource discovery metric",
    )

    # Cost tracking
    track_costs: bool = Field(
        default=True,
        description="Whether to track and report costs",
    )

    # Logging
    verbose: bool = Field(
        default=False,
        description="Enable verbose output",
    )
    log_tool_calls: bool = Field(
        default=True,
        description="Log tool calls during execution",
    )

    # Local traces
    save_traces: bool = Field(
        default=True,
        description="Save detailed traces to local traces/ directory",
    )
    traces_dir: Path = Field(
        default=Path("traces"),
        description="Directory for saving trace files",
    )

    # DeepEval / Evaluator LLM settings
    evaluator_model: str = Field(
        default=DEFAULT_EVALUATOR_MODEL,
        description="Model to use for LLM-based evaluation metrics. "
        "Supports OpenRouter models, OpenAI models, or Claude models.",
    )

    # Test data
    cases_dir: Path = Field(
        default=Path("cases"),
        description="Directory containing test case YAML files",
    )

    # Agent behavior restriction
    restrict_to_mcp_tools: bool = Field(
        default=True,
        description="Restrict agent to only use MCP tools (disable Bash, Glob, etc.)",
    )

    # Langfuse tracing (self-hosted, pre-configured keys work out of the box)
    langfuse_enabled: bool = Field(
        default=False,
        description="Enable Langfuse tracing for eval runs",
    )
    langfuse_host: str = Field(
        default="http://localhost:3000",
        description="Langfuse server URL (self-hosted)",
    )
    langfuse_public_key: str = Field(
        default="pk-lf-mcp-eval-local",
        description="Langfuse project public key (default works with docker-compose)",
    )
    langfuse_secret_key: str = Field(
        default="sk-lf-mcp-eval-local",
        description="Langfuse project secret key (default works with docker-compose)",
    )


# Pricing per million tokens (as of 2025)
MODEL_PRICING = {
    "claude-sonnet-4-5": {"input": 3.00, "output": 15.00},
    "claude-opus-4-5": {"input": 15.00, "output": 75.00},
    "claude-haiku-4-5": {"input": 0.80, "output": 4.00},
}


def calculate_cost(model: str, input_tokens: int, output_tokens: int) -> float:
    """Calculate cost in USD for a given model and token count."""
    pricing = MODEL_PRICING.get(model, MODEL_PRICING["claude-sonnet-4-5"])
    input_cost = (input_tokens / 1_000_000) * pricing["input"]
    output_cost = (output_tokens / 1_000_000) * pricing["output"]
    return input_cost + output_cost
