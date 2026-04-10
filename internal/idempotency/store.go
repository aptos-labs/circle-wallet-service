package idempotency

import (
	"sync"
	"time"
)

// CachedResponse stores a previously returned HTTP response for an
// idempotency key so that retried requests get the same answer.
type CachedResponse struct {
	StatusCode int
	Body       []byte
	CreatedAt  time.Time
}

// Store is a TTL-based in-memory cache keyed by idempotency key.
type Store struct {
	mu      sync.RWMutex
	entries map[string]*CachedResponse
	ttl     time.Duration
	done    chan struct{}
}

// NewStore creates an idempotency store that evicts entries after ttl.
func NewStore(ttl time.Duration) *Store {
	s := &Store{
		entries: make(map[string]*CachedResponse),
		ttl:     ttl,
		done:    make(chan struct{}),
	}
	go s.reaper()
	return s
}

// Get returns a cached response for the key, or nil if not found.
// The returned value is a deep copy so callers cannot mutate cached data.
func (s *Store) Get(key string) *CachedResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	resp, ok := s.entries[key]
	if !ok {
		return nil
	}
	bodyCopy := make([]byte, len(resp.Body))
	copy(bodyCopy, resp.Body)
	return &CachedResponse{
		StatusCode: resp.StatusCode,
		Body:       bodyCopy,
		CreatedAt:  resp.CreatedAt,
	}
}

// Set stores a response for the given idempotency key.
func (s *Store) Set(key string, statusCode int, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bodyCopy := make([]byte, len(body))
	copy(bodyCopy, body)
	s.entries[key] = &CachedResponse{
		StatusCode: statusCode,
		Body:       bodyCopy,
		CreatedAt:  time.Now(),
	}
}

// Close stops the background reaper.
func (s *Store) Close() {
	close(s.done)
}

func (s *Store) reaper() {
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

func (s *Store) evict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.ttl)
	for key, resp := range s.entries {
		if resp.CreatedAt.Before(cutoff) {
			delete(s.entries, key)
		}
	}
}
