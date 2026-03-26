#!/usr/bin/env bash
#
# demo.sh — Wrap an existing Aptos Move contract using the generic Contract API.
#
# This script uses the standard 0x1::coin module (Aptos Framework) as a
# concrete example. Replace the function IDs with your own contract's
# functions to wrap any deployed module.
#
# Usage:
#   export API_URL=http://localhost:8080
#   export API_KEY=your-api-key
#   ./demo.sh

set -euo pipefail

API_URL="${API_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"

# An address to query — replace with a real funded address on your network.
SAMPLE_ADDRESS="${SAMPLE_ADDRESS:-0x1}"

# Aptos coin type argument (used for generic coin functions).
COIN_TYPE="0x1::aptos_coin::AptosCoin"

header() {
  echo ""
  echo "============================================================"
  echo "  $1"
  echo "============================================================"
}

# ------------------------------------------------------------------
# Helper: POST to the API and print the response.
# ------------------------------------------------------------------
api_post() {
  local endpoint="$1"
  local body="$2"
  curl -s -X POST "${API_URL}${endpoint}" \
    -H "Authorization: ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "$body" | python3 -m json.tool 2>/dev/null || echo "(raw) $(curl -s -X POST "${API_URL}${endpoint}" -H "Authorization: ${API_KEY}" -H "Content-Type: application/json" -d "$body")"
}

# ------------------------------------------------------------------
# Helper: GET from the API and print the response.
# ------------------------------------------------------------------
api_get() {
  local endpoint="$1"
  curl -s -X GET "${API_URL}${endpoint}" \
    -H "Authorization: ${API_KEY}" | python3 -m json.tool 2>/dev/null || echo "(raw) $(curl -s -X GET "${API_URL}${endpoint}" -H "Authorization: ${API_KEY}")"
}

# ------------------------------------------------------------------
# Helper: Submit a transaction and poll until it reaches a terminal state.
# ------------------------------------------------------------------
submit_and_poll() {
  local endpoint="$1"
  local body="$2"
  local timeout="${3:-60}"

  echo "-> Submitting transaction..."
  local response
  response=$(curl -s -X POST "${API_URL}${endpoint}" \
    -H "Authorization: ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d "$body")

  echo "$response" | python3 -m json.tool 2>/dev/null || echo "$response"

  local txn_id
  txn_id=$(echo "$response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('transaction_id',''))" 2>/dev/null)

  if [ -z "$txn_id" ]; then
    echo "!! No transaction_id in response, skipping poll."
    return 1
  fi

  echo ""
  echo "-> Polling transaction ${txn_id} (timeout: ${timeout}s)..."

  local elapsed=0
  while [ "$elapsed" -lt "$timeout" ]; do
    local status_response
    status_response=$(curl -s -X GET "${API_URL}/v1/transactions/${txn_id}" \
      -H "Authorization: ${API_KEY}")

    local status
    status=$(echo "$status_response" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null)

    case "$status" in
      confirmed)
        echo "-> Confirmed!"
        echo "$status_response" | python3 -m json.tool 2>/dev/null
        return 0
        ;;
      failed|permanently_failed|expired)
        echo "-> Terminal failure: $status"
        echo "$status_response" | python3 -m json.tool 2>/dev/null
        return 1
        ;;
      *)
        echo "   status=$status, waiting..."
        sleep 2
        elapsed=$((elapsed + 2))
        ;;
    esac
  done

  echo "!! Timed out waiting for transaction $txn_id"
  return 1
}


# ==================================================================
# 1. Health Check
# ==================================================================
header "1. Health Check"
api_get "/v1/health"


# ==================================================================
# 2. Query: Read on-chain state via view functions
# ==================================================================
header "2. Query — Check if an account exists (0x1::account::exists_at)"

echo "Calling view function 0x1::account::exists_at..."
api_post "/v1/contracts/query" "$(cat <<EOF
{
  "function_id": "0x1::account::exists_at",
  "type_arguments": [],
  "arguments": ["${SAMPLE_ADDRESS}"]
}
EOF
)"


# ------------------------------------------------------------------
header "2b. Query — Get coin balance (0x1::coin::balance with type arg)"

echo "Calling view function 0x1::coin::balance<AptosCoin>..."
echo "(This will fail if the address has no CoinStore — that's expected)"
api_post "/v1/contracts/query" "$(cat <<EOF
{
  "function_id": "0x1::coin::balance",
  "type_arguments": ["${COIN_TYPE}"],
  "arguments": ["${SAMPLE_ADDRESS}"]
}
EOF
)"


# ------------------------------------------------------------------
header "2c. Query — Check coin supply (0x1::coin::supply with type arg)"

echo "Calling view function 0x1::coin::supply<AptosCoin>..."
api_post "/v1/contracts/query" "$(cat <<EOF
{
  "function_id": "0x1::coin::supply",
  "type_arguments": ["${COIN_TYPE}"],
  "arguments": []
}
EOF
)"


# ==================================================================
# 3. Execute: Submit an entry function transaction
# ==================================================================
header "3. Execute — Transfer coins (0x1::aptos_account::transfer)"

echo "Submitting an entry function call..."
echo "(This requires a funded signer role configured in the server)"
echo ""

# Replace 'owner' with whichever signer role you have configured,
# and the 'to' address with a real recipient.
RECIPIENT="${RECIPIENT:-0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef}"
AMOUNT="${AMOUNT:-1000}"

submit_and_poll "/v1/contracts/execute" "$(cat <<EOF
{
  "function_id": "0x1::aptos_account::transfer",
  "type_arguments": [],
  "arguments": ["${RECIPIENT}", "${AMOUNT}"],
  "signer": "owner"
}
EOF
)" 90


# ==================================================================
# 4. Execute with type arguments
# ==================================================================
header "4. Execute — Coin transfer with type arg (0x1::coin::transfer)"

echo "This shows how to pass type_arguments for generic entry functions."
echo ""

submit_and_poll "/v1/contracts/execute" "$(cat <<EOF
{
  "function_id": "0x1::coin::transfer",
  "type_arguments": ["${COIN_TYPE}"],
  "arguments": ["${RECIPIENT}", "${AMOUNT}"],
  "signer": "owner"
}
EOF
)" 90


# ==================================================================
# 5. Execute with gas override
# ==================================================================
header "5. Execute — With custom gas limit"

echo "You can override the server's default max_gas_amount per request."
echo ""

submit_and_poll "/v1/contracts/execute" "$(cat <<EOF
{
  "function_id": "0x1::aptos_account::transfer",
  "type_arguments": [],
  "arguments": ["${RECIPIENT}", "${AMOUNT}"],
  "signer": "owner",
  "max_gas_amount": 200000
}
EOF
)" 90


# ==================================================================
# Done
# ==================================================================
header "Done"
echo "All examples completed."
echo ""
echo "To wrap your own contract, change the function_id to point at your"
echo "deployed module (e.g. 0xYOUR_ADDR::your_module::your_function)."
echo "The server resolves argument types from the on-chain ABI automatically."
