package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/go-sql-driver/mysql"
)

// Store implements store.Queue for MySQL.
type Store struct {
	db *sql.DB
}

// New creates a MySQL-backed store.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Close implements store.Store. The underlying *sql.DB is owned by the caller (e.g. cmd/server).
func (s *Store) Close() error {
	return nil
}

// Create inserts a new transaction row.
func (s *Store) Create(ctx context.Context, rec *store.TransactionRecord) error {
	var idemp any
	if strings.TrimSpace(rec.IdempotencyKey) != "" {
		idemp = rec.IdempotencyKey
	} else {
		idemp = nil
	}

	var maxGas sql.NullInt64
	if rec.MaxGasAmount != nil {
		maxGas = sql.NullInt64{Int64: int64(*rec.MaxGasAmount), Valid: true}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO transactions (
			id, sender_address, wallet_id, fee_payer_wallet_id, fee_payer_address,
			status, sequence_number, function_id, payload_json,
			max_gas_amount, idempotency_key, txn_hash, error_message, webhook_url, attempt_count, last_error,
			failure_kind, vm_status,
			created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.SenderAddress, rec.WalletID, nullStr(rec.FeePayerWalletID), nullStr(rec.FeePayerAddress),
		string(rec.Status), nullU64(rec.SequenceNumber),
		rec.FunctionID, rec.PayloadJSON, maxGas, idemp, nullStr(rec.TxnHash),
		nullStr(rec.ErrorMessage), nullStr(rec.WebhookURL), rec.AttemptCount, nullStr(rec.LastError),
		nullStr(rec.FailureKind), nullStr(rec.VmStatus),
		rec.CreatedAt, rec.UpdatedAt, rec.ExpiresAt,
	)
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1062 {
			return fmt.Errorf("%w", store.ErrIdempotencyConflict)
		}
		return err
	}
	return nil
}

