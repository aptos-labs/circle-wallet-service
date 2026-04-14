#!/usr/bin/env bash
#
# High-throughput curl examples for the Circle Wallet Service.
#
# The server processes transactions FIFO per sender but in parallel across
# senders. Using multiple wallets gives proportional throughput.
#
# Prerequisites:
#   export API_BASE_URL=http://localhost:8080   # optional, defaults to localhost
#   export API_KEY=your-bearer-token
#   export WALLET_ID=your-wallet-id
#   export ADDRESS=0xYourSenderAddress
#   export FEE_PAYER_WALLET_ID=optional-fee-payer-wallet-id
#   export FEE_PAYER_ADDRESS=0xOptionalFeePayerAddress

set -euo pipefail

BASE="${API_BASE_URL:-http://localhost:8080}"
AUTH="Authorization: Bearer ${API_KEY:?API_KEY is required}"
CT="Content-Type: application/json"

# =========================================================================
# 1. Submit a single transaction
# =========================================================================

echo "=== Submit single transaction ==="

curl -s -w "\nHTTP %{http_code}\n" \
  -X POST "$BASE/v1/execute" \
  -H "$AUTH" -H "$CT" \
  -d "{
    \"wallet_id\": \"${WALLET_ID:?}\",
    \"address\": \"${ADDRESS:?}\",
    \"function_id\": \"0x1::aptos_account::transfer\",
    \"arguments\": [\"$ADDRESS\", \"1\"]
  }"

echo ""

# =========================================================================
# 2. Submit with idempotency key (safe to retry)
# =========================================================================

echo "=== Idempotent submission (retry-safe) ==="

IDEMP_KEY="my-unique-key-$(date +%s)"

for attempt in 1 2; do
  echo "  attempt $attempt:"
  curl -s -w "\n  HTTP %{http_code}\n" \
    -X POST "$BASE/v1/execute" \
    -H "$AUTH" -H "$CT" \
    -d "{
      \"wallet_id\": \"$WALLET_ID\",
      \"address\": \"$ADDRESS\",
      \"function_id\": \"0x1::aptos_account::transfer\",
      \"arguments\": [\"$ADDRESS\", \"1\"],
      \"idempotency_key\": \"$IDEMP_KEY\"
    }"
done

echo ""

# =========================================================================
# 3. Submit with fee payer (sponsored transaction)
# =========================================================================

if [[ -n "${FEE_PAYER_WALLET_ID:-}" && -n "${FEE_PAYER_ADDRESS:-}" ]]; then
  echo "=== Fee-payer transaction ==="

  curl -s -w "\nHTTP %{http_code}\n" \
    -X POST "$BASE/v1/execute" \
    -H "$AUTH" -H "$CT" \
    -d "{
      \"wallet_id\": \"$WALLET_ID\",
      \"address\": \"$ADDRESS\",
      \"function_id\": \"0x1::aptos_account::transfer\",
      \"arguments\": [\"$ADDRESS\", \"1\"],
      \"fee_payer\": {
        \"wallet_id\": \"$FEE_PAYER_WALLET_ID\",
        \"address\": \"$FEE_PAYER_ADDRESS\"
      }
    }"

  echo ""
fi

# =========================================================================
# 4. Submit with webhook (fire-and-forget, no polling needed)
# =========================================================================

echo "=== Webhook-based submission ==="

curl -s -w "\nHTTP %{http_code}\n" \
  -X POST "$BASE/v1/execute" \
  -H "$AUTH" -H "$CT" \
  -d "{
    \"wallet_id\": \"$WALLET_ID\",
    \"address\": \"$ADDRESS\",
    \"function_id\": \"0x1::aptos_account::transfer\",
    \"arguments\": [\"$ADDRESS\", \"1\"],
    \"webhook_url\": \"https://your-server.example.com/webhook\"
  }"

echo ""

# =========================================================================
# 5. Poll transaction status
# =========================================================================

echo "=== Poll transaction status ==="

poll_transaction() {
  local tx_id="$1"
  local max_attempts="${2:-30}"
  local delay=1

  for ((i = 1; i <= max_attempts; i++)); do
    result=$(curl -s -H "$AUTH" "$BASE/v1/transactions/$tx_id")
    status=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

    echo "  poll $i: status=$status"

    case "$status" in
      confirmed|failed|expired)
        echo "  final: $result"
        return 0
        ;;
    esac

    sleep "$delay"
    delay=$(( delay < 5 ? delay * 2 : 5 ))
  done

  echo "  timed out after $max_attempts polls"
  return 1
}

