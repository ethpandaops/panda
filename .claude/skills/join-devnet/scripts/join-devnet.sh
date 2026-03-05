#!/usr/bin/env bash
set -euo pipefail

# Join an ethpandaops devnet with a local EL+CL Docker node pair
# Usage: join-devnet.sh <devnet-name> <config-dir> [el_client] [cl_client] [el_image] [cl_image]
#
# Environment variables:
#   CHECKPOINT_SYNC_URL  Beacon API URL for checkpoint sync (e.g. https://beacon.<devnet>.ethpandaops.io)
#                        Dramatically speeds up CL sync on long-running devnets.

if [ $# -lt 2 ]; then
  echo "Usage: $0 <devnet-name> <config-dir> [el_client] [cl_client] [el_image] [cl_image]"
  echo ""
  echo "  devnet-name  Devnet identifier (e.g. bal-devnet-2)"
  echo "  config-dir   Path to network config directory"
  echo "  el_client    geth (default), besu, nethermind, reth, ethrex, erigon, nimbus-el"
  echo "  cl_client    lighthouse (default), lodestar, teku, prysm, nimbus, grandine"
  echo "  el_image     Override EL Docker image (default: ethpandaops/<el>:<devnet>)"
  echo "  cl_image     Override CL Docker image (default: ethpandaops/<cl>:<devnet>)"
  echo ""
  echo "Environment variables:"
  echo "  CHECKPOINT_SYNC_URL  Beacon API URL for checkpoint sync"
  exit 1
fi

DEVNET="$1"
CONFIG_DIR="$(cd "$2" && pwd)"
EL="${3:-geth}"
CL="${4:-lighthouse}"
# nimbus-el Docker image is published as nimbus-eth1
EL_DOCKER_NAME="$EL"
if [ "$EL" = "nimbus-el" ]; then
  EL_DOCKER_NAME="nimbus-eth1"
fi
EL_IMAGE="${5:-ethpandaops/$EL_DOCKER_NAME:$DEVNET}"
CL_IMAGE="${6:-ethpandaops/$CL:$DEVNET}"

# Use -local suffix images if they exist and no override given
if docker image inspect "ethpandaops/$EL_DOCKER_NAME:${DEVNET}-local" >/dev/null 2>&1 && [ -z "${5:-}" ]; then
  EL_IMAGE="ethpandaops/$EL_DOCKER_NAME:${DEVNET}-local"
  echo "Using local EL image: $EL_IMAGE"
fi
if docker image inspect "ethpandaops/$CL:${DEVNET}-local" >/dev/null 2>&1 && [ -z "${6:-}" ]; then
  CL_IMAGE="ethpandaops/$CL:${DEVNET}-local"
  echo "Using local CL image: $CL_IMAGE"
fi

# Validate config directory
for f in jwt.hex config.yaml genesis.ssz bootstrap_nodes.txt enodes.txt; do
  if [ ! -f "$CONFIG_DIR/$f" ]; then
    echo "ERROR: Missing required config file: $CONFIG_DIR/$f"
    exit 1
  fi
done

# Extract chain ID from genesis or config
CHAIN_ID=""
if [ -f "$CONFIG_DIR/genesis.json" ]; then
  CHAIN_ID=$(python3 -c "
import json, sys
with open('$CONFIG_DIR/genesis.json') as f:
    g = json.load(f)
    cid = g.get('config', {}).get('chainId', '')
    print(cid)
" 2>/dev/null || true)
fi
if [ -z "$CHAIN_ID" ] && [ -f "$CONFIG_DIR/besu.json" ]; then
  CHAIN_ID=$(python3 -c "
import json, sys
with open('$CONFIG_DIR/besu.json') as f:
    g = json.load(f)
    cid = g.get('config', {}).get('chainId', '')
    print(cid)
" 2>/dev/null || true)
fi
if [ -z "$CHAIN_ID" ] && [ -f "$CONFIG_DIR/chainspec.json" ]; then
  CHAIN_ID=$(python3 -c "
import json, sys
with open('$CONFIG_DIR/chainspec.json') as f:
    g = json.load(f)
    cid = g.get('params', {}).get('networkID', g.get('params', {}).get('chainId', ''))
    # Handle hex chain IDs
    if isinstance(cid, str) and cid.startswith('0x'):
        cid = int(cid, 16)
    print(cid)
" 2>/dev/null || true)
fi
if [ -z "$CHAIN_ID" ]; then
  echo "WARNING: Could not extract chain ID from genesis files. Using 1 as default."
  CHAIN_ID=1
fi

ENODES=$(paste -sd, "$CONFIG_DIR/enodes.txt")
BOOTNODES=$(paste -sd, "$CONFIG_DIR/bootstrap_nodes.txt")
MY_IP=$(curl -s4 --connect-timeout 5 ifconfig.me || echo "127.0.0.1")

DATA_DIR="$HOME/devnet-data/$DEVNET"
EL_DATADIR="$DATA_DIR/$EL"
CL_DATADIR="$DATA_DIR/$CL"
CONTAINER_EL="${DEVNET}-${EL}"
CONTAINER_CL="${DEVNET}-${CL}"

# Ports
EL_RPC=8545
EL_AUTH=8551
EL_P2P=30303
CL_HTTP=5052
CL_P2P=9000

# Checkpoint sync URL (optional, set via env or auto-detect)
CHECKPOINT_SYNC_URL="${CHECKPOINT_SYNC_URL:-}"

echo "=== Joining $DEVNET ==="
echo "EL: $EL ($EL_IMAGE)"
echo "CL: $CL ($CL_IMAGE)"
echo "Chain ID: $CHAIN_ID"
echo "Data: $DATA_DIR"
echo "IP: $MY_IP"
if [ -n "$CHECKPOINT_SYNC_URL" ]; then
  echo "Checkpoint sync: $CHECKPOINT_SYNC_URL"
fi
echo ""

# Clean up previous containers
for c in "$CONTAINER_EL" "$CONTAINER_CL"; do
  docker rm -f "$c" 2>/dev/null || true
done

# Create Docker network
docker network create "$DEVNET" 2>/dev/null || true

mkdir -p "$EL_DATADIR" "$CL_DATADIR"

# Pull images (skip for local-only images)
echo "Pulling images..."
if docker image inspect "$EL_IMAGE" >/dev/null 2>&1; then
  echo "EL image exists locally, skipping pull"
else
  docker pull "$EL_IMAGE"
fi
if docker image inspect "$CL_IMAGE" >/dev/null 2>&1; then
  echo "CL image exists locally, skipping pull"
else
  docker pull "$CL_IMAGE"
fi

# --- Start EL ---
echo "Starting EL ($EL)..."

case "$EL" in
  geth)
    docker run --rm \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      "$EL_IMAGE" \
      init --datadir /data /config/genesis.json 2>&1 || true

    docker run -d --name "$CONTAINER_EL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      -p $EL_RPC:8545 -p $EL_AUTH:8551 -p $EL_P2P:30303/tcp -p $EL_P2P:30303/udp \
      "$EL_IMAGE" \
      --datadir /data \
      --networkid "$CHAIN_ID" \
      --port 30303 \
      --http --http.addr 0.0.0.0 --http.port 8545 \
      --http.api eth,net,web3,debug,admin,txpool \
      --http.vhosts '*' --http.corsdomain '*' \
      --authrpc.addr 0.0.0.0 --authrpc.port 8551 --authrpc.vhosts '*' \
      --authrpc.jwtsecret /config/jwt.hex \
      --nat "extip:$MY_IP" \
      --discovery.v5 \
      --bootnodes "$ENODES" \
      --syncmode full --gcmode archive
    ;;

  besu)
    if [ ! -f "$CONFIG_DIR/besu.json" ]; then
      echo "ERROR: besu requires besu.json chainspec in config directory"
      exit 1
    fi
    docker run -d --name "$CONTAINER_EL" \
      --network "$DEVNET" \
      --memory="${BESU_MEM_LIMIT:-12g}" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      -p $EL_RPC:8545 -p $EL_AUTH:8551 -p $EL_P2P:30303/tcp -p $EL_P2P:30303/udp \
      "$EL_IMAGE" \
      --data-path=/data \
      --genesis-file=/config/besu.json \
      --network-id="$CHAIN_ID" \
      --p2p-port=30303 \
      --rpc-http-enabled --rpc-http-host=0.0.0.0 --rpc-http-port=8545 \
      --rpc-http-api=ETH,NET,WEB3,DEBUG,ADMIN,TXPOOL,TRACE \
      --rpc-http-cors-origins='*' \
      --engine-rpc-enabled --engine-rpc-port=8551 --engine-host-allowlist='*' \
      --engine-jwt-secret=/config/jwt.hex \
      --nat-method=NONE --p2p-host="$MY_IP" \
      --bootnodes="$ENODES" \
      --sync-mode=FULL \
      --Xhttp-timeout-seconds=600
    ;;

  nethermind)
    if [ ! -f "$CONFIG_DIR/chainspec.json" ]; then
      echo "ERROR: nethermind requires chainspec.json in config directory"
      exit 1
    fi
    docker run -d --name "$CONTAINER_EL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      -p $EL_RPC:8545 -p $EL_AUTH:8551 -p $EL_P2P:30303/tcp -p $EL_P2P:30303/udp \
      "$EL_IMAGE" \
      --datadir /data \
      --Init.ChainSpecPath /config/chainspec.json \
      --Network.ExternalIp "$MY_IP" \
      --Network.DiscoveryPort 30303 --Network.P2PPort 30303 \
      --JsonRpc.Enabled true --JsonRpc.Host 0.0.0.0 --JsonRpc.Port 8545 \
      --JsonRpc.EnabledModules 'Eth,Net,Web3,Debug,Admin,TxPool,Trace' \
      --JsonRpc.EngineHost 0.0.0.0 --JsonRpc.EnginePort 8551 \
      --JsonRpc.JwtSecretFile /config/jwt.hex \
      --Network.Bootnodes "$ENODES"
    ;;

  reth)
    docker run -d --name "$CONTAINER_EL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      -p $EL_RPC:8545 -p $EL_AUTH:8551 -p $EL_P2P:30303/tcp -p $EL_P2P:30303/udp \
      "$EL_IMAGE" \
      node \
      --datadir /data \
      --chain /config/genesis.json \
      --port 30303 \
      --http --http.addr 0.0.0.0 --http.port 8545 \
      --http.api eth,net,web3,debug,admin,txpool,trace \
      --http.corsdomain '*' \
      --authrpc.addr 0.0.0.0 --authrpc.port 8551 \
      --authrpc.jwtsecret /config/jwt.hex \
      --nat "extip:$MY_IP" \
      --bootnodes "$ENODES" \
      --full
    ;;

  ethrex)
    docker run -d --name "$CONTAINER_EL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      -e RUST_LOG=info \
      -p $EL_RPC:8545 -p $EL_AUTH:8551 -p $EL_P2P:30303/tcp -p $EL_P2P:30303/udp \
      "$EL_IMAGE" \
      --datadir /data \
      --network /config/genesis.json \
      --p2p.port 30303 \
      --http.addr 0.0.0.0 --http.port 8545 \
      --authrpc.addr 0.0.0.0 --authrpc.port 8551 \
      --authrpc.jwtsecret /config/jwt.hex \
      --bootnodes "$ENODES" \
      --syncmode full \
      --log.level info
    ;;

  erigon)
    # Erigon requires init before first run (like geth)
    docker run --rm \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      "$EL_IMAGE" \
      init --datadir /data /config/genesis.json 2>&1 || true

    docker run -d --name "$CONTAINER_EL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      -p $EL_RPC:8545 -p $EL_AUTH:8551 -p $EL_P2P:30303/tcp -p $EL_P2P:30303/udp \
      "$EL_IMAGE" \
      --datadir /data \
      --networkid "$CHAIN_ID" \
      --port 30303 \
      --http --http.addr 0.0.0.0 --http.port 8545 \
      --http.api eth,net,web3,debug,admin,txpool,trace \
      --http.vhosts '*' --http.corsdomain '*' \
      --authrpc.addr 0.0.0.0 --authrpc.port 8551 \
      --authrpc.jwtsecret /config/jwt.hex \
      --nat "extip:$MY_IP" \
      --bootnodes "$ENODES" \
      --prune.mode=archive
    ;;

  nimbus-el)
    docker run -d --name "$CONTAINER_EL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$EL_DATADIR:/data" \
      -p $EL_RPC:8545 -p $EL_AUTH:8551 -p $EL_P2P:30303/tcp -p $EL_P2P:30303/udp \
      "$EL_IMAGE" \
      --data-dir=/data \
      --network=/config/genesis.json \
      --tcp-port=30303 \
      --http-port=8545 --http-address=0.0.0.0 \
      --rpc --rpc-api=admin,eth,debug \
      --ws --ws-api=admin,eth,debug \
      --engine-api --engine-api-port=8551 --engine-api-address=0.0.0.0 \
      --jwt-secret=/config/jwt.hex \
      --nat="extip:$MY_IP" \
      --bootstrap-node="$ENODES"
    ;;

  *)
    echo "Unknown EL client: $EL"
    echo "Supported: geth, besu, nethermind, reth, ethrex, erigon, nimbus-el"
    exit 1
    ;;
