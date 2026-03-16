#!/usr/bin/env bash
#
# Smoke test for the Contract API.
#
# Exercises key endpoints against an already-running server using curl + jq.
# Does NOT deploy any contract or start the server — run those first.
#
# Usage:
#   ./scripts/smoke-test.sh                                   # defaults: localhost:8080, no API key
#   BASE_URL=http://localhost:9090 API_KEY=secret ./scripts/smoke-test.sh
#
# Requirements: curl, jq

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-}"

PASS=0
FAIL=0

# --- helpers ---

auth_header() {
  if [ -n "$API_KEY" ]; then
    echo "Authorization: $API_KEY"
  else
    echo "X-Smoke-Test: true"
  fi
}

check() {
  local name="$1"
  local expected_status="$2"
  shift 2

  local http_code body
  body=$(curl -s -w "\n%{http_code}" -H "$(auth_header)" "$@") || true
  http_code=$(echo "$body" | tail -1)
  body=$(echo "$body" | sed '$d')

  if [ "$http_code" = "$expected_status" ]; then
    printf "  \033[32mPASS\033[0m  %s (HTTP %s)\n" "$name" "$http_code"
    PASS=$((PASS + 1))
  else
    printf "  \033[31mFAIL\033[0m  %s — expected HTTP %s, got %s\n" "$name" "$expected_status" "$http_code"
    if [ -n "$body" ]; then
      printf "        %s\n" "$body"
    fi
    FAIL=$((FAIL + 1))
  fi
}

check_json_field() {
  local name="$1"
  local field="$2"
  local expected="$3"
  shift 3

  local body
  body=$(curl -s -H "$(auth_header)" "$@") || true
  local actual
  actual=$(echo "$body" | jq -r ".$field // empty" 2>/dev/null) || true

  if [ "$actual" = "$expected" ]; then
    printf "  \033[32mPASS\033[0m  %s (.%s = %s)\n" "$name" "$field" "$expected"
    PASS=$((PASS + 1))
  else
    printf "  \033[31mFAIL\033[0m  %s — expected .%s = %s, got %s\n" "$name" "$field" "$expected" "$actual"
    FAIL=$((FAIL + 1))
  fi
}

post_json() {
  local path="$1"
  local data="$2"
  curl -s -H "$(auth_header)" -H "Content-Type: application/json" -d "$data" "${BASE_URL}${path}"
}

poll_transaction() {
  local txn_id="$1"
  local timeout="${2:-60}"
  local deadline=$((SECONDS + timeout))

  while [ $SECONDS -lt $deadline ]; do
    local status
    status=$(curl -s -H "$(auth_header)" "${BASE_URL}/v1/transactions/${txn_id}" | jq -r '.status // empty' 2>/dev/null) || true
    case "$status" in
      confirmed|failed|permanently_failed)
        echo "$status"
        return 0
        ;;
    esac
    sleep 2
  done
  echo "timeout"
  return 1
}

# --- checks ---

echo ""
printf "Smoke testing %s\n" "$BASE_URL"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

echo ""
echo "Health & Docs"
check "GET /v1/health" "200" "${BASE_URL}/v1/health"
check "GET /v1/openapi.yaml" "200" "${BASE_URL}/v1/openapi.yaml"
check "GET /v1/docs" "200" "${BASE_URL}/v1/docs"
check_json_field "Health status field" "status" "ok" "${BASE_URL}/v1/health"

echo ""
echo "Validation (expected 400s)"
check "POST /v1/contracts/execute empty body" "400" -X POST -H "Content-Type: application/json" -d '{}' "${BASE_URL}/v1/contracts/execute"
check "POST /v1/contracts/execute missing signer" "400" -X POST -H "Content-Type: application/json" \
  -d '{"function_id":"0x1::mod::func","arguments":[]}' "${BASE_URL}/v1/contracts/execute"
check "POST /v1/contracts/query missing function_id" "400" -X POST -H "Content-Type: application/json" \
  -d '{"arguments":[]}' "${BASE_URL}/v1/contracts/query"

echo ""
echo "Transaction Tracking"
check "GET /v1/transactions/nonexistent" "404" "${BASE_URL}/v1/transactions/nonexistent-id"

# --- summary ---

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
