// Command server is the HTTP entrypoint for the wallet service.
//
// It wires together the four long-lived goroutines that make up the runtime:
//
//   - Submitter  — per-sender workers that claim queued rows, sign via Circle,
//     and submit to the Aptos node. See internal/submitter.
//   - Poller     — confirms submitted transactions by hash and flips terminal
//     statuses. See internal/poller.
//   - Webhook    — delivery worker that drains the webhook outbox.
//   - HTTP server — handles /v1/execute, /v1/query, /v1/transactions/{id},
//     /v1/health.
//
// Startup sequence: load config → run DB migrations → open MySQL → construct
// clients (Circle, Aptos), allowing each to be absent so the server can still
// serve /v1/health and return 503 on the affected endpoints → start background
// goroutines → start HTTP server. Shutdown is driven by SIGINT/SIGTERM through
// a cancelled context; the HTTP server gets a bounded graceful shutdown.
//
// See TRANSACTION_PIPELINE.md for a full description of how a request flows
// from /v1/execute to an on-chain transaction.
package main
