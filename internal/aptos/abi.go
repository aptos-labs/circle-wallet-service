package aptos

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// ABICache fetches and caches module ABIs from an Aptos node.
// Safe for concurrent use.
type ABICache struct {
	mu      sync.RWMutex
	modules map[string]*moduleABI // keyed by "addr::module"
	nodeURL string
	client  *http.Client
}

// moduleABI stores the parsed function signatures for a single module.
type moduleABI struct {
	Functions map[string][]string // function name → param type strings (signer params stripped)
}

// NewABICache creates a new ABI cache that fetches from the given Aptos node URL.
func NewABICache(nodeURL string) *ABICache {
	return &ABICache{
		modules: make(map[string]*moduleABI),
		nodeURL: strings.TrimRight(nodeURL, "/"),
		client:  &http.Client{},
	}
}

// GetFunctionParams returns the non-signer parameter type strings for the given entry function.
// Results are cached per module for the server's lifetime.
func (c *ABICache) GetFunctionParams(addr, module, function string) ([]string, error) {
	key := addr + "::" + module
	c.mu.RLock()
	mod, ok := c.modules[key]
	c.mu.RUnlock()
	if ok {
		return mod.lookupFunction(function)
	}

	// Cache miss — fetch the module ABI from the node.
	mod, err := c.fetchModule(addr, module)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.modules[key] = mod
	c.mu.Unlock()

	return mod.lookupFunction(function)
}

func (m *moduleABI) lookupFunction(name string) ([]string, error) {
	params, ok := m.Functions[name]
	if !ok {
		return nil, fmt.Errorf("function %q not found in module ABI", name)
	}
	return params, nil
}

// fetchModule retrieves the module ABI from the Aptos node REST API.
func (c *ABICache) fetchModule(addr, module string) (*moduleABI, error) {
	url := fmt.Sprintf("%s/accounts/%s/module/%s", c.nodeURL, addr, module)
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch module ABI: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch module ABI: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ABI struct {
			ExposedFunctions []struct {
				Name              string   `json:"name"`
				Params            []string `json:"params"`
				IsEntry           bool     `json:"is_entry"`
				IsView            bool     `json:"is_view"`
				GenericTypeParams []struct {
					Constraints []string `json:"constraints"`
				} `json:"generic_type_params"`
			} `json:"exposed_functions"`
		} `json:"abi"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read module ABI response: %w", err)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse module ABI: %w", err)
	}

	mod := &moduleABI{
		Functions: make(map[string][]string),
	}
	for _, fn := range result.ABI.ExposedFunctions {
		// Strip &signer params — they are filled by the transaction sender
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