// Update replaces a row by id.
func (s *Store) Update(ctx context.Context, rec *store.TransactionRecord) error {
	var maxGas sql.NullInt64
	if rec.MaxGasAmount != nil {
		maxGas = sql.NullInt64{Int64: int64(*rec.MaxGasAmount), Valid: true}
	}
	var idemp any
	if strings.TrimSpace(rec.IdempotencyKey) != "" {
		idemp = rec.IdempotencyKey
	} else {
		idemp = nil
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE transactions SET
			sender_address = ?, wallet_id = ?, fee_payer_wallet_id = ?, fee_payer_address = ?,
			status = ?, sequence_number = ?, function_id = ?, payload_json = ?,
			max_gas_amount = ?, idempotency_key = ?, txn_hash = ?, error_message = ?, webhook_url = ?,
			attempt_count = ?, last_error = ?, failure_kind = ?, vm_status = ?,
			updated_at = ?, expires_at = ?
		WHERE id = ?`,
		rec.SenderAddress, rec.WalletID, nullStr(rec.FeePayerWalletID), nullStr(rec.FeePayerAddress),
		string(rec.Status), nullU64(rec.SequenceNumber),
		rec.FunctionID, rec.PayloadJSON, maxGas, idemp, nullStr(rec.TxnHash),
		nullStr(rec.ErrorMessage), nullStr(rec.WebhookURL), rec.AttemptCount, nullStr(rec.LastError),
		nullStr(rec.FailureKind), nullStr(rec.VmStatus),
		rec.UpdatedAt, rec.ExpiresAt, rec.ID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("record with id %q not found", rec.ID)
	}
	return nil
}

// UpdateIfStatus atomically updates the record only if its current status matches expectedStatus.
// Returns true if the row was updated, false if another host already changed its status.
func (s *Store) UpdateIfStatus(ctx context.Context, rec *store.TransactionRecord, expected store.TxnStatus) (bool, error) {
	var maxGas sql.NullInt64
	if rec.MaxGasAmount != nil {
		maxGas = sql.NullInt64{Int64: int64(*rec.MaxGasAmount), Valid: true}
	}
	var idemp any
	if strings.TrimSpace(rec.IdempotencyKey) != "" {
		idemp = rec.IdempotencyKey
	} else {
		idemp = nil
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE transactions SET
			sender_address = ?, wallet_id = ?, fee_payer_wallet_id = ?, fee_payer_address = ?,
			status = ?, sequence_number = ?, function_id = ?, payload_json = ?,
			max_gas_amount = ?, idempotency_key = ?, txn_hash = ?, error_message = ?, webhook_url = ?,
			attempt_count = ?, last_error = ?, failure_kind = ?, vm_status = ?,
			updated_at = ?, expires_at = ?
		WHERE id = ? AND status = ?`,
		rec.SenderAddress, rec.WalletID, nullStr(rec.FeePayerWalletID), nullStr(rec.FeePayerAddress),
		string(rec.Status), nullU64(rec.SequenceNumber),
		rec.FunctionID, rec.PayloadJSON, maxGas, idemp, nullStr(rec.TxnHash),
		nullStr(rec.ErrorMessage), nullStr(rec.WebhookURL), rec.AttemptCount, nullStr(rec.LastError),
		nullStr(rec.FailureKind), nullStr(rec.VmStatus),
		rec.UpdatedAt, rec.ExpiresAt, rec.ID, string(expected),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Get returns a record by id.
func (s *Store) Get(ctx context.Context, id string) (*store.TransactionRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(idempotency_key, ''), status, COALESCE(txn_hash, ''), sender_address, wallet_id,
			COALESCE(fee_payer_wallet_id, ''), COALESCE(fee_payer_address, ''),
			COALESCE(payload_json, ''), sequence_number, max_gas_amount,
			COALESCE(error_message, ''), COALESCE(webhook_url, ''), attempt_count, COALESCE(last_error, ''),
			COALESCE(failure_kind, ''), COALESCE(vm_status, ''),
			created_at, updated_at, expires_at, function_id
		FROM transactions WHERE id = ?`, id)
	return scanRecord(row)
}

// GetByIdempotencyKey returns a record by idempotency key.
func (s *Store) GetByIdempotencyKey(ctx context.Context, key string) (*store.TransactionRecord, error) {
	if key == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(idempotency_key, ''), status, COALESCE(txn_hash, ''), sender_address, wallet_id,
			COALESCE(fee_payer_wallet_id, ''), COALESCE(fee_payer_address, ''),
			COALESCE(payload_json, ''), sequence_number, max_gas_amount,
			COALESCE(error_message, ''), COALESCE(webhook_url, ''), attempt_count, COALESCE(last_error, ''),
			COALESCE(failure_kind, ''), COALESCE(vm_status, ''),
			created_at, updated_at, expires_at, function_id
		FROM transactions WHERE idempotency_key = ?`, key)
	return scanRecord(row)
}

// ListByStatus returns records matching status.
func (s *Store) ListByStatus(ctx context.Context, status store.TxnStatus) ([]*store.TransactionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(idempotency_key, ''), status, COALESCE(txn_hash, ''), sender_address, wallet_id,
			COALESCE(fee_payer_wallet_id, ''), COALESCE(fee_payer_address, ''),
			COALESCE(payload_json, ''), sequence_number, max_gas_amount,
			COALESCE(error_message, ''), COALESCE(webhook_url, ''), attempt_count, COALESCE(last_error, ''),
			COALESCE(failure_kind, ''), COALESCE(vm_status, ''),
			created_at, updated_at, expires_at, function_id
		FROM transactions WHERE status = ?`, string(status))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	var out []*store.TransactionRecord
	for rows.Next() {
		rec, err := scanRecordRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanRecord(row *sql.Row) (*store.TransactionRecord, error) {
	var rec store.TransactionRecord
	var statusStr string
	var seq sql.NullInt64
	var maxGas sql.NullInt64
	var payload sql.NullString
	err := row.Scan(
		&rec.ID, &rec.IdempotencyKey, &statusStr, &rec.TxnHash, &rec.SenderAddress, &rec.WalletID,
		&rec.FeePayerWalletID, &rec.FeePayerAddress,
		&payload, &seq, &maxGas,
		&rec.ErrorMessage, &rec.WebhookURL, &rec.AttemptCount, &rec.LastError,
		&rec.FailureKind, &rec.VmStatus,
		&rec.CreatedAt, &rec.UpdatedAt, &rec.ExpiresAt, &rec.FunctionID,
	)
	rec.Status = store.TxnStatus(statusStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if payload.Valid {
		rec.PayloadJSON = payload.String
	}
	if seq.Valid {
		u := uint64(seq.Int64)
		rec.SequenceNumber = &u
	}
	if maxGas.Valid {
		u := uint64(maxGas.Int64)
		rec.MaxGasAmount = &u
	}
	return &rec, nil
}

func scanRecordRows(rows *sql.Rows) (*store.TransactionRecord, error) {
	var rec store.TransactionRecord
	var statusStr string
	var seq sql.NullInt64
	var maxGas sql.NullInt64
	var payload sql.NullString
	err := rows.Scan(
		&rec.ID, &rec.IdempotencyKey, &statusStr, &rec.TxnHash, &rec.SenderAddress, &rec.WalletID,
		&rec.FeePayerWalletID, &rec.FeePayerAddress,
		&payload, &seq, &maxGas,
		&rec.ErrorMessage, &rec.WebhookURL, &rec.AttemptCount, &rec.LastError,
		&rec.FailureKind, &rec.VmStatus,
		&rec.CreatedAt, &rec.UpdatedAt, &rec.ExpiresAt, &rec.FunctionID,
	)
	rec.Status = store.TxnStatus(statusStr)
	if err != nil {
		return nil, err
	}
	if payload.Valid {
		rec.PayloadJSON = payload.String
	}
	if seq.Valid {
		u := uint64(seq.Int64)
		rec.SequenceNumber = &u
	}
	if maxGas.Valid {
		u := uint64(maxGas.Int64)
		rec.MaxGasAmount = &u
	}
	return &rec, nil
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullU64(u *uint64) sql.NullInt64 {
	if u == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*u), Valid: true}
}
