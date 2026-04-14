package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

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
	if err != nil {
		return nil, fmt.Errorf("load claimed record %s: %w", id, err)
	}
	if rec == nil {
		return nil, fmt.Errorf("claimed record %s not found after commit", id)
	}
	return rec, nil
}

// ReconcileSequence implements store.Queue.
func (s *Store) ReconcileSequence(ctx context.Context, senderAddress string, chainSeq uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = GREATEST(next_sequence, ?), updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?`, chainSeq, senderAddress)
	return err
}

// RecoverStaleProcessing resets rows stuck in processing (e.g. worker crash) back to queued.
// It also decrements the sequence counter for each affected sender to avoid gaps.
func (s *Store) RecoverStaleProcessing(ctx context.Context, olderThan time.Duration) (int64, error) {
	sec := int64(olderThan / time.Second)
	if sec < 1 {
		sec = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.QueryContext(ctx, `
		SELECT sender_address, COUNT(*) FROM transactions
		WHERE status = ? AND sequence_number IS NOT NULL
		  AND updated_at < (UTC_TIMESTAMP(3) - INTERVAL ? SECOND)
		GROUP BY sender_address
		FOR UPDATE
	`, string(store.StatusProcessing), sec)
	if err != nil {
		return 0, err
	}
	senderCounts := make(map[string]int64)
	for rows.Next() {
		var addr string
		var cnt int64
		if err := rows.Scan(&addr, &cnt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		senderCounts[addr] = cnt
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE transactions
		SET status = ?, sequence_number = NULL, updated_at = UTC_TIMESTAMP(3)
		WHERE status = ? AND updated_at < (UTC_TIMESTAMP(3) - INTERVAL ? SECOND)
	`, string(store.StatusQueued), string(store.StatusProcessing), sec)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	for addr, cnt := range senderCounts {
		_, err := tx.ExecContext(ctx, `
			UPDATE account_sequences SET next_sequence = GREATEST(next_sequence - ?, 0), updated_at = UTC_TIMESTAMP(3)
			WHERE sender_address = ?
		`, cnt, addr)
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return affected, nil
}

// ShiftSenderSequences resets transactions with a sequence number higher than
// the failed one back to queued so they can be re-signed with correct sequences.
// Both the transaction reset and the sequence counter adjustment run in a single
// SQL transaction to prevent the counter from drifting if either statement fails.
func (s *Store) ShiftSenderSequences(ctx context.Context, senderAddress string, failedSeqNum uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `
		UPDATE transactions 
		SET sequence_number = NULL, status = ?, updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ? AND sequence_number > ? AND status IN (?, ?)
	`, string(store.StatusQueued), senderAddress, failedSeqNum, string(store.StatusQueued), string(store.StatusProcessing))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		_, err = tx.ExecContext(ctx, `
			UPDATE account_sequences 
			SET next_sequence = GREATEST(next_sequence - ?, 0), updated_at = UTC_TIMESTAMP(3)
			WHERE sender_address = ?
		`, n, senderAddress)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// ReleaseSequence decrements the sequence counter for a sender by 1,
// used when a claimed transaction is requeued before submission.
func (s *Store) ReleaseSequence(ctx context.Context, senderAddress string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = GREATEST(next_sequence - 1, 0), updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?
	`, senderAddress)
	return err
}

var _ store.Queue = (*Store)(nil)
