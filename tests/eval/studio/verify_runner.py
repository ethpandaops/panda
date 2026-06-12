"""Run a question N times against a (scratch) panda server; print JSON results.

Runs as a subprocess of the studio with PANDA_CONFIG/PATH/MCP_EVAL_MCP_URL pointed
at a patched worktree's build — a fresh process so the scratch environment and
opencode server cache never leak into the studio's normal runs.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from pathlib import Path

EVAL_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(EVAL_DIR))

from agent.opencode_agent import OpenCodeAgent  # noqa: E402
from config.settings import EvalSettings  # noqa: E402


async def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--question", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--route", default="cli")
    ap.add_argument("--runs", type=int, default=3)
    ap.add_argument("--timeout", type=float, default=240.0)
    ap.add_argument("--mcp-url", default="http://localhost:2480")
    args = ap.parse_args()

    settings = EvalSettings(
        model=args.model,
        opencode_route=args.route,
        opencode_timeout=args.timeout,
        mcp_url=args.mcp_url,
        langfuse_enabled=False,
    )
    agent = OpenCodeAgent(settings)
    results = []
    try:
        for _ in range(args.runs):
            r = await agent.execute(args.question)
            results.append(
                {
                    "answer": r.output,
                    "error": r.error_message if r.is_error else None,
                    "duration_ms": r.duration_ms,
                    "tokens": {"input": r.input_tokens, "output": r.output_tokens},
                    "tool_calls": [
                        {
                            "name": tc.name,
                            "input": json.dumps(tc.input, default=str)
                            if not isinstance(tc.input, str)
                            else tc.input,
                            "output": str(tc.result or ""),
                            "status": "error" if tc.is_error else "completed",
                            "duration_ms": tc.duration_ms,
                        }
                        for tc in r.tool_calls
                    ],
                }
            )
    finally:
        agent.close()
    print(json.dumps(results))


if __name__ == "__main__":
    asyncio.run(main())
