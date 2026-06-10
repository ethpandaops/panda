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
FROM golang:1.26-bookworm@sha256:5d2b868674b57c9e48cdd39e891acce4196b6926ca6d11e9c270a8f85106203d AS builder

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
# Stage 2: Runtime
# =============================================================================
FROM debian:bookworm-slim@sha256:0104b334637a5f19aa9c983a91b54c89887c0984081f2068983107a6f6c21eeb

# Install runtime dependencies for Docker access and health checks
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates docker.io netcat-openbsd && \
    rm -rf /var/lib/apt/lists/*

# Create non-root user
RUN useradd -m -s /bin/bash panda && \
    usermod -aG docker panda 2>/dev/null || true

WORKDIR /app

# Copy binaries from builder
COPY --from=builder /app/panda-server /app/panda-server
COPY --from=builder /app/panda-proxy /app/panda-proxy

# Create directories
RUN mkdir -p /config /shared /output /data/storage /data/cache && \
    chown -R panda:panda /app /config /shared /output /data/storage /data/cache

# Entrypoint runs as root to fix volume ownership, then drops to panda.
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh && \
    apt-get update && apt-get install -y --no-install-recommends gosu && \
    rm -rf /var/lib/apt/lists/*

# Expose ports
EXPOSE 2480 2490

# Health check - verify the MCP server port is accepting connections
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD nc -z localhost 2480 || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["/app/panda-server", "serve"]
