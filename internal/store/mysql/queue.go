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
func (s *Store) ClaimNextQueued(ctx context.Context) (*store.TransactionRecord, uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
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
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
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
			return nil, 0, err
		}
		dbNext = sql.NullInt64{Int64: 0, Valid: true}
	} else if err != nil {
		return nil, 0, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE transactions SET status = ?, updated_at = UTC_TIMESTAMP(3) WHERE id = ?
	`, string(store.StatusProcessing), id)
	if err != nil {
		return nil, 0, err
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	committed = true

	rec, err := s.Get(ctx, id)
	if err != nil || rec == nil {
		return nil, 0, fmt.Errorf("load claimed record %s: %w", id, err)
	}
	return rec, uint64(dbNext.Int64), nil
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

// RecoverStaleProcessing resets rows stuck in processing (e.g. worker crash) back to queued.
func (s *Store) RecoverStaleProcessing(ctx context.Context, olderThan time.Duration) (int64, error) {
	sec := int64(olderThan / time.Second)
	if sec < 1 {
		sec = 1
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE transactions
		SET status = ?, updated_at = UTC_TIMESTAMP(3)
		WHERE status = ? AND updated_at < (UTC_TIMESTAMP(3) - INTERVAL ? SECOND)
	`, string(store.StatusQueued), string(store.StatusProcessing), sec)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

var _ store.Queue = (*Store)(nil)
