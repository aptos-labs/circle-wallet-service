package aptos

import (
	"fmt"
	"sync"

	"github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
)

// ABICache cahces the ABI so it can be looked up quickly without a lot of API calls
type ABICache struct {
	mu      sync.RWMutex
	modules map[string]*api.MoveModule
	client  aptos.AptosRpcClient
}

// NewABICache initializes the ABICache
func NewABICache(client aptos.AptosRpcClient) *ABICache {
	return &ABICache{
		modules: make(map[string]*api.MoveModule),
		client:  client,
	}
}

// GetFunctionParams gets all the function parameters from the ABI, going through the cache
func (c *ABICache) GetFunctionParams(addr *aptos.AccountAddress, module, function string) ([]string, error) {
	key := addr.StringLong() + "::" + module
	c.mu.RLock()
	mod, ok := c.modules[key]
	c.mu.RUnlock()
	if ok {
		return lookupFunction(mod, function)
	}
	mod, err := c.fetchModule(*addr, module)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.modules[key] = mod
	c.mu.Unlock()
	return lookupFunction(mod, function)
}

// lookupFunction searches for the function in the module
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
