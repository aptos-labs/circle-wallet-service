package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// nullTimeVal converts a time.Time to sql.NullTime. Zero time becomes NULL.
func nullTimeVal(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// timeFromNull converts sql.NullTime back to time.Time (zero if NULL).
func timeFromNull(nt sql.NullTime) time.Time {
	if nt.Valid {
		return nt.Time
	}
	return time.Time{}
}

// SQLiteStore implements Store backed by SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens a SQLite database at the given path and runs migrations.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite handles one writer at a time

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) CreateTransaction(ctx context.Context, rec *TransactionRecord) error {
	now := time.Now().UTC()
	rec.CreatedAt = now
	rec.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO transactions (id, operation_type, status, txn_hash, nonce, sender_address,
			attempt, max_retries, request_payload, error_message, created_at, updated_at, expires_at, retry_after)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.OperationType, rec.Status, rec.TxnHash, rec.Nonce, rec.SenderAddress,
		rec.Attempt, rec.MaxRetries, rec.RequestPayload, rec.ErrorMessage,
		rec.CreatedAt, rec.UpdatedAt, rec.ExpiresAt, nullTimeVal(rec.RetryAfter),
	)
	if err != nil {
		return fmt.Errorf("create transaction: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateTransaction(ctx context.Context, rec *TransactionRecord) error {
	rec.UpdatedAt = time.Now().UTC()

	_, err := s.db.ExecContext(ctx, `
		UPDATE transactions SET
			status = ?, txn_hash = ?, nonce = ?, attempt = ?,
			error_message = ?, updated_at = ?, expires_at = ?, retry_after = ?
		WHERE id = ?`,
		rec.Status, rec.TxnHash, rec.Nonce, rec.Attempt,
		rec.ErrorMessage, rec.UpdatedAt, rec.ExpiresAt, nullTimeVal(rec.RetryAfter), rec.ID,
	)
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetTransaction(ctx context.Context, id string) (*TransactionRecord, error) {
	rec := &TransactionRecord{}
	var retryAfter sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, operation_type, status, txn_hash, nonce, sender_address,
			attempt, max_retries, request_payload, error_message, created_at, updated_at, expires_at, retry_after
		FROM transactions WHERE id = ?`, id,
	).Scan(
		&rec.ID, &rec.OperationType, &rec.Status, &rec.TxnHash, &rec.Nonce, &rec.SenderAddress,
		&rec.Attempt, &rec.MaxRetries, &rec.RequestPayload, &rec.ErrorMessage,
		&rec.CreatedAt, &rec.UpdatedAt, &rec.ExpiresAt, &retryAfter,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get transaction: %w", err)
	}
	rec.RetryAfter = timeFromNull(retryAfter)
	return rec, nil
}

func (s *SQLiteStore) ListByStatus(ctx context.Context, status TxnStatus, limit int) ([]*TransactionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_type, status, txn_hash, nonce, sender_address,
			attempt, max_retries, request_payload, error_message, created_at, updated_at, expires_at, retry_after
		FROM transactions WHERE status = ?
		ORDER BY created_at ASC LIMIT ?`, status, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list by status: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRecords(rows)
}

func (s *SQLiteStore) ListRetryable(ctx context.Context, limit int) ([]*TransactionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_type, status, txn_hash, nonce, sender_address,
			attempt, max_retries, request_payload, error_message, created_at, updated_at, expires_at, retry_after
		FROM transactions
		WHERE status IN ('failed', 'expired') AND attempt < max_retries
		ORDER BY created_at ASC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list retryable: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRecords(rows)
}

func (s *SQLiteStore) ListPendingRetries(ctx context.Context, limit int) ([]*TransactionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_type, status, txn_hash, nonce, sender_address,
			attempt, max_retries, request_payload, error_message, created_at, updated_at, expires_at, retry_after
		FROM transactions
		WHERE status = 'pending' AND attempt > 0
			AND (retry_after IS NULL OR retry_after <= datetime('now'))
		ORDER BY created_at ASC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending retries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRecords(rows)
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func scanRecords(rows *sql.Rows) ([]*TransactionRecord, error) {
	var records []*TransactionRecord
	for rows.Next() {
		rec := &TransactionRecord{}
		var retryAfter sql.NullTime
		err := rows.Scan(
			&rec.ID, &rec.OperationType, &rec.Status, &rec.TxnHash, &rec.Nonce, &rec.SenderAddress,
			&rec.Attempt, &rec.MaxRetries, &rec.RequestPayload, &rec.ErrorMessage,
			&rec.CreatedAt, &rec.UpdatedAt, &rec.ExpiresAt, &retryAfter,
		)
		if err != nil {
			return nil, fmt.Errorf("scan record: %w", err)
		}
		rec.RetryAfter = timeFromNull(retryAfter)
		records = append(records, rec)
	}
	return records, rows.Err()
}
