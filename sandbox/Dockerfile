# ethpandaops MCP Sandbox Container
# This container provides a secure execution environment for Python code
# with the ethpandaops library pre-installed for Ethereum network data analysis.
#
# Multi-stage: the builder holds uv and resolves the full dependency tree into a
# staging prefix; the final image copies that prefix as a complete install
# artifact (site-packages, console scripts, and package data) without ever
# shipping uv. Dependencies are installed --only-binary, so a missing wheel is a
# hard error rather than an unlocked source compile, and no C toolchain is
# needed in either stage.

FROM python:3.11-slim@sha256:1042b61448fef4ba92d16a8c7eb4996d027568ce64792a7877fd88511e0af7c6 AS builder

# uv (build-time only; never copied into the final image)
COPY --from=ghcr.io/astral-sh/uv:0.11.17@sha256:03bdc89bb9798628846e60c3a9ad19006c8c3c724ccd2985a33145c039a0577b /uv /uvx /bin/

# Install Python dependencies into a staging prefix. requirements.txt is compiled
# from requirements.in with hashes (`uv pip compile --generate-hashes
# --universal`); --require-hashes makes pip refuse anything that doesn't match
# the lock, and --only-binary=:all: forbids source builds (every dependency must
# resolve to a prebuilt wheel).
COPY sandbox/requirements.txt /tmp/requirements.txt
RUN uv pip install --python /usr/local/bin/python3 --prefix /install --no-cache \
    --require-hashes --only-binary=:all: -r /tmp/requirements.txt

# Install the ethpandaops platform library. Copied after the heavy dependency
# layer so edits to these (frequently changed) files don't bust the deps cache.
COPY sandbox/ethpandaops /opt/ethpandaops-pkg
# Each module's helper is named after its module, so the wildcard lands each as
# ethpandaops/<name>.py. A wildcard (not an explicit list) so a new module is
# bundled automatically — matching scripts/sandbox-hash.sh, which already globs
# modules/*/python/*.py, and the server Dockerfile's identical COPY. No drift.
COPY modules/*/python/*.py /opt/ethpandaops-pkg/ethpandaops/

# --no-deps: the package's dependencies are already installed (hash-locked)
# above; resolving them here would bypass the lock. The package is pure Python,
# so no toolchain is required to build it.
RUN uv pip install --python /usr/local/bin/python3 --prefix /install --no-cache --no-deps /opt/ethpandaops-pkg

FROM python:3.11-slim@sha256:1042b61448fef4ba92d16a8c7eb4996d027568ce64792a7877fd88511e0af7c6

# librsvg provides rsvg-convert, which the chartkit library uses to rasterise its
# self-contained SVG charts. The chart font (Inter) is embedded as a data URI; the extra
# font packages give librsvg a fontconfig fallback for glyphs Inter lacks — DejaVu covers
# Latin-ext/Greek/Cyrillic and Noto Color Emoji covers emotes (best-effort under librsvg).
RUN apt-get update && apt-get install -y --no-install-recommends \
    librsvg2-bin \
    fonts-dejavu-core \
    fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user and runtime directories.
RUN useradd -m -s /bin/bash sandbox && \
    mkdir -p /shared /output && \
    chown sandbox:sandbox /shared /output

# Copy the resolved environment from the builder onto the runtime's /usr/local,
# bringing site-packages, console scripts, and package data. uv and the staging
# tooling are intentionally left behind.
COPY --from=builder /install /usr/local

# Set working directory
WORKDIR /home/sandbox

# Switch to non-root user
USER sandbox

# Default command (overridden by the sandbox backend)
CMD ["python", "--version"]
