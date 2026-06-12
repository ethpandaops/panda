# ethpandaops MCP Sandbox Container
# This container provides a secure execution environment for Python code
# with the ethpandaops library pre-installed for Ethereum network data analysis.

FROM python:3.11-slim@sha256:a3ab0b966bc4e91546a033e22093cb840908979487a9fc0e6e38295747e49ac0

# Install uv and system dependencies
COPY --from=ghcr.io/astral-sh/uv:0.11.17@sha256:03bdc89bb9798628846e60c3a9ad19006c8c3c724ccd2985a33145c039a0577b /uv /uvx /bin/
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user for security
RUN useradd -m -s /bin/bash sandbox

# Create directories
RUN mkdir -p /shared /output && \
    chown sandbox:sandbox /shared /output

# Install Python dependencies. requirements.txt is compiled from requirements.in
# with hashes (`uv pip compile --generate-hashes --universal`); --require-hashes
# makes pip refuse anything that doesn't match the lock.
COPY sandbox/requirements.txt /tmp/requirements.txt
RUN uv pip install --system --no-cache --require-hashes -r /tmp/requirements.txt && \
    rm /tmp/requirements.txt

# Install the ethpandaops platform library
COPY sandbox/ethpandaops /opt/ethpandaops-pkg

# Copy extension Python modules into the package
COPY modules/benchmarkoor/python/benchmarkoor.py /opt/ethpandaops-pkg/ethpandaops/benchmarkoor.py
COPY modules/block_archive/python/block_archive.py /opt/ethpandaops-pkg/ethpandaops/block_archive.py
COPY modules/cbt/python/cbt.py /opt/ethpandaops-pkg/ethpandaops/cbt.py
COPY modules/clickhouse/python/clickhouse.py /opt/ethpandaops-pkg/ethpandaops/clickhouse.py
COPY modules/dora/python/dora.py /opt/ethpandaops-pkg/ethpandaops/dora.py
COPY modules/forky/python/forky.py /opt/ethpandaops-pkg/ethpandaops/forky.py
COPY modules/loki/python/loki.py /opt/ethpandaops-pkg/ethpandaops/loki.py
COPY modules/prometheus/python/prometheus.py /opt/ethpandaops-pkg/ethpandaops/prometheus.py
COPY modules/ethnode/python/ethnode.py /opt/ethpandaops-pkg/ethpandaops/ethnode.py
COPY modules/tracoor/python/tracoor.py /opt/ethpandaops-pkg/ethpandaops/tracoor.py

# --no-deps: the package's dependencies are already installed (hash-locked)
# above; resolving them here would bypass the lock.
RUN uv pip install --system --no-cache --no-deps /opt/ethpandaops-pkg && rm -rf /opt/ethpandaops-pkg

# Set working directory
WORKDIR /home/sandbox

# Switch to non-root user
USER sandbox

# Default command (overridden by the sandbox backend)
CMD ["python", "--version"]
