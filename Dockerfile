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
FROM golang:1.25-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    git ca-certificates curl && \
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

# Download embedding model (architecture-independent)
RUN mkdir -p /assets/all-MiniLM-L6-v2 && \
    curl -sL -o /assets/all-MiniLM-L6-v2/model.onnx \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx && \
    curl -sL -o /assets/all-MiniLM-L6-v2/tokenizer.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/tokenizer.json && \
    curl -sL -o /assets/all-MiniLM-L6-v2/config.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/config.json && \
    curl -sL -o /assets/all-MiniLM-L6-v2/special_tokens_map.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/special_tokens_map.json && \
    curl -sL -o /assets/all-MiniLM-L6-v2/tokenizer_config.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/tokenizer_config.json

# =============================================================================
# Stage 2: Runtime
# =============================================================================
FROM debian:bookworm-slim

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

# Copy embedding model
COPY --from=builder /assets/all-MiniLM-L6-v2 /usr/share/panda/all-MiniLM-L6-v2

# Create directories
RUN mkdir -p /config /shared /output /data/storage && \
    chown -R panda:panda /app /config /shared /output /data/storage

# Expose ports
EXPOSE 2480 2490

# Health check - verify the MCP server port is accepting connections
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD nc -z localhost 2480 || exit 1

ENTRYPOINT ["/app/panda-server"]
CMD ["serve"]
