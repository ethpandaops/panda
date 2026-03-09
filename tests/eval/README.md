# ethpandaops-mcp Evaluation Harness

LLM evaluation framework for testing the ethpandaops-mcp server using **Claude Agent SDK (Python)** and **DeepEval**.

## Overview

This evaluation harness tests end-to-end task completion against the ethpandaops-mcp server with live ClickHouse data. It uses:

- **[Claude Agent SDK (Python)](https://platform.claude.com/docs/en/agent-sdk/python)** - Same agent loop that powers Claude Code, native MCP support
- **[DeepEval](https://github.com/confident-ai/deepeval)** - Rich agent-specific metrics, pytest integration
- **[uv](https://docs.astral.sh/uv/)** - Fast Python package management

## Quick Start

### Prerequisites

- Python 3.11+
- [uv](https://docs.astral.sh/uv/getting-started/installation/) installed
- Running ethpandaops-mcp server (with auth disabled for testing)
- `ANTHROPIC_API_KEY` environment variable set
- `OPENAI_API_KEY` environment variable set (for DeepEval LLM-based metrics)

### Installation

```bash
cd tests/eval
uv sync
```

### Running Tests

```bash
# Run all tests with default model (Sonnet 4.5)
uv run python -m scripts.run_eval

# Run with specific model
uv run python -m scripts.run_eval --model claude-opus-4-5
uv run python -m scripts.run_eval --model claude-haiku-4-5

# Run specific test category
uv run python -m scripts.run_eval --category basic_queries -v

# Run only visualization tests
uv run python -m scripts.run_eval --markers visualization

# Skip slow multi-step tests
uv run python -m scripts.run_eval -m "not slow"

# List all available test cases
uv run python -m scripts.run_eval --list

# Or use the installed script directly
uv run mcp-eval --model claude-sonnet-4-5
```

### Interactive REPL

```bash
# Start REPL with default model
uv run python -m scripts.repl

# Use a different model
uv run python -m scripts.repl --model claude-haiku-4-5

# Enable verbose output
uv run python -m scripts.repl --verbose

# Or use the installed script
uv run mcp-repl --verbose
```

REPL Commands:
- `/new` - Start new session
- `/cost` - Show session costs
- `/tools` - Show tool calls
- `/verbose` - Toggle verbose mode
- `/help` - Show help
- `/quit` - Exit

## Test Categories

### Basic Queries (`cases/basic_queries.yaml`)
Single-turn queries testing fundamental data retrieval:
- Block counts
- Client diversity
- Attestation data
- Validator information
- Testnet queries

### Multi-Step Sessions (`cases/multi_step.yaml`)
Multi-turn conversations with session persistence:
- Data analysis workflows
- Trend analysis
- Complex investigations

### Visualizations (`cases/visualizations.yaml`)
Chart and visualization generation:
- Pie charts
- Line charts
- Histograms
- Heatmaps

## Custom Metrics

### DataPlausibilityMetric
Validates Ethereum-specific data constraints:
- Block counts (~7200/day for mainnet)
- Valid client names
- Reasonable numeric ranges

### ResourceDiscoveryMetric
Checks if agent reads schemas/resources before querying:
- Penalizes blind queries without schema discovery
- Rewards use of the `search` tool

### VisualizationURLMetric
Validates visualization output:
- Checks for valid image URLs in output
- Supports S3/R2/GCS URL patterns

## Configuration

### Environment Variables

```bash
# Required
ANTHROPIC_API_KEY=sk-ant-...       # For Claude models
OPENAI_API_KEY=sk-...              # For DeepEval metrics

# ethpandaops-mcp connection
MCP_EVAL_MCP_URL=http://localhost:2480  # Default

# Model selection
MCP_EVAL_MODEL=claude-sonnet-4-5  # Default

# Evaluation options
MCP_EVAL_VERBOSE=false            # Detailed logging
MCP_EVAL_TRACK_COSTS=true         # Cost tracking

# Metric thresholds
MCP_EVAL_TOOL_CORRECTNESS_THRESHOLD=0.5
MCP_EVAL_TASK_COMPLETION_THRESHOLD=0.5
MCP_EVAL_RESOURCE_DISCOVERY_THRESHOLD=0.7

# Optional - Confident AI (for dashboards)
CONFIDENT_API_KEY=...
```

### Settings File

See `config/defaults.yaml` for all available settings.

## Project Structure

```
tests/eval/
├── pyproject.toml              # Python dependencies
├── conftest.py                 # pytest fixtures
├── pytest.ini
├── README.md
├── docker-compose.langfuse.yaml # Langfuse self-hosted setup
│
├── config/
│   ├── settings.py            # Pydantic settings
│   └── defaults.yaml          # Default configuration
│
├── agent/
│   └── wrapper.py             # Claude Agent SDK wrapper + Langfuse
│
├── metrics/
│   ├── data_quality.py        # G-Eval: data plausibility
│   ├── visualization.py       # URL existence check
│   └── resource_discovery.py  # Schema discovery check
│
├── cases/
│   ├── basic_queries.yaml     # Simple ClickHouse queries
│   ├── multi_step.yaml        # Complex workflows
│   └── visualizations.yaml    # Chart generation tests
│
├── tests/
│   ├── test_basic_queries.py
│   ├── test_multi_step.py
│   └── test_visualizations.py
│
├── scripts/
│   ├── run_eval.py            # Main runner with CLI
│   ├── repl.py                # Interactive REPL mode
│   ├── langfuse.py            # Langfuse management CLI
│   └── generate_report.py     # Report generation
│
└── traces/                     # Local trace output (JSON files)
```

## Adding Test Cases

### Basic Query Test

Add to `cases/basic_queries.yaml`:

```yaml
- id: my_new_test
  description: Description of what this tests
  input: "Natural language query for the agent"
  expected_tools:
    - mcp__ethpandaops__execute_python
  metrics:
    tool_correctness: 0.8
    task_completion: 0.7
  network: mainnet
  tags:
    - basic
    - custom
```

### Multi-Step Test

Add to `cases/multi_step.yaml`:

```yaml
- id: my_workflow
  description: Multi-step workflow test
  network: mainnet
  tags:
    - multi_step
    - custom
  steps:
    - prompt: "First step of the workflow"
      expected_tools:
        - mcp__ethpandaops__execute_python
      expect_session_id: true

    - prompt: "Second step using previous data"
      use_previous_session: true
      metrics:
        task_completion: 0.8
```

## Model Comparison

| Model | Best For | Cost | Speed |
|-------|----------|------|-------|
| **Claude Sonnet 4.5** | Default - best balance for agents | $$ | Fast |
| **Claude Opus 4.5** | Maximum intelligence, complex reasoning | $$$$ | Moderate |
| **Claude Haiku 4.5** | Quick tests, cost-sensitive runs, REPL | $ | Fastest |

## CI/CD

GitHub Actions workflow runs:
- **Scheduled**: Daily at 6am UTC with Sonnet 4.5
- **On-demand**: Via workflow_dispatch with model selection

See `.github/workflows/eval.yaml` for configuration.

## Langfuse (Trace Visualization)

Langfuse provides a web UI for viewing evaluation traces, costs, and metrics over time.

### Quick Start

```bash
# Start Langfuse (self-hosted via Docker)
uv run python -m scripts.langfuse up

# Wait for startup (~1-2 minutes on first run)

# Enable tracing in your .env
echo "MCP_EVAL_LANGFUSE_ENABLED=true" >> .env

# Run tests - traces automatically sent to Langfuse
uv run python -m scripts.run_eval

# View traces at http://localhost:3000
# Login: admin@mcp.local / adminadmin
```

Pre-configured with default API keys - no manual setup needed!

### Langfuse Features

- **Trace Visualization**: See full execution traces with tool calls
- **Cost Tracking**: Per-run and aggregate cost analysis
- **Metric Scores**: DeepEval metrics recorded as Langfuse scores
- **Session Grouping**: Multi-turn conversations grouped by session
- **Trends Over Time**: Compare runs across different days/models

### Management Commands

```bash
uv run python -m scripts.langfuse up      # Start Langfuse
uv run python -m scripts.langfuse down    # Stop Langfuse
uv run python -m scripts.langfuse logs -f # View logs
uv run python -m scripts.langfuse status  # Check status
uv run python -m scripts.langfuse reset   # Delete all data
```

### Architecture

Langfuse v3 runs with these services:
- **langfuse-web**: Main UI and API (port 3000)
- **langfuse-worker**: Background processing
- **postgres**: Main database
- **clickhouse**: Analytics database
- **redis**: Cache
- **minio**: S3-compatible blob storage

## Troubleshooting

### Server Connection Issues

Ensure ethpandaops-mcp server is running with auth disabled:
```bash
./mcp serve --config config.yaml
# config.yaml should have: auth.enabled: false
```


## DeepEval Integration

### Using Confident AI Dashboard

```bash
# Login to Confident AI
deepeval login

# Run with dashboard integration
python -m scripts.run_eval --deepeval --confident
```

### Direct DeepEval CLI

```bash
uv run deepeval test run tests/test_basic_queries.py
```
