---
name: install-panda
description: >-
  Install and set up panda from scratch. Use when the user wants to install
  panda, get started with panda, or set up their environment. Triggers on:
  install panda, setup panda, get started, getting started.
---

# Install panda

Guide the user through installing and setting up panda.

## Install the binary

Run the install script:

```bash
curl -fsSL https://raw.githubusercontent.com/ethpandaops/panda/master/install.sh | bash
```

This installs the `panda` binary to `~/.local/bin/` and adds it to PATH.

## Set up

Run the interactive setup:

```bash
panda init
```

This handles Docker checks, image pulls, config, auth, and starting the server.

## Getting started

Once installed, point the user to the built-in getting-started guide for next steps:

- **CLI:** `panda getting-started`
- **MCP:** read the `panda://getting-started` resource
