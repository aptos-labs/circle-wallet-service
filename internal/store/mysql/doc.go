// Package mysql implements [store.Queue] backed by MySQL.
//
// Transaction rows and per-sender Aptos sequence counters are stored in two
// tables: `transactions` and `account_sequences`. The claim operation uses
// SELECT ... FOR UPDATE to provide row-level locking so multiple server
// instances can safely share the same database without double-processing.
//
// Webhook delivery records (the persistent outbox) are also stored here,
// implementing [webhook.WebhookStore].
package mysql
