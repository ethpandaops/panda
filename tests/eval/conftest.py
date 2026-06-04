"""pytest configuration and fixtures for ethpandaops-panda evaluation."""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Any, Generator

import pytest

from agent import make_agent
from config.settings import EvalSettings

if TYPE_CHECKING:
    from langfuse import Langfuse


@pytest.fixture(scope="session")
def eval_settings() -> EvalSettings:
    """Load evaluation settings from environment."""
    return EvalSettings()


@pytest.fixture
def agent(eval_settings: EvalSettings):
    """Create a fresh agent instance (backend selected by settings.agent_api)."""
    return make_agent(eval_settings)


class CostTracker:
    """Track costs across multiple test runs."""

    def __init__(self) -> None:
        self.costs: list[dict[str, Any]] = []

    def record(
        self,
        test_id: str,
        model: str,
        input_tokens: int,
        output_tokens: int,
        cost_usd: float | None,
        duration_ms: int,
        judge_cost_usd: float | None = 0.0,
        judge_input_tokens: int = 0,
        judge_output_tokens: int = 0,
    ) -> None:
        """Record agent + judge cost for a test run."""
        self.costs.append({
            "test_id": test_id,
            "model": model,
            "agent_cost_usd": cost_usd or 0.0,
            "agent_input_tokens": input_tokens,
            "agent_output_tokens": output_tokens,
            "judge_cost_usd": judge_cost_usd or 0.0,
            "judge_input_tokens": judge_input_tokens,
            "judge_output_tokens": judge_output_tokens,
            "duration_ms": duration_ms,
        })

    def total_agent_cost(self) -> float:
        """Total cost of the model under test."""
        return sum(c["agent_cost_usd"] for c in self.costs)

    def total_judge_cost(self) -> float:
        """Total cost of the LLM judge (evaluator metrics)."""
        return sum(c["judge_cost_usd"] for c in self.costs)

    def total_cost(self) -> float:
        """Get total cost (agent + judge) across all recorded tests."""
        return self.total_agent_cost() + self.total_judge_cost()

    def summary(self) -> str:
        """Generate a summary report."""
        if not self.costs:
            return "No costs recorded"

        agent_cost = self.total_agent_cost()
        judge_cost = self.total_judge_cost()
        total_cost = agent_cost + judge_cost
        agent_in = sum(c["agent_input_tokens"] for c in self.costs)
        agent_out = sum(c["agent_output_tokens"] for c in self.costs)
        judge_in = sum(c["judge_input_tokens"] for c in self.costs)
        judge_out = sum(c["judge_output_tokens"] for c in self.costs)
        total_duration = sum(c["duration_ms"] for c in self.costs)
        n = len(self.costs)

        return (
            f"Cost Summary:\n"
            f"  Tests: {n}\n"
            f"  Agent Cost: ${agent_cost:.6f} ({agent_in:,} in / {agent_out:,} out)\n"
            f"  Judge Cost: ${judge_cost:.6f} ({judge_in:,} in / {judge_out:,} out)\n"
            f"  Total Cost: ${total_cost:.6f}\n"
            f"  Total Duration: {total_duration:,}ms\n"
            f"  Avg Cost/Test: ${total_cost / n:.6f}"
        )


class TraceRecorder:
    """Record and save detailed traces for each test."""

    def __init__(self, settings: EvalSettings) -> None:
        self.settings = settings
        self.traces: list[dict[str, Any]] = []
        self.run_id = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H-%M-%S")

    def record(
        self,
        test_id: str,
        input_prompt: str,
        output: str,
        tool_calls: list[dict[str, Any]],
        metrics: list[dict[str, Any]],
        cost_usd: float | None,
        duration_ms: int,
        input_tokens: int,
        output_tokens: int,
        is_error: bool,
        error_message: str | None = None,
        langfuse: Langfuse | None = None,
        trace_id: str | None = None,
    ) -> None:
        """Record a test trace.

        Args:
            test_id: Unique identifier for the test
            input_prompt: The input prompt sent to the agent
            output: The agent's output response
            tool_calls: List of tool call records
            metrics: List of metric results
            cost_usd: Cost in USD (if available)
            duration_ms: Duration in milliseconds
            input_tokens: Number of input tokens
            output_tokens: Number of output tokens
            is_error: Whether the test resulted in an error
            error_message: Error message if is_error is True
            langfuse: Optional Langfuse client for score recording
            trace_id: Optional Langfuse trace ID for score recording
        """
        self.traces.append({
            "test_id": test_id,
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "input": input_prompt,
            "output": output,
            "tool_calls": tool_calls,
            "metrics": metrics,
            "cost_usd": cost_usd or 0.0,
            "duration_ms": duration_ms,
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
            "is_error": is_error,
            "error_message": error_message,
        })

        # Record scores to Langfuse if enabled (using SDK v3 create_score method)
        if langfuse and trace_id:
            for metric in metrics:
                langfuse.create_score(
                    trace_id=trace_id,
                    name=metric["name"],
                    value=metric["score"],
                    comment=f"passed={metric['passed']}",
                )

    def save(self) -> Path | None:
        """Save all traces to disk."""
        if not self.settings.save_traces or not self.traces:
            return None

        run_dir = Path(self.settings.traces_dir) / self.run_id
        run_dir.mkdir(parents=True, exist_ok=True)

        # Save individual test traces
        for trace in self.traces:
            test_file = run_dir / f"{trace['test_id']}.json"
            with open(test_file, "w") as f:
                json.dump(trace, f, indent=2, default=str)

        # Save summary
        summary = {
            "run_id": self.run_id,
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "model": self.settings.model,
            "evaluator_model": self.settings.evaluator_model,
            "total_tests": len(self.traces),
            "passed": sum(1 for t in self.traces if not t["is_error"]),
            "failed": sum(1 for t in self.traces if t["is_error"]),
            "total_cost_usd": sum(t["cost_usd"] for t in self.traces),
            "total_duration_ms": sum(t["duration_ms"] for t in self.traces),
            "total_input_tokens": sum(t["input_tokens"] for t in self.traces),
            "total_output_tokens": sum(t["output_tokens"] for t in self.traces),
            "tests": [
                {
                    "test_id": t["test_id"],
                    "is_error": t["is_error"],
                    "cost_usd": t["cost_usd"],
                    "duration_ms": t["duration_ms"],
                    "input_tokens": t["input_tokens"],
                    "output_tokens": t["output_tokens"],
                    "metrics": {
                        m["name"]: {"score": m["score"], "passed": m["passed"]}
                        for m in t["metrics"]
                    },
                }
                for t in self.traces
            ],
        }
        summary_file = run_dir / "summary.json"
        with open(summary_file, "w") as f:
            json.dump(summary, f, indent=2)

        return run_dir


