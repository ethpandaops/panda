"""Resource discovery metric for ethpandaops-panda evaluation."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from deepeval.metrics import BaseMetric

if TYPE_CHECKING:
    from deepeval.test_case import LLMTestCase


class ResourceDiscoveryMetric(BaseMetric):
    """Check if agent reads resources before executing queries.

    This metric evaluates whether the agent properly discovers available
    schemas, datasources, and API documentation before executing ClickHouse
    queries. Good agents should read resource URIs to understand the data
    model before writing queries.
    """

    def __init__(
        self,
        threshold: float = 0.7,
        required_resource_prefixes: list[str] | None = None,
    ) -> None:
        """Initialize the metric.

        Args:
            threshold: Minimum score to pass (0.0 to 1.0).
            required_resource_prefixes: URI prefixes that should be accessed.
                Defaults to common ethpandaops-panda resource types.
        """
        self.threshold = threshold
        self.required_resource_prefixes = required_resource_prefixes or [
            "datasources://",
            "clickhouse://",
            "api://",
            "networks://",
            "examples://",
        ]
        self.score: float = 0.0
        self.reason: str = ""
        self._success: bool = False

    async def a_measure(self, test_case: LLMTestCase) -> float:
        """Async measurement (delegates to sync measure)."""
        return self.measure(test_case)

    def measure(self, test_case: LLMTestCase) -> float:
        """Check if resources were read before tool execution.

        Args:
            test_case: The LLM test case with additional_metadata containing
                'resources_read' and 'tool_calls' from the agent execution.

        Returns:
            Score between 0.0 and 1.0 indicating resource discovery quality.
        """
        metadata = test_case.additional_metadata or {}
        resources_read: list[str] = metadata.get("resources_read", [])
        tool_calls: list[dict[str, Any]] = metadata.get("tool_calls", [])

        # Check if any resource URIs were accessed
        resource_uris_accessed = [
            uri
            for uri in resources_read
            if any(uri.startswith(prefix) for prefix in self.required_resource_prefixes)
        ]

        # Check if execute_python was called (indicating a query was run)
        executed_code = any(
            "execute_python" in str(tc.get("name", "")) for tc in tool_calls
        )

        # Check if the search tool was used (also counts as resource discovery)
        searched_resources = any(
            str(tc.get("name", "")).endswith("__search")
            or str(tc.get("name", "")) == "search"
            for tc in tool_calls
        )

        if not executed_code:
            # No code execution, resource discovery not applicable
            self.score = 1.0
            self.reason = "No code execution occurred, resource discovery N/A"
            self._success = True
        elif resource_uris_accessed or searched_resources:
            # Good: agent read resources before querying
            discovery_methods = []
            if resource_uris_accessed:
                discovery_methods.append(
                    f"read {len(resource_uris_accessed)} resource(s)"
                )
            if searched_resources:
                discovery_methods.append("used semantic search")

            self.score = 1.0
            self.reason = f"Agent {', '.join(discovery_methods)} before querying"
            self._success = True
        else:
            # Bad: executed code without reading resources
            self.score = 0.0
            self.reason = "Executed code without reading schema/API resources or using semantic search"
            self._success = False

        return self.score

    def is_successful(self) -> bool:
        """Check if the metric passed the threshold."""
        return self._success and self.score >= self.threshold

    @property
    def __name__(self) -> str:
        """Return the metric name."""
        return "ResourceDiscoveryMetric"
