#!/usr/bin/env bash
#
# Full-stack localnet integration test using the generic contract API.
#
# Starts an Aptos localnet, generates funded accounts, starts the API server,
# and tests execute/query against standard Aptos framework functions
# (APT transfers via primary_fungible_store).
#
# No custom contract deployment needed — tests use 0x1 framework functions.
#
# Usage:
#   ./scripts/localnet-test.sh
#   make localnet-test
#
# Requirements: aptos CLI, curl, jq, go

set -euo pipefail

# --- Configuration ---

NODE_URL="http://127.0.0.1:8080/v1"
FAUCET_URL="http://127.0.0.1:8081"
SERVER_PORT=9090
SERVER_URL="http://127.0.0.1:${SERVER_PORT}"
APT_ASSET="0xA"  # APT fungible asset metadata address

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TMPDIR=$(mktemp -d "${TMPDIR:-/tmp}/localnet-test.XXXXXX")
AHOME="$TMPDIR/aptos-home"   # Isolated HOME for aptos CLI

LOCALNET_PID=""
SERVER_PID=""
PASS=0
FAIL=0

# --- Cleanup (runs on EXIT) ---

cleanup() {
  echo ""
  echo "=== Cleanup ==="
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    echo "  Stopped server (PID $SERVER_PID)"
  fi
  if [ -n "$LOCALNET_PID" ]; then
    kill "$LOCALNET_PID" 2>/dev/null || true
    sleep 1
    kill -9 "$LOCALNET_PID" 2>/dev/null || true
    echo "  Stopped localnet (PID $LOCALNET_PID)"
  fi
  rm -rf "$TMPDIR"
  echo "  Removed $TMPDIR"
}
trap cleanup EXIT

# --- Helpers ---

log()  { printf "\n\033[1;34m=== %s ===\033[0m\n" "$1"; }
pass() { printf "  \033[32mPASS\033[0m  %s\n" "$1"; PASS=$((PASS + 1)); }
fail() { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; FAIL=$((FAIL + 1)); }

# Run aptos CLI with isolated HOME so config/data go to temp dir.
aptos_cmd() {
  HOME="$AHOME" aptos "$@"
}

# Extract account address for a given profile from aptos config.
get_address() {
  aptos_cmd config show-profiles --profile "$1" 2>/dev/null \
    | jq -r ".Result.\"$1\".account"
}

# Poll a URL until it responds 200, with timeout.
wait_for_url() {
  local url="$1" timeout="${2:-30}" label="${3:-service}"
  local deadline=$((SECONDS + timeout))
  while [ $SECONDS -lt $deadline ]; do
    if curl -sf "$url" >/dev/null 2>&1; then
      echo "  $label ready"
      return 0
    fi
    sleep 1
  done
  echo "  ERROR: timeout waiting for $label at $url"
  return 1
}

# Query APT balance for an address via the generic query endpoint.
get_apt_balance() {
  local result
  result=$(curl -sf -X POST -H "Content-Type: application/json" \
    -d "{\"function_id\":\"0x1::primary_fungible_store::balance\",\"type_arguments\":[],\"arguments\":[\"$1\",\"${APT_ASSET}\"]}" \
    "$SERVER_URL/v1/contracts/query")
  echo "$result" | jq -r '.result[0] // empty'
}

# Submit an execute transaction and return the transaction ID.
execute_txn() {
  local function_id="$1"
  local signer_role="$2"
  local args_json="$3"
  local type_args="${4:-[]}"
  curl -sf -X POST -H "Content-Type: application/json" \
    -d "{\"function_id\":\"${function_id}\",\"type_arguments\":${type_args},\"arguments\":${args_json},\"signer\":\"${signer_role}\"}" \
    "$SERVER_URL/v1/contracts/execute" | jq -r '.transaction_id'
}

# Poll a transaction until terminal status.
poll_txn() {
  local txn_id="$1" timeout="${2:-60}"
  local deadline=$((SECONDS + timeout))
  while [ $SECONDS -lt $deadline ]; do
    local status
    status=$(curl -sf "$SERVER_URL/v1/transactions/$txn_id" | jq -r '.status // empty') || true
    case "$status" in
      confirmed|failed|permanently_failed) echo "$status"; return 0 ;;
    esac
    sleep 1
  done
  echo "timeout"
  return 1
}

# --- 1. Pre-flight checks ---

log "Pre-flight checks"
for cmd in aptos curl jq go; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "  ERROR: $cmd not found on PATH"
    exit 1
  fi
  echo "  $cmd: $(command -v "$cmd")"
done

# --- 2. Build server ---

log "Building server"
mkdir -p "$TMPDIR/bin"
(cd "$PROJECT_DIR" && go build -o "$TMPDIR/bin/server" ./cmd/server)
echo "  Built $TMPDIR/bin/server"

# --- 3. Start localnet ---

log "Starting Aptos localnet"
mkdir -p "$AHOME"
HOME="$AHOME" aptos node run-localnet --force-restart --assume-yes \
  &>"$TMPDIR/localnet.log" &
LOCALNET_PID=$!
echo "  PID: $LOCALNET_PID (logs: $TMPDIR/localnet.log)"
wait_for_url "$NODE_URL" 60 "Localnet node"

# Verify localnet is still running.
if ! kill -0 "$LOCALNET_PID" 2>/dev/null; then
  echo "  ERROR: Localnet process died. Last 20 lines of log:"
  tail -20 "$TMPDIR/localnet.log"
  exit 1
fi

# --- 4. Generate keys and fund accounts ---

