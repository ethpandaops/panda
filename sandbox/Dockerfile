# ethpandaops MCP Sandbox Container
# This container provides a secure execution environment for Python code
# with the ethpandaops library pre-installed for Ethereum network data analysis.

FROM python:3.11-slim

# Install uv and system dependencies
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user for security
RUN useradd -m -s /bin/bash sandbox

# Create directories
RUN mkdir -p /shared /output && \
    chown sandbox:sandbox /shared /output

# Install Python dependencies
COPY sandbox/requirements.txt /tmp/requirements.txt
RUN uv pip install --system --no-cache -r /tmp/requirements.txt && \
    rm /tmp/requirements.txt

# Install the ethpandaops platform library
COPY sandbox/ethpandaops /opt/ethpandaops-pkg

# Copy extension Python modules into the package
COPY modules/cbt/python/cbt.py /opt/ethpandaops-pkg/ethpandaops/cbt.py
COPY modules/clickhouse/python/clickhouse.py /opt/ethpandaops-pkg/ethpandaops/clickhouse.py
COPY modules/dora/python/dora.py /opt/ethpandaops-pkg/ethpandaops/dora.py
COPY modules/loki/python/loki.py /opt/ethpandaops-pkg/ethpandaops/loki.py
COPY modules/prometheus/python/prometheus.py /opt/ethpandaops-pkg/ethpandaops/prometheus.py
COPY modules/ethnode/python/ethnode.py /opt/ethpandaops-pkg/ethpandaops/ethnode.py

RUN uv pip install --system --no-cache /opt/ethpandaops-pkg && rm -rf /opt/ethpandaops-pkg

# Set working directory
WORKDIR /home/sandbox

# Switch to non-root user
USER sandbox

# Default command (overridden by the sandbox backend)
CMD ["python", "--version"]
