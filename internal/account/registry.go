package account

import (
	"fmt"
	"strings"

	"github.com/aptos-labs/jc-contract-integration/internal/signer"
)

// Registry maps signer addresses to their signers.
type Registry struct {
	signers map[string]signer.Signer // keyed by normalized address (lowercase, 0x-prefixed)
}

// NewRegistry creates an empty account registry.
func NewRegistry() *Registry {
	return &Registry{signers: make(map[string]signer.Signer)}
}

// Register adds a signer, keyed by its Aptos address.
func (r *Registry) Register(s signer.Signer) {
	addr := s.Address()
	r.signers[normalizeAddress(addr.String())] = s
}

// Get returns the signer for the given address.
func (r *Registry) Get(address string) (signer.Signer, error) {
	s, ok := r.signers[normalizeAddress(address)]
	if !ok {
		return nil, fmt.Errorf("no signer configured for address %q", address)
	}
	return s, nil
}

// Addresses returns all registered signer addresses.
func (r *Registry) Addresses() []string {
	out := make([]string, 0, len(r.signers))
	for addr := range r.signers {
		out = append(out, addr)
	}
	return out
}

// normalizeAddress lowercases and ensures 0x prefix for consistent map lookups.
func normalizeAddress(addr string) string {
	addr = strings.ToLower(addr)
	if !strings.HasPrefix(addr, "0x") {
		addr = "0x" + addr
	}
	return addr
}