esac

echo "EL container: $CONTAINER_EL"
echo "Waiting 10s for EL to initialize..."
sleep 10

# --- Start CL ---
echo "Starting CL ($CL)..."

case "$CL" in
  lighthouse)
    LH_EXTRA_ARGS=()
    if [ -n "$CHECKPOINT_SYNC_URL" ]; then
      # --checkpoint-sync-url and --allow-insecure-genesis-sync are mutually exclusive
      LH_EXTRA_ARGS+=(--checkpoint-sync-url "$CHECKPOINT_SYNC_URL")
    else
      LH_EXTRA_ARGS+=(--allow-insecure-genesis-sync)
    fi
    docker run -d --name "$CONTAINER_CL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$CL_DATADIR:/data" \
      -p $CL_HTTP:5052 -p $CL_P2P:9000/tcp -p $CL_P2P:9000/udp \
      "$CL_IMAGE" \
      lighthouse beacon_node \
      --datadir /data \
      --testnet-dir /config \
      --execution-jwt /config/jwt.hex \
      --execution-endpoint "http://$CONTAINER_EL:8551" \
      --listen-address 0.0.0.0 --port 9000 --discovery-port 9000 \
      --http --http-address 0.0.0.0 --http-port 5052 \
      --enr-address "$MY_IP" --enr-tcp-port 9000 --enr-udp-port 9000 \
      --quic-port 9001 --enr-quic-port 9001 \
      --boot-nodes "$BOOTNODES" \
      --reconstruct-historic-states \
      --disable-upnp \
      "${LH_EXTRA_ARGS[@]}"
    ;;

  lodestar)
    LS_EXTRA_ARGS=()
    if [ -n "$CHECKPOINT_SYNC_URL" ]; then
      LS_EXTRA_ARGS+=(--checkpointSyncUrl "$CHECKPOINT_SYNC_URL")
    fi
    docker run -d --name "$CONTAINER_CL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$CL_DATADIR:/data" \
      -p $CL_HTTP:5052 -p $CL_P2P:9000/tcp -p $CL_P2P:9000/udp \
      "$CL_IMAGE" \
      beacon \
      --dataDir /data \
      --paramsFile /config/config.yaml \
      --genesisStateFile /config/genesis.ssz \
      --jwtSecret /config/jwt.hex \
      --execution.urls "http://$CONTAINER_EL:8551" \
      --listenAddress 0.0.0.0 --port 9000 \
      --rest --rest.address 0.0.0.0 --rest.port 5052 \
      --rest.namespace '*' \
      --enr.ip "$MY_IP" --enr.tcp 9000 --enr.udp 9000 \
      --bootnodes "$BOOTNODES" \
      "${LS_EXTRA_ARGS[@]}"
    ;;

  teku)
    TEKU_EXTRA_ARGS=()
    if [ -n "$CHECKPOINT_SYNC_URL" ]; then
      TEKU_EXTRA_ARGS+=(--checkpoint-sync-url="$CHECKPOINT_SYNC_URL")
    fi
    docker run -d --name "$CONTAINER_CL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$CL_DATADIR:/data" \
      -p $CL_HTTP:5052 -p $CL_P2P:9000/tcp -p $CL_P2P:9000/udp \
      "$CL_IMAGE" \
      --data-path=/data \
      --network=/config/config.yaml \
      --initial-state=/config/genesis.ssz \
      --ee-jwt-secret-file=/config/jwt.hex \
      --ee-endpoint="http://$CONTAINER_EL:8551" \
      --p2p-port=9000 --p2p-advertised-ip="$MY_IP" \
      --rest-api-enabled --rest-api-interface=0.0.0.0 --rest-api-port=5052 \
      --rest-api-host-allowlist='*' \
      --p2p-discovery-bootnodes="$BOOTNODES" \
      --Xee-version=eebyebye \
      "${TEKU_EXTRA_ARGS[@]}"
    ;;

  prysm)
    PRYSM_EXTRA_ARGS=()
    if [ -n "$CHECKPOINT_SYNC_URL" ]; then
      PRYSM_EXTRA_ARGS+=(--checkpoint-sync-url="$CHECKPOINT_SYNC_URL" --genesis-beacon-api-url="$CHECKPOINT_SYNC_URL")
    fi
    docker run -d --name "$CONTAINER_CL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$CL_DATADIR:/data" \
      -p $CL_HTTP:5052 -p $CL_P2P:9000/tcp -p $CL_P2P:9000/udp \
      "$CL_IMAGE" \
      --datadir=/data \
      --chain-config-file=/config/config.yaml \
      --genesis-state=/config/genesis.ssz \
      --jwt-secret=/config/jwt.hex \
      --execution-endpoint="http://$CONTAINER_EL:8551" \
      --p2p-tcp-port=9000 --p2p-udp-port=9000 \
      --p2p-host-ip="$MY_IP" \
      --grpc-gateway-host=0.0.0.0 --grpc-gateway-port=5052 \
      --rpc-host=0.0.0.0 --rpc-port=4000 \
      --bootstrap-node="$BOOTNODES" \
      --accept-terms-of-use \
      --min-sync-peers=1 \
      "${PRYSM_EXTRA_ARGS[@]}"
    ;;

  nimbus)
    NIMBUS_EXTRA_ARGS=()
    if [ -n "$CHECKPOINT_SYNC_URL" ]; then
      NIMBUS_EXTRA_ARGS+=(--external-beacon-api-url="$CHECKPOINT_SYNC_URL")
    fi
    docker run -d --name "$CONTAINER_CL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$CL_DATADIR:/data" \
      -p $CL_HTTP:5052 -p $CL_P2P:9000/tcp -p $CL_P2P:9000/udp \
      "$CL_IMAGE" \
      --data-dir=/data \
      --network=/config \
      --jwt-secret=/config/jwt.hex \
      --web3-url="http://$CONTAINER_EL:8551" \
      --tcp-port=9000 --udp-port=9000 \
      --nat="extip:$MY_IP" \
      --rest --rest-address=0.0.0.0 --rest-port=5052 \
      --bootstrap-node="$BOOTNODES" \
      --insecure-netkey-password=true \
      "${NIMBUS_EXTRA_ARGS[@]}"
    ;;

  grandine)
    GRANDINE_EXTRA_ARGS=()
    if [ -n "$CHECKPOINT_SYNC_URL" ]; then
      GRANDINE_EXTRA_ARGS+=(--checkpoint-sync-url="$CHECKPOINT_SYNC_URL")
    fi
    docker run -d --name "$CONTAINER_CL" \
      --network "$DEVNET" \
      -v "$CONFIG_DIR:/config:ro" \
      -v "$CL_DATADIR:/data" \
      -p $CL_HTTP:5052 -p $CL_P2P:9000/tcp -p $CL_P2P:9000/udp \
      "$CL_IMAGE" \
      --data-dir=/data \
      --configuration-directory=/config \
      --jwt-secret=/config/jwt.hex \
      --eth1-rpc-urls="http://$CONTAINER_EL:8551" \
      --listen-port=9000 \
      --discovery-port=9000 \
      --http-address=0.0.0.0 --http-port=5052 \
      --enr-address="$MY_IP" --enr-tcp-port=9000 --enr-udp-port=9000 \
      --boot-nodes="$BOOTNODES" \
      "${GRANDINE_EXTRA_ARGS[@]}"
    ;;

  *)
    echo "Unknown CL client: $CL"
    echo "Supported: lighthouse, lodestar, teku, prysm, nimbus, grandine"
    exit 1
    ;;
esac

echo "CL container: $CONTAINER_CL"
echo ""
echo "=== $DEVNET: Both containers running ==="
echo "EL RPC:  http://localhost:$EL_RPC"
echo "CL HTTP: http://localhost:$CL_HTTP"
echo ""
echo "Logs:"
echo "  docker logs -f $CONTAINER_EL"
echo "  docker logs -f $CONTAINER_CL"
echo ""
echo "Health:"
echo "  curl -s http://localhost:$CL_HTTP/eth/v1/node/syncing | jq"
echo "  curl -s -X POST http://localhost:$EL_RPC -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"method\":\"eth_blockNumber\",\"params\":[],\"id\":1}' | jq"
echo ""
echo "Stop:"
echo "  docker rm -f $CONTAINER_EL $CONTAINER_CL"
echo ""
echo "Full cleanup:"
echo "  docker rm -f $CONTAINER_EL $CONTAINER_CL && rm -rf $DATA_DIR && docker network rm $DEVNET"
