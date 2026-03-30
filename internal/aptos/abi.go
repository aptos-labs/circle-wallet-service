package aptos

import (
	"fmt"
	"sync"

	"github.com/aptos-labs/aptos-go-sdk"
)

// ABICache cahces the ABI so it can be looked up quickly without a lot of API calls
type ABICache struct {
	mu      sync.RWMutex
	modules map[string]*moduleABI
	client  aptos.AptosRpcClient
}

// moduleABI is the definition of a module, but only the Functions matter for this case
type moduleABI struct {
	Functions map[string][]string
}

// NewABICache initializes the ABICache
func NewABICache(client aptos.AptosRpcClient) *ABICache {
	return &ABICache{
		modules: make(map[string]*moduleABI),
		client:  client,
	}
}

// GetFunctionParams gets all the function parameters from the ABI, going through the cache
func (c *ABICache) GetFunctionParams(addr, module, function string) ([]string, error) {
	key := addr + "::" + module
	c.mu.RLock()
	mod, ok := c.modules[key]
	c.mu.RUnlock()
	if ok {
		return mod.lookupFunction(function)
	}
	mod, err := c.fetchModule(addr, module)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.modules[key] = mod
	c.mu.Unlock()
	return mod.lookupFunction(function)
}

// lookupFunction searches for the function in the module
func (m *moduleABI) lookupFunction(name string) ([]string, error) {
	params, ok := m.Functions[name]
	if !ok {
		return nil, fmt.Errorf("function %q not found in module ABI", name)
	}
	return params, nil
}

func (c *ABICache) fetchModule(addr, module string) (*moduleABI, error) {
	// Load the account address
	address := aptos.AccountAddress{}
	err := address.ParseStringRelaxed(addr)
	if err != nil {
		return nil, err
	}
	moduleData, err := c.client.AccountModule(address, module)
	if err != nil {
		return nil, fmt.Errorf("fetch module ABI: %w", err)
	}

	mod := &moduleABI{Functions: make(map[string][]string)}

	// Cut out all the unnecessary parts
	for _, fn := range moduleData.Abi.ExposedFunctions {
		var params []string
		for _, p := range fn.Params {
			if p == "&signer" || p == "signer" {
				continue
			}
			params = append(params, p)
		}
		mod.Functions[fn.Name] = params
	}
	return mod, nil
}
