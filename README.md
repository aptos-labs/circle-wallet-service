# Aptos Contract API

Generic REST API for interacting with Aptos Move contracts. Uses [Circle Programmable Wallets](https://developers.circle.com/w3s/programmable-wallets-an-overview) for transaction signing with fee-payer sponsored transactions.

## Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/v1/execute` | Yes | Submit an entry function transaction (async, returns 202) |
| `POST` | `/v1/query` | Yes | Call a view function (sync, returns 200) |
| `GET` | `/v1/transactions/{id}` | Yes | Poll transaction status |
| `GET` | `/v1/health` | No | Health check |

## Quick Start

### 1. Prerequisites

- Go 1.26+
- A [Circle developer account](https://console.circle.com) with:
  - API key
  - Entity secret (32-byte hex)
  - Wallet set ID

### 2. Create Circle Wallets

```bash
cp .env.example .env
# Fill in CIRCLE_API_KEY, CIRCLE_ENTITY_SECRET, CIRCLE_WALLET_SET_ID
make create-wallets
```

This prints wallet details and a ready-to-paste `CIRCLE_WALLETS=` line for your `.env`.

### 3. Fund Wallets

Fund your wallet with testnet APT at https://aptos.dev/en/network/faucet

### 4. Configure

Fill in the remaining `.env` values (at minimum `API_KEY` and `APTOS_NODE_URL`). See [Configuration](#configuration) for all options.

### 5. Run

```bash
make run
```

### 6. Test

```bash
# Unit tests (no credentials needed)
make test

# E2E tests (requires server running + funded wallet)
make test-e2e

# Full check (fmt + vet + lint + unit tests)
make check
```

## Configuration

All configuration is via environment variables or a `.env` file in the project root.

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_PORT` | `8080` | HTTP listen port |
| `API_KEY` | *(required)* | Bearer token for protected endpoints |
| `TESTING_MODE` | `false` | Set `true` to disable auth (development only) |

### Aptos

| Variable | Default | Description |
|----------|---------|-------------|
| `APTOS_NODE_URL` | *(required)* | Aptos node RPC URL (e.g. `https://api.testnet.aptoslabs.com/v1`) |
| `APTOS_CHAIN_ID` | `0` | Chain ID: `1` = mainnet, `2` = testnet |

### Circle

| Variable | Default | Description |
|----------|---------|-------------|
| `CIRCLE_API_KEY` | *(required)* | Circle API key |
| `CIRCLE_ENTITY_SECRET` | *(required)* | 32-byte hex entity secret |
| `CIRCLE_WALLETS` | `[]` | JSON array of wallet objects (see below) |
| `CIRCLE_WALLET_SET_ID` | | Wallet set ID (only needed for `make create-wallets`) |

**`CIRCLE_WALLETS` format:**

```json
[
  {
    "wallet_id": "uuid",
    "address": "0xAPTOS_ADDRESS",
    "public_key": "0xED25519_PUBLIC_KEY"
  }
]
```

If `public_key` is omitted, it is fetched from Circle at server startup.

### Transaction Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_GAS_AMOUNT` | `100000` | Default max gas per transaction |
| `TXN_EXPIRATION_SECONDS` | `60` | On-chain transaction expiration window |
| `POLL_INTERVAL_SECONDS` | `5` | Background poller check interval |
| `STORE_TTL_SECONDS` | `180` | In-memory store eviction TTL |

### Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBHOOK_URL` | *(empty)* | Global webhook URL for transaction status notifications |

## API Reference

### Authentication

All endpoints except `/v1/health` require authentication. Pass your API key via either:

```
Authorization: Bearer <API_KEY>
```

or:

```
X-API-Key: <API_KEY>
```

### POST /v1/execute

Submit an entry function transaction for async execution.

**Request:**

```json
{
  "wallet_id": "circle-wallet-uuid",
  "function_id": "0x1::aptos_account::transfer",
  "type_arguments": [],
  "arguments": ["0xRECIPIENT_ADDRESS", "100"],
  "max_gas_amount": 50000,
  "webhook_url": "https://example.com/hook"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `wallet_id` | string | Yes | Circle wallet UUID **or** Aptos address |
| `function_id` | string | Yes | `address::module::function` |
| `type_arguments` | string[] | No | Move type arguments |
| `arguments` | any[] | No | Function arguments (types resolved from on-chain ABI) |
| `max_gas_amount` | uint64 | No | Override default max gas |
| `webhook_url` | string | No | Per-request webhook URL |

**Response (202 Accepted):**

```json
{
  "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "submitted",
  "txn_hash": "0xabc123..."
}
```

**Errors:**

| Status | Cause |
|--------|-------|
| 400 | Invalid request, unknown wallet, ABI resolution failure, argument mismatch |
| 401 | Missing or invalid API key |
| 500 | Transaction build, signing, or submission failure |

### POST /v1/query

Call a Move view function and return the result synchronously.

**Request:**

```json
{
  "function_id": "0x1::coin::balance",
  "type_arguments": ["0x1::aptos_coin::AptosCoin"],
  "arguments": ["0xYOUR_ADDRESS"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `function_id` | string | Yes | `address::module::function` |
| `type_arguments` | string[] | No | Move type arguments |
| `arguments` | any[] | No | Function arguments (types resolved from on-chain ABI) |

**Response (200 OK):**

```json
{
  "result": ["1000000000"]
}
```

**Errors:**

| Status | Cause |
|--------|-------|
| 400 | Invalid request, ABI resolution failure, argument mismatch |
| 401 | Missing or invalid API key |
| 502 | Aptos node view call failed |

### GET /v1/transactions/{id}

Poll the status of a previously submitted transaction.

**Response (200 OK):**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "confirmed",
  "txn_hash": "0xabc123...",
  "sender_address": "0x1234...",
  "function_id": "0x1::aptos_account::transfer",
  "wallet_id": "circle-wallet-uuid",
  "created_at": "2026-03-27T18:42:08Z",
  "updated_at": "2026-03-27T18:42:15Z",
  "expires_at": "2026-03-27T18:43:08Z"
}
```

**Transaction status lifecycle:**

```
pending → submitted → confirmed
                    → failed     (VM error, error_message populated)
                    → expired    (on-chain expiration reached)
```

**Errors:**

| Status | Cause |
|--------|-------|
| 400 | Missing transaction ID |
| 401 | Missing or invalid API key |
| 404 | Transaction not found (or evicted from in-memory store) |

### GET /v1/health

```json
{"status": "ok"}
```

No authentication required.

## Webhooks

When a transaction reaches a terminal status (`confirmed`, `failed`, or `expired`), the API sends a POST request to the webhook URL.

**Webhook resolution:** per-request `webhook_url` takes precedence over the global `WEBHOOK_URL`.

**Payload:**

```json
{
  "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "confirmed",
  "txn_hash": "0xabc123...",
  "sender_address": "0x1234...",
  "function_id": "0x1::aptos_account::transfer",
  "timestamp": "2026-03-27T18:42:15Z"
}
```

On failure, `error_message` is included:

```json
{
  "transaction_id": "...",
  "status": "failed",
  "error_message": "EXECUTION_FAILURE: Move abort...",
  "..."
}
```

**Delivery:** async, best-effort, up to 3 retries with backoff (1s, 2s, 3s). No retry on 4xx client errors.

## CLI

A command-line tool for interacting with a running server.

```bash
# Health check
go run ./cmd/cli health

# Query a view function
go run ./cmd/cli query \
  -f "0x1::coin::balance" \
  -t "0x1::aptos_coin::AptosCoin" \
  -a "0xYOUR_ADDRESS"

# Submit a transaction (by wallet UUID or Aptos address)
go run ./cmd/cli execute \
  -w "0xYOUR_WALLET_ADDRESS" \
  -f "0x1::aptos_account::transfer" \
  -a "0xRECIPIENT" -a "100"

# Check transaction status
go run ./cmd/cli status -id "TRANSACTION_UUID"

# Watch until confirmed/failed/expired
go run ./cmd/cli watch -id "TRANSACTION_UUID"
```

Or via make: `make cli ARGS="health"`

The CLI reads `API_KEY` and `API_BASE_URL` (default `http://localhost:8080`) from `.env`.

## Architecture

```
cmd/
  server/main.go            HTTP server, wiring, graceful shutdown
  cli/main.go               CLI for testing against a running server
internal/
  config/                   Env-based configuration with .env support
  aptos/
    abi.go                  ABI cache — fetches and caches module ABIs from Aptos node
    args.go                 BCS serialization — converts JSON arguments to BCS bytes by Move type
    client.go               Aptos SDK wrapper — orderless txn building, fee-payer wrapping, submit, view
  circle/
    client.go               Circle HTTP client — RSA key cache, entity secret encryption, sign/transaction
    signer.go               Fee-payer transaction signing via Circle's sign/transaction endpoint
  handler/
    handler.go              Shared JSON response helpers
    execute.go              POST /v1/execute — ABI resolve, BCS serialize, build, sign, submit
    query.go                POST /v1/query — ABI resolve, BCS serialize, call view
    transaction.go          GET /v1/transactions/{id} — status lookup
  store/
    store.go                Store interface and TransactionRecord type
    memory.go               In-memory implementation with TTL-based eviction
  poller/
    poller.go               Background goroutine — polls submitted txns, updates status, fires webhooks
  webhook/
    notifier.go             Async webhook delivery with retries
examples/
  e2e_test.go               End-to-end tests against a running server
  create_wallets/main.go    Helper to create Circle wallets on Aptos testnet
```

### Signing Flow

Per the [Circle Aptos Signing APIs Tutorial](https://developers.circle.com):

1. Build entry function payload from untyped JSON arguments (ABI-resolved, BCS-serialized)
2. Build fee-payer `RawTransactionWithData` via `BuildTransactionMultiAgent` with `FeePayer` option (sender = fee-payer = same wallet)
3. BCS-serialize the `RawTransactionWithData` to hex
4. Send to Circle `sign/transaction` with encrypted entity secret
5. Get partial signature, build `FeePayerTransactionAuthenticator` (same signature for sender and fee-payer)
6. Submit signed transaction to Aptos

### ABI Resolution

Both `/v1/execute` and `/v1/query` resolve argument types automatically:

1. Parse `function_id` into address, module, function
2. Fetch module ABI from the Aptos node (cached per module for server lifetime)
3. Strip `&signer` parameters
4. Validate argument count matches
5. BCS-serialize each argument according to its Move type

Supported Move types: `address`, `bool`, `u8`, `u16`, `u32`, `u64`, `u128`, `u256`, `0x1::string::String`, `vector<T>`, `0x1::object::Object<T>`.

### In-Memory Store

Transactions are tracked in memory with automatic TTL eviction. The default TTL (`STORE_TTL_SECONDS=180`) is longer than the on-chain expiry (`TXN_EXPIRATION_SECONDS=60`) to allow time for polling and webhook delivery. After eviction, `GET /v1/transactions/{id}` returns 404.

The `Store` interface is defined separately from the implementation, so it can be swapped for a persistent backend (e.g. SQLite, Postgres) without changing any other code.

## Make Targets

Run `make` to see all available targets:

```
make                 Show help and getting-started steps
make build           Build server and CLI binaries to bin/
make run             Run the server (loads .env)
make cli ARGS="..."  Run the CLI
make test            Run unit tests
make test-e2e        Run e2e tests (server must be running)
make test-all        Run unit + e2e tests
make fmt             Format code with gofumpt
make vet             Run go vet
make lint            Run golangci-lint
make check           Format + vet + lint + unit tests
make create-wallets  Create Circle wallets on Aptos testnet
make clean           Remove build artifacts
```
