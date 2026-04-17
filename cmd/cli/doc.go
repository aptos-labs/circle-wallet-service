// Command cli is a convenience client for exercising the Aptos Contract API
// server from the shell — health checks, view calls, entry-function execution,
// and transaction-status polling.
//
// Usage:
//
//	go run ./cmd/cli <command> [flags]
//
// Commands:
//
//	health                         Check server health
//	query                          Call a view function
//	execute                        Submit a transaction
//	status <transaction_id>        Poll transaction status
//	watch  <transaction_id>        Poll until terminal status
package main
