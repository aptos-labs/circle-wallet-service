package idempotency

import (
	"testing"
	"time"
)

func TestSetAndGet(t *testing.T) {
	s := NewStore(time.Minute)
	defer s.Close()

	body := []byte(`{"transaction_id":"abc","status":"submitted"}`)
	s.Set("key-1", 202, body)

	got := s.Get("key-1")
	if got == nil {
		t.Fatal("expected cached response")
	}
	if got.StatusCode != 202 {
		t.Errorf("StatusCode = %d, want 202", got.StatusCode)
	}
	if string(got.Body) != string(body) {
		t.Errorf("Body = %q, want %q", got.Body, body)
	}
}

func TestGetMiss(t *testing.T) {
	s := NewStore(time.Minute)
	defer s.Close()

	got := s.Get("nonexistent")
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestEviction(t *testing.T) {
	s := NewStore(50 * time.Millisecond)
	defer s.Close()

	s.Set("key-1", 200, []byte("ok"))

	if s.Get("key-1") == nil {
		t.Fatal("expected cached response before eviction")
	}

	time.Sleep(100 * time.Millisecond)
	s.evict()

	if s.Get("key-1") != nil {
		t.Fatal("expected eviction")
	}
}

func TestIsolation(t *testing.T) {
	s := NewStore(time.Minute)
	defer s.Close()

	body := []byte(`{"data":"original"}`)
	s.Set("key-1", 200, body)

	got := s.Get("key-1")
	got.Body[0] = 'X'

	got2 := s.Get("key-1")
	if got2.Body[0] == 'X' {
		t.Fatal("mutation leaked through to stored copy")
	}
}
