# ethpandaops Panda Server Dockerfile
#
# Build:
#   docker build -t panda:latest .
#
# Run:
#   docker run -p 2480:2480 -v /var/run/docker.sock:/var/run/docker.sock panda:latest

# =============================================================================
# Stage 1: Go builder
# =============================================================================
FROM golang:1.26-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy go mod files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY modules/ modules/
COPY internal/ internal/
COPY runbooks/ runbooks/
COPY datasets/ datasets/

# Build with version info
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/ethpandaops/panda/internal/version.Version=${VERSION} \
    -X github.com/ethpandaops/panda/internal/version.GitCommit=${GIT_COMMIT} \
    -X github.com/ethpandaops/panda/internal/version.BuildTime=${BUILD_TIME}" \
    -o panda-server ./cmd/server

# Build proxy binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/ethpandaops/panda/internal/version.Version=${VERSION} \
    -X github.com/ethpandaops/panda/internal/version.GitCommit=${GIT_COMMIT} \
    -X github.com/ethpandaops/panda/internal/version.BuildTime=${BUILD_TIME}" \
    -o panda-proxy ./cmd/proxy

# =============================================================================
# Stage 2: Runtime (single image, all backends)
# =============================================================================
FROM debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241

# Runtime deps. docker.io/netcat for the docker backend + healthcheck; python3 +
# librsvg + fonts are the lean provisioning floor for the *direct* backend (the
# heavy Python wheels are NOT baked — see the entrypoint). librsvg rasterises
# chartkit's SVG charts; the fonts give it a glyph fallback.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates docker.io netcat-openbsd \
    python3 librsvg2-bin fonts-dejavu-core fonts-noto-color-emoji && \
    rm -rf /var/lib/apt/lists/*

# Create non-root user. panda-sandbox (uid/gid 1002) is the dedicated,
# owns-nothing identity the direct backend drops untrusted Python to; keeping it
# distinct from panda is what makes the server's config and credential files
# unreadable to executed code (see pkg/sandbox/direct_harden_linux.go and
# sandbox.exec_uid/exec_gid).
RUN useradd -m -s /bin/bash panda && \
    usermod -aG docker panda 2>/dev/null || true; \
    groupadd -g 1002 panda-sandbox && \
    useradd -u 1002 -g 1002 -M -s /usr/sbin/nologin panda-sandbox

WORKDIR /app

# Copy binaries from builder
COPY --from=builder /app/panda-server /app/panda-server
COPY --from=builder /app/panda-proxy /app/panda-proxy

# Direct-backend provisioning assets. Used only when PANDA_SANDBOX_BACKEND=direct:
# the entrypoint builds /opt/panda-venv from these at first boot. The heavy wheels
# install from the hash lock at boot rather than baking into the image, so the
# default (docker/gvisor) image stays lean — uv + python3 are the only always-on
# cost. requirements.txt + ethpandaops are the SAME files the sandbox image uses
# (wildcard COPY matches sandbox/Dockerfile + scripts/sandbox-hash.sh — no drift).
COPY --from=ghcr.io/astral-sh/uv:0.11.17@sha256:03bdc89bb9798628846e60c3a9ad19006c8c3c724ccd2985a33145c039a0577b /uv /uvx /bin/
COPY sandbox/requirements.txt /opt/panda-sandbox/requirements.txt
COPY sandbox/ethpandaops /opt/panda-sandbox/ethpandaops-pkg
COPY modules/*/python/*.py /opt/panda-sandbox/ethpandaops-pkg/ethpandaops/

# Create directories
RUN mkdir -p /config /shared /output /data/storage /data/cache && \
    chown -R panda:panda /app /config /shared /output /data/storage /data/cache

# Entrypoint runs as root to fix volume ownership, then drops to panda.
# gosu drops privileges for docker/gvisor; setpriv (util-linux) does the same
# for the direct backend but can also raise the ambient caps the re-exec
# trampoline needs (see docker-entrypoint.sh for the set).
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh && \
    apt-get update && apt-get install -y --no-install-recommends gosu util-linux && \
    rm -rf /var/lib/apt/lists/*

# Expose ports
EXPOSE 2480 2490

# Health check - verify the MCP server port is accepting connections. The start
# period is generous: the direct backend installs its venv on first boot before
# the server starts listening (docker/gvisor start immediately).
HEALTHCHECK --interval=30s --timeout=5s --start-period=120s --retries=3 \
    CMD nc -z localhost 2480 || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["/app/panda-server", "serve"]
