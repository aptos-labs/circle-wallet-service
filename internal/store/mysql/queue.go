package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// ClaimNextQueued implements store.Queue.
func (s *Store) ClaimNextQueued(ctx context.Context) (*store.TransactionRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var id, sender string
	err = tx.QueryRowContext(ctx, `
		SELECT id, sender_address FROM transactions
		WHERE status = ?
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE
	`, string(store.StatusQueued)).Scan(&id, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var dbNext sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT next_sequence FROM account_sequences WHERE sender_address = ? FOR UPDATE
	`, sender).Scan(&dbNext)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO account_sequences (sender_address, next_sequence) VALUES (?, 0)
		`, sender)
		if err != nil {
			return nil, err
		}
		dbNext = sql.NullInt64{Int64: 0, Valid: true}
	} else if err != nil {
		return nil, err
	}

	allocated := uint64(dbNext.Int64)

	_, err = tx.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = next_sequence + 1, updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?
	`, sender)
	if err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE transactions SET status = ?, sequence_number = ?, updated_at = UTC_TIMESTAMP(3) WHERE id = ?
	`, string(store.StatusProcessing), allocated, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true

	rec, err := s.Get(ctx, id)
	if err != nil || rec == nil {
		return nil, fmt.Errorf("load claimed record %s: %w", id, err)
	}
	return rec, nil
}

// ListQueuedSenders implements store.Queue.
func (s *Store) ListQueuedSenders(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sender_address FROM transactions
		WHERE status = ?
		GROUP BY sender_address
		ORDER BY MIN(created_at) ASC
	`, string(store.StatusQueued))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var senders []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		senders = append(senders, addr)
	}
	return senders, rows.Err()
}

// ClaimNextQueuedForSender implements store.Queue.
func (s *Store) ClaimNextQueuedForSender(ctx context.Context, senderAddress string) (*store.TransactionRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var id string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM transactions
		WHERE status = ? AND sender_address = ?
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE
	`, string(store.StatusQueued), senderAddress).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var dbNext sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT next_sequence FROM account_sequences WHERE sender_address = ? FOR UPDATE
	`, senderAddress).Scan(&dbNext)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO account_sequences (sender_address, next_sequence) VALUES (?, 0)
		`, senderAddress)
		if err != nil {
			return nil, err
		}
		dbNext = sql.NullInt64{Int64: 0, Valid: true}
	} else if err != nil {
		return nil, err
	}

	allocated := uint64(dbNext.Int64)

	_, err = tx.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = next_sequence + 1, updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?
	`, senderAddress)
	if err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE transactions SET status = ?, sequence_number = ?, updated_at = UTC_TIMESTAMP(3) WHERE id = ?
	`, string(store.StatusProcessing), allocated, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true

	rec, err := s.Get(ctx, id)
	if err != nil || rec == nil {
		return nil, fmt.Errorf("load claimed record %s: %w", id, err)
	}
	return rec, nil
}

// UpsertNextSequence implements store.Queue.
func (s *Store) UpsertNextSequence(ctx context.Context, senderAddress string, next uint64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO account_sequences (sender_address, next_sequence)
		VALUES (?, ?)
		ON DUPLICATE KEY UPDATE next_sequence = VALUES(next_sequence), updated_at = UTC_TIMESTAMP(3)
	`, senderAddress, next)
	return err
}

// ReconcileSequence implements store.Queue.
func (s *Store) ReconcileSequence(ctx context.Context, senderAddress string, chainSeq uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = GREATEST(next_sequence, ?), updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?`, chainSeq, senderAddress)
	return err
}

// RecoverStaleProcessing resets rows stuck in processing (e.g. worker crash) back to queued.
func (s *Store) RecoverStaleProcessing(ctx context.Context, olderThan time.Duration) (int64, error) {
	sec := int64(olderThan / time.Second)
	if sec < 1 {
		sec = 1
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE transactions
		SET status = ?, sequence_number = NULL, updated_at = UTC_TIMESTAMP(3)
		WHERE status = ? AND updated_at < (UTC_TIMESTAMP(3) - INTERVAL ? SECOND)
	`, string(store.StatusQueued), string(store.StatusProcessing), sec)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ShiftSenderSequences resets transactions with a sequence number higher than
// the failed one back to queued so they can be re-signed with correct sequences.
func (s *Store) ShiftSenderSequences(ctx context.Context, senderAddress string, failedSeqNum uint64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE transactions 
		SET sequence_number = NULL, status = 'queued', updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ? AND sequence_number > ? AND status IN ('queued', 'processing')
	`, senderAddress, failedSeqNum)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE account_sequences 
		SET next_sequence = GREATEST(next_sequence - ?, 0), updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?
	`, n, senderAddress)
	return err
}

var _ store.Queue = (*Store)(nil)
