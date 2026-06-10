FROM alpine:3.24@sha256:a2d49ea686c2adfe3c992e47dc3b5e7fa6e6b5055609400dc2acaeb241c829f4

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -g 1000 panda-proxy && \
    adduser -u 1000 -G panda-proxy -D panda-proxy

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/panda-proxy /usr/local/bin/panda-proxy

USER panda-proxy

EXPOSE 18081

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:18081/health || exit 1

ENTRYPOINT ["panda-proxy"]
