package aptos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/aptos-go-sdk/bcs"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

// Client wraps the Aptos Go SDK with default gas and expiration settings.
// It exposes Inner for direct SDK access when needed (e.g. account info lookup).
//
// When apiKey is non-empty it is sent as `Authorization: Bearer <apiKey>` on
// every request we make to the node: SDK calls (ABI fetch, account info,
// submit, transaction-by-hash), and the raw HTTP paths we own (/view and
// /transactions/simulate). Hitting the public testnet endpoint without a key
// runs into `40000 compute units per 300s` per-IP limits under throughput
// load, which manifests as 429s that cascade into test flakiness. Providing a
// key (e.g. a Geomi key URL's bearer) lifts that ceiling.
type Client struct {
	Inner         *aptossdk.Client
	nodeURL       string
	apiKey        string
	expirationSec int64
	maxGasAmount  uint64
}

// NewClient builds an Aptos client. Pass apiKey="" to send no auth header.
func NewClient(nodeURL string, chainID uint8, expirationSec int64, maxGasAmount uint64, apiKey string) (*Client, error) {
	sdkClient, err := aptossdk.NewClient(aptossdk.NetworkConfig{
		NodeUrl: nodeURL,
		ChainId: chainID,
	})
	if err != nil {
		return nil, fmt.Errorf("create aptos client: %w", err)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		sdkClient.SetHeader("Authorization", "Bearer "+apiKey)
	}
	return &Client{
		Inner:         sdkClient,
		nodeURL:       strings.TrimRight(nodeURL, "/"),
		apiKey:        apiKey,
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

// simulateHTTPClient is used for /transactions/simulate calls only. We own it
// because the SDK's simulate helpers demand a TransactionSigner (i.e. a private
// key) — this service delegates signing to Circle so we never hold the private
// key, only the public key. Instead we build the simulation authenticator
// ourselves with a zero signature and POST the BCS-encoded signed txn directly.
var simulateHTTPClient = &http.Client{Timeout: 30 * time.Second}

// SimulateFeePayerTransaction runs /transactions/simulate for the given raw
// fee-payer transaction. It hand-builds the simulation authenticators (zero
// signatures, real public keys) so no TransactionSigner / private key is
// required — simulation semantics on Aptos only require the public keys.
//
// Returns the decoded UserTransaction on success. Callers should inspect
// Success and VmStatus to decide whether to proceed to signing. Network and
// node errors are returned as-is so IsTransientSimulationError can classify
// them.
//
// senderPubKeyHex and feePayerPubKeyHex are 0x-prefixed Ed25519 public keys.
// For self-pay transactions pass the same value for both.
func (c *Client) SimulateFeePayerTransaction(
	ctx context.Context,
	rawTxn *aptossdk.RawTransactionWithData,
	senderPubKeyHex string,
	feePayerPubKeyHex string,
) (*api.UserTransaction, error) {
	senderAuth, err := buildSimAuth(senderPubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("sender sim auth: %w", err)
	}
	feePayerAuth, err := buildSimAuth(feePayerPubKeyHex)
	if err != nil {
		return nil, fmt.Errorf("fee payer sim auth: %w", err)
	}

	signedTxn, ok := rawTxn.ToFeePayerSignedTransaction(senderAuth, feePayerAuth, []crypto.AccountAuthenticator{})
	if !ok {
		return nil, errors.New("failed to assemble simulation transaction")
	}

	body, err := bcs.Serialize(signedTxn)
	if err != nil {
		return nil, fmt.Errorf("bcs serialize simulation: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.nodeURL+"/transactions/simulate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build simulate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x.aptos.signed_transaction+bcs")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := simulateHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("simulate request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read simulate response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Carry the HTTP status forward so IsTransientSimulationError can classify.
		return nil, &SimulateHTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var results []*api.UserTransaction
	if err := json.Unmarshal(respBody, &results); err != nil {
		return nil, fmt.Errorf("parse simulate response: %w", err)
	}
	if len(results) == 0 {
		return nil, errors.New("simulate returned empty result")
	}
	return results[0], nil
}

// buildSimAuth constructs a zero-signature Ed25519 AccountAuthenticator for
// simulation purposes. Mirrors crypto.Ed25519PrivateKey.SimulationAuthenticator
// but starts from just the public key (we never have the private key).
func buildSimAuth(pubKeyHex string) (*crypto.AccountAuthenticator, error) {
	if strings.TrimSpace(pubKeyHex) == "" {
		return nil, errors.New("public key is empty")
	}
	pk := &crypto.Ed25519PublicKey{}
	if err := pk.FromHex(pubKeyHex); err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	return &crypto.AccountAuthenticator{
		Variant: crypto.AccountAuthenticatorEd25519,
		Auth: &crypto.Ed25519Authenticator{
			PubKey: pk,
			Sig:    &crypto.Ed25519Signature{},
		},
	}, nil
}

// SimulateHTTPError is returned by SimulateFeePayerTransaction when the node
// responds with a non-2xx status. It carries the status code so callers can
// classify 503/429 as transient while treating 4xx as a real rejection.
type SimulateHTTPError struct {
	StatusCode int
	Body       string
}

func (e *SimulateHTTPError) Error() string {
	return fmt.Sprintf("simulate HTTP %d: %s", e.StatusCode, e.Body)
}

// IsSequenceVmStatus reports whether a simulation's VmStatus indicates a
// sequence-number mismatch with chain state (rather than a business-logic
// rejection like INSUFFICIENT_BALANCE).
//
// The Aptos VM surfaces three flavours: SEQUENCE_NUMBER_TOO_OLD (our counter
// is behind the chain — most common when an external submitter advanced the
// account), SEQUENCE_NUMBER_TOO_NEW (we skipped a slot — rare, usually from a
// bad reconcile), and SEQUENCE_NUMBER_TOO_BIG (seq beyond MAX_U64 window).
//
// All three are reconcilable: fetching the chain's current sequence and
// raising (or leaving) our counter accordingly lets the next attempt use a
// valid slot. They must NOT be treated as permanent failures — doing so
// leaves the local counter stuck while every subsequent submit hits the same
// wall.
func IsSequenceVmStatus(vmStatus string) bool {
	if vmStatus == "" {
		return false
	}
	s := strings.ToUpper(vmStatus)
	return strings.Contains(s, "SEQUENCE_NUMBER_TOO_OLD") ||
		strings.Contains(s, "SEQUENCE_NUMBER_TOO_NEW") ||
		strings.Contains(s, "SEQUENCE_NUMBER_TOO_BIG")
}

// IsTransientSimulationError returns true when the error from
// SimulateFeePayerTransaction indicates a retriable node-side problem — 429
// (rate limit), 503 (unavailable), or a connection/timeout. In these cases
// the submitter should requeue the record rather than marking it failed,
// because the simulation result is unknown, not "rejected".
func IsTransientSimulationError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *SimulateHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			return true
		}
		return false
	}
	// net.Error covers context deadline exceeded, DNS failures, connection
	// refused, TLS timeouts — all conditions where retrying may succeed.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// Fallback string sniff for SDK-wrapped errors that don't unwrap to a
	// net.Error (rare, but keeps the classifier robust against wrapping).
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "eof") {
		return true
	}
	return false
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
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

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