_cost_tracker_instance: CostTracker | None = None
_trace_recorder_instance: TraceRecorder | None = None
_eval_settings_instance: EvalSettings | None = None


@pytest.fixture(scope="session")
def cost_tracker() -> Generator[CostTracker, None, None]:
    """Session-scoped cost tracker that prints summary at end."""
    global _cost_tracker_instance
    tracker = CostTracker()
    _cost_tracker_instance = tracker
    yield tracker


@pytest.fixture(scope="session")
def trace_recorder(eval_settings: EvalSettings) -> Generator[TraceRecorder, None, None]:
    """Session-scoped trace recorder that saves traces at end."""
    global _trace_recorder_instance, _eval_settings_instance
    recorder = TraceRecorder(eval_settings)
    _trace_recorder_instance = recorder
    _eval_settings_instance = eval_settings
    yield recorder


def pytest_runtest_logreport(report: pytest.TestReport) -> None:
    """Emit one clean line per question as it finishes (xdist-safe: this runs on
    the controller and reads the summary the test stashed via record_property)."""
    if report.when != "call":
        return
    summary = None
    for name, value in report.user_properties:
        if name == "smoke":
            try:
                summary = json.loads(value)
            except (TypeError, ValueError):
                summary = None
            break
    if summary is not None:
        status = "PASS" if summary.get("passed") else "FAIL"
        print(
            f"\n[smoke] {summary.get('id', ''):<22} {status}  "
            f"{summary.get('duration_s', 0):>5.1f}s  "
            f"${summary.get('cost_usd', 0):.4f}  "
            f"tools={summary.get('tools', 0):<2} "
            f"score={summary.get('score', 0):.2f}  "
            f"| {summary.get('answer', '')}",
            flush=True,
        )
    elif report.failed:
        # Errored before producing a summary (e.g. opencode serve crash).
        test_id = report.nodeid.split("[")[-1].rstrip("]")
        last = (report.longreprtext or "").strip().splitlines()
        reason = last[-1][:90] if last else "error"
        print(f"\n[smoke] {test_id:<22} FAIL  {report.duration:>5.1f}s  (error: {reason})", flush=True)


def pytest_terminal_summary(
    terminalreporter: Any,
    exitstatus: int,
    config: pytest.Config,
) -> None:
    """Print cost summary and save traces at end of test session."""
    # Print cost summary
    if _cost_tracker_instance and _cost_tracker_instance.costs:
        terminalreporter.write_sep("=", "Agent Cost Summary")
        terminalreporter.write_line(_cost_tracker_instance.summary())
        terminalreporter.write_sep("=", "")

    # Save traces
    if _trace_recorder_instance:
        trace_dir = _trace_recorder_instance.save()
        if trace_dir:
            terminalreporter.write_sep("=", "Traces Saved")
            terminalreporter.write_line(f"  Location: {trace_dir}")
            terminalreporter.write_line(f"  Tests: {len(_trace_recorder_instance.traces)}")
            terminalreporter.write_sep("=", "")


def pytest_configure(config: pytest.Config) -> None:
    """Configure custom markers."""
    config.addinivalue_line(
        "markers", "slow: marks tests as slow (deselect with '-m \"not slow\"')"
    )
    config.addinivalue_line(
        "markers", "visualization: marks visualization tests"
    )
    config.addinivalue_line(
        "markers", "multi_step: marks multi-step session tests"
    )
    config.addinivalue_line(
        "markers", "smoke: marks fast CI smoke tests (select with '-m smoke')"
    )


def pytest_collection_modifyitems(
    config: pytest.Config, items: list[pytest.Item]
) -> None:
    """Modify test collection to add markers based on test names."""
    for item in items:
        # Add markers based on test file names
        if "visualization" in item.nodeid:
            item.add_marker(pytest.mark.visualization)
        if "multi_step" in item.nodeid:
            item.add_marker(pytest.mark.multi_step)
            item.add_marker(pytest.mark.slow)
