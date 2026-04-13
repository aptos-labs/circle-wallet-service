package nonce

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"
)

const maxGenerateAttempts = 4

type entry struct {
	createdAt time.Time
}

// Store tracks used nonces per Aptos address with automatic TTL expiry.
// The Aptos chain enforces that replay-protection nonces must be unique
// within a 60-second window per address; we keep nonces for a configurable
// TTL (should be >= the chain window) so the same nonce is never reused.
type Store struct {
	mu   sync.Mutex
	used map[string]map[uint64]entry // address -> nonce -> entry
	ttl  time.Duration
	done chan struct{}
}

// NewStore creates a nonce store that evicts entries older than ttl.
func NewStore(ttl time.Duration) *Store {
	s := &Store{
		used: make(map[string]map[uint64]entry),
		ttl:  ttl,
		done: make(chan struct{}),
	}
	go s.reaper()
	return s
}

// Generate creates a cryptographically random nonce for the given address
// and records it. Retries up to maxGenerateAttempts times on the extremely
// unlikely event of a collision.
func (s *Store) Generate(address string) (uint64, error) {
	for attempt := range maxGenerateAttempts {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return 0, fmt.Errorf("generate nonce: %w", err)
		}
		nonce := binary.LittleEndian.Uint64(buf[:])

		s.mu.Lock()
		addrMap, ok := s.used[address]
		if !ok {
			addrMap = make(map[uint64]entry)
			s.used[address] = addrMap
		}

		if _, exists := addrMap[nonce]; !exists {
			addrMap[nonce] = entry{createdAt: time.Now()}
			s.mu.Unlock()
			return nonce, nil
		}
		s.mu.Unlock()

		_ = attempt // consumed by range
	}

	return 0, fmt.Errorf("generate nonce: exhausted %d attempts for address %s", maxGenerateAttempts, address)
}

// IsUsed checks whether the given nonce has already been consumed for the address.
func (s *Store) IsUsed(address string, nonce uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addrMap, ok := s.used[address]; ok {
		_, used := addrMap[nonce]
		return used
	}
	return false
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
	for addr, addrMap := range s.used {
		for nonce, e := range addrMap {
			if e.createdAt.Before(cutoff) {
				delete(addrMap, nonce)
			}
		}
		if len(addrMap) == 0 {
			delete(s.used, addr)
		}
	}
}
