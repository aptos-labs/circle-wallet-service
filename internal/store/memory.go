package store

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store implementation with TTL-based eviction.
type MemoryStore struct {
	mu             sync.RWMutex
	records        map[string]*TransactionRecord
	idempotencyIdx map[string]string // idempotency_key -> record ID
	ttl            time.Duration
	done           chan struct{}
}

// NewMemoryStore creates a MemoryStore and starts a background reaper goroutine
// that periodically evicts records older than the given TTL.
func NewMemoryStore(ttl time.Duration) *MemoryStore {
	s := &MemoryStore{
		records:        make(map[string]*TransactionRecord),
		idempotencyIdx: make(map[string]string),
		ttl:            ttl,
		done:           make(chan struct{}),
	}
	go s.reaper()
	return s
}

// Create stores a new transaction record. It returns an error if a record with
// the same ID already exists.
func (s *MemoryStore) Create(_ context.Context, rec *TransactionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.records[rec.ID]; exists {
		return fmt.Errorf("record with id %q already exists", rec.ID)
	}

	now := time.Now()
	cp := *rec
	cp.CreatedAt = now
	cp.UpdatedAt = now
	s.records[rec.ID] = &cp

	if cp.IdempotencyKey != "" {
		s.idempotencyIdx[cp.IdempotencyKey] = cp.ID
	}

	return nil
}

// Update replaces an existing record. It returns an error if the record is not found.
func (s *MemoryStore) Update(_ context.Context, rec *TransactionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, exists := s.records[rec.ID]
	if !exists {
		return fmt.Errorf("record with id %q not found", rec.ID)
	}

	// Clean up old idempotency index entry if the key changed.
	if old.IdempotencyKey != "" && old.IdempotencyKey != rec.IdempotencyKey {
		delete(s.idempotencyIdx, old.IdempotencyKey)
	}

	cp := *rec
	cp.UpdatedAt = time.Now()
	s.records[rec.ID] = &cp

	if cp.IdempotencyKey != "" {
		s.idempotencyIdx[cp.IdempotencyKey] = cp.ID
	}

	return nil
}

// Get returns a copy of the record with the given ID, or nil if not found.
func (s *MemoryStore) Get(_ context.Context, id string) (*TransactionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.records[id]
	if !ok {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

// GetByIdempotencyKey returns a copy of the record matching the given
// idempotency key, or nil if no record matches.
func (s *MemoryStore) GetByIdempotencyKey(_ context.Context, key string) (*TransactionRecord, error) {
	if key == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.idempotencyIdx[key]
	if !ok {
		return nil, nil
	}
	rec, ok := s.records[id]
	if !ok {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

// ListByStatus returns copies of all records matching the given status.
func (s *MemoryStore) ListByStatus(_ context.Context, status TxnStatus) ([]*TransactionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*TransactionRecord
	for _, rec := range s.records {
		if rec.Status == status {
			cp := *rec
			result = append(result, &cp)
		}
	}
	return result, nil
}

// Close stops the background reaper goroutine.
func (s *MemoryStore) Close() error {
	close(s.done)
	return nil
}

// reaper runs in a goroutine and periodically calls evict.
func (s *MemoryStore) reaper() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.evict()
		case <-s.done:
			return
		}
	}
}

// evict deletes records with CreatedAt older than the configured TTL.
func (s *MemoryStore) evict() {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-s.ttl)
	for id, rec := range s.records {
		if rec.CreatedAt.Before(cutoff) {
			if rec.IdempotencyKey != "" {
				delete(s.idempotencyIdx, rec.IdempotencyKey)
			}
			delete(s.records, id)
		}
	}
}