# Submit one and poll it
TX_RESPONSE=$(curl -s -X POST "$BASE/v1/execute" \
  -H "$AUTH" -H "$CT" \
  -d "{
    \"wallet_id\": \"$WALLET_ID\",
    \"address\": \"$ADDRESS\",
    \"function_id\": \"0x1::aptos_account::transfer\",
    \"arguments\": [\"$ADDRESS\", \"1\"]
  }")

TX_ID=$(echo "$TX_RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['transaction_id'])" 2>/dev/null || echo "")

if [[ -n "$TX_ID" ]]; then
  echo "  transaction_id: $TX_ID"
  poll_transaction "$TX_ID"
else
  echo "  submit failed: $TX_RESPONSE"
fi

echo ""

# =========================================================================
# 6. Check webhook delivery status
# =========================================================================

if [[ -n "${TX_ID:-}" ]]; then
  echo "=== Webhook delivery status ==="
  curl -s -H "$AUTH" "$BASE/v1/transactions/$TX_ID/webhooks" | python3 -m json.tool 2>/dev/null || true
  echo ""
fi

# =========================================================================
# 7. Query a view function (synchronous, no transaction)
# =========================================================================

echo "=== Query view function ==="

curl -s -w "\nHTTP %{http_code}\n" \
  -X POST "$BASE/v1/query" \
  -H "$AUTH" -H "$CT" \
  -d "{
    \"function_id\": \"0x1::coin::balance\",
    \"type_arguments\": [\"0x1::aptos_coin::AptosCoin\"],
    \"arguments\": [\"$ADDRESS\"]
  }"

echo ""

# =========================================================================
# 8. Batch submission across multiple wallets (high throughput)
# =========================================================================

echo "=== Batch submission (parallel across wallets) ==="
echo "The server processes senders in parallel. N wallets ≈ N× throughput."
echo ""

# Define wallets as newline-separated JSON objects:
#   WALLET_LIST='
#   {"wallet_id":"w1","address":"0xaaa"}
#   {"wallet_id":"w2","address":"0xbbb"}
#   {"wallet_id":"w3","address":"0xccc"}
#   '

if [[ -n "${WALLET_LIST:-}" ]]; then
  TOTAL=20
  PARALLEL_JOBS=10

  submit_one() {
    local idx="$1"
    local line
    # Round-robin wallet selection
    local wallet_count
    wallet_count=$(echo "$WALLET_LIST" | grep -c .)
    local wallet_idx=$(( idx % wallet_count ))
    line=$(echo "$WALLET_LIST" | sed -n "$((wallet_idx + 1))p")

    local wid addr
    wid=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['wallet_id'])")
    addr=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin)['address'])")

    curl -s -X POST "$BASE/v1/execute" \
      -H "$AUTH" -H "$CT" \
      -d "{
        \"wallet_id\": \"$wid\",
        \"address\": \"$addr\",
        \"function_id\": \"0x1::aptos_account::transfer\",
        \"arguments\": [\"$addr\", \"1\"],
        \"idempotency_key\": \"batch-$idx-$(date +%s%N)\"
      }"
  }
  export -f submit_one
  export BASE AUTH CT WALLET_LIST

  START_TIME=$(date +%s)

  seq 0 $((TOTAL - 1)) | xargs -P "$PARALLEL_JOBS" -I{} bash -c 'submit_one {}'

  END_TIME=$(date +%s)
  ELAPSED=$((END_TIME - START_TIME))
  echo ""
  echo "  submitted $TOTAL transactions in ${ELAPSED}s with $PARALLEL_JOBS parallel jobs"
else
  echo "  (skipped — set WALLET_LIST to enable multi-wallet batch)"
  echo "  Example:"
  echo "    export WALLET_LIST='"
  echo '    {"wallet_id":"w1","address":"0xaaa"}'
  echo '    {"wallet_id":"w2","address":"0xbbb"}'
  echo "    '"
fi

echo ""

# =========================================================================
# Tips for maximum throughput
# =========================================================================

cat <<'TIPS'
=========================================
THROUGHPUT TIPS

1. Multi-wallet parallelism
   The server processes senders in parallel. Using N wallets
   gives ~N× throughput. Round-robin requests across wallets.

2. Fire-and-forget with webhooks
   Pass "webhook_url" in the execute request to skip polling.
   The server POSTs the final status to your endpoint.

3. Idempotency keys
   Always include idempotency_key in production. Safe to retry
   on network errors — the server deduplicates by key.

4. Backpressure (429)
   If you get HTTP 429, honor the Retry-After header.

5. Server tuning
   - submitter.poll_interval_ms: lower = faster pickup
   - signing_pipeline_depth: higher = more ahead-of-time signing

6. Max gas
   Set max_gas_amount to cap per-txn gas costs.
=========================================
TIPS
