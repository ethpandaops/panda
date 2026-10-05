# =============================================================================
# Runtime
# =============================================================================
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

# shadow provides usermod/groupmod, which the entrypoint uses to re-number the
# panda user to the owner of the mounted credentials (non-1000 host UIDs).
RUN apk add --no-cache ca-certificates tzdata docker-cli su-exec shadow

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
