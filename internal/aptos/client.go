package aptos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
)

// Client wraps the Aptos Go SDK with default gas and expiration settings.
// It exposes Inner for direct SDK access when needed (e.g. account info lookup).
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

// BuildTransaction builds a transaction wrapped as fee-payer
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

// BuildFeePayerTransaction builds a fee-payer transaction (sender = fee-payer).
// sequenceNumber is the Aptos account sequence to embed (managed by the caller / DB).
func (c *Client) BuildFeePayerTransaction(
	senderAddr aptossdk.AccountAddress,
	feePayerAddr aptossdk.AccountAddress,
	payload aptossdk.TransactionPayload,
	maxGasAmount uint64,
	sequenceNumber uint64,
) (*aptossdk.RawTransactionWithData, error) {
	gas := c.maxGasAmount
	if maxGasAmount > 0 {
		gas = maxGasAmount
	}

	rawTxn, err := c.Inner.BuildTransactionMultiAgent(senderAddr, payload,
		aptossdk.FeePayer(&feePayerAddr),
		aptossdk.SequenceNumber(sequenceNumber),
		aptossdk.ExpirationSeconds(c.expirationSec),
		aptossdk.MaxGasAmount(gas),
	)
	if err != nil {
		return nil, fmt.Errorf("build transaction: %w", err)
	}

	return rawTxn, nil
}

func (c *Client) SubmitTransaction(signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error) {
	return c.Inner.SubmitTransaction(signed)
}

func (c *Client) TransactionByHash(hash string) (*api.Transaction, error) {
	return c.Inner.TransactionByHash(hash)
}

var viewHTTPClient = &http.Client{Timeout: 30 * time.Second}

// View calls the Aptos /view endpoint with BCS-serialized arguments.
func (c *Client) View(ctx context.Context, functionID string, typeArgs []string, args [][]byte) ([]any, error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.nodeURL+"/view", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("build view request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := viewHTTPClient.Do(req)
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
