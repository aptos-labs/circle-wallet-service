#!/usr/bin/env bash
#
# Test the generic contract API against a live testnet using standard Aptos
# framework functions (APT transfers via primary_fungible_store).
#
# No custom contract needed — tests use 0x1 framework functions.
#
# Usage:
#   SENDER=0x... ./scripts/testnet-test.sh
#   SENDER=0x... BASE_URL=http://localhost:9090 ./scripts/testnet-test.sh
#
# SENDER should be the address of a signer configured in the server (e.g. owner or minter).

set -euo pipefail

SENDER="${SENDER:?Set SENDER to the address of a configured signer role}"
SIGNER_ROLE="${SIGNER_ROLE:-owner}"
BASE_URL="${BASE_URL:-http://localhost:8080}"
APT_ASSET="0xA"  # APT fungible asset metadata address
PASS=0
FAIL=0

# --- helpers ---

pass() { printf "  \033[32mPASS\033[0m  %s\n" "$1"; PASS=$((PASS + 1)); }
fail() { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; FAIL=$((FAIL + 1)); }

get_apt_balance() {
  curl -sf -X POST "${BASE_URL}/v1/contracts/query" \
    -H "Content-Type: application/json" \
    -d "{\"function_id\":\"0x1::primary_fungible_store::balance\",\"type_arguments\":[],\"arguments\":[\"$1\",\"${APT_ASSET}\"]}" \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['result'][0])"
}

execute() {
  local function_id="$1" args="$2" signer="$3"
  curl -sf -X POST "${BASE_URL}/v1/contracts/execute" \
    -H "Content-Type: application/json" \
    -d "{\"function_id\":\"${function_id}\",\"type_arguments\":[],\"arguments\":${args},\"signer\":\"${signer}\"}"
}

poll_txn() {
  local txn_id="$1" timeout="${2:-90}"
  local deadline=$((SECONDS + timeout))
  while [ $SECONDS -lt $deadline ]; do
    local status
    status=$(curl -sf "${BASE_URL}/v1/transactions/${txn_id}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null) || true
    case "$status" in
      confirmed|failed|permanently_failed|expired) echo "$status"; return 0 ;;
    esac
    sleep 2
  done
  echo "timeout"
  return 1
}

extract_json() {
  python3 -c "import sys,json; data=json.load(sys.stdin); $1"
}

# --- Tests ---

printf "\nTesting generic contract API against testnet\n"
printf "Sender: %s (signer role: %s)\n" "$SENDER" "$SIGNER_ROLE"
printf "Server: %s\n" "$BASE_URL"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# 1. Health
echo ""
echo "Health & Docs"
HEALTH=$(curl -sf "${BASE_URL}/v1/health")
if echo "$HEALTH" | python3 -c "import sys,json; assert json.load(sys.stdin)['status']=='ok'" 2>/dev/null; then
  pass "GET /v1/health → ok"
else
  fail "GET /v1/health"
fi

OPENAPI_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/v1/openapi.yaml")
if [ "$OPENAPI_STATUS" = "200" ]; then
  pass "GET /v1/openapi.yaml → 200"
else
  fail "GET /v1/openapi.yaml → $OPENAPI_STATUS"
fi

# 2. Query — APT balance
echo ""
echo "Query Endpoints (view functions)"
BALANCE=$(get_apt_balance "$SENDER")
if [ -n "$BALANCE" ] && [ "$BALANCE" != "0" ]; then
  pass "APT balance($SENDER) = $BALANCE"
else
  fail "APT balance($SENDER) = $BALANCE (expected non-zero, account needs funding)"
fi

# 3. Validation errors
echo ""
echo "Validation (expected errors)"
ERR_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${BASE_URL}/v1/contracts/execute" \
  -H "Content-Type: application/json" -d '{}')
if [ "$ERR_STATUS" = "400" ]; then
  pass "execute({}) → 400"
else
  fail "execute({}) → $ERR_STATUS, expected 400"
fi

ERR_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${BASE_URL}/v1/contracts/query" \
  -H "Content-Type: application/json" -d '{}')
if [ "$ERR_STATUS" = "400" ]; then
  pass "query({}) → 400"
else
  fail "query({}) → $ERR_STATUS, expected 400"
fi

TXN_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/v1/transactions/nonexistent")
if [ "$TXN_STATUS" = "404" ]; then
  pass "GET /v1/transactions/nonexistent → 404"
else
  fail "GET /v1/transactions/nonexistent → $TXN_STATUS, expected 404"
fi

# 4. Execute — APT self-transfer (1000 octas)
echo ""
echo "Execute + Poll (on-chain transactions)"
BALANCE_BEFORE=$(get_apt_balance "$SENDER")
echo "  Balance before: $BALANCE_BEFORE"

echo "  Transferring 1000 octas (APT) to self..."
RESULT=$(execute \
  "0x1::primary_fungible_store::transfer" \
  "[\"${APT_ASSET}\", \"${SENDER}\", \"1000\"]" \
  "$SIGNER_ROLE")
TXN_ID=$(echo "$RESULT" | extract_json "print(data['transaction_id'])")

if [ -n "$TXN_ID" ] && [ "$TXN_ID" != "None" ]; then
  pass "execute(transfer) → transaction_id=$TXN_ID"

  echo "  Polling transaction (up to 90s)..."
  STATUS=$(poll_txn "$TXN_ID" 90)

  if [ "$STATUS" = "confirmed" ]; then
    TXN_DETAIL=$(curl -sf "${BASE_URL}/v1/transactions/${TXN_ID}")
    TXN_HASH=$(echo "$TXN_DETAIL" | extract_json "print(data.get('txn_hash',''))")
    pass "Transfer confirmed on-chain"
    echo ""
    echo "  Transaction: https://explorer.aptoslabs.com/txn/${TXN_HASH}?network=testnet"

    # Self-transfer: balance should decrease by gas only (transfer amount cancels out)
    BALANCE_AFTER=$(get_apt_balance "$SENDER")
    echo "  Balance after: $BALANCE_AFTER (decreased by gas fees)"
    if [ "$BALANCE_AFTER" -lt "$BALANCE_BEFORE" ]; then
      pass "Balance decreased by gas (self-transfer net zero, gas consumed)"
    else
      fail "Balance did not decrease — expected gas to be consumed"
    fi
  elif [ "$STATUS" = "failed" ] || [ "$STATUS" = "permanently_failed" ]; then
    TXN_DETAIL=$(curl -sf "${BASE_URL}/v1/transactions/${TXN_ID}")
    ERROR=$(echo "$TXN_DETAIL" | extract_json "print(data.get('error_message','unknown'))")
    fail "Transfer ${STATUS}: $ERROR"
  else
    fail "Transfer: status=$STATUS (timeout or unexpected)"
  fi
else
  fail "execute(transfer) did not return transaction_id: $RESULT"
fi

# --- Summary ---

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
printf "Results: \033[32m%d passed\033[0m" "$PASS"
if [ "$FAIL" -gt 0 ]; then
  printf ", \033[31m%d failed\033[0m" "$FAIL"
fi
echo ""

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
echo ""
echo "All testnet tests passed!"
