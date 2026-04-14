package mysql

import (
	"context"
	"database/sql"

	"github.com/aptos-labs/jc-contract-integration/internal/webhook"
)

func (s *Store) CreateDelivery(ctx context.Context, rec *webhook.DeliveryRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, transaction_id, url, payload, status, attempts, last_attempt_at, last_error, next_retry_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.TransactionID, rec.URL, rec.Payload, rec.Status,
		rec.Attempts, rec.LastAttemptAt, nullStr(rec.LastError),
		rec.NextRetryAt, rec.CreatedAt,
	)
	return err
}

func (s *Store) ClaimPendingDeliveries(ctx context.Context, limit int) ([]*webhook.DeliveryRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, transaction_id, url, payload, status, attempts, last_attempt_at, last_error, next_retry_at, created_at
		FROM webhook_deliveries
		WHERE status = 'pending' AND next_retry_at <= UTC_TIMESTAMP(3)
		ORDER BY next_retry_at ASC
		LIMIT ?
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}

	var out []*webhook.DeliveryRecord
	var ids []any
	for rows.Next() {
		rec, err := scanDelivery(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, rec)
		ids = append(ids, rec.ID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(ids) > 0 {
		query := "UPDATE webhook_deliveries SET status = 'delivering' WHERE id IN (?" + repeatParam(len(ids)-1) + ")"
		_, err = tx.ExecContext(ctx, query, ids...)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	for _, rec := range out {
		rec.Status = "delivering"
	}
	return out, nil
}

func (s *Store) UpdateDelivery(ctx context.Context, rec *webhook.DeliveryRecord) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status = ?, attempts = ?, last_attempt_at = ?, last_error = ?, next_retry_at = ?
		WHERE id = ?`,
		rec.Status, rec.Attempts, rec.LastAttemptAt, nullStr(rec.LastError),
		rec.NextRetryAt, rec.ID,
	)
	return err
}

func (s *Store) ListByTransactionID(ctx context.Context, txnID string) ([]*webhook.DeliveryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, transaction_id, url, payload, status, attempts, last_attempt_at, last_error, next_retry_at, created_at
		FROM webhook_deliveries
		WHERE transaction_id = ?
		ORDER BY created_at`, txnID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*webhook.DeliveryRecord
	for rows.Next() {
		rec, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanDelivery(rows *sql.Rows) (*webhook.DeliveryRecord, error) {
	var rec webhook.DeliveryRecord
	var lastAttempt sql.NullTime
	var lastError sql.NullString
	err := rows.Scan(
		&rec.ID, &rec.TransactionID, &rec.URL, &rec.Payload, &rec.Status,
		&rec.Attempts, &lastAttempt, &lastError, &rec.NextRetryAt, &rec.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastAttempt.Valid {
		rec.LastAttemptAt = &lastAttempt.Time
	}
	if lastError.Valid {
		rec.LastError = lastError.String
	}
	return &rec, nil
}

func repeatParam(n int) string {
	s := ""
	for range n {
		s += ", ?"
	}
	return s
}
