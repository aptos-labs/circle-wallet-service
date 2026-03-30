# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## Project Overview

Generic REST API for interacting with Aptos Move contracts. Part of the Aptos Labs organization (`aptos-labs/jc-contract-integration`).

- **Language:** Go
- **Blockchain:** Aptos

## Build Commands

```bash
go build ./...
go test ./...
go test -run TestName ./path/to/package   # run a single test
go vet ./...
make check                                 # fmt + vet + lint + test-race
```

## API Endpoints

- `POST /v1/contracts/execute` — Submit an entry function transaction (async, 202)
- `POST /v1/contracts/query` — Call a view function (sync, 200)
- `GET /v1/transactions/{id}` — Poll transaction status
- `GET /v1/health` — Health check

## Key Architecture

- **ABI Cache** (`internal/aptos/abi.go`): Fetches and caches module ABIs from the Aptos node to resolve argument types at runtime
- **BCS Serializer** (`internal/aptos/args.go`): Generic type-directed serialization from JSON values to BCS bytes
- **Execute Handler** (`internal/api/handler/execute.go`): Builds entry function payloads from untyped JSON arguments
- **Query Handler** (`internal/api/handler/query.go`): Proxies view function calls to the Aptos node's /view endpoint
- **Transaction Manager** (`internal/txn/`): Async submission with retry, polling, and persistence
- **Signer Interface** (`internal/signer/`): Local keys or Circle Programmable Wallets

## Environment

- `.env` files are gitignored — use `.env` for local secrets (API keys, RPC endpoints)
- Never commit credentials or private keys
