FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/panda /usr/local/bin/panda

ENTRYPOINT ["panda"]
