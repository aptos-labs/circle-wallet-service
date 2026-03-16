package txn

import (
	"testing"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

func TestValidTransitions(t *testing.T) {
	tests := []struct {
		from  store.TxnStatus
		to    store.TxnStatus
		valid bool
	}{
		{store.StatusPending, store.StatusSubmitted, true},
		{store.StatusPending, store.StatusFailed, true},
		{store.StatusPending, store.StatusConfirmed, false},
		{store.StatusSubmitted, store.StatusConfirmed, true},
		{store.StatusSubmitted, store.StatusFailed, true},
		{store.StatusSubmitted, store.StatusExpired, true},
		{store.StatusSubmitted, store.StatusPending, false},
		{store.StatusFailed, store.StatusPending, true},
		{store.StatusFailed, store.StatusPermanentlyFailed, true},
		{store.StatusFailed, store.StatusConfirmed, false},
		{store.StatusExpired, store.StatusPending, true},
		{store.StatusExpired, store.StatusPermanentlyFailed, true},
		{store.StatusConfirmed, store.StatusFailed, false},
		{store.StatusPermanentlyFailed, store.StatusPending, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.from)+"→"+string(tc.to), func(t *testing.T) {
			got := ValidTransition(tc.from, tc.to)
			if got != tc.valid {
				t.Errorf("ValidTransition(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.valid)
			}
		})
	}
}
