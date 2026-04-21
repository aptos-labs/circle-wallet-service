package aptos

import (
	"fmt"
	"sync"
	"time"

	"github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
)

// defaultABICacheTTL is how long a cached module ABI is served before the next
// access triggers a refresh from the Aptos node.
const defaultABICacheTTL = 10 * time.Minute

// abiEntry is the info about a single move module
type abiEntry struct {
	module    *api.MoveModule
	fetchedAt time.Time
}

// ABICache fetches and caches Move module ABIs from the Aptos node so that
// argument types can be resolved at runtime without repeated network calls.
// Entries are evicted after a configurable TTL (default 10 minutes) to pick up
// contract upgrades.
type ABICache struct {
	// Guards modules for concurrent access.
	mu      sync.RWMutex
	modules map[string]abiEntry
	client  aptos.AptosRpcClient
	ttl     time.Duration
}

// NewABICache creates a cache backed by the given RPC client.
func NewABICache(client aptos.AptosRpcClient) *ABICache {
	return &ABICache{
		modules: make(map[string]abiEntry),
		client:  client,
		ttl:     defaultABICacheTTL,
	}
}

// GetFunctionParams returns the Move type strings for a function's parameters,
// fetching the module ABI from the node on first access. Signer parameters are
// included in the raw ABI and must be stripped by the caller.
func (c *ABICache) GetFunctionParams(addr *aptos.AccountAddress, module, function string) ([]string, error) {
	key := addr.StringLong() + "::" + module
	c.mu.RLock()
	entry, ok := c.modules[key]
	c.mu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < c.ttl {
		return lookupFunction(entry.module, function)
	}
	mod, err := c.fetchModule(*addr, module)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.modules[key] = abiEntry{module: mod, fetchedAt: time.Now()}
	c.mu.Unlock()
	return lookupFunction(mod, function)
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

// BuildEntryFunctionPayload parses functionId ("addr::mod::fn"), fetches the
// module ABI, and constructs an [aptos.EntryFunction] with BCS-serialized arguments.
func (c *ABICache) BuildEntryFunctionPayload(functionId string, typeArgs []string, args []any) (*aptos.EntryFunction, error) {
	moduleAddr, moduleName, functionName, err := ParseFunctionID(functionId)
	if err != nil {
		return nil, fmt.Errorf("failed to parse functionID %w", err)
	}
	abi, err := c.fetchModule(*moduleAddr, moduleName)
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
