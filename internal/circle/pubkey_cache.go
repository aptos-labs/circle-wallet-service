package circle

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"
)

// PublicKeyCache lazily resolves and caches Ed25519 public keys for Circle
// wallets. Concurrent requests for the same wallet ID are coalesced via
// singleflight so that at most one Circle API call is made per wallet.
type PublicKeyCache struct {
	client *Client
	mu     sync.RWMutex
	keys   map[string]string
	group  singleflight.Group
}

func NewPublicKeyCache(client *Client) *PublicKeyCache {
	return &PublicKeyCache{
		client: client,
		keys:   make(map[string]string),
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
// from Circle on first access and caching for the lifetime of the process.
func (c *PublicKeyCache) Resolve(ctx context.Context, walletID string) (string, error) {
	c.mu.RLock()
	if pk, ok := c.keys[walletID]; ok {
		c.mu.RUnlock()
		return pk, nil
	}
	c.mu.RUnlock()

	v, err, _ := c.group.Do(walletID, func() (interface{}, error) {
		wallet, err := c.client.GetWallet(ctx, walletID)
		if err != nil {
			return "", err
		}
		raw := strings.TrimSpace(wallet.Data.Wallet.InitialPublicKey)
		if raw == "" {
			return "", fmt.Errorf("circle wallet %s has no initialPublicKey", walletID)
		}
		pk := ensure0xPrefix(raw)

		c.mu.Lock()
		if existing, ok := c.keys[walletID]; ok {
			c.mu.Unlock()
			return existing, nil
		}
		c.keys[walletID] = pk
		c.mu.Unlock()

		return pk, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// Set manually seeds the cache (useful in tests or when the key is already known).
func (c *PublicKeyCache) Set(walletID, publicKeyHex string) {
	pk := ensure0xPrefix(publicKeyHex)
	if pk == "" {
		return
	}

	c.mu.Lock()
	c.keys[walletID] = pk
	c.mu.Unlock()
}
