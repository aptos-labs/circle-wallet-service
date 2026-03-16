package account

import (
	"fmt"

	"github.com/aptos-labs/jc-contract-integration/internal/signer"
)

// Role constants for contract signer accounts.
const (
	RoleMinter          = "minter"
	RoleDenylister      = "denylister"
	RoleMasterMinter    = "master_minter"
	RoleMetadataUpdater = "metadata_updater"
	RoleOwner           = "owner"
)

// Registry maps roles to their signers.
type Registry struct {
	signers map[string]signer.Signer
}

// NewRegistry creates an empty account registry.
func NewRegistry() *Registry {
	return &Registry{signers: make(map[string]signer.Signer)}
}

// Register adds a signer for the given role.
func (r *Registry) Register(role string, s signer.Signer) {
	r.signers[role] = s
}

// Get returns the signer for the given role.
func (r *Registry) Get(role string) (signer.Signer, error) {
	s, ok := r.signers[role]
	if !ok {
		return nil, fmt.Errorf("no signer configured for role %q", role)
	}
	return s, nil
}
