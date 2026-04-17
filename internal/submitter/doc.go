// Package submitter runs the background transaction processing pipeline.
//
// A dispatcher goroutine periodically queries for senders with queued work and
// spawns one worker goroutine per sender address. Each worker operates a signing
// pipeline: it claims transactions FIFO, resolves ABIs, signs via Circle, and
// submits to Aptos. While a submission is in flight, the next transaction is
// being signed ahead of time (pipeline depth is configurable).
//
// On transient failures the transaction is re-queued with a backoff sleep.
// On permanent failure (expiration, max retry duration) the transaction is
// marked failed, subsequent transactions for the same sender are shifted
// (re-queued with new sequence numbers), and a webhook notification is sent.
//
// A separate recovery loop periodically reclaims transactions stuck in
// "processing" beyond a configurable threshold (e.g. after a crash).
package submitter
