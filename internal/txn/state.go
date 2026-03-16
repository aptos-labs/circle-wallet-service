package txn

import "github.com/aptos-labs/jc-contract-integration/internal/store"

// ValidTransition checks if a status transition is allowed.
func ValidTransition(from, to store.TxnStatus) bool {
	switch from {
	case store.StatusPending:
		return to == store.StatusSubmitted || to == store.StatusFailed
	case store.StatusSubmitted:
		return to == store.StatusConfirmed || to == store.StatusFailed || to == store.StatusExpired
	case store.StatusFailed, store.StatusExpired:
		// Retryable: can go back to pending (re-attempted) or permanently failed
		return to == store.StatusPending || to == store.StatusPermanentlyFailed
	default:
		return false
	}
}
