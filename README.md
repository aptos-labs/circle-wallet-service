# Aptos Contract API

Production-grade REST API for submitting and tracking Aptos Move transactions. Uses [Circle Programmable Wallets](https://developers.circle.com/w3s/programmable-wallets-an-overview) for custodial signing with optional fee-payer (gas station) sponsored transactions.

Wallet IDs and addresses are provided per-request — no wallet configuration on the server. The server manages Aptos sequence numbers, transaction lifecycle, retries, and webhook notifications. It scales horizontally: multiple instances share a MySQL database with row-level locking to prevent duplicate processing.

## Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/v1/execute` | Yes | Enqueue an entry function transaction (async, returns 202) |
| `POST` | `/v1/query` | Yes | Call a view function (sync, returns 200) |
| `GET` | `/v1/transactions/{id}` | Yes | Poll transaction status and metadata |
| `GET` | `/v1/transactions/{id}/webhooks` | Yes | List webhook delivery attempts for a transaction |
| `GET` | `/v1/health` | No | Health check (`?deep=1` verifies MySQL connectivity) |

## Quick Start

### 1. Prerequisites

- Go 1.26+
- MySQL 8.x (local or managed) and a database for this service
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

This prints wallet details (wallet ID and Aptos address). Note the wallet ID and address — you'll pass them per-request to `/v1/execute`.

### 3. Fund Wallets

Fund your wallet with testnet APT at https://aptos.dev/en/network/faucet

### 4. Configure

Fill in the remaining `.env` values (at minimum `API_KEY`, `MYSQL_DSN`, and `APTOS_NODE_URL`). See [Configuration](#configuration) for all options.

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

### E2E tests (local and CI)

End-to-end tests live in `examples/e2e_test.go` and use the build tag `e2e`. They call a **running** server, MySQL, Aptos testnet, and Circle.

**Local**

1. Configure `.env` (same as server): `MYSQL_DSN`, `API_KEY`, Aptos, Circle credentials. Fund your wallets on testnet.
2. Start the server: `make run`
3. In another terminal: `make test-e2e` (runs `go test -tags=e2e ./examples/ -v -count=1`)

**Extra environment variables (optional)**

| Variable | Description |
|----------|-------------|
| `E2E_BASE_URL` | Server URL (default `http://localhost:8080`) |
| `E2E_API_KEY` | Bearer secret; defaults to `API_KEY` |
| `E2E_WALLET_ID` / `E2E_WALLET_ADDR` / `E2E_WALLET_PUBLIC_KEY` | Override first wallet from `CIRCLE_WALLETS` |
| `E2E_WALLET2_ID` / `E2E_WALLET2_ADDR` / `E2E_WALLET2_PUBLIC_KEY` | Second wallet, or use two entries in `CIRCLE_WALLETS` |
| `E2E_THROUGHPUT` | Concurrent executes per wallet in throughput tests (default 8, max 50) |
| `E2E_MYSQL_DSN` | MySQL DSN for the stale-processing recovery test; defaults to `MYSQL_DSN` |

Tests that need two funded wallets skip if only one wallet is configured. Inline-wallet and some idempotency paths need `public_key` in JSON or `E2E_WALLET_PUBLIC_KEY`.

**GitHub Actions**

Workflow [.github/workflows/e2e.yml](.github/workflows/e2e.yml) runs on `workflow_dispatch` and on `push` to `main`. It starts MySQL, builds the server, runs it with secrets, then `go test -tags=e2e ./examples/`. If required secrets are missing, the job **skips** E2E steps and succeeds (so forks without secrets stay green).

Configure these **repository secrets** to run live E2E: `API_KEY`, `APTOS_NODE_URL`, `APTOS_CHAIN_ID` (recommended, e.g. `2` for testnet), `CIRCLE_API_KEY`, `CIRCLE_ENTITY_SECRET`, `CIRCLE_WALLETS` (JSON array of `{"wallet_id","address","public_key"}` objects used by E2E tests; use **two** wallets to exercise dual-wallet throughput).

## Configuration

Configuration is loaded from `config.yaml` (or the path in `CONFIG_PATH`), with environment variables taking precedence. A `.env` file is also supported via godotenv.

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_PORT` | `8080` | HTTP listen port |
| `API_KEY` | *(required)* | Bearer token for protected endpoints |
| `TESTING_MODE` | `false` | Set `true` to disable auth (development only) |

### MySQL

| Variable | Default | Description |
|----------|---------|-------------|
| `MYSQL_DSN` | *(required)* | [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) DSN (e.g. `user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true`) |

The server runs embedded migrations on startup. `GET /v1/health?deep=1` checks database connectivity.

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
| `CIRCLE_WALLET_SET_ID` | | Wallet set ID (only needed for `make create-wallets`) |

Wallet IDs and addresses are provided per-request in the API (not in server config). Public keys are resolved automatically from Circle.

### Transaction Settings

| Variable / YAML | Default | Description |
|-----------------|---------|-------------|
| `MAX_GAS_AMOUNT` / `transaction.max_gas_amount` | `2000000` | Default max gas per transaction |
| `TXN_EXPIRATION_SECONDS` / `transaction.expiration_seconds` | `60` | On-chain transaction expiration window |
| `WEBHOOK_URL` / `webhook.global_url` | *(empty)* | Global webhook URL for status notifications |

### Submitter & Poller

Configurable in `config.yaml` (see `config.yaml` for full list):

| YAML key | Default | Description |
|----------|---------|-------------|
| `submitter.poll_interval_ms` | `200` | How often the dispatcher checks for queued work |
| `submitter.signing_pipeline_depth` | `4` | How many transactions to sign ahead per sender |
| `submitter.max_retry_duration_seconds` | `300` | Max time before marking a transaction permanently failed |
| `submitter.retry_interval_seconds` | `5` | Base retry interval on transient failure |
| `poller.interval_seconds` | `5` | How often to poll on-chain for submitted transactions |

### Rate Limiting

| YAML key | Default | Description |
|----------|---------|-------------|
| `rate_limit.enabled` | `false` | Enable upstream rate limiting |
| `rate_limit.requests_per_second` | `100` | Global rate limit |
| `rate_limit.burst` | `200` | Burst capacity |

## Security

- **Authentication:** All endpoints except `/v1/health` require a Bearer token or `X-API-Key` header. Comparison uses constant-time comparison to prevent timing attacks.
- **SSRF protection:** Webhook URLs are validated to reject private, loopback, and link-local addresses — both at request time and at delivery time (dial-level blocking).
- **Request body limit:** All JSON request bodies are limited to 1 MB.
- **Rate limiting:** Optional token-bucket rate limiter returns 429 with `Retry-After` header when capacity is exceeded.

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
  "address": "0xSENDER_ADDRESS",
  "function_id": "0x1::aptos_account::transfer",
  "type_arguments": [],
  "arguments": ["0xRECIPIENT_ADDRESS", "100"],
  "max_gas_amount": 50000,
  "webhook_url": "https://example.com/hook",
  "fee_payer": {
    "wallet_id": "fee-payer-wallet-uuid",
    "address": "0xFEE_PAYER_ADDRESS"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `wallet_id` | string | Yes | Circle wallet UUID |
| `address` | string | Yes | Aptos sender address |
| `function_id` | string | Yes | `address::module::function` |
| `type_arguments` | string[] | No | Move type arguments |
| `arguments` | any[] | No | Function arguments (types resolved from on-chain ABI) |
| `max_gas_amount` | uint64 | No | Override default max gas |
| `webhook_url` | string | No | Per-request webhook URL |
| `idempotency_key` | string | No | Optional idempotency key (also accepted as `Idempotency-Key` header) |
| `fee_payer` | object | No | Separate fee-payer wallet: `{"wallet_id": "...", "address": "..."}` |

**Response (202 Accepted):** The request is persisted and queued. A background worker assigns the Aptos account sequence number, signs via Circle, and submits to the network.

```json
{
  "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "queued"
}
```

Poll `GET /v1/transactions/{id}` until `status` is `submitted` (includes `txn_hash`), then `confirmed` or `failed`.

**Errors:**

| Status | Cause |
|--------|-------|
| 400 | Invalid request, unknown wallet |
| 401 | Missing or invalid API key |
| 500 | Failed to persist the queued transaction |

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
queued → processing → submitted → confirmed
                              → failed     (VM error, error_message populated)
                              → expired    (on-chain expiration reached)
```

**Errors:**

| Status | Cause |
|--------|-------|
| 400 | Missing transaction ID |
| 401 | Missing or invalid API key |
| 404 | Transaction not found |

### GET /v1/health

Returns server liveness. No authentication required.

```json
{"status": "ok"}
```

With `?deep=1`, also verifies MySQL connectivity:

```json
{"status": "ok", "db": "ok"}
```

Returns HTTP 500 with `"db": "unreachable"` if the database is down.

## Webhooks

When a transaction reaches a terminal status (`confirmed`, `failed`, or `expired`), a webhook delivery is queued to notify your application.

**URL resolution:** per-request `webhook_url` takes precedence over the global `WEBHOOK_URL` environment variable. If neither is set, no webhook is sent.

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

**Delivery:** Persistent outbox pattern — deliveries are stored in MySQL and delivered by a background worker with exponential backoff (up to 5 retries, capped at 5 minutes between attempts). The worker also recovers orphaned deliveries stuck in `delivering` status for over 5 minutes (e.g. after a crash).

**Retry policy:**
- **2xx** — success, delivery complete
- **408, 429** — retryable (transient client errors)
- **Other 4xx** — permanent failure, not retried
- **5xx / network error** — retried with exponential backoff

**SSRF protection:** webhook URLs are validated at input time (no private/loopback IPs, no `localhost`) and again at dial time by the delivery worker.

**Delivery history:** `GET /v1/transactions/{id}/webhooks` returns all delivery attempts for a transaction:

```json
[
  {
    "id": "delivery-uuid",
    "transaction_id": "txn-uuid",
    "url": "https://example.com/hook",
    "status": "delivered",
    "attempts": 1,
    "created_at": "2026-03-27T18:42:16Z"
  }
]
```

## High Throughput

The server processes transactions **FIFO per sender** but **in parallel across senders**. Using N wallets gives approximately N× throughput.

### Key Patterns

1. **Multi-wallet parallelism** — Round-robin requests across wallets. Each sender gets its own worker with a signing pipeline.
2. **Fire-and-forget with webhooks** — Pass `webhook_url` to skip polling. The server delivers completion status via HTTP POST.
3. **Idempotency keys** — Always include `idempotency_key` in production. Safe to retry on network errors.
4. **Respect backpressure** — If you get HTTP 429, honor the `Retry-After` header.

### Server Tuning (`config.yaml`)

```yaml
submitter:
  poll_interval_ms: 100          # lower = faster job pickup (default 200)
  signing_pipeline_depth: 8      # sign ahead while submitting (default 4)
  retry_interval_seconds: 3      # retry on transient failure (default 5)

rate_limit:
  enabled: true
  requests_per_second: 500       # upstream protection
  burst: 1000
```

### Throughput Estimates

| Wallets | Pipeline Depth | Expected Throughput |
|---------|----------------|---------------------|
| 1       | 4              | ~2-4 txn/s          |
| 4       | 4              | ~8-16 txn/s         |
| 10      | 8              | ~20-40 txn/s        |

Throughput is bounded by Circle signing latency (~200-500ms) and Aptos block time (~1-2s). The pipeline overlaps signing and submission to maximize throughput per wallet.

### Examples

Complete runnable examples in 4 languages live in `examples/high_throughput/`:

```bash
# Go (recommended — same language as the server)
export API_KEY=your-token
export WALLETS='[{"wallet_id":"w1","address":"0xabc"},{"wallet_id":"w2","address":"0xdef"}]'
export TXN_COUNT=20
go run ./examples/high_throughput

# Python (requires: pip install aiohttp)
python examples/high_throughput/python_example.py

# TypeScript (requires: Node 18+)
npx tsx examples/high_throughput/typescript_example.ts

# Shell/curl (single + batch examples)
bash examples/high_throughput/curl_examples.sh
```

Each example demonstrates:
- Round-robin wallet assignment for max parallelism
- Semaphore-bounded concurrent submissions
- Idempotency keys for safe retries
- Exponential backoff polling with jitter
- Webhook listener setup
- Throughput stats at the end

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
  config/                   YAML + env config with .env support
  aptos/
    abi.go                  ABI cache — fetches and caches module ABIs from Aptos node
    args.go                 BCS serialization — converts JSON arguments to BCS bytes by Move type
    client.go               Aptos SDK wrapper — fee-payer wrapping with explicit sequence, submit, view
  circle/
    client.go               Circle HTTP client — RSA key cache, entity secret encryption, sign/transaction
    signer.go               Fee-payer transaction signing via Circle's sign/transaction endpoint
    pubkey_cache.go         Auto-resolves wallet public keys from Circle (lazy, thread-safe)
  handler/
    handler.go              Shared JSON response helpers
    execute.go              POST /v1/execute — validate, enqueue with optional fee-payer
    query.go                POST /v1/query — ABI resolve, BCS serialize, call view
    transaction.go          GET /v1/transactions/{id} — status lookup
    webhook.go              GET /v1/transactions/{id}/webhooks — delivery history
    webhookurl.go           Webhook URL validation (SSRF prevention)
    ratelimit.go            Opt-in token-bucket rate limiting middleware
  submitter/
    submitter.go            Dispatcher + per-sender workers with signing pipeline
  store/
    store.go                Store + Queue interfaces, TransactionRecord
    memory.go               In-memory implementation for testing
    mysql/                  MySQL implementation — atomic sequence, claim, shift, webhook outbox
  poller/
    poller.go               Confirms submitted txns, conditional updates (multi-host safe)
  webhook/
    store.go                WebhookStore interface + DeliveryRecord type
    notifier.go             Inserts delivery records into the persistent outbox
    worker.go               Background worker: claims, delivers, retries with exponential backoff
  db/migrations/            Embedded SQL migrations (auto-applied on startup)
examples/
  e2e_test.go               End-to-end tests against a running server
  high_throughput/           High-speed usage examples (Go, Python, TypeScript, curl)
  create_wallets/main.go    Helper to create Circle wallets on Aptos testnet
config.yaml                 Default configuration with all tunables
```

### Signing Flow

Per the [Circle Aptos Signing APIs Tutorial](https://developers.circle.com):

1. Dispatcher spawns a per-sender worker; worker claims a queued transaction (atomically allocates sequence number)
2. Build entry function from ABI-resolved JSON arguments
3. Resolve public key from Circle (cached) via `PublicKeyCache`
4. Build fee-payer `RawTransactionWithData` with explicit `SequenceNumber`, gas, and expiration
5. BCS-serialize and send to Circle `sign/transaction` (sender wallet, and separately fee-payer wallet if different)
6. Assemble `FeePayerTransactionAuthenticator` and submit to Aptos
7. Pipeline: while step 6 executes, the worker starts steps 1-5 for the next transaction

### ABI Resolution

Both `/v1/execute` and `/v1/query` resolve argument types automatically:

1. Parse `function_id` into address, module, function
2. Fetch module ABI from the Aptos node (cached per module for server lifetime)
3. Strip `&signer` parameters
4. Validate argument count matches
5. BCS-serialize each argument according to its Move type

Supported Move types: `address`, `bool`, `u8`, `u16`, `u32`, `u64`, `u128`, `u256`, `0x1::string::String`, `vector<T>`, `0x1::object::Object<T>`.

### Persistence and sequencing

Transactions and per-sender Aptos sequence state are stored in **MySQL**. `POST /v1/execute` inserts a `queued` row; the **submitter** dispatcher spawns a worker per sender address. Each worker claims transactions FIFO, atomically allocating a sequence number in the same SQL transaction (`SELECT ... FOR UPDATE` + increment). Workers operate a signing pipeline — signing the next transaction while the current one is being submitted. The **poller** confirms or fails submitted transactions by hash, using conditional updates (`WHERE status = 'submitted'`) to prevent duplicate processing across multiple server instances. On permanent failure, subsequent transactions for the same sender are automatically shifted (re-queued with new sequence numbers).

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