log "Generating keys & funding accounts"
ROLES="sender receiver"
for role in $ROLES; do
  aptos_cmd key generate --key-type ed25519 --output-file "$TMPDIR/${role}" \
    </dev/null 2>/dev/null

  aptos_cmd init \
    --profile "$role" \
    --private-key-file "$TMPDIR/${role}.key" \
    --rest-url "$NODE_URL" \
    --network custom \
    --skip-faucet \
    --assume-yes \
    </dev/null 2>/dev/null

  aptos_cmd account fund-with-faucet \
    --profile "$role" \
    --faucet-url "$FAUCET_URL" \
    --url "$NODE_URL" \
    </dev/null 2>/dev/null

  echo "  $role: $(get_address "$role")"
done

SENDER_ADDR=$(get_address sender)
RECEIVER_ADDR=$(get_address receiver)

# --- 5. Start API server ---

log "Starting API server on port $SERVER_PORT"
SENDER_KEY=$(cat "$TMPDIR/sender.key")

APTOS_NODE_URL="$NODE_URL" \
APTOS_CHAIN_ID=4 \
SERVER_PORT="$SERVER_PORT" \
SIGNER_PROVIDER=local \
TESTING_MODE=true \
OWNER_PRIVATE_KEY="$SENDER_KEY" \
MINTER_PRIVATE_KEY="$SENDER_KEY" \
SQLITE_PATH="$TMPDIR/test.db" \
POLL_INTERVAL_SECONDS=1 \
  "$TMPDIR/bin/server" &>"$TMPDIR/server.log" &
SERVER_PID=$!
echo "  PID: $SERVER_PID (logs: $TMPDIR/server.log)"
wait_for_url "$SERVER_URL/v1/health" 15 "API server"

# Verify server is still running.
if ! kill -0 "$SERVER_PID" 2>/dev/null; then
  echo "  ERROR: Server process died. Last 20 lines of log:"
  tail -20 "$TMPDIR/server.log"
  exit 1
fi

# --- 6. Run smoke tests ---

log "Running smoke tests"
BASE_URL="$SERVER_URL" "$SCRIPT_DIR/smoke-test.sh"

# --- 7. On-chain verification ---

log "On-chain verification tests"

# Get initial balances
SENDER_BAL_BEFORE=$(get_apt_balance "$SENDER_ADDR")
RECEIVER_BAL_BEFORE=$(get_apt_balance "$RECEIVER_ADDR")
echo "  Sender balance before:   $SENDER_BAL_BEFORE"
echo "  Receiver balance before: $RECEIVER_BAL_BEFORE"

# Transfer 1000 octas (0.00001 APT) from sender to receiver
TRANSFER_AMOUNT="1000"
echo ""
echo "  Test: Transfer $TRANSFER_AMOUNT octas from sender to receiver"
TXN_ID=$(execute_txn \
  "0x1::primary_fungible_store::transfer" \
  "minter" \
  "[\"${APT_ASSET}\", \"${RECEIVER_ADDR}\", \"${TRANSFER_AMOUNT}\"]")

if [ -n "$TXN_ID" ] && [ "$TXN_ID" != "null" ]; then
  echo "  ...polling $TXN_ID"
  STATUS=$(poll_txn "$TXN_ID" 60)
  if [ "$STATUS" = "confirmed" ]; then
    pass "Transfer transaction confirmed"

    RECEIVER_BAL_AFTER=$(get_apt_balance "$RECEIVER_ADDR")
    EXPECTED=$((RECEIVER_BAL_BEFORE + TRANSFER_AMOUNT))
    if [ "$RECEIVER_BAL_AFTER" = "$EXPECTED" ]; then
      pass "Receiver balance increased by $TRANSFER_AMOUNT (now $RECEIVER_BAL_AFTER)"
    else
      fail "Receiver balance = $RECEIVER_BAL_AFTER, expected $EXPECTED"
    fi
  else
    fail "Transfer transaction: expected confirmed, got $STATUS"
  fi
else
  fail "Transfer did not return transaction_id"
fi

# Transfer back: another 500 octas from sender to receiver
TRANSFER_AMOUNT_2="500"
echo ""
echo "  Test: Transfer $TRANSFER_AMOUNT_2 more octas"
TXN_ID=$(execute_txn \
  "0x1::primary_fungible_store::transfer" \
  "minter" \
  "[\"${APT_ASSET}\", \"${RECEIVER_ADDR}\", \"${TRANSFER_AMOUNT_2}\"]")

if [ -n "$TXN_ID" ] && [ "$TXN_ID" != "null" ]; then
  echo "  ...polling $TXN_ID"
  STATUS=$(poll_txn "$TXN_ID" 60)
  if [ "$STATUS" = "confirmed" ]; then
    pass "Second transfer confirmed"

    RECEIVER_BAL_FINAL=$(get_apt_balance "$RECEIVER_ADDR")
    EXPECTED=$((RECEIVER_BAL_BEFORE + TRANSFER_AMOUNT + TRANSFER_AMOUNT_2))
    if [ "$RECEIVER_BAL_FINAL" = "$EXPECTED" ]; then
      pass "Receiver balance = $RECEIVER_BAL_FINAL (correct after both transfers)"
    else
      fail "Receiver balance = $RECEIVER_BAL_FINAL, expected $EXPECTED"
    fi
  else
    fail "Second transfer: expected confirmed, got $STATUS"
  fi
else
  fail "Second transfer did not return transaction_id"
fi

# --- Summary ---

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf "On-chain results: \033[32m%d passed\033[0m" "$PASS"
if [ "$FAIL" -gt 0 ]; then
  printf ", \033[31m%d failed\033[0m" "$FAIL"
fi
echo ""

if [ "$FAIL" -gt 0 ]; then
  echo ""
  echo "Server logs: $TMPDIR/server.log"
  echo "Localnet logs: $TMPDIR/localnet.log"
  exit 1
fi

echo ""
echo "All localnet tests passed!"
