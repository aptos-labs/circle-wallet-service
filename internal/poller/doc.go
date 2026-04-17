// Package poller confirms submitted transactions by polling the Aptos node.
//
// The [Poller] periodically lists all "submitted" transactions from the store,
// looks up each by txn_hash on-chain, and transitions them to "confirmed",
// "failed", or "expired" based on the result. Updates use [store.Store.UpdateIfStatus]
// (conditional on status = "submitted") so multiple server instances can poll
// concurrently without double-processing.
package poller
