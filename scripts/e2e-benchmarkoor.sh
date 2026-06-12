#!/usr/bin/env bash
# End-to-end test of the benchmarkoor integration:
#   panda CLI -> panda server -> panda proxy (API-key injection) -> benchmarkoor API
#
# Runs a real benchmarkoor API server (pinned image, the same commit the
# vendored contract-test spec was copied from) over hand-crafted fixture
# results, fronted by the panda proxy holding a freshly minted read-only API
# key, and asserts every benchmarkoor.* operation round-trips through the
# candidate-built binaries. Also asserts the vendored OpenAPI spec still
# matches the running instance, covering the routes the offline contract test
# can't (live_runs, query/suites, max_runs_per_client).
#
# Requirements: docker, curl, jq, go. Builds panda binaries if missing.
#
# Usage: ./scripts/e2e-benchmarkoor.sh
set -euo pipefail

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Pinned to the commit pkg/server/testdata/benchmarkoor_openapi.yaml was
# vendored from (ethpandaops/benchmarkoor@9b8a5d8c).
BENCHMARKOOR_IMAGE="${BENCHMARKOOR_IMAGE:-ghcr.io/ethpandaops/benchmarkoor:master-9b8a5d8}"
SANDBOX_IMAGE="${SANDBOX_IMAGE:-ethpandaops/panda:sandbox-latest}"

BENCH_PORT="${BENCH_PORT:-19090}"
PROXY_PORT="${PROXY_PORT:-18091}"
SERVER_PORT="${SERVER_PORT:-2491}"
BENCH_CONTAINER="panda-e2e-benchmarkoor"

WORKDIR="$(mktemp -d)"
SERVER_PID=""
PROXY_PID=""

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  [ -n "$PROXY_PID" ] && kill "$PROXY_PID" 2>/dev/null || true
  docker rm -f "$BENCH_CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo; echo "=== $* ==="; }

step "Write fixture results (2 runs, 1 suite)"
FIXTURES="$WORKDIR/fixtures"
mkdir -p "$FIXTURES/runs/run-geth-001" "$FIXTURES/runs/run-reth-001" "$FIXTURES/suites/ab12cd34ef56ab12"

cat > "$FIXTURES/runs/run-geth-001/config.json" << 'EOF'
{
  "timestamp": 1765000000,
  "timestamp_end": 1765000600,
  "suite_hash": "ab12cd34ef56ab12",
  "status": "completed",
  "instance": {
    "id": "geth-instance-1",
    "client": "geth",
    "image": "ethereum/client-go:latest"
  },
  "test_counts": { "total": 2, "passed": 2, "failed": 0 },
  "metadata": { "labels": { "run": "e2e" } }
}
EOF

cat > "$FIXTURES/runs/run-geth-001/result.json" << 'EOF'
{
  "tests": {
    "test_erc20_transfer": {
      "dir": "test_erc20_transfer",
      "steps": {
        "setup": {
          "aggregated": {
            "time_total": 120000000, "gas_used_total": 0, "gas_used_time_total": 0,
            "success": 1, "fail": 0, "msg_count": 1,
            "method_stats": { "times": {}, "mgas_s": {} }
          }
        },
        "test": {
          "aggregated": {
            "time_total": 85000000, "gas_used_total": 15000000, "gas_used_time_total": 85000000,
            "success": 1, "fail": 0, "msg_count": 1,
            "method_stats": { "times": {}, "mgas_s": {} }
          }
        }
      }
    },
    "test_uniswap_swap": {
      "dir": "test_uniswap_swap",
      "steps": {
        "setup": {
          "aggregated": {
            "time_total": 110000000, "gas_used_total": 0, "gas_used_time_total": 0,
            "success": 1, "fail": 0, "msg_count": 1,
            "method_stats": { "times": {}, "mgas_s": {} }
          }
        },
        "test": {
          "aggregated": {
            "time_total": 92000000, "gas_used_total": 18000000, "gas_used_time_total": 92000000,
            "success": 1, "fail": 0, "msg_count": 1,
            "method_stats": { "times": {}, "mgas_s": {} }
          }
        }
      }
    }
  }
}
EOF

