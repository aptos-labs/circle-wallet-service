# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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
make check                                 # fmt + vet + lint + unit tests
```

## API Endpoints

- `POST /v1/execute` — Enqueue an entry function transaction (202, `status: queued`)
- `POST /v1/query` — Call a view function (sync, 200)
- `GET /v1/transactions/{id}` — Poll transaction status
- `GET /v1/health` — Health check (`?deep=1` pings MySQL)

## Key Architecture

- **ABI Cache** (`internal/aptos/abi.go`): Fetches and caches module ABIs from the Aptos node
- **Execute Handler** (`internal/handler/execute.go`): Validates requests, SSRF-checks webhook URLs, inserts `queued` rows in MySQL
- **Submitter** (`internal/submitter/`): Background dispatcher + per-sender workers with signing pipeline
- **MySQL Store** (`internal/store/mysql/`): Transactions + `account_sequences`; atomic claim/queue with row-level locking
- **Migrations** (`internal/db/migrations/`): Embedded SQL; auto-applied on startup
- **Query Handler** (`internal/handler/query.go`): Proxies view calls to the Aptos node `/view` endpoint
- **Poller** (`internal/poller/`): Confirms submitted transactions by hash; conditional updates prevent double-processing
- **Circle Signer** (`internal/circle/`): BCS serialize → encrypt entity secret → `sign/transaction` → AccountAuthenticator
- **Public Key Cache** (`internal/circle/pubkey_cache.go`): Lazy, thread-safe wallet public key resolution with singleflight
- **Webhook Outbox** (`internal/webhook/`): Persistent outbox pattern — notifier inserts records, worker delivers with backoff

## Environment

- `.env` files are gitignored — use `.env` for local secrets (API keys, `MYSQL_DSN`, RPC endpoints)
- Never commit credentials or private keys
- **MySQL is required** (`MYSQL_DSN`); migrations run automatically when the server starts
