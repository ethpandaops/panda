"""Data source validation metrics for ethpandaops-panda evaluation."""

from __future__ import annotations

import re
from typing import TYPE_CHECKING, Any

from deepeval.metrics import BaseMetric

if TYPE_CHECKING:
    from deepeval.test_case import LLMTestCase


class DataSourceMetric(BaseMetric):
    """Verify that agent used specific data sources and tables.

    This metric inspects the tool calls to verify that:
    1. The agent queried ClickHouse (not some other data source)
    2. The agent queried the expected tables
    3. Optionally, the agent used specific columns
    """

    def __init__(
        self,
        expected_tables: list[str] | None = None,
        expected_datasource: str = "clickhouse",
        expected_columns: list[str] | None = None,
        require_all_tables: bool = False,
        threshold: float = 1.0,
    ) -> None:
        """Initialize the metric.

        Args:
            expected_tables: List of table names that should be queried.
                Can be partial matches (e.g., "beacon_api_eth_v1" matches
                "beacon_api_eth_v1_events_block").
            expected_datasource: Expected data source type (e.g., "clickhouse").
            expected_columns: List of columns that should be referenced.
            require_all_tables: If True, ALL expected tables must be used.
                If False, at least ONE must be used.
            threshold: Minimum score to pass (default 1.0 = must match).
        """
        self.expected_tables = expected_tables or []
        self.expected_datasource = expected_datasource.lower()
        self.expected_columns = expected_columns or []
        self.require_all_tables = require_all_tables
        self.threshold = threshold

        self.score: float = 0.0
        self.reason: str = ""
        self._success: bool = False

        # Tracking what was found
        self.found_tables: list[str] = []
        self.found_datasource: str | None = None
        self.found_columns: list[str] = []

    async def a_measure(self, test_case: LLMTestCase) -> float:
        """Async measurement (delegates to sync measure)."""
        return self.measure(test_case)

    def measure(self, test_case: LLMTestCase) -> float:
        """Check if expected data sources and tables were used.

        Args:
            test_case: The LLM test case with additional_metadata containing
                'tool_calls' from the agent execution.

        Returns:
            Score between 0.0 and 1.0.
        """
        metadata = test_case.additional_metadata or {}
        tool_calls: list[dict[str, Any]] = metadata.get("tool_calls", [])

        # Reset tracking
        self.found_tables = []
        self.found_datasource = None
        self.found_columns = []

        # Extract all Python code that was executed
        executed_code_blocks: list[str] = []
        for tc in tool_calls:
            tool_name = str(tc.get("name", ""))
            if "execute_python" in tool_name:
                # Get the code from the tool input
                tool_input = tc.get("input", {})
                if isinstance(tool_input, dict):
                    code = tool_input.get("code", "")
                    if code:
                        executed_code_blocks.append(code)

        if not executed_code_blocks:
            self.score = 0.0
            self.reason = "No Python code was executed"
            self._success = False
            return self.score

        # Combine all executed code for analysis
        all_code = "\n".join(executed_code_blocks)

        # Check for data source type
        self._detect_datasource(all_code, tool_calls)

        # Check for table references
        self._detect_tables(all_code)

        # Check for column references
        self._detect_columns(all_code)

        # Calculate score
        self._calculate_score()

        return self.score

    def _detect_datasource(self, code: str, tool_calls: list[dict[str, Any]]) -> None:
        """Detect which data source was used."""
        code_lower = code.lower()

        # Check for ClickHouse indicators
        clickhouse_indicators = [
            "clickhouse",
            "ch_client",
            "execute_query",
            "from default.",
            "from canonical_",
            "from beacon_api_",
            "from mempool_",
            "from mev_relay_",
            "ethpandaops.clickhouse",
        ]

        for indicator in clickhouse_indicators:
            if indicator.lower() in code_lower:
                self.found_datasource = "clickhouse"
                return

        # Check tool results for ClickHouse connection info
        for tc in tool_calls:
            result = str(tc.get("result", "")).lower()
            if "clickhouse" in result:
                self.found_datasource = "clickhouse"
                return

    def _detect_tables(self, code: str) -> None:
        """Detect which tables were referenced in the code."""
        # Common Xatu table prefixes
        table_patterns = [
            r"FROM\s+([a-zA-Z_][a-zA-Z0-9_]*)",
            r"JOIN\s+([a-zA-Z_][a-zA-Z0-9_]*)",
            r"from\s+['\"]?([a-zA-Z_][a-zA-Z0-9_]*)['\"]?",
            r"table\s*=\s*['\"]([a-zA-Z_][a-zA-Z0-9_]*)['\"]",
            # Python string patterns for table names
            r"['\"]+(beacon_api_[a-zA-Z0-9_]+)['\"]",
            r"['\"]+(canonical_[a-zA-Z0-9_]+)['\"]",
            r"['\"]+(mempool_[a-zA-Z0-9_]+)['\"]",
            r"['\"]+(mev_relay_[a-zA-Z0-9_]+)['\"]",
            r"['\"]+(libp2p_[a-zA-Z0-9_]+)['\"]",
        ]

        found = set()
        for pattern in table_patterns:
            matches = re.findall(pattern, code, re.IGNORECASE)
            for match in matches:
                # Filter out common SQL keywords
                if match.lower() not in {"select", "where", "and", "or", "as", "on"}:
                    found.add(match)

        self.found_tables = list(found)

    def _detect_columns(self, code: str) -> None:
        """Detect which columns were referenced."""
        if not self.expected_columns:
            return

        code_lower = code.lower()
        for col in self.expected_columns:
            if col.lower() in code_lower:
                self.found_columns.append(col)

    def _calculate_score(self) -> None:
        """Calculate the final score based on findings."""
        issues = []
        scores = []

        # Check datasource
        if self.expected_datasource:
            if self.found_datasource == self.expected_datasource:
                scores.append(1.0)
            else:
                scores.append(0.0)
                issues.append(
                    f"Expected datasource '{self.expected_datasource}', "
                    f"found '{self.found_datasource}'"
                )

        # Check tables
        if self.expected_tables:
            matched_tables = []
            for expected in self.expected_tables:
                for found in self.found_tables:
                    if expected.lower() in found.lower():
                        matched_tables.append(expected)
                        break

            if self.require_all_tables:
                # All tables must be matched
                table_score = len(matched_tables) / len(self.expected_tables)
                if table_score < 1.0:
                    missing = set(self.expected_tables) - set(matched_tables)
                    issues.append(f"Missing tables: {missing}")
            else:
                # At least one table must match
                table_score = 1.0 if matched_tables else 0.0
                if table_score == 0.0:
                    issues.append(
                        f"None of expected tables {self.expected_tables} found. "
                        f"Found: {self.found_tables}"
                    )

            scores.append(table_score)

        # Check columns
        if self.expected_columns:
            col_score = len(self.found_columns) / len(self.expected_columns)
            scores.append(col_score)
            if col_score < 1.0:
                missing = set(self.expected_columns) - set(self.found_columns)
                issues.append(f"Missing columns: {missing}")

        # Calculate final score
        if scores:
            self.score = sum(scores) / len(scores)
        else:
            self.score = 1.0  # No requirements specified

        # Build reason
        if issues:
            self.reason = "; ".join(issues)
            self._success = False
        else:
            parts = []
            if self.found_datasource:
                parts.append(f"datasource={self.found_datasource}")
            if self.found_tables:
                parts.append(f"tables={self.found_tables[:3]}")
            if self.found_columns:
                parts.append(f"columns={self.found_columns[:3]}")
            self.reason = f"Verified: {', '.join(parts)}"
            self._success = self.score >= self.threshold

    def is_successful(self) -> bool:
        """Check if the metric passed the threshold."""
        return self._success

    @property
    def __name__(self) -> str:
        """Return the metric name."""
        return "DataSourceMetric"


class TableUsageMetric(DataSourceMetric):
    """Convenience metric for checking table usage only."""

    def __init__(
        self,
        tables: list[str],
        require_all: bool = False,
        threshold: float = 1.0,
    ) -> None:
        """Initialize with just table requirements.

        Args:
            tables: List of table names that should be queried.
            require_all: If True, ALL tables must be used.
            threshold: Minimum score to pass.
        """
        super().__init__(
            expected_tables=tables,
            expected_datasource="clickhouse",
            require_all_tables=require_all,
            threshold=threshold,
        )

    @property
    def __name__(self) -> str:
        return "TableUsageMetric"