cat > "$FIXTURES/runs/run-geth-001/result.block-logs.json" << 'EOF'
{
  "test_erc20_transfer": {
    "level": "info",
    "msg": "block executed",
    "block": { "number": 20000001, "hash": "0xabc123", "gas_used": 15000000, "tx_count": 42 },
    "timing": { "execution_ms": 61.2, "state_read_ms": 12.4, "state_hash_ms": 5.8, "commit_ms": 3.1, "total_ms": 82.5 },
    "throughput": { "mgas_per_sec": 181.8 },
    "state_reads": { "accounts": 150, "storage_slots": 320, "code": 42, "code_bytes": 84000 },
    "state_writes": { "accounts": 45, "accounts_deleted": 0, "storage_slots": 98, "storage_slots_deleted": 2, "code": 1, "code_bytes": 2400 },
    "cache": {
      "account": { "hits": 120, "misses": 30, "hit_rate": 0.8 },
      "storage": { "hits": 250, "misses": 70, "hit_rate": 0.781 },
      "code": { "hits": 38, "misses": 4, "hit_rate": 0.905, "hit_bytes": 76000, "miss_bytes": 8000 }
    }
  }
}
EOF

cat > "$FIXTURES/runs/run-reth-001/config.json" << 'EOF'
{
  "timestamp": 1765000060,
  "timestamp_end": 1765000700,
  "suite_hash": "ab12cd34ef56ab12",
  "status": "completed",
  "instance": {
    "id": "reth-instance-1",
    "client": "reth",
    "image": "ghcr.io/paradigmxyz/reth:latest"
  },
  "test_counts": { "total": 2, "passed": 1, "failed": 1 },
  "metadata": { "labels": { "run": "e2e" } }
}
EOF

cat > "$FIXTURES/runs/run-reth-001/result.json" << 'EOF'
{
  "tests": {
    "test_erc20_transfer": {
      "dir": "test_erc20_transfer",
      "steps": {
        "test": {
          "aggregated": {
            "time_total": 78000000, "gas_used_total": 15000000, "gas_used_time_total": 78000000,
            "success": 1, "fail": 0, "msg_count": 1,
            "method_stats": { "times": {}, "mgas_s": {} }
          }
        }
      }
    },
    "test_uniswap_swap": {
      "dir": "test_uniswap_swap",
      "steps": {
        "test": {
          "aggregated": {
            "time_total": 0, "gas_used_total": 0, "gas_used_time_total": 0,
            "success": 0, "fail": 1, "msg_count": 1,
            "method_stats": { "times": {}, "mgas_s": {} }
          }
        }
      }
    }
  }
}
EOF

cat > "$FIXTURES/suites/ab12cd34ef56ab12/summary.json" << 'EOF'
{
  "hash": "ab12cd34ef56ab12",
  "metadata": { "labels": { "name": "E2E Benchmark Suite" } },
  "tests": [ { "name": "test_erc20_transfer" }, { "name": "test_uniswap_swap" } ]
}
EOF

chmod -R a+rX "$FIXTURES"

step "Start benchmarkoor API ($BENCHMARKOOR_IMAGE)"
mkdir -p "$WORKDIR/bench-data"
chmod a+rwx "$WORKDIR/bench-data"

cat > "$WORKDIR/benchmarkoor-config.yaml" << 'EOF'
api:
  server:
    listen: ":9090"
    cors_origins: ["*"]
  auth:
    session_ttl: 24h
    anonymous_read: false
    basic:
      enabled: true
      users:
        - username: admin
          password: changeme
          role: admin
  database:
    driver: sqlite
    sqlite:
      path: /data/benchmarkoor.db
  storage:
    local:
      enabled: true
      discovery_paths:
        results: /fixtures
  indexing:
    enabled: true
    interval: 5s
    database:
      driver: sqlite
      sqlite:
        path: /data/benchmarkoor.db
EOF

