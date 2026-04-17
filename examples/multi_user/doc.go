// Multi-user concurrency example for the Circle Wallet Service.
//
// The service is designed as a shared, multi-tenant HTTP API. Different clients
// (different wallets, different workloads) can use it simultaneously without
// blocking each other. This example simulates three independent "users" hitting
// the same server with different usage patterns:
//
//   - Alice   — Power user. Submits a burst of transfers via /v1/execute.
//   - Bob     — Application backend. Occasional transfers plus frequent balance
//     queries via /v1/query.
//   - Charlie — Read-only dashboard. Only uses /v1/query and
//     /v1/transactions/{id}. Never signs.
//
// What you should observe from the timing logs:
//
//  1. Alice's transfers and Bob's transfers submit in parallel. The submitter
//     spawns one worker per sender address, so different senders don't queue
//     behind each other.
//  2. Within Alice's stream, transfers are processed FIFO — the submitter's
//     per-sender worker signs and submits in the order the API received them.
//  3. Charlie's queries and transaction-status polls run with zero interference
//     from the execute traffic: /v1/query is synchronous (proxied directly to
//     the Aptos node's /view endpoint) and doesn't touch the submitter queue
//     at all.
//
// Usage:
//
//	export API_KEY=your-bearer-token
//	export ALICE_WALLET_ID=...
//	export ALICE_ADDRESS=0x...
//	export BOB_WALLET_ID=...
//	export BOB_ADDRESS=0x...
//	export CHARLIE_WATCH_ADDRESS=0x...   # optional; defaults to ALICE_ADDRESS
//	go run ./examples/multi_user
package main
