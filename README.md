# Aptos Contract API (Rewrite)

REST API for submitting and querying Aptos Move contracts. Uses Circle Programmable Wallets for transaction signing.

## Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/v1/execute` | Yes | Submit an entry function transaction (async, 202) |
| `POST` | `/v1/query` | Yes | Call a view function (sync, 200) |
| `GET` | `/v1/transactions/{id}` | Yes | Poll transaction status |
| `GET` | `/v1/health` | No | Health check |

## Quick Start

### 1. Create Circle Wallets

You need a Circle developer account with an API key, entity secret, and wallet set ID.

```bash
cd rewrite
cp .env.example .env
# Fill in CIRCLE_API_KEY, CIRCLE_ENTITY_SECRET, CIRCLE_WALLET_SET_ID
make create-wallets
```

This prints wallet details and a `CIRCLE_WALLETS=` line. Paste it into `.env`.

### 2. Fund Wallets

Fund your wallet with testnet APT at https://aptos.dev/en/network/faucet

### 3. Run the Server

```bash
make run
```

### 4. Test

```bash
# Unit tests (no credentials needed)
make test

# E2e tests (requires server running + funded wallet in .env)
make test-e2e
```

## Configuration

All config is via environment variables (or `.env` file).

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_PORT` | `8080` | HTTP port |
| `API_KEY` | (required) | Auth key for protected endpoints |
| `TESTING_MODE` | `false` | Disable auth (dev only) |
| `APTOS_NODE_URL` | `https://api.testnet.aptoslabs.com/v1` | Aptos node RPC |
| `APTOS_CHAIN_ID` | `2` | 1=mainnet, 2=testnet |
| `CIRCLE_API_KEY` | (required) | Circle API key |
| `CIRCLE_ENTITY_SECRET` | (required) | 32-byte hex entity secret |
| `CIRCLE_WALLETS` | `[]` | JSON array: `[{"wallet_id":"...","address":"...","public_key":"..."}]` |
| `CIRCLE_WALLET_SET_ID` | | Wallet set ID (for wallet creation only) |
| `WEBHOOK_URL` | | Global webhook URL for status notifications |
| `MAX_GAS_AMOUNT` | `100000` | Default max gas per transaction |
| `TXN_EXPIRATION_SECONDS` | `60` | On-chain transaction expiry |
| `POLL_INTERVAL_SECONDS` | `5` | Poller check interval |
| `STORE_TTL_SECONDS` | `180` | In-memory store eviction TTL |

## API Usage

### Execute (submit a transaction)

```bash
curl -X POST http://localhost:8080/v1/execute \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "wallet_id": "circle-wallet-uuid",
    "function_id": "0x1::aptos_account::transfer",
    "type_arguments": [],
    "arguments": ["0xRECIPIENT", "100"]
  }'
```

Response (202):
```json
{
  "transaction_id": "uuid",
  "status": "submitted",
  "txn_hash": "0x..."
}
```

Optional fields: `max_gas_amount` (uint64), `webhook_url` (string).

### Query (call a view function)

```bash
curl -X POST http://localhost:8080/v1/query \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "function_id": "0x1::coin::balance",
    "type_arguments": ["0x1::aptos_coin::AptosCoin"],
    "arguments": ["0xYOUR_ADDRESS"]
  }'
```

Response (200):
```json
{
  "result": ["1000000"]
}
```

### Poll Transaction Status

```bash
curl http://localhost:8080/v1/transactions/TRANSACTION_ID \
  -H "Authorization: Bearer YOUR_API_KEY"
```

Status values: `pending` → `submitted` → `confirmed` | `failed` | `expired`

### Webhooks

Set `webhook_url` per-request or `WEBHOOK_URL` globally. On terminal status the API POSTs:

```json
{
  "transaction_id": "uuid",
  "status": "confirmed",
  "txn_hash": "0x...",
  "sender_address": "0x...",
  "function_id": "0x1::module::function",
  "timestamp": "2026-03-27T..."
}
```

## Architecture

```
cmd/server/main.go          Entry point, wiring, graceful shutdown
internal/config/             Env-based configuration
internal/aptos/              ABI cache, BCS serialization, Aptos client
internal/circle/             Circle API client, fee-payer signing (sign/transaction)
internal/store/              Store interface + in-memory TTL implementation
internal/handler/            HTTP handlers (execute, query, transaction)
internal/poller/             Background transaction status poller
internal/webhook/            Async webhook delivery with retries
```

### Signing Flow

Per the [Circle Aptos Signing APIs Tutorial](https://developers.circle.com):

1. Build orderless transaction with replay-protection nonce
2. Wrap as fee-payer `RawTransactionWithData` (sender = fee-payer = same wallet)
3. BCS-serialize → hex, send to Circle `sign/transaction`
4. Combine partial signature into `FeePayerTransactionAuthenticator`
5. Submit to Aptos

### Make Targets

```
make build           Build server binary to bin/server
make run             Run the server
make test            Run unit tests
make test-e2e        Run e2e tests (server must be running)
make test-all        Run unit + e2e tests
make vet             Run go vet
make check           Vet + unit tests
make create-wallets  Create Circle wallets on Aptos testnet
make clean           Remove build artifacts
```
