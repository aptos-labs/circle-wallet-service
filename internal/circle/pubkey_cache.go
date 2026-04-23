package circle

import (
	"container/list"
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
)

// defaultPublicKeyCacheMaxEntries caps the number of wallet public keys kept
// in memory. The cache is keyed by wallet ID, which comes from caller-supplied
// transaction payloads — an attacker with /v1/execute access could keep
// throwing fresh wallet IDs at us and grow the map without bound (each miss
// would still hit Circle and fail, but the key string + empty slot would
// stick around forever). A size-bounded LRU puts a ceiling on how much
// untrusted keyspace can accumulate; 1024 comfortably holds a typical fleet
// while staying well under a megabyte of memory.
const defaultPublicKeyCacheMaxEntries = 1024

type pubkeyEntry struct {
	walletID  string
	publicKey string
	elem      *list.Element
}

// PublicKeyCache lazily resolves and caches Ed25519 public keys for Circle
// wallets. Concurrent requests for the same wallet ID are coalesced via
// singleflight so that at most one Circle API call is made per wallet.
//
// Capacity is bounded by a size-based LRU (see defaultPublicKeyCacheMaxEntries).
type PublicKeyCache struct {
	client     *Client
	mu         sync.Mutex
	entries    map[string]*pubkeyEntry
	lru        *list.List // front = most recent
	maxEntries int
	group      singleflight.Group
}

func NewPublicKeyCache(client *Client) *PublicKeyCache {
	return &PublicKeyCache{
		client:     client,
		entries:    make(map[string]*pubkeyEntry, defaultPublicKeyCacheMaxEntries),
		lru:        list.New(),
		maxEntries: defaultPublicKeyCacheMaxEntries,
	}
}

func ensure0xPrefix(hex string) string {
	hex = strings.TrimSpace(hex)
	if hex == "" {
		return ""
	}
	if strings.HasPrefix(hex, "0x") || strings.HasPrefix(hex, "0X") {
		return hex
	}
	return "0x" + hex
}

// Resolve returns the 0x-prefixed Ed25519 public key for walletID, fetching
// from Circle on first access and caching for the lifetime of the process
// (subject to LRU eviction).
func (c *PublicKeyCache) Resolve(ctx context.Context, walletID string) (string, error) {
	if pk, ok := c.lookup(walletID); ok {
		return pk, nil
	}

	v, err, _ := c.group.Do(walletID, func() (any, error) {
		wallet, err := c.client.GetWallet(ctx, walletID)
		if err != nil {
			return "", err
		}
		raw := strings.TrimSpace(wallet.Data.Wallet.InitialPublicKey)
		if raw == "" {
			return "", fmt.Errorf("circle wallet %s has no initialPublicKey", walletID)
		}
		pk := ensure0xPrefix(raw)
		c.store(walletID, pk)
		return pk, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// lookup returns a cached public key and promotes its LRU position. Misses
// return ok=false.
func (c *PublicKeyCache) lookup(walletID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[walletID]
	if !ok {
		return "", false
	}
	c.lru.MoveToFront(entry.elem)
	return entry.publicKey, true
}

// store inserts or refreshes a cache entry and evicts LRU victims if the
// size cap is exceeded.
func (c *PublicKeyCache) store(walletID, publicKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[walletID]; ok {
		existing.publicKey = publicKey
		c.lru.MoveToFront(existing.elem)
		return
	}
	entry := &pubkeyEntry{walletID: walletID, publicKey: publicKey}
	entry.elem = c.lru.PushFront(entry)
	c.entries[walletID] = entry
	// Evict LRU entries until we're back under the cap.
	for c.lru.Len() > c.maxEntries {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		victim := oldest.Value.(*pubkeyEntry)
		c.lru.Remove(oldest)
		delete(c.entries, victim.walletID)
	}
}

// Set manually seeds the cache (useful in tests or when the key is already known).
func (c *PublicKeyCache) Set(walletID, publicKeyHex string) {
	pk := ensure0xPrefix(publicKeyHex)
	if pk == "" {
		return
	}
	c.store(walletID, pk)
}
