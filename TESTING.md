# Testing Guide

This document covers all testing modes for the Contract API server, from fast unit tests to full testnet integration with Circle Programmable Wallets.

## Table of Contents

- [Unit & Integration Tests](#unit--integration-tests)
- [Smoke Tests](#smoke-tests)
- [Localnet Tests](#localnet-tests)
- [End-to-End Tests (Devnet)](#end-to-end-tests-devnet)
- [Testnet with Circle Wallets](#testnet-with-circle-wallets)
  - [Step 1: Circle Account Setup](#step-1-circle-account-setup)
  - [Step 2: Create Role Wallets](#step-2-create-role-wallets)
  - [Step 3: Create Owner Key](#step-3-create-owner-key)
  - [Step 4: Fund Accounts](#step-4-fund-accounts)
  - [Step 5: Deploy the Contract](#step-5-deploy-the-contract)
  - [Step 6: Configure .env](#step-6-configure-env)
  - [Step 7: Start the Server](#step-7-start-the-server)
  - [Step 8: Assign Roles](#step-8-assign-roles)
  - [Step 9: Run Smoke Tests](#step-9-run-smoke-tests)

---

## Unit & Integration Tests

Runs all Go unit tests with no external dependencies.

```bash
make test          # all tests
make test-race     # with race detector (use before committing)
make check         # fmt + vet + lint + test-race
```

---

## Smoke Tests

Curl-based tests against an already-running server. Tests the HTTP layer end-to-end without requiring a real blockchain.

**Quick start (testing mode — no keys needed):**

```bash
# Terminal 1: start server with ephemeral keys and no auth
TESTING_MODE=true make run

# Terminal 2: run smoke tests
make smoke-test
```

**Against a real server:**

```bash
BASE_URL=http://localhost:8080 API_KEY=your-key make smoke-test
```

The smoke test exercises health, validation errors, and transaction tracking.

---

## Localnet Tests

Starts an Aptos localnet, deploys the contract, starts the API server, and runs on-chain verification through the generic execute/query endpoints.

**Prerequisites:**
- `aptos` CLI on PATH — [install](https://aptos.dev/tools/aptos-cli)

```bash
make localnet-test
```

This is the most comprehensive automated test — it exercises the full stack including BCS serialization, ABI resolution, and transaction lifecycle.

---

## End-to-End Tests (Devnet)

Deploys the contract to Aptos devnet, starts an in-process server with local keys, and runs the full API flow.

> **Note:** The e2e tests are currently being updated for the generic contract API. Use `make localnet-test` for full integration testing.

**Prerequisites:**
- `aptos` CLI on PATH — [install](https://aptos.dev/tools/aptos-cli)
- Network access to `api.devnet.aptoslabs.com`

```bash
make e2e
```

---

## Testnet with Circle Wallets

This tests the full production signing path: Circle Programmable Wallets sign transactions on Aptos testnet.

**Architecture note:** The Aptos CLI requires a local private key to publish packages, so the **owner role uses a local key** for deployment. The remaining roles (master_minter, minter, denylister, metadata_updater) use Circle wallets. The server auto-fetches each wallet's public key from Circle at startup, so `*_PUBLIC_KEY` env vars are optional.

### Step 1: Circle Account Setup

1. Create a developer account at [console.circle.com](https://console.circle.com)
2. Create an API key → `CIRCLE_API_KEY`
3. Generate and register your Entity Secret:

```bash
cd dev-controlled-wallets && npm install && cd ..
make circle-setup
```

This generates a random 32-byte secret, registers it with Circle, saves a recovery file, and prints:
```
CIRCLE_ENTITY_SECRET=<64-char-hex>
```
Add that to your `.env`. The server encrypts it with Circle's RSA public key automatically at startup — you never need to compute or store the ciphertext yourself.

### Step 2: Create Role Wallets

Run the provided script to create one Circle wallet per role on Aptos testnet:

```bash
make circle-wallets
```

The script creates a wallet set with four EOA wallets (`APTOS-TESTNET`) and prints the env block:

```
MASTER_MINTER_WALLET_ID=...
MASTER_MINTER_ADDRESS=...
MINTER_WALLET_ID=...
MINTER_ADDRESS=...
DENYLISTER_WALLET_ID=...
DENYLISTER_ADDRESS=...
METADATA_UPDATER_WALLET_ID=...
METADATA_UPDATER_ADDRESS=...
```

Each wallet's `initialPublicKey` is included in the Circle API response. The server fetches it automatically at startup, but you can also pin it manually via `*_PUBLIC_KEY` env vars to skip the API call.

### Step 3: Create Owner Key

The owner must be a local Aptos key (required for CLI deployment):

```bash
aptos key generate --output-file owner.key
```

This produces `owner.key` (private key) and `owner.key.pub` (public key). Set:

```
OWNER_PRIVATE_KEY=0x...   # contents of owner.key
OWNER_ADDRESS=0x...       # derived account address
```

### Step 4: Fund Accounts

Fund the owner address and all four Circle wallet addresses with testnet APT:

- **Aptos testnet faucet**: [aptos.dev/en/network/faucet](https://aptos.dev/en/network/faucet)

Each account needs enough APT to cover gas (a few APT is plenty for testing).

### Step 5: Deploy the Contract

```bash
cp .env.example .env
# Fill in at minimum: OWNER_ADDRESS, OWNER_PRIVATE_KEY, APTOS_NODE_URL

./scripts/deploy-testnet.sh
```

This compiles, publishes, and initializes the contract. Note the owner address — this is the contract address used in `function_id` values.

### Step 6: Configure .env

Your `.env` should now have:

```bash
# Server
API_KEY=any-secret-you-choose
SIGNER_PROVIDER=circle

# Aptos
APTOS_NODE_URL=https://api.testnet.aptoslabs.com/v1
APTOS_CHAIN_ID=2

# Owner (local key — used for role assignment)
OWNER_PRIVATE_KEY=0x...
OWNER_ADDRESS=0x...

# Circle
CIRCLE_API_KEY=...
CIRCLE_ENTITY_SECRET=<64-char-hex-from-register-entity-secret.ts>

# Circle wallets (from Step 2)
MASTER_MINTER_WALLET_ID=...
MASTER_MINTER_ADDRESS=...
MINTER_WALLET_ID=...
MINTER_ADDRESS=...
DENYLISTER_WALLET_ID=...
DENYLISTER_ADDRESS=...
METADATA_UPDATER_WALLET_ID=...
METADATA_UPDATER_ADDRESS=...
```

See `.env.example` for all available options with descriptions.

### Step 7: Start the Server

```bash
make run
```

At startup the server will:
1. Fetch each Circle wallet's public key from `GET /v1/w3s/wallets/{id}` (unless manually pinned via `*_PUBLIC_KEY`)
2. Validate all signers can parse their keys and addresses
3. Log the configured roles

### Step 8: Assign Roles

After deployment, all roles default to the owner's address. Reassign them to the Circle wallet addresses using the generic execute endpoint:

```bash
API_KEY=your-key
BASE=http://localhost:8080
CONTRACT=0x<owner-address>

# Assign master minter
curl -X POST "$BASE/v1/contracts/execute" \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"function_id\": \"${CONTRACT}::contractInt::update_master_minter\",
       \"arguments\": [\"$MASTER_MINTER_ADDRESS\"],
       \"signer\": \"owner\"}"

# Assign denylister
curl -X POST "$BASE/v1/contracts/execute" \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"function_id\": \"${CONTRACT}::contractInt::update_denylister\",
       \"arguments\": [\"$DENYLISTER_ADDRESS\"],
       \"signer\": \"owner\"}"

# Assign metadata updater
curl -X POST "$BASE/v1/contracts/execute" \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"function_id\": \"${CONTRACT}::contractInt::update_metadata_updater\",
       \"arguments\": [\"$METADATA_UPDATER_ADDRESS\"],
       \"signer\": \"owner\"}"

# Configure the minter with an allowance
curl -X POST "$BASE/v1/contracts/execute" \
  -H "Authorization: $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"function_id\": \"${CONTRACT}::contractInt::configure_minter\",
       \"arguments\": [\"$MINTER_ADDRESS\", \"1000000\"],
       \"signer\": \"master_minter\"}"
```

Each request returns a `transaction_id`. Poll `GET /v1/transactions/{id}` until `status` is `confirmed`.

### Step 9: Run Smoke Tests

```bash
BASE_URL=http://localhost:8080 API_KEY=your-key make smoke-test
```

The smoke test will exercise all endpoints. Watch the server logs to confirm Circle sign-message calls are being made.

---

## Troubleshooting

**"circle wallet has no initialPublicKey"** — The wallet was created before Circle added Aptos public key support, or it's on a different network. Re-create the wallet or set `*_PUBLIC_KEY` manually (extract from the Circle console or from on-chain tx history via `./scripts/get-aptos-pubkey.sh 0xADDRESS`).

**"parse public key: ..."** — The `*_PUBLIC_KEY` value is malformed. It must be a 64-character hex string, optionally prefixed with `0x`.

**"CIRCLE_API_KEY is required"** — Forgot to set `SIGNER_PROVIDER=circle` or the API key is missing.

**Transaction stuck in `pending`** — Check that the wallet address has enough testnet APT for gas. Check the poller logs for errors.

**"resolve ABI" errors** — The server couldn't fetch the module ABI from the Aptos node. Verify the `function_id` address has the contract deployed and the node URL is reachable.
