"""SQL extraction and agreement scoring for schema probing."""

from __future__ import annotations

import json
import math
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
    num_turns: int = 0
    num_tool_calls: int = 0


@dataclass
class AgreementResult:
    """Agreement analysis across N attempts."""

    table_agreement: float = 0.0
    all_agreed: bool = False
    tables_used: dict[str, int] = field(default_factory=dict)
    finding: str = ""
    entropy: float = 0.0


@dataclass
class ProbeResult:
    """Full result for a single probe question."""

    id: str
    question: str
    attempts: list[AttemptResult] = field(default_factory=list)
    agreement: AgreementResult = field(default_factory=AgreementResult)


# System/metadata tables to exclude from agreement scoring.
# These are used for schema discovery, not for answering the question.
_IGNORED_TABLES = frozenset({
    "system.tables",
    "system.columns",
    "tables",
    "columns",
    "information_schema.tables",
    "information_schema.columns",
})

_EXTRACT_TABLES_PROMPT = """\
The Python code below contains SQL queries sent to ClickHouse. Extract the LITERAL table names that appear in FROM and JOIN clauses in the SQL strings.

Rules:
- ONLY return table names that literally appear as text in the SQL strings after FROM or JOIN keywords
- Strip schema/database prefixes (e.g. "default.foo" → "foo", "mainnet.bar" → "bar", "{network}.baz" → "baz")
- Do NOT include CTE aliases (names defined in WITH clauses, e.g. "WITH latest AS (...)" — "latest" is NOT a table)
- Do NOT include subquery aliases or temporary names defined in the SQL itself
- Do NOT infer or guess table names from column names, variable names, or context
- Do NOT include Python module names, function names, or imports
- If the code has no SQL queries or no FROM/JOIN clauses, return an empty array

Python code:
```python
{code}
```

Return ONLY a JSON array of the literal table names found. Example: ["canonical_beacon_block", "fct_block_first_seen_by_node"]
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
            return sorted(set(str(t) for t in tables) - _IGNORED_TABLES)
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

    # Shannon entropy over the distribution of table-set choices.
    # 0 = all personas agree. Higher = more confusion.
    n = len(valid)
    entropy = 0.0
    if n > 1:
        for count in set_counts.values():
            p = count / n
            if p > 0:
                entropy -= p * math.log2(p)

    return AgreementResult(
        table_agreement=table_agreement,
        all_agreed=all_agreed,
        tables_used=dict(all_tables),
        finding=finding,
        entropy=round(entropy, 4),
    )
