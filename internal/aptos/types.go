package aptos

import (
	aptossdk "github.com/aptos-labs/aptos-go-sdk"
)

// ParseAddress parses a hex string into an AccountAddress.
func ParseAddress(s string) (aptossdk.AccountAddress, error) {
	var addr aptossdk.AccountAddress
	err := addr.ParseStringRelaxed(s)
	return addr, err
}