docker rm -f "$BENCH_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$BENCH_CONTAINER" \
  -p "127.0.0.1:$BENCH_PORT:9090" \
  -v "$WORKDIR/benchmarkoor-config.yaml:/config.yaml:ro" \
  -v "$FIXTURES:/fixtures:ro" \
  -v "$WORKDIR/bench-data:/data" \
  "$BENCHMARKOOR_IMAGE" api --config /config.yaml >/dev/null

BENCH_URL="http://127.0.0.1:$BENCH_PORT"
for i in $(seq 1 30); do
  curl -sf "$BENCH_URL/api/v1/health" >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { docker logs "$BENCH_CONTAINER" | tail -30; fail "benchmarkoor API not healthy"; }
  sleep 2
done
echo "benchmarkoor healthy"

step "Mint a read-only API key"
curl -sf -c "$WORKDIR/cookies" -X POST "$BENCH_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"changeme"}' >/dev/null
API_KEY="$(curl -sf -b "$WORKDIR/cookies" -X POST "$BENCH_URL/api/v1/auth/api-keys" \
  -H 'Content-Type: application/json' -d '{"name":"panda-e2e"}' | jq -r '.key')"
case "$API_KEY" in bmk_*) echo "minted key ${API_KEY:0:12}...";; *) fail "unexpected API key: $API_KEY";; esac

step "Assert upstream auth boundary"
ANON_STATUS="$(curl -s -o /dev/null -w '%{http_code}' "$BENCH_URL/api/v1/index/query/runs?limit=1")"
[ "$ANON_STATUS" = "401" ] || fail "anonymous read should be 401, got $ANON_STATUS"
echo "anonymous read rejected (401)"

step "Wait for the indexer to pick up the fixtures"
for i in $(seq 1 30); do
  COUNT="$(curl -sf -H "Authorization: Bearer $API_KEY" "$BENCH_URL/api/v1/index/query/runs" | jq '.data | length')" || COUNT=0
  [ "$COUNT" = "2" ] && break
  [ "$i" = 30 ] && { docker logs "$BENCH_CONTAINER" | tail -30; fail "indexer never indexed 2 runs (got $COUNT)"; }
  sleep 2
done
echo "2 runs indexed"

step "Assert vendored OpenAPI spec matches the running instance"
curl -sf "$BENCH_URL/api/v1/openapi.json" | jq -S . > "$WORKDIR/openapi-live.json"
cat > "$WORKDIR/yaml2json.go" << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}

	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		panic(err)
	}

	out, err := json.Marshal(raw)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(out))
}
EOF
go run "$WORKDIR/yaml2json.go" pkg/server/testdata/benchmarkoor_openapi.yaml | jq -S . > "$WORKDIR/openapi-vendored.json"
diff -u "$WORKDIR/openapi-vendored.json" "$WORKDIR/openapi-live.json" \
  || fail "vendored spec drifted from the running instance — refresh pkg/server/testdata/benchmarkoor_openapi.yaml and re-pin"
echo "vendored spec matches the live /api/v1/openapi.json"

step "Build panda binaries"
[ -x ./panda ] && [ -x ./panda-server ] && [ -x ./panda-proxy ] || make build build-proxy

step "Start panda proxy with the benchmarkoor datasource"
cat > "$WORKDIR/proxy-config.yaml" << EOF
server:
  listen_addr: "127.0.0.1:$PROXY_PORT"

auth:
  mode: none

benchmarkoor:
  - name: production
    description: "E2E benchmarkoor fixture instance"
    url: "$BENCH_URL"
    ui_url: "https://benchmarkoor.example.com"
    api_key: "$API_KEY"
EOF

./panda-proxy --config "$WORKDIR/proxy-config.yaml" > "$WORKDIR/proxy.log" 2>&1 &
PROXY_PID=$!
for i in $(seq 1 30); do
  curl -sf "http://127.0.0.1:$PROXY_PORT/health" >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { tail -30 "$WORKDIR/proxy.log"; fail "proxy not healthy"; }
  sleep 1
done
echo "proxy healthy"

curl -sf "http://127.0.0.1:$PROXY_PORT/datasources" | jq -e '.benchmarkoor_info[0].name == "production"' >/dev/null \
  || fail "proxy discovery does not advertise the benchmarkoor datasource"
echo "proxy discovery advertises benchmarkoor_info"

