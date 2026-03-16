# Contract Integration

Generic REST API for interacting with Aptos Move contracts — submit entry function transactions, call view functions, and track transaction status.

Part of the Aptos Labs organization (`aptos-labs/jc-contract-integration`).

## Architecture

```
cmd/
  server/       API server — async transaction submission, query endpoints, health check
  openapi/      OpenAPI spec generator (YAML/JSON output)
contracts/
  sources/      Move smart contract (contractInt module)
  tests/        Move unit tests
internal/
  account/      Role-based account registry
  api/          HTTP handlers, middleware, OpenAPI spec
  aptos/        Aptos client wrapper, ABI cache, BCS serialization
  config/       Environment-based configuration
  signer/       Transaction signing (local keys or Circle)
  store/        SQLite-backed transaction persistence
  txn/          Transaction lifecycle manager and poller
e2e/            End-to-end tests (requires aptos CLI and devnet)
scripts/        Utility scripts (smoke tests, deployment, localnet)
```

## API Surface

Two generic endpoints that work with **any** Aptos Move contract:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/contracts/execute` | POST | Submit an entry function transaction (async, returns 202) |
| `/v1/contracts/query` | POST | Call a view function (sync, returns 200) |
| `/v1/transactions/{id}` | GET | Poll transaction status |
| `/v1/health` | GET | Health check |
| `/v1/docs` | GET | Interactive API docs (Scalar UI) |
| `/v1/openapi.yaml` | GET | OpenAPI 3.0 spec |

### Execute (write)

```bash
curl -X POST http://localhost:8080/v1/contracts/execute \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "function_id": "0x1234::contractInt::mint",
    "type_arguments": [],
    "arguments": ["0x5678abcd...", "10000"],
    "signer": "minter"
  }'
# → {"transaction_id": "550e8400-...", "status": "pending"}
```

### Query (read)

```bash
curl -X POST http://localhost:8080/v1/contracts/query \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "function_id": "0x1234::contractInt::balance_of",
    "type_arguments": [],
    "arguments": ["0x5678abcd..."]
  }'
# → {"result": ["10000"]}
```

Arguments are **untyped** (strings, numbers, arrays) — the server resolves types from the on-chain module ABI and handles BCS serialization automatically.

## Prerequisites

- **Go 1.26+**
- **Aptos CLI** (for contract deployment and e2e tests) — [install](https://aptos.dev/tools/aptos-cli)
- **golangci-lint** (for linting) — `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`
- **gofumpt** (for formatting) — `go install mvdan.cc/gofumpt@latest`

## Quick Start

```bash
# Copy and configure environment
cp .env.example .env
# Edit .env with your RPC endpoint, keys, and signer configuration

# Build
make build

# Run the API server
make run

# Run tests
make test
```

## Make Targets

| Target              | Description                                              |
|---------------------|----------------------------------------------------------|
| `make build`        | Compile server and openapi binaries into `bin/`          |
| `make test`         | Run all Go tests                                         |
| `make test-race`    | Run tests with race detector                             |
| `make vet`          | Run `go vet`                                             |
| `make lint`         | Run `golangci-lint`                                      |
| `make fmt`          | Format code with `gofumpt`                               |
| `make check`        | Run all validations (fmt-check + vet + lint + test-race) |
| `make run`          | Build and start the API server                           |
| `make e2e`          | Deploy contract to devnet and run end-to-end tests       |
| `make smoke-test`   | Run curl-based smoke tests against a running server      |
| `make localnet-test`| Start localnet, deploy, run full integration tests       |
| `make openapi-yaml` | Write OpenAPI spec to `openapi.yaml`                     |
| `make openapi-json` | Write OpenAPI spec to `openapi.json`                     |
| `make clean`        | Remove build artifacts                                   |

## Smart Contract

The Move contract (`contracts/sources/contractInt.move`) implements:

- **Fungible asset** with unlimited supply via Aptos `primary_fungible_store`
- **Minter allowances** — master minter configures per-minter mint limits
- **Denylist** — denylister can block addresses from receiving tokens
- **Role-based access control** — owner, master minter, denylister, metadata updater
- **Pause** — halts all minting and burning when activated

### Contract Roles

| Role                 | Permissions                                            |
|----------------------|--------------------------------------------------------|
| **Owner**            | Update master minter, denylister, and metadata updater |
| **Master Minter**    | Configure/remove minters, adjust allowances            |
| **Minter**           | Mint (up to allowance) and burn tokens                 |
| **Denylister**       | Add/remove addresses from the denylist                 |
| **Metadata Updater** | Update token name, symbol, icon, and project URI       |

### Contract Testing

```bash
cd contracts
aptos move test --dev
```

## Signer Providers

The server supports two signer backends configured via `SIGNER_PROVIDER`:

- **`local`** — signs with private keys from environment variables
- **`circle`** — delegates signing to Circle's Programmable Wallets

## Environment

- `.env` files are gitignored — use `.env` for local secrets
- Never commit credentials or private keys
- See `.env.example` for all available configuration options
