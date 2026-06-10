# =============================================================================
# Runtime
# =============================================================================
FROM alpine:3.24@sha256:a2d49ea686c2adfe3c992e47dc3b5e7fa6e6b5055609400dc2acaeb241c829f4

RUN apk add --no-cache ca-certificates tzdata docker-cli su-exec

RUN addgroup -g 1000 panda && \
    adduser -u 1000 -G panda -D panda

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/panda-server /usr/local/bin/panda-server

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
