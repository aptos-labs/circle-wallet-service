package aptos

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type ABICache struct {
	mu      sync.RWMutex
	modules map[string]*moduleABI
	nodeURL string
	client  *http.Client
}

type moduleABI struct {
	Functions map[string][]string
}

func NewABICache(nodeURL string) *ABICache {
	return &ABICache{
		modules: make(map[string]*moduleABI),
		nodeURL: strings.TrimRight(nodeURL, "/"),
		client:  &http.Client{},
	}
}

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

func (m *moduleABI) lookupFunction(name string) ([]string, error) {
	params, ok := m.Functions[name]
	if !ok {
		return nil, fmt.Errorf("function %q not found in module ABI", name)
	}
	return params, nil
}

func (c *ABICache) fetchModule(addr, module string) (*moduleABI, error) {
	url := fmt.Sprintf("%s/accounts/%s/module/%s", c.nodeURL, addr, module)
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch module ABI: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch module ABI: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		ABI struct {
			ExposedFunctions []struct {
				Name   string   `json:"name"`
				Params []string `json:"params"`
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
	mod := &moduleABI{Functions: make(map[string][]string)}
	for _, fn := range result.ABI.ExposedFunctions {
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
