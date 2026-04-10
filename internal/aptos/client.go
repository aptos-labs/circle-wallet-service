package aptos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
)

type Client struct {
	Inner         *aptossdk.Client
	nodeURL       string
	expirationSec int64
	maxGasAmount  uint64
}

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
		nodeURL:       strings.TrimRight(nodeURL, "/"),
		expirationSec: expirationSec,
		maxGasAmount:  maxGasAmount,
	}, nil
}

// BuildTransaction builds an transaction wrapped as fee-payer
// (sender = fee-payer = senderAddr). Returns the RawTransactionWithData for signing.
func (c *Client) BuildTransaction(
	senderAddr aptossdk.AccountAddress,
	payload aptossdk.TransactionPayload,
	maxGasAmount uint64,
) (*aptossdk.RawTransaction, error) {
	gas := c.maxGasAmount
	if maxGasAmount > 0 {
		gas = maxGasAmount
	}

	rawTxn, err := c.Inner.BuildTransaction(senderAddr, payload,
		aptossdk.ExpirationSeconds(c.expirationSec),
		aptossdk.MaxGasAmount(gas),
	)
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}

	return rawTxn, nil
}

// BuildFeePayerTransaction builds an transaction wrapped as fee-payer
// (sender = fee-payer = senderAddr). Returns the RawTransactionWithData for signing.
func (c *Client) BuildFeePayerTransaction(
	senderAddr aptossdk.AccountAddress,
	feePayerAddr aptossdk.AccountAddress,
	payload aptossdk.TransactionPayload,
	maxGasAmount uint64,
) (*aptossdk.RawTransactionWithData, error) {
	gas := c.maxGasAmount
	if maxGasAmount > 0 {
		gas = maxGasAmount
	}

	rawTxn, err := c.Inner.BuildTransactionMultiAgent(senderAddr, payload,
		aptossdk.FeePayer(&feePayerAddr),
		aptossdk.ExpirationSeconds(c.expirationSec),
		aptossdk.MaxGasAmount(gas),
	)
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}

	return rawTxn, nil
}

// BuildOrderlessTransaction builds a transaction that uses a replay-protection
// nonce instead of an ordered sequence number. This allows transactions to be
// submitted in any order without maintaining a sequence counter.
func (c *Client) BuildOrderlessTransaction(
	senderAddr aptossdk.AccountAddress,
	entryFn *aptossdk.EntryFunction,
	replayNonce uint64,
	maxGasAmount uint64,
) (*aptossdk.RawTransaction, error) {
	gas := c.maxGasAmount
	if maxGasAmount > 0 {
		gas = maxGasAmount
	}

	payload := aptossdk.TransactionPayload{
		Payload: &aptossdk.TransactionInnerPayload{
			Payload: &aptossdk.TransactionInnerPayloadV1{
				Executable: aptossdk.TransactionExecutable{
					Inner: entryFn,
				},
				ExtraConfig: aptossdk.TransactionExtraConfig{
					Inner: &aptossdk.TransactionExtraConfigV1{
						MultisigAddress:       nil,
						ReplayProtectionNonce: &replayNonce,
					},
				},
			},
		},
	}

	rawTxn, err := c.Inner.BuildTransaction(senderAddr, payload,
		aptossdk.ExpirationSeconds(c.expirationSec),
		aptossdk.MaxGasAmount(gas),
	)
	if err != nil {
		return nil, fmt.Errorf("build orderless transaction: %w", err)
	}
	return rawTxn, nil
}

// BuildOrderlessFeePayerTransaction builds an orderless transaction with a
// fee-payer wrapper. It combines nonce-based replay protection with sponsored
// gas payment.
func (c *Client) BuildOrderlessFeePayerTransaction(
	senderAddr aptossdk.AccountAddress,
	feePayerAddr aptossdk.AccountAddress,
	entryFn *aptossdk.EntryFunction,
	replayNonce uint64,
	maxGasAmount uint64,
) (*aptossdk.RawTransactionWithData, error) {
	gas := c.maxGasAmount
	if maxGasAmount > 0 {
		gas = maxGasAmount
	}

	payload := aptossdk.TransactionPayload{
		Payload: &aptossdk.TransactionInnerPayload{
			Payload: &aptossdk.TransactionInnerPayloadV1{
				Executable: aptossdk.TransactionExecutable{
					Inner: entryFn,
				},
				ExtraConfig: aptossdk.TransactionExtraConfig{
					Inner: &aptossdk.TransactionExtraConfigV1{
						MultisigAddress:       nil,
						ReplayProtectionNonce: &replayNonce,
					},
				},
			},
		},
	}

	rawTxn, err := c.Inner.BuildTransactionMultiAgent(senderAddr, payload,
		aptossdk.FeePayer(&feePayerAddr),
		aptossdk.ExpirationSeconds(c.expirationSec),
		aptossdk.MaxGasAmount(gas),
	)
	if err != nil {
		return nil, fmt.Errorf("build orderless fee-payer transaction: %w", err)
	}
	return rawTxn, nil
}

func (c *Client) SubmitTransaction(signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error) {
	return c.Inner.SubmitTransaction(signed)
}

func (c *Client) TransactionByHash(hash string) (*api.Transaction, error) {
	return c.Inner.TransactionByHash(hash)
}

// View calls the Aptos /view endpoint with BCS-serialized arguments.
func (c *Client) View(functionID string, typeArgs []string, args [][]byte) ([]any, error) {
	hexArgs := make([]string, len(args))
	for i, b := range args {
		hexArgs[i] = "0x" + fmt.Sprintf("%x", b)
	}

	body := map[string]any{
		"function":       functionID,
		"type_arguments": typeArgs,
		"arguments":      hexArgs,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal view request: %w", err)
	}

	url := c.nodeURL + "/view"
	resp, err := http.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("view request: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read view response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("view error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result []any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse view response: %w", err)
	}
	return result, nil
}
