"""SQL extraction and agreement scoring for schema probing."""

from __future__ import annotations

import json
import os
import re
from collections import Counter
from dataclasses import dataclass, field
from typing import Any


@dataclass
class AttemptResult:
    """Result of a single probe attempt."""

    tables: list[str] = field(default_factory=list)
    persona: str = "default"
    error: bool = False
    error_message: str | None = None
    cost_usd: float = 0.0
    duration_ms: int = 0


@dataclass
class AgreementResult:
    """Agreement analysis across N attempts."""

    table_agreement: float = 0.0
    all_agreed: bool = False
    tables_used: dict[str, int] = field(default_factory=dict)
    finding: str = ""


@dataclass
class ProbeResult:
    """Full result for a single probe question."""

    id: str
    question: str
    attempts: list[AttemptResult] = field(default_factory=list)
    agreement: AgreementResult = field(default_factory=AgreementResult)


_EXTRACT_TABLES_PROMPT = """\
You are analyzing Python code that was generated to query ClickHouse databases.

Extract the ClickHouse table names that this code queries. Return ONLY a JSON array of table name strings. If no tables are found, return an empty array.

Rules:
- Include only ClickHouse table names (not Python module names, variables, or imports)
- Strip schema prefixes like "default." or "{network}." — return just the table name
- Include tables from FROM, JOIN, and subquery clauses

Python code:
```python
{code}
```

Return ONLY the JSON array, nothing else. Example: ["canonical_beacon_block", "beacon_api_eth_v1_events_attestation"]
"""


def _get_evaluator_client() -> tuple[Any, str]:
    """Get an OpenAI-compatible client for the evaluator model."""
    from openai import AsyncOpenAI

    api_key = os.environ.get("OPENROUTER_API_KEY")
    if not api_key:
        raise ValueError(
            "OPENROUTER_API_KEY required for LLM-based table extraction. "
            "Set it in your environment."
        )

    model = os.environ.get("MCP_EVAL_EVALUATOR_MODEL", "google/gemini-2.5-flash")

    client = AsyncOpenAI(
        api_key=api_key,
        base_url="https://openrouter.ai/api/v1",
    )
    return client, model


async def extract_tables(tool_calls: list[dict[str, Any]]) -> list[str]:
    """Extract ClickHouse table names from execute_python tool calls using an LLM."""
    code_blocks: list[str] = []
    for tc in tool_calls:
        if "execute_python" in str(tc.get("name", "")):
            tool_input = tc.get("input", {})
            if isinstance(tool_input, dict):
                code = tool_input.get("code", "")
                if code:
                    code_blocks.append(code)

    if not code_blocks:
        return []

    all_code = "\n\n# ---\n\n".join(code_blocks)
    prompt = _EXTRACT_TABLES_PROMPT.replace("{code}", all_code)

    client, model = _get_evaluator_client()
    response = await client.chat.completions.create(
        model=model,
        messages=[{"role": "user", "content": prompt}],
        temperature=0,
        extra_headers={
            "HTTP-Referer": "https://github.com/ethpandaops/panda",
            "X-Title": "panda-probe",
        },
    )

    text = response.choices[0].message.content or "[]"

    # Parse the JSON array from the response (handle markdown fencing)
    text = text.strip()
    if text.startswith("```"):
        text = re.sub(r"^```\w*\n?", "", text)
        text = re.sub(r"\n?```$", "", text)
        text = text.strip()

    try:
        tables = json.loads(text)
        if isinstance(tables, list):
            return sorted(set(str(t) for t in tables))
    except json.JSONDecodeError:
        pass

    return []


def score_agreement(attempts: list[AttemptResult]) -> AgreementResult:
    """Score N-way agreement across probe attempts."""
    valid = [a for a in attempts if not a.error]

    if not valid:
        return AgreementResult(
            table_agreement=0.0,
            all_agreed=False,
            tables_used={},
            finding="all attempts errored",
        )

    # Count table sets (as frozensets for comparison)
    table_sets = [frozenset(a.tables) for a in valid]
    set_counts = Counter(table_sets)
    most_common_set, most_common_count = set_counts.most_common(1)[0]

    # Flat table frequency
    all_tables: Counter[str] = Counter()
    for a in valid:
        for t in a.tables:
            all_tables[t] += 1

    table_agreement = most_common_count / len(valid)
    all_agreed = len(set_counts) == 1

    if all_agreed:
        tables_str = ", ".join(sorted(most_common_set)) or "(no tables detected)"
        finding = f"all {len(valid)} agreed on {tables_str}"
    else:
        parts = []
        for table_set, count in set_counts.most_common():
            tables_str = ", ".join(sorted(table_set)) or "(no tables)"
            parts.append(f"{count}/{len(valid)} used {tables_str}")
        finding = "; ".join(parts)

    return AgreementResult(
        table_agreement=table_agreement,
        all_agreed=all_agreed,
        tables_used=dict(all_tables),
        finding=finding,
    )