step "Start panda server"
# Sandbox containers must reach the server: on Linux host networking works; on
# macOS the Docker VM needs host.docker.internal.
if [ "$(uname)" = "Darwin" ]; then
  SANDBOX_NETWORK="bridge"
  SANDBOX_SERVER_URL="http://host.docker.internal:$SERVER_PORT"
else
  SANDBOX_NETWORK="host"
  SANDBOX_SERVER_URL="http://127.0.0.1:$SERVER_PORT"
fi

cat > "$WORKDIR/server-config.yaml" << EOF
server:
  host: "0.0.0.0"
  port: $SERVER_PORT
  base_url: "http://127.0.0.1:$SERVER_PORT"
  sandbox_url: "$SANDBOX_SERVER_URL"

sandbox:
  backend: docker
  image: "$SANDBOX_IMAGE"
  network: "$SANDBOX_NETWORK"

proxy:
  url: "http://127.0.0.1:$PROXY_PORT"
EOF

./panda-server serve --config "$WORKDIR/server-config.yaml" > "$WORKDIR/server.log" 2>&1 &
SERVER_PID=$!
for i in $(seq 1 120); do
  curl -sf "http://127.0.0.1:$SERVER_PORT/health" >/dev/null 2>&1 && break
  [ "$i" = 120 ] && { tail -60 "$WORKDIR/server.log"; fail "server not healthy"; }
  sleep 2
done
echo "server healthy"

cat > "$WORKDIR/client-config.yaml" << EOF
server:
  base_url: "http://127.0.0.1:$SERVER_PORT"
EOF
export PANDA_CONFIG="$WORKDIR/client-config.yaml"

panda() { ./panda "$@"; }

step "CLI: datasources"
panda benchmarkoor datasources | tee "$WORKDIR/out" | grep -q production || fail "datasources missing 'production'"
panda datasources | grep -q production || fail "'panda datasources' missing the benchmarkoor datasource"

step "CLI: runs"
panda benchmarkoor runs | tee "$WORKDIR/out"
grep -q run-geth-001 "$WORKDIR/out" || fail "runs missing run-geth-001"
grep -q run-reth-001 "$WORKDIR/out" || fail "runs missing run-reth-001"

panda benchmarkoor runs --client geth | tee "$WORKDIR/out"
grep -q run-geth-001 "$WORKDIR/out" || fail "client filter dropped the geth run"
grep -q run-reth-001 "$WORKDIR/out" && fail "client filter leaked the reth run" || true

panda benchmarkoor runs --filter tests_failed=gt.0 | tee "$WORKDIR/out"
grep -q run-reth-001 "$WORKDIR/out" || fail "tests_failed filter missing the reth run"
grep -q run-geth-001 "$WORKDIR/out" && fail "tests_failed filter leaked the geth run" || true

step "CLI: run (single)"
panda benchmarkoor run run-geth-001 | tee "$WORKDIR/out"
jq -e '.run_id == "run-geth-001" and .client == "geth" and .tests_passed == 2 and .has_result == true' "$WORKDIR/out" >/dev/null \
  || fail "get_run returned unexpected payload"

step "CLI: suites + suite-stats"
panda benchmarkoor suites | tee "$WORKDIR/out"
grep -q ab12cd34ef56ab12 "$WORKDIR/out" || fail "suites missing the fixture suite"
grep -q "E2E Benchmark Suite" "$WORKDIR/out" || fail "suites missing the suite name"

panda benchmarkoor suite-stats ab12cd34ef56ab12 --max-runs-per-client 5 | tee "$WORKDIR/out"
jq -e '.test_erc20_transfer.durations | length >= 2' "$WORKDIR/out" >/dev/null \
  || fail "suite stats missing per-client durations for test_erc20_transfer"

step "CLI: tests + block-logs"
panda benchmarkoor tests --run run-geth-001 --order test_mgas_s.desc | tee "$WORKDIR/out"
grep -q test_erc20_transfer "$WORKDIR/out" || fail "test stats missing test_erc20_transfer"

