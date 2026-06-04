"""Pydantic settings for ethpandaops-panda evaluation harness."""

from pathlib import Path

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

# Default values - single source of truth
DEFAULT_AGENT_MODEL = "opencode-go/deepseek-v4-flash"
DEFAULT_EVALUATOR_MODEL = "google/gemini-3-flash-preview"


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
        description="Model under test, as '<provider>/<model>'. With agent_api='opencode', "
        "an opencode provider/model (e.g. opencode-go/deepseek-v4-flash); with "
        "agent_api='openai', an OpenAI-class id (e.g. deepseek/deepseek-v4-flash via "
        "OpenRouter); with agent_api='anthropic', any Claude id.",
    )
    agent_api: str = Field(
        default="opencode",
        description="Agent backend: 'opencode' (drives `opencode serve` via the opencode "
        "SDK against panda's MCP or CLI), 'openai' (OpenAI-compatible chat-completions with "
        "a native MCP tool loop, e.g. OpenRouter), or 'anthropic' (Claude Agent SDK).",
    )
    agent_api_key_env: str = Field(
        default="OPENROUTER_API_KEY",
        description="Name of the env var holding the API key for the OpenAI-class "
        "agent backend. (The opencode backend reads OPENCODE_GO_API_KEY directly.)",
    )
    opencode_route: str = Field(
        default="mcp",
        description="For agent_api='opencode': 'mcp' gives opencode panda's MCP server; "
        "'cli' gives it a shell + the built `panda` binary and steers it through the CLI.",
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
    agent_base_url: str = Field(
        default="",
        description="If set, injected as ANTHROPIC_BASE_URL for the model under test. "
        "Point at an Anthropic-compatible gateway (e.g. LiteLLM in front of OpenRouter) "
        "to evaluate non-Claude models. Empty = talk to Anthropic directly (Claude only).",
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
