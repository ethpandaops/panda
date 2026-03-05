---
name: join-devnet
description: Join an ethpandaops Ethereum devnet with a local EL+CL node pair using Docker. Use when joining a devnet, syncing to a test network, debugging client behavior on a live devnet, or testing a fix image against a devnet.
argument-hint: <devnet-name> [el_client] [cl_client]
disable-model-invocation: true
---

# Join ethpandaops Devnet

Spin up a local EL+CL Docker node pair that syncs to a live ethpandaops devnet.

## Prerequisites

- Docker installed and running
- Network config files for the target devnet (see [Obtain Network Config](#obtain-network-config))

## Quick Start

Run the bundled join script:

```bash
bash ${CLAUDE_SKILL_DIR}/scripts/join-devnet.sh <devnet-name> <config-dir> [el_client] [cl_client] [el_image] [cl_image]
```

**Arguments:**

| Arg | Default | Description |
|-----|---------|-------------|
| `devnet-name` | (required) | Devnet identifier (e.g. `bal-devnet-2`, `fusaka-devnet-3`) |
| `config-dir` | (required) | Path to directory containing network config files |
| `el_client` | `geth` | EL client: `geth`, `besu`, `nethermind`, `reth`, `ethrex`, `erigon`, `nimbus-el` |
| `cl_client` | `lighthouse` | CL client: `lighthouse`, `lodestar` |
| `el_image` | `ethpandaops/<el>:<devnet>` | Override EL Docker image |
| `cl_image` | `ethpandaops/<cl>:<devnet>` | Override CL Docker image |

**Environment variables:**

| Var | Description |
|-----|-------------|
| `CHECKPOINT_SYNC_URL` | Beacon API URL for checkpoint sync. Dramatically speeds up CL sync on long-running devnets. Typically `https://beacon.<devnet>.ethpandaops.io`. |

**Example — join with default images:**

```bash
bash ${CLAUDE_SKILL_DIR}/scripts/join-devnet.sh bal-devnet-2 ~/devnet-2/network-config geth lighthouse
```

**Example — with checkpoint sync (recommended for long-running devnets):**

```bash
CHECKPOINT_SYNC_URL=https://beacon.bal-devnet-2.ethpandaops.io \
  bash ${CLAUDE_SKILL_DIR}/scripts/join-devnet.sh bal-devnet-2 ~/devnet-2/network-config geth lighthouse
```

**Example — test a local fix image:**

```bash
bash ${CLAUDE_SKILL_DIR}/scripts/join-devnet.sh bal-devnet-2 ~/devnet-2/network-config reth lighthouse ethpandaops/reth:fix-local
```

## Obtain Network Config

The config directory MUST contain these files:

| File | Required | Purpose |
|------|----------|---------|
| `genesis.json` | For geth/reth/ethrex/erigon | EL genesis (geth format) |
| `besu.json` | For besu | Besu chainspec |
| `chainspec.json` | For nethermind | Nethermind chainspec |
| `config.yaml` | Yes | CL beacon chain config |
| `genesis.ssz` | Yes | CL genesis state |
| `bootstrap_nodes.txt` | Yes | CL bootnode ENRs (one per line) |
| `enodes.txt` | Yes | EL bootnode enodes (one per line) |
| `jwt.hex` | Yes | JWT secret for EL-CL auth |

### Download from a running devnet

If the devnet has an accessible beacon node, fetch config files:

```bash
DEVNET="<devnet-name>"
BEACON="https://beacon.${DEVNET}.ethpandaops.io"
AUTH=""  # Set to "-u user:pass" if auth required

mkdir -p ~/devnet-config/${DEVNET}
cd ~/devnet-config/${DEVNET}

# CL config
curl -s ${AUTH} ${BEACON}/eth/v1/config/spec | jq -r '.data | to_entries | map("\(.key): \(.value)") | .[]' > config.yaml
curl -s ${AUTH} ${BEACON}/eth/v2/debug/beacon/states/genesis -o genesis.ssz -H "Accept: application/octet-stream"

# Bootnodes (from Dora or ask the devnet operator)
# EL genesis (typically distributed by the devnet operator)

# JWT secret
openssl rand -hex 32 > jwt.hex
```

Most ethpandaops devnets distribute config files via their operator. Check for a `network-config/` directory in the devnet's setup repo, or ask the devnet coordinator.

## Monitoring

After joining, check sync status:

```bash
# CL sync status
curl -s http://localhost:5052/eth/v1/node/syncing | jq

# EL block number
curl -s -X POST http://localhost:8545 -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq

# Peer count
curl -s http://localhost:5052/eth/v1/node/peers | jq '.data | length'

# Docker logs
docker logs -f <devnet>-<el_client>
docker logs -f <devnet>-<cl_client>
```

## Cleanup

```bash
# Stop containers
docker rm -f <devnet>-<el_client> <devnet>-<cl_client>

# Remove data (full reset)
rm -rf ~/devnet-data/<devnet>/

# Remove Docker network
docker network rm <devnet> 2>/dev/null
```

## Client-Specific Notes

- **Besu**: Uses `besu.json` chainspec, not `genesis.json`. Gets 12GB memory limit by default.
- **Nethermind**: Uses `chainspec.json`, not `genesis.json`.
- **Ethrex**: Requires `--syncmode full`. Log level set via `RUST_LOG` env var.
- **Reth**: Uses `--full` flag for full sync. Uses `genesis.json`.
- **Erigon**: Uses `genesis.json`. May need `--syncmode=full` depending on version.
- **Geth**: Needs one-time `init` with `genesis.json` before starting.

## Testing a Fix

1. Build the fix image in your client repo:
   ```bash
   docker build -t ethpandaops/<client>:fix-local .
   ```

2. Join with the fix image:
   ```bash
   bash ${CLAUDE_SKILL_DIR}/scripts/join-devnet.sh <devnet> <config-dir> <client> lighthouse ethpandaops/<client>:fix-local
   ```

3. Watch for errors:
   ```bash
   docker logs -f <devnet>-<client> 2>&1 | grep -i 'error\|invalid\|mismatch'
   ```
