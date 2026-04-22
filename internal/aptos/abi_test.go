package aptos

import (
	"container/list"
	"testing"
	"time"

	"github.com/aptos-labs/aptos-go-sdk/api"
)

func newTestCache(maxEntries int, ttl time.Duration) *ABICache {
	return &ABICache{
		modules:    make(map[string]*abiEntry, maxEntries),
		lru:        list.New(),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

func TestABICache_StoreAndGetWithinTTL(t *testing.T) {
	t.Parallel()
	c := newTestCache(4, time.Minute)
	mod := &api.MoveModule{}
	c.mu.Lock()
	c.storeLocked("k1", mod)
	entry, ok := c.modules["k1"]
	length := c.lru.Len()
	c.mu.Unlock()

	if !ok || entry.module != mod {
		t.Fatalf("missing or wrong module under k1: %+v", entry)
	}
	if length != 1 {
		t.Fatalf("lru.Len() = %d, want 1", length)
	}
}

func TestABICache_EvictsOldestWhenFull(t *testing.T) {
	t.Parallel()
	c := newTestCache(3, time.Minute)
	c.mu.Lock()
	c.storeLocked("k1", &api.MoveModule{})
	c.storeLocked("k2", &api.MoveModule{})
	c.storeLocked("k3", &api.MoveModule{})
	// k1 is currently oldest; touching k2 keeps k1 as the LRU candidate.
	c.lru.MoveToFront(c.modules["k2"].elem)
	c.storeLocked("k4", &api.MoveModule{}) // should evict k1
	_, hasK1 := c.modules["k1"]
	present := map[string]bool{}
	for _, want := range []string{"k2", "k3", "k4"} {
		_, present[want] = c.modules[want]
	}
	length := c.lru.Len()
	c.mu.Unlock()

	if hasK1 {
		t.Fatalf("k1 should have been evicted")
	}
	for k, ok := range present {
		if !ok {
			t.Fatalf("%s missing", k)
		}
	}
	if length != 3 {
		t.Fatalf("lru.Len() = %d, want 3", length)
	}
}

func TestABICache_UpdateExistingRefreshesTimestamp(t *testing.T) {
	t.Parallel()
	c := newTestCache(4, time.Minute)
	first := &api.MoveModule{}
	second := &api.MoveModule{}
	c.mu.Lock()
	c.storeLocked("k1", first)
	fetchedFirst := c.modules["k1"].fetchedAt
	c.mu.Unlock()

	time.Sleep(5 * time.Millisecond)

	c.mu.Lock()
	c.storeLocked("k1", second)
	entry := c.modules["k1"]
	length := c.lru.Len()
	c.mu.Unlock()
	if entry.module != second {
		t.Fatalf("module not updated on re-store")
	}
	if !entry.fetchedAt.After(fetchedFirst) {
		t.Fatalf("fetchedAt not refreshed: %v !> %v", entry.fetchedAt, fetchedFirst)
	}
	if length != 1 {
		t.Fatalf("lru.Len() = %d, want 1 (no duplicate entry)", length)
	}
}

func TestABICache_BulkInsertHonorsCap(t *testing.T) {
	t.Parallel()
	c := newTestCache(4, time.Minute)
	c.mu.Lock()
	for i := range 50 {
		c.storeLocked(string(rune('a'+i)), &api.MoveModule{})
	}
	length := c.lru.Len()
	size := len(c.modules)
	c.mu.Unlock()
	if length != 4 || size != 4 {
		t.Fatalf("after 50 inserts with cap=4: lru.Len=%d, modules=%d", length, size)
	}
}
