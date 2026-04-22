package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// ListQueuedSenders returns the distinct sender addresses that currently have
// at least one queued transaction, oldest-waiting-sender first.
//
// The dispatcher calls this each tick to know which per-sender workers to
// spawn. Ordering by MIN(created_at) gives rough fairness when the dispatcher
// is catching up from a backlog — the sender whose oldest queued txn is oldest
// gets served first.
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

// ClaimNextQueuedForSender atomically claims the oldest queued transaction for
// the given sender and allocates it the next sequence number.
//
// The entire operation runs in a single MySQL transaction:
//
//  1. SELECT … FOR UPDATE picks the oldest queued row (by created_at, id) and
//     locks it so no other worker can double-claim.
//  2. SELECT next_sequence FROM account_sequences FOR UPDATE locks the per-sender
//     counter row (or creates it at 0 on first use).
//  3. The counter is incremented.
//  4. The transaction row is flipped to status=processing with the allocated
//     sequence_number.
//
// Returns (nil, nil) if no queued rows exist for this sender.
//
// Invariant: every row in status=processing or status=submitted has a
// sequence_number equal to the value of account_sequences.next_sequence at the
// moment it was claimed. The counter is monotonically non-decreasing under
// normal operation; failure paths (ReleaseSequence, ShiftSenderSequences,
// RecoverStaleProcessing) decrement it to close gaps when rows go back to
// queued without having been submitted.
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

// ReconcileSequence raises the per-sender counter to the given on-chain
// sequence if the counter has fallen behind.
//
// Called when the Aptos node returns a sequence-number error or when the
// pipeline drains after a failure. GREATEST is deliberately one-directional
// up: we never lower the counter based on chain state, because txns we just
// submitted may not yet be indexed on the node we're querying, and lowering
// would cause a duplicate-sequence conflict on the next submit.
func (s *Store) ReconcileSequence(ctx context.Context, senderAddress string, chainSeq uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = GREATEST(next_sequence, ?), updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?`, chainSeq, senderAddress)
	return err
}

// ForceResetSequenceToChain snaps the per-sender counter down to chain truth.
//
// Background. ReconcileSequence is GREATEST-only, which is correct when the
// chain is AHEAD of our counter (most common — an external submitter
// advanced the account). When the chain is BEHIND our counter — which
// happens if we've burned sequences on retries without ever landing one on
// chain (e.g. repeated SEQUENCE_NUMBER_TOO_NEW simulations) — GREATEST is a
// no-op and the counter stays stuck. Every subsequent claim then allocates
// an even-further-ahead sequence, simulate rejects again, and the submitter
// loops forever never advancing.
//
// This method resets the counter to:
//
//	chainSeq + count(rows for sender where status='submitted' AND sequence_number >= chainSeq)
//
// The +N is the number of transactions we've broadcast that the chain
// hasn't indexed yet. Lowering to chainSeq alone would let the next claim
// collide with an in-flight submit; including the pending count keeps
// sequences monotonic across the reset.
//
// Runs in a single SQL transaction so the count-of-submitted and the counter
// update are consistent even under concurrent worker activity.
func (s *Store) ForceResetSequenceToChain(ctx context.Context, senderAddress string, chainSeq uint64) error {
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

	var inflight uint64
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transactions
		WHERE sender_address = ?
		  AND status = ?
		  AND sequence_number IS NOT NULL
		  AND sequence_number >= ?
	`, senderAddress, string(store.StatusSubmitted), chainSeq).Scan(&inflight)
	if err != nil {
		return err
	}

	target := chainSeq + inflight
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = ?, updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?
	`, target, senderAddress); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// RecoverStaleProcessing resets rows stuck in processing (e.g. worker crash)
// back to queued. It also decrements the sequence counter for each affected
// sender to avoid gaps.
//
// Called periodically by submitter.recoverLoop. A row is considered stale if
// its updated_at is older than olderThan — long enough that a healthy worker
// would have moved it forward by now. The threshold has to be long enough to
// survive a slow Circle signing round-trip but short enough that a crashed
// worker's work becomes available to the next dispatcher tick within a
// reasonable window.
//
// Rows with txn_hash set are deliberately excluded. A processing row with a
// hash means the submitter pre-persisted the hash, broadcast to chain, but
// its post-submit status flip failed mid-flight. Reverting such a row to
// queued would clear its sequence and cause the next worker to re-sign at a
// different sequence number — a duplicate on-chain broadcast. The poller's
// processing+hash recovery path owns those rows.
//
// The counter decrement is bounded at zero (GREATEST(… - ?, 0)) to survive the
// pathological case where the counter has already been reconciled with chain
// state that's lower than the recovered count.
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
		  AND (txn_hash IS NULL OR txn_hash = '')
		  AND updated_at < (UTC_TIMESTAMP(3) - INTERVAL ? SECOND)
		GROUP BY sender_address
		FOR UPDATE
	`, string(store.StatusProcessing), sec)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = rows.Close()
	}()
	senderCounts := make(map[string]int64)
	for rows.Next() {
		var addr string
		var cnt int64
		if err := rows.Scan(&addr, &cnt); err != nil {
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
		WHERE status = ?
		  AND (txn_hash IS NULL OR txn_hash = '')
		  AND updated_at < (UTC_TIMESTAMP(3) - INTERVAL ? SECOND)
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

// ReleaseSequence decrements the sequence counter for a sender by 1.
//
// Called when a claimed transaction is requeued before submission — e.g. on
// transient signing failure in submitter.requeueTransient. The caller must
// have already cleared the transaction row's sequence_number; this function
// only touches the counter.
//
// Bounded at zero to survive double-releases or sequence-reconcile races.
func (s *Store) ReleaseSequence(ctx context.Context, senderAddress string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE account_sequences SET next_sequence = GREATEST(next_sequence - 1, 0), updated_at = UTC_TIMESTAMP(3)
		WHERE sender_address = ?
	`, senderAddress)
	return err
}

var _ store.Queue = (*Store)(nil)
