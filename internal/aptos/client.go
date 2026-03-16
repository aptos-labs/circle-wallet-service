package aptos

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
)

// Client wraps the Aptos SDK client with orderless transaction building.
type Client struct {
	Inner         *aptossdk.Client
	expirationSec int64
	maxGasAmount  uint64
}

// NewClient creates a new Aptos client from a node URL and optional chain ID.
// expirationSec sets the on-chain transaction expiration window.
// maxGasAmount sets the default max gas per transaction.
func NewClient(nodeURL string, chainID uint8, expirationSec int64, maxGasAmount uint64) (*Client, error) {
	sdkClient, err := aptossdk.NewClient(aptossdk.NetworkConfig{
		NodeUrl: nodeURL,
		ChainId: chainID,
	})
	if err != nil {
		return nil, fmt.Errorf("create aptos client: %w", err)
	}
	return &Client{
		Inner:         sdkClient,
		expirationSec: expirationSec,
		maxGasAmount:  maxGasAmount,
	}, nil
}

// BuildOrderlessTransaction constructs a raw transaction using a random replay nonce
// instead of a sequence number, enabling parallel submission.
// If maxGasAmount > 0, it overrides the client's default.
func (c *Client) BuildOrderlessTransaction(
	sender aptossdk.AccountAddress,
	payload aptossdk.TransactionPayload,
	maxGasAmount uint64,
) (*aptossdk.RawTransaction, uint64, error) {
	nonce, err := randomNonce()
	if err != nil {
		return nil, 0, fmt.Errorf("generate nonce: %w", err)
	}
	rawTxn, err := c.BuildOrderlessTransactionWithNonce(sender, payload, nonce, maxGasAmount)
	if err != nil {
		return nil, 0, err
	}
	return rawTxn, nonce, nil
}

// BuildOrderlessTransactionWithNonce builds an orderless transaction with a specific nonce.
// If maxGasAmount > 0, it overrides the client's default.
func (c *Client) BuildOrderlessTransactionWithNonce(
	sender aptossdk.AccountAddress,
	payload aptossdk.TransactionPayload,
	nonce uint64,
	maxGasAmount uint64,
) (*aptossdk.RawTransaction, error) {
	// Extract the EntryFunction from the standard payload wrapper
	ef, ok := payload.Payload.(*aptossdk.EntryFunction)
	if !ok {
		return nil, fmt.Errorf("payload must be an EntryFunction, got %T", payload.Payload)
	}

	// Wrap with orderless config (replay protection nonce instead of sequence number)
	orderlessPayload := aptossdk.TransactionPayload{
		Payload: &aptossdk.TransactionInnerPayload{
			Payload: &aptossdk.TransactionInnerPayloadV1{
				Executable: aptossdk.TransactionExecutable{
					Inner: ef,
				},
				ExtraConfig: aptossdk.TransactionExtraConfig{
					Inner: &aptossdk.TransactionExtraConfigV1{
						ReplayProtectionNonce: &nonce,
					},
				},
			},
		},
	}

	gas := c.maxGasAmount
	if maxGasAmount > 0 {
		gas = maxGasAmount
	}

	rawTxn, err := c.Inner.BuildTransaction(sender, orderlessPayload,
		aptossdk.ExpirationSeconds(c.expirationSec),
		aptossdk.MaxGasAmount(gas),
	)
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}
	return rawTxn, nil
}

// SubmitTransaction delegates to the inner SDK client, satisfying the TxnClient interface.
func (c *Client) SubmitTransaction(signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error) {
	return c.Inner.SubmitTransaction(signed)
}

// TransactionByHash delegates to the inner SDK client, satisfying the TxnClient interface.
func (c *Client) TransactionByHash(hash string) (*api.Transaction, error) {
	return c.Inner.TransactionByHash(hash)
}

// randomNonce generates a cryptographically random uint64 for replay protection.
func randomNonce() (uint64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	// Mask off the high bit so the value fits in a signed int64.
	// database/sql rejects uint64 values >= 2^63 because SQLite uses
	// signed 64-bit integers. 63 bits of randomness is plenty for
	// replay-protection nonces.
	return binary.LittleEndian.Uint64(buf[:]) & 0x7FFFFFFFFFFFFFFF, nil
}
