"""Pydantic settings for ethpandaops-panda evaluation harness."""

import os
from pathlib import Path

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

# Default values - single source of truth. Everything else references these; don't
# re-hardcode the strings at call sites.
DEFAULT_AGENT_MODEL = "opencode-go/deepseek-v4-flash"
DEFAULT_AGENT_ROUTE = "cli"
# A subject spec is "<provider>/<model>:<route>".
DEFAULT_SUBJECT = f"{DEFAULT_AGENT_MODEL}:{DEFAULT_AGENT_ROUTE}"
# The loop optimizes across TWO agent models by default, so a harness improvement has to
# help BOTH (it can't overfit to one) — and two subjects double the confidence gate's cells.
# Both ride the opencode-go provider, so one API key covers them (CI included).
DEFAULT_SUBJECTS = [DEFAULT_SUBJECT, f"opencode-go/mimo-v2.5:{DEFAULT_AGENT_ROUTE}"]
# Judge quality matters more than judge cost (~$0.003/grade): a flaky judge contaminates
# the harden gates, so the judge must be a reliable rubric-follower AND family-distinct
# from the subjects (a judge scoring its own family is a self-preference risk — that rules
# out deepseek-* and mimo-* here). qwen3.7-plus rides the same opencode-go gateway as the
# subjects, so one API key covers the whole eval; benched clean over 60 smoke grades,
# where minimax-m3 and deepseek-v4-pro both emitted malformed rubric JSON (false
# negatives) through this same path.
DEFAULT_EVALUATOR_MODEL = "qwen3.7-plus"
# The zen gateway is OpenAI-compatible; promptfoo grades through its generic
# openai:chat driver pointed at this base URL.
OPENCODE_ZEN_BASE_URL = "https://opencode.ai/zen/go/v1"


def _opencode_key_envar() -> str:
    """The env var holding the opencode-go key: CI exports OPENCODE_GO_API_KEY, local
    dev typically has OPENCODE_API_KEY. promptfoo reads exactly one name."""
    return "OPENCODE_GO_API_KEY" if os.environ.get("OPENCODE_GO_API_KEY") else "OPENCODE_API_KEY"


def grader_for(model: str) -> dict:
    """A promptfoo grading-provider spec for an opencode-go model."""
    return {
        "id": f"openai:chat:{model}",
        "config": {
            "apiBaseUrl": OPENCODE_ZEN_BASE_URL,
            "apiKeyEnvar": _opencode_key_envar(),
        },
    }


DEFAULT_GRADER = grader_for(DEFAULT_EVALUATOR_MODEL)


class EvalSettings(BaseSettings):
    """Configuration for the ethpandaops-panda evaluation harness."""

    model_config = SettingsConfigDict(
        env_prefix="MCP_EVAL_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # Model under test
    model: str = Field(
        default=DEFAULT_AGENT_MODEL,
        description="Model under test as an opencode '<provider>/<model>' "
        "(e.g. opencode-go/deepseek-v4-flash).",
    )
    opencode_route: str = Field(
        default="mcp",
        description="'mcp' gives opencode panda's MCP server; 'cli' gives it a shell "
        "+ the built `panda` binary and steers it through the CLI.",
    )
    opencode_sandbox: bool = Field(
        default=False,
        description="Run opencode inside a container with no repo mount (only a linux "
        "`panda` binary + config) so the subject's bash can't read the eval cases. The "
        "harness sets OPENCODE_SANDBOX_PANDA_BIN + OPENCODE_SANDBOX_SERVER_URL.",
    )
    opencode_timeout: float = Field(
        default=90.0,
        description="Per-question timeout (seconds) for the opencode SDK client.",
    )
    reasoning_effort: str = Field(
        default="high",
        description="Reasoning/thinking effort for the model under test "
        "(none, low, medium, high). Maps to the agent's thinking-token budget.",
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

    # Grader / evaluator LLM settings (the promptfoo llm-rubric judge default)
    evaluator_model: str = Field(
        default=DEFAULT_EVALUATOR_MODEL,
        description="Default model for grading llm-rubric asserts. "
        "Supports OpenRouter models, OpenAI models, or Claude models.",
    )

    # Test data
    cases_dir: Path = Field(
        default=Path("cases"),
        description="Directory containing test case YAML files",
    )

    # Agent behavior restriction
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
