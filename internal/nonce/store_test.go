package nonce

import (
	"testing"
	"time"
)

func TestGenerate_Unique(t *testing.T) {
	s := NewStore(time.Minute)
	defer s.Close()

	seen := make(map[uint64]bool)
	for range 100 {
		n, err := s.Generate("0xabc")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if seen[n] {
			t.Fatalf("duplicate nonce %d", n)
		}
		seen[n] = true
	}
}

func TestIsUsed(t *testing.T) {
	s := NewStore(time.Minute)
	defer s.Close()

	n, err := s.Generate("0xabc")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !s.IsUsed("0xabc", n) {
		t.Fatal("expected nonce to be used")
	}

	if s.IsUsed("0xabc", n+1) {
		t.Fatal("unexpected nonce marked as used")
	}

	if s.IsUsed("0xother", n) {
		t.Fatal("nonce should not be used for a different address")
	}
}

func TestEviction(t *testing.T) {
	s := NewStore(50 * time.Millisecond)
	defer s.Close()

	n, err := s.Generate("0xabc")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !s.IsUsed("0xabc", n) {
		t.Fatal("expected nonce to be used before eviction")
	}

	time.Sleep(100 * time.Millisecond)
	s.evict()

	if s.IsUsed("0xabc", n) {
		t.Fatal("expected nonce to be evicted")
	}
}
