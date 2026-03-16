#!/usr/bin/env bash
#
# get-aptos-pubkey.sh — extract an Ed25519 public key from Aptos transaction history.
#
# When a wallet submits a transaction on Aptos, the transaction's authenticator
# contains the raw Ed25519 public key. This script queries recent transactions
# for a given address and prints the public key.
#
# Usage:
#   ./scripts/get-aptos-pubkey.sh 0xYOUR_WALLET_ADDRESS
#   NODE_URL=https://api.mainnet.aptoslabs.com/v1 ./scripts/get-aptos-pubkey.sh 0x...
#
# Requirements: curl, jq

set -euo pipefail

ADDRESS="${1:-}"
NODE_URL="${NODE_URL:-https://api.testnet.aptoslabs.com/v1}"

if [ -z "$ADDRESS" ]; then
  echo "Usage: $0 <aptos-address>" >&2
  echo "  NODE_URL=https://api.testnet.aptoslabs.com/v1 $0 0x..." >&2
  exit 1
fi

echo "Querying $NODE_URL for transactions from $ADDRESS ..." >&2

TRANSACTIONS=$(curl -s \
  "${NODE_URL}/accounts/${ADDRESS}/transactions?limit=25" \
  -H "Accept: application/json")

if [ -z "$TRANSACTIONS" ] || [ "$TRANSACTIONS" = "null" ]; then
  echo "Error: no response from node" >&2
  exit 1
fi

# Check for API error
ERROR=$(echo "$TRANSACTIONS" | jq -r '.message // empty' 2>/dev/null || true)
if [ -n "$ERROR" ]; then
  echo "Error from Aptos API: $ERROR" >&2
  exit 1
fi

# Try to find an Ed25519 public key in a user transaction authenticator.
# Aptos returns public_key in the authenticator for single-key Ed25519 accounts.
PUBKEY=$(echo "$TRANSACTIONS" | jq -r '
  .[] |
  select(.type == "user_transaction") |
  .signature |
  (
    # Single-key Ed25519
    select(.type == "ed25519_signature") | .public_key
  ) // (
    # Multi-agent: look at the sender signature
    select(.sender != null) | .sender |
    select(.type == "ed25519_signature") | .public_key
  )
' 2>/dev/null | head -1)

if [ -z "$PUBKEY" ] || [ "$PUBKEY" = "null" ]; then
  echo "" >&2
  echo "No transactions found for $ADDRESS on this network." >&2
  echo "" >&2
  echo "The wallet must have submitted at least one transaction before" >&2
  echo "its public key appears in the chain. Options:" >&2
  echo "  1. Check the Circle Developer Console — it may display the public key." >&2
  echo "  2. Fund the wallet and have it send any transaction, then re-run this script." >&2
  exit 1
fi

echo "$PUBKEY"
