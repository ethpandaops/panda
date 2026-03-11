"""Visualization metrics for ethpandaops-panda evaluation."""

from __future__ import annotations

import re
from typing import TYPE_CHECKING

from deepeval.metrics import BaseMetric

if TYPE_CHECKING:
    from deepeval.test_case import LLMTestCase


class VisualizationURLMetric(BaseMetric):
    """Check if visualization output contains valid URLs.

    This metric evaluates whether the agent's output includes URLs
    pointing to generated visualizations (charts, graphs, etc.) that
    were uploaded to storage.
    """

    def __init__(
        self,
        threshold: float = 0.8,
        allowed_suffixes: list[str] | None = None,
        url_patterns: list[str] | None = None,
    ) -> None:
        """Initialize the metric.

        Args:
            threshold: Minimum score to pass (0.0 to 1.0).
            allowed_suffixes: File suffixes to look for.
                Defaults to common image and document types.
            url_patterns: Regex patterns to match valid URLs.
                Defaults to common cloud storage patterns.
        """
        self.threshold = threshold
        self.allowed_suffixes = allowed_suffixes or [
            "png",
            "jpg",
            "jpeg",
            "svg",
            "html",
            "pdf",
        ]
        self.url_patterns = url_patterns or [
            r"https?://[^\s]+\.(" + "|".join(self.allowed_suffixes) + r")",
            r"https?://[^\s]*/api/v1/storage/files/[^\s]*",
            r"https?://[^\s]*r2\.cloudflarestorage\.com[^\s]*",
            r"https?://[^\s]*s3\.[^\s]*amazonaws\.com[^\s]*",
            r"https?://[^\s]*storage\.googleapis\.com[^\s]*",
        ]
        self.score: float = 0.0
        self.reason: str = ""
        self._success: bool = False
        self.found_urls: list[str] = []

    async def a_measure(self, test_case: LLMTestCase) -> float:
        """Async measurement (delegates to sync measure)."""
        return self.measure(test_case)

    def measure(self, test_case: LLMTestCase) -> float:
        """Check if output contains visualization URLs.

        Args:
            test_case: The LLM test case with actual_output containing
                the agent's response.

        Returns:
            Score between 0.0 and 1.0 indicating visualization presence.
        """
        output = test_case.actual_output or ""
        self.found_urls = []

        # Search for URLs matching our patterns
        for pattern in self.url_patterns:
            matches = re.findall(pattern, output, re.IGNORECASE)
            if matches:
                # For suffix pattern, reconstruct full URLs
                if pattern.startswith(r"https?://[^\s]+\.("):
                    # Find full URLs with these suffixes
                    full_url_pattern = r"https?://[^\s]+\.(" + "|".join(
                        self.allowed_suffixes
                    ) + r")"
                    full_matches = re.findall(full_url_pattern, output, re.IGNORECASE)
                    for ext in full_matches:
                        # Find the actual full URL
                        url_with_ext = re.search(
                            r"(https?://[^\s]+\." + ext + r")", output, re.IGNORECASE
                        )
                        if url_with_ext:
                            self.found_urls.append(url_with_ext.group(1))
                else:
                    self.found_urls.extend(matches if isinstance(matches, list) else [matches])

        # Deduplicate
        self.found_urls = list(set(self.found_urls))

        if self.found_urls:
            self.score = 1.0
            self.reason = f"Found {len(self.found_urls)} visualization URL(s) in output"
            self._success = True
        else:
            self.score = 0.0
            self.reason = "No visualization URL found in output"
            self._success = False

        return self.score

    def is_successful(self) -> bool:
        """Check if the metric passed the threshold."""
        return self._success and self.score >= self.threshold

    @property
    def __name__(self) -> str:
        """Return the metric name."""
        return "VisualizationURLMetric"


class OutputFileMetric(BaseMetric):
    """Check if agent used output files appropriately.

    This metric evaluates whether the agent created and referenced
    output files (CSV, JSON, etc.) during execution.
    """

    def __init__(
        self,
        threshold: float = 0.8,
        expected_file_types: list[str] | None = None,
    ) -> None:
        """Initialize the metric.

        Args:
            threshold: Minimum score to pass (0.0 to 1.0).
            expected_file_types: File types that should be created.
        """
        self.threshold = threshold
        self.expected_file_types = expected_file_types or ["csv", "json", "parquet"]
        self.score: float = 0.0
        self.reason: str = ""
        self._success: bool = False

    async def a_measure(self, test_case: LLMTestCase) -> float:
        """Async measurement (delegates to sync measure)."""
        return self.measure(test_case)

    def measure(self, test_case: LLMTestCase) -> float:
        """Check if output files were created.

        Args:
            test_case: The LLM test case with additional_metadata containing
                'tool_calls' from the agent execution.

        Returns:
            Score between 0.0 and 1.0 indicating output file creation.
        """
        metadata = test_case.additional_metadata or {}
        tool_calls = metadata.get("tool_calls", [])

        # Check for list_output_files or get_output_file calls
        listed_files = any(
            "list_output_files" in str(tc.get("name", "")) for tc in tool_calls
        )
        got_files = any(
            "get_output_file" in str(tc.get("name", "")) for tc in tool_calls
        )

        # Check if execute_python produced files
        file_references = []
        for tc in tool_calls:
            result = str(tc.get("result", ""))
            for ext in self.expected_file_types:
                if f".{ext}" in result.lower():
                    file_references.append(ext)

        if file_references or listed_files or got_files:
            self.score = 1.0
            methods = []
            if file_references:
                methods.append(f"created {len(set(file_references))} file type(s)")
            if listed_files:
                methods.append("listed output files")
            if got_files:
                methods.append("retrieved output files")
            self.reason = f"Agent {', '.join(methods)}"
            self._success = True
        else:
            self.score = 0.0
            self.reason = "No output files created or retrieved"
            self._success = False

        return self.score

    def is_successful(self) -> bool:
        """Check if the metric passed the threshold."""
        return self._success and self.score >= self.threshold

    @property
    def __name__(self) -> str:
        """Return the metric name."""
        return "OutputFileMetric"
