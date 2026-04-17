// Package handler implements the HTTP handlers for the v1 API.
//
// Endpoints:
//   - POST /v1/execute — validate and enqueue a transaction ([Execute])
//   - POST /v1/query   — call a Move view function ([Query])
//   - GET  /v1/transactions/{id} — poll transaction status ([GetTransaction])
//   - GET  /v1/transactions/{id}/webhooks — delivery history ([ListWebhookDeliveries])
//
// Request bodies are capped at 1 MB and decoded with DisallowUnknownFields.
package handler