panda benchmarkoor block-logs --run run-geth-001 | tee "$WORKDIR/out"
grep -q 20000001 "$WORKDIR/out" || fail "block logs missing block 20000001"

step "CLI: live"
panda benchmarkoor live | tee "$WORKDIR/out"
grep -q "No benchmark runs currently executing." "$WORKDIR/out" || fail "live runs should be empty"

step "CLI: file"
panda benchmarkoor file results/runs/run-geth-001/result.json | tee "$WORKDIR/out" >/dev/null
jq -e '.tests.test_erc20_transfer' "$WORKDIR/out" >/dev/null || fail "file fetch returned unexpected content"

step "CLI: links"
panda benchmarkoor link run-geth-001 | tee "$WORKDIR/out"
grep -qx "https://benchmarkoor.example.com/runs/run-geth-001" "$WORKDIR/out" || fail "run link wrong"
panda benchmarkoor link --suite ab12cd34ef56ab12 | tee "$WORKDIR/out"
grep -qx "https://benchmarkoor.example.com/suites/ab12cd34ef56ab12" "$WORKDIR/out" || fail "suite link wrong"

step "Python lib via the sandbox"
# The python module is baked into the sandbox image at build time; skip this
# leg when the configured image predates it (rebuild with 'make docker-sandbox').
if docker run --rm --entrypoint python3 "$SANDBOX_IMAGE" -c "import ethpandaops.benchmarkoor" >/dev/null 2>&1; then
  ./panda execute << 'PY' | tee "$WORKDIR/out"
from ethpandaops import benchmarkoor

datasources = benchmarkoor.list_datasources()
print("datasources:", [d["name"] for d in datasources])

runs = benchmarkoor.list_runs(order="timestamp.desc")
print("runs:", sorted(r["run_id"] for r in runs))

failing = benchmarkoor.list_runs(filters={"tests_failed": "gt.0"})
print("failing:", [r["run_id"] for r in failing])

run = benchmarkoor.get_run("run-geth-001")
print("geth passed:", run["tests_passed"], "of", run["tests_total"])

stats = benchmarkoor.get_suite_stats("ab12cd34ef56ab12")
print("suite tests:", sorted(stats.keys()))

result = benchmarkoor.get_file("results/runs/run-geth-001/result.json")
print("file tests:", sorted(result["tests"].keys()))

print("link:", benchmarkoor.link_run("run-geth-001"))
PY
  grep -q "datasources: \['production'\]" "$WORKDIR/out" || fail "python list_datasources wrong"
  grep -q "runs: \['run-geth-001', 'run-reth-001'\]" "$WORKDIR/out" || fail "python list_runs wrong"
  grep -q "failing: \['run-reth-001'\]" "$WORKDIR/out" || fail "python filtered list_runs wrong"
  grep -q "geth passed: 2 of 2" "$WORKDIR/out" || fail "python get_run wrong"
  grep -q "suite tests: \['test_erc20_transfer', 'test_uniswap_swap'\]" "$WORKDIR/out" || fail "python suite stats wrong"
  grep -q "file tests: \['test_erc20_transfer', 'test_uniswap_swap'\]" "$WORKDIR/out" || fail "python get_file wrong"
  grep -q "link: https://benchmarkoor.example.com/runs/run-geth-001" "$WORKDIR/out" || fail "python link_run wrong"
  echo "python lib round-tripped through the sandbox"
else
  if [ "${E2E_PYTHON:-0}" = "1" ]; then
    fail "sandbox image $SANDBOX_IMAGE does not contain ethpandaops.benchmarkoor — rebuild with 'make docker-sandbox'"
  fi
  echo "SKIP: sandbox image $SANDBOX_IMAGE predates the benchmarkoor python module"
fi

step "Proxy enforces read-only access"
WRITE_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H 'X-Datasource: production' "http://127.0.0.1:$PROXY_PORT/benchmarkoor/api/v1/auth/api-keys")"
[ "$WRITE_STATUS" = "405" ] || fail "proxy should reject writes with 405, got $WRITE_STATUS"
echo "writes rejected at the proxy (405)"

echo
echo "e2e OK: panda CLI -> server -> proxy -> benchmarkoor round-tripped every operation"
