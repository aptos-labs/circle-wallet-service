// Package webhook implements the persistent outbox pattern for webhook delivery.
//
// When a transaction reaches a terminal status, [Notifier.Notify] inserts a
// [DeliveryRecord] into MySQL. The background [Worker] claims pending records,
// POSTs the JSON payload to the target URL, and retries with exponential backoff
// on transient failures (5xx, network errors, 408, 429). Permanent client errors
// (other 4xx) are not retried.
//
// The Worker also recovers deliveries orphaned in "delivering" state for more
// than 5 minutes (e.g. after a crash) by resetting them to "pending".
package webhook
