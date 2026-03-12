# =============================================================================
# Stage 1: Download embedding model
# =============================================================================
FROM alpine:3.21 AS model-downloader

RUN apk add --no-cache curl

RUN mkdir -p /model/all-MiniLM-L6-v2 && \
    curl -sL -o /model/all-MiniLM-L6-v2/model.onnx \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx && \
    curl -sL -o /model/all-MiniLM-L6-v2/tokenizer.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/tokenizer.json && \
    curl -sL -o /model/all-MiniLM-L6-v2/config.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/config.json && \
    curl -sL -o /model/all-MiniLM-L6-v2/special_tokens_map.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/special_tokens_map.json && \
    curl -sL -o /model/all-MiniLM-L6-v2/tokenizer_config.json \
        https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/tokenizer_config.json

# =============================================================================
# Stage 2: Runtime
# =============================================================================
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata docker-cli su-exec

RUN addgroup -g 1000 panda && \
    adduser -u 1000 -G panda -D panda

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/panda-server /usr/local/bin/panda-server

COPY --from=model-downloader /model/all-MiniLM-L6-v2 /usr/share/panda/all-MiniLM-L6-v2

# Pre-create storage directory with correct ownership.
# Docker copies this ownership into new named volumes.
RUN mkdir -p /data/storage && chown panda:panda /data/storage

# Entrypoint runs as root to fix volume ownership, then drops to panda.
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

EXPOSE 2480

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:2480/health || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["panda-server", "serve"]
