package aptos

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
)

// defaultABICacheTTL is how long a cached module ABI is served before the next
// access triggers a refresh from the Aptos node.
const defaultABICacheTTL = 10 * time.Minute

// defaultABICacheMaxEntries caps the number of module ABIs retained in memory.
// The /v1/query endpoint accepts arbitrary addr::module pairs from untrusted
// input, so an unbounded map would let callers grow the cache without limit.
// A size-bounded LRU evicts the least-recently-used entry once the cap is
// reached; 512 comfortably holds all modules a typical integration touches.
const defaultABICacheMaxEntries = 512

// abiEntry is the info about a single move module. It also holds the list
// element pointer so we can move it to the front in O(1) on access.
type abiEntry struct {
	key       string
	module    *api.MoveModule
	fetchedAt time.Time
	elem      *list.Element
}

// ABICache fetches and caches Move module ABIs from the Aptos node so that
// argument types can be resolved at runtime without repeated network calls.
// Entries are evicted after a configurable TTL (default 10 minutes) to pick up
// contract upgrades, or when the LRU is at capacity.
type ABICache struct {
	// Guards modules + lru for concurrent access.
	mu         sync.Mutex
	modules    map[string]*abiEntry
	lru        *list.List // front = most recent; element.Value is *abiEntry
	client     aptos.AptosRpcClient
	ttl        time.Duration
	maxEntries int
}

// NewABICache creates a cache backed by the given RPC client.
func NewABICache(client aptos.AptosRpcClient) *ABICache {
	return &ABICache{
		modules:    make(map[string]*abiEntry, defaultABICacheMaxEntries),
		lru:        list.New(),
		client:     client,
		ttl:        defaultABICacheTTL,
		maxEntries: defaultABICacheMaxEntries,
	}
}

// GetFunctionParams returns the Move type strings for a function's parameters,
// fetching the module ABI from the node on first access. Signer parameters are
// included in the raw ABI and must be stripped by the caller.
func (c *ABICache) GetFunctionParams(addr *aptos.AccountAddress, module, function string) ([]string, error) {
	mod, err := c.getModule(*addr, module)
	if err != nil {
		return nil, err
	}
	return lookupFunction(mod, function)
}

// getModule returns the ABI for addr::module, serving from cache when possible
// and falling back to a node fetch on miss or TTL expiry. It is the single
// entry point that both GetFunctionParams and BuildEntryFunctionPayload go
// through, so a warm cache benefits every code path that resolves a Move
// module — previously BuildEntryFunctionPayload fetched unconditionally,
// burning a node round-trip per queued transaction.
func (c *ABICache) getModule(addr aptos.AccountAddress, module string) (*api.MoveModule, error) {
	key := addr.StringLong() + "::" + module

	// Fast path: live cache hit → promote and return.
	c.mu.Lock()
	if entry, ok := c.modules[key]; ok && time.Since(entry.fetchedAt) < c.ttl {
		c.lru.MoveToFront(entry.elem)
		mod := entry.module
		c.mu.Unlock()
		return mod, nil
	}
	c.mu.Unlock()

	// Slow path: fetch outside the lock so concurrent misses for other keys
	// aren't serialized behind a network round-trip.
	mod, err := c.fetchModule(addr, module)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.storeLocked(key, mod)
	c.mu.Unlock()
	return mod, nil
}

// storeLocked inserts/refreshes an entry and enforces the size cap.
// Caller must hold c.mu.
func (c *ABICache) storeLocked(key string, mod *api.MoveModule) {
	if existing, ok := c.modules[key]; ok {
		existing.module = mod
		existing.fetchedAt = time.Now()
		c.lru.MoveToFront(existing.elem)
		return
	}
	entry := &abiEntry{key: key, module: mod, fetchedAt: time.Now()}
	entry.elem = c.lru.PushFront(entry)
	c.modules[key] = entry
	// Evict LRU entries until we're under the cap. In steady state this
	// removes at most one entry per insert, but the loop guards against
	// future changes that might temporarily push us further over.
	for c.lru.Len() > c.maxEntries {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		victim := oldest.Value.(*abiEntry)
		c.lru.Remove(oldest)
		delete(c.modules, victim.key)
	}
}

func lookupFunction(m *api.MoveModule, name string) ([]string, error) {
	for _, fun := range m.ExposedFunctions {
		if fun.Name == name {
			return fun.Params, nil
		}
	}

	return nil, fmt.Errorf("function %q not found in module ABI", name)
}

func (c *ABICache) fetchModule(address aptos.AccountAddress, module string) (*api.MoveModule, error) {
	moduleData, err := c.client.AccountModule(address, module)
	if err != nil {
		return nil, fmt.Errorf("fetch module ABI: %w", err)
	}
	return moduleData.Abi, nil
}

// BuildEntryFunctionPayload parses functionId ("addr::mod::fn"), resolves the
// module ABI (cache-first), and constructs an [aptos.EntryFunction] with
// BCS-serialized arguments.
func (c *ABICache) BuildEntryFunctionPayload(functionId string, typeArgs []string, args []any) (*aptos.EntryFunction, error) {
	moduleAddr, moduleName, functionName, err := ParseFunctionID(functionId)
	if err != nil {
		return nil, fmt.Errorf("failed to parse functionID %w", err)
	}
	abi, err := c.getModule(*moduleAddr, moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch module abi %w", err)
	}

	typeArgsAny := ToAnySlice(typeArgs)

	entryFunction, err := aptos.EntryFunctionFromAbi(abi, *moduleAddr, moduleName, functionName, typeArgsAny, args)
	if err != nil {
		return nil, fmt.Errorf("failed to parse entry function %w", err)
	}
	return entryFunction, nil
}

func ToAnySlice[T any](slice []T) []any {
	ret := make([]any, len(slice))
	for i, v := range slice {
		ret[i] = v
	}
	return ret
}
