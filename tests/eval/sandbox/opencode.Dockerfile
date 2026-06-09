# Sandbox image for the eval SUBJECT (opencode).
#
# The subject's bash tool must not be able to read the repo (the test cases + rubrics live
# there). So opencode runs INSIDE this container with NO repo mount — only a linux `panda`
# binary + a config are mounted in at run time, and the panda server is reached over
# host.docker.internal. The agent's bash sees this container's filesystem, never the host's.
#
# Static image (opencode pinned); the candidate `panda` binary is cross-compiled on the host
# and bind-mounted, so this image does not need rebuilding when panda changes.
FROM node:22-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl jq \
    && rm -rf /var/lib/apt/lists/*

# Pin to the same opencode the host/CI eval uses.
ARG OPENCODE_VERSION=1.15.0
RUN npm install -g opencode-ai@${OPENCODE_VERSION} \
    && OCDIR="$(npm root -g)/opencode-ai" \
    && if [ -f "$OCDIR/postinstall.mjs" ]; then node "$OCDIR/postinstall.mjs" || true; fi \
    && opencode --version

# The candidate `panda` is bind-mounted at /opt/pandabin (a DIRECTORY, so a rebuilt binary
# is picked up live across harden rounds — a file mount would pin the old inode). This
# symlink puts it on PATH.
RUN mkdir -p /opt/pandabin && ln -sf /opt/pandabin/panda /usr/local/bin/panda

# Where the subject's bash runs (a scratch dir, never the repo).
WORKDIR /work
ENTRYPOINT []
