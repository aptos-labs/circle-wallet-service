# Example: Wrapping an Existing Contract

This example shows how to use the generic Contract API to interact with an
already-deployed Aptos Move contract -- no custom handlers needed.

The API's two generic endpoints work with **any** Move module:

| Endpoint | Use |
|----------|-----|
| `POST /v1/contracts/execute` | Call entry functions (writes, async) |
| `POST /v1/contracts/query` | Call view functions (reads, sync) |

The server fetches the module's ABI from the Aptos node, resolves argument
types automatically, and handles BCS serialization. You just pass plain JSON
values.

## Prerequisites

1. A running instance of the Contract API server (see root README)
2. An already-deployed Move contract on the target network
3. A signer role configured for the account that should sign transactions

## How It Works

```
Your Move Contract (on-chain)
    |
    |  ABI fetched + cached automatically
    |
    v
+---------------------------------+
|  Contract API Server            |
|                                 |
|  POST /v1/contracts/execute     |  <- JSON in, BCS serialization handled
|  POST /v1/contracts/query       |  <- proxied to Aptos /view endpoint
|  GET  /v1/transactions/{id}     |  <- poll for confirmation
+---------------------------------+
    |
    v
  Aptos Node (testnet / mainnet / localnet)
```

No code changes to the API server are needed. The generic endpoints resolve
everything from the on-chain ABI at runtime.

## What This Example Covers

The script `demo.sh` wraps the standard `0x1::coin` module (Aptos Framework)
as a concrete example. It demonstrates:

- **Querying on-chain state** via view functions (balances, supply)
- **Executing entry functions** (transfer)
- **Polling for transaction confirmation** (async submit -> poll loop)
- **Type arguments** for generic functions
- **Adapting to any contract** -- just change the address, module, and function names

## Running

```bash
# Configure (edit these or export before running)
export API_URL=http://localhost:8080
export API_KEY=your-api-key

chmod +x demo.sh
./demo.sh
```

## Adapting to Your Own Contract

Replace the function IDs in `demo.sh` with your contract's functions. For
example, if you deployed a NFT minting contract at `0xABCD`:

```bash
# Query: check if a token exists
curl -s -X POST "$API_URL/v1/contracts/query" \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "function_id": "0xABCD::nft::token_exists",
    "type_arguments": [],
    "arguments": ["0xABCD", "My Collection", "Token #1"]
  }'

# Execute: create a new token
curl -s -X POST "$API_URL/v1/contracts/execute" \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "function_id": "0xABCD::nft::create",
    "type_arguments": [],
    "arguments": ["My Collection", "Token #1", "https://example.com/meta.json"],
    "signer": "owner"
  }'
```

The server handles ABI lookup and BCS encoding for any deployed module.
