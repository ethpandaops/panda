FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -g 1000 panda && \
    adduser -u 1000 -G panda -D panda

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/panda-server /usr/local/bin/panda-server

USER panda

EXPOSE 2480

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:2480/health || exit 1

ENTRYPOINT ["panda-server"]
CMD ["serve"]
