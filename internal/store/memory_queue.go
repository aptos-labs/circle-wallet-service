package store

import (
	"context"
	"sort"
	"time"
)

// MemoryStore Queue methods — an in-memory implementation of the Queue
// interface. Semantics mirror internal/store/mysql/queue.go, but concurrency
// control is provided by the existing MemoryStore mutex instead of row-level
// locks. Intended for tests and local tooling; not production.

// ClaimNextQueuedForSender picks the oldest queued transaction for the sender,
// allocates the next sequence, and flips it to processing — atomically under
// the store mutex. Returns (nil, nil) if no queued rows exist for the sender.
func (s *MemoryStore) ClaimNextQueuedForSender(_ context.Context, senderAddress string) (*TransactionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidate *TransactionRecord
	for _, rec := range s.records {
		if rec.Status != StatusQueued || rec.SenderAddress != senderAddress {
			continue
		}
		if candidate == nil ||
			rec.CreatedAt.Before(candidate.CreatedAt) ||
			(rec.CreatedAt.Equal(candidate.CreatedAt) && rec.ID < candidate.ID) {
			candidate = rec
		}
	}
	if candidate == nil {
		return nil, nil
	}

	allocated := s.sequences[senderAddress]
	s.sequences[senderAddress] = allocated + 1

	candidate.Status = StatusProcessing
	candidate.SequenceNumber = &allocated
	candidate.UpdatedAt = time.Now()

	cp := *candidate
	if cp.SequenceNumber != nil {
		v := *cp.SequenceNumber
		cp.SequenceNumber = &v
	}
	return &cp, nil
}

// ListQueuedSenders returns the distinct sender addresses with queued work,
// ordered by the oldest queued row's CreatedAt (rough fairness).
func (s *MemoryStore) ListQueuedSenders(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	oldest := make(map[string]time.Time)
	for _, rec := range s.records {
		if rec.Status != StatusQueued {
			continue
		}
		if t, ok := oldest[rec.SenderAddress]; !ok || rec.CreatedAt.Before(t) {
			oldest[rec.SenderAddress] = rec.CreatedAt
		}
	}
	senders := make([]string, 0, len(oldest))
	for addr := range oldest {
		senders = append(senders, addr)
	}
	sort.Slice(senders, func(i, j int) bool {
		ti, tj := oldest[senders[i]], oldest[senders[j]]
		if ti.Equal(tj) {
			return senders[i] < senders[j]
		}
		return ti.Before(tj)
	})
	return senders, nil
}

// ReconcileSequence raises the sender's counter to chainSeq only if the
// counter is currently lower (one-directional up, matches MySQL's GREATEST).
func (s *MemoryStore) ReconcileSequence(_ context.Context, senderAddress string, chainSeq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequences[senderAddress] < chainSeq {
		s.sequences[senderAddress] = chainSeq
	}
	return nil
}

// ForceResetSequenceToChain snaps the counter down to
// chainSeq + count(submitted rows with sequence_number >= chainSeq).
// See the MySQL implementation for the detailed rationale — the +N preserves
// in-flight broadcasts so the reset cannot collide with them.
func (s *MemoryStore) ForceResetSequenceToChain(_ context.Context, senderAddress string, chainSeq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var inflight uint64
	for _, rec := range s.records {
		if rec.SenderAddress != senderAddress || rec.Status != StatusSubmitted || rec.SequenceNumber == nil {
			continue
		}
		if *rec.SequenceNumber >= chainSeq {
			inflight++
		}
	}
	s.sequences[senderAddress] = chainSeq + inflight
	return nil
}

// RecoverStaleProcessing resets processing rows whose UpdatedAt is older than
// olderThan back to queued, decrementing each affected sender's counter by the
// number of recovered rows. Rows with TxnHash set are excluded — they are
// owned by the poller's processing+hash recovery path (see Fix #1).
func (s *MemoryStore) RecoverStaleProcessing(_ context.Context, olderThan time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	senderCounts := make(map[string]int64)
	var affected int64
	for _, rec := range s.records {
		if rec.Status != StatusProcessing || rec.SequenceNumber == nil {
			continue
		}
		if rec.TxnHash != "" {
			continue
		}
		if !rec.UpdatedAt.Before(cutoff) {
			continue
		}
		rec.Status = StatusQueued
		rec.SequenceNumber = nil
		rec.UpdatedAt = time.Now()
		senderCounts[rec.SenderAddress]++
		affected++
	}
	for addr, cnt := range senderCounts {
		cur := s.sequences[addr]
		if uint64(cnt) >= cur {
			s.sequences[addr] = 0
		} else {
			s.sequences[addr] = cur - uint64(cnt)
		}
	}
	return affected, nil
}

// ShiftSenderSequences requeues all rows for the sender with sequence > failedSeqNum
// (from queued or processing), clears their sequence numbers, and decrements the
// counter by the number of affected rows. Bounded at zero.
func (s *MemoryStore) ShiftSenderSequences(_ context.Context, senderAddress string, failedSeqNum uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n uint64
	for _, rec := range s.records {
		if rec.SenderAddress != senderAddress || rec.SequenceNumber == nil {
			continue
		}
		if rec.Status != StatusQueued && rec.Status != StatusProcessing {
			continue
		}
		if *rec.SequenceNumber <= failedSeqNum {
			continue
		}
		rec.Status = StatusQueued
		rec.SequenceNumber = nil
		rec.UpdatedAt = time.Now()
		n++
	}
	if n > 0 {
		cur := s.sequences[senderAddress]
		if n >= cur {
			s.sequences[senderAddress] = 0
		} else {
			s.sequences[senderAddress] = cur - n
		}
	}
	return nil
}

// ReleaseSequence decrements the sender's counter by 1, bounded at zero. Used
// when a claimed transaction is returned to queued before submission.
func (s *MemoryStore) ReleaseSequence(_ context.Context, senderAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequences[senderAddress] > 0 {
		s.sequences[senderAddress]--
	}
	return nil
}

var _ Queue = (*MemoryStore)(nil)
