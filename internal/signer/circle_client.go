package signer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CircleClient is an HTTP client for the Circle Programmable Wallets API.
// It caches Circle's RSA public key so that per-request entity secret encryption
// (required by Circle's replay protection) only does local crypto after the first call.
type CircleClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client

	rsaKeyMu sync.Mutex
	rsaKey   *rsa.PublicKey
}

// NewCircleClient creates a new Circle API client.
func NewCircleClient(apiKey string) *CircleClient {
	return &CircleClient{
		apiKey:  apiKey,
		baseURL: "https://api.circle.com/v1/w3s",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// WalletResponse is the response from the get-wallet endpoint.
type WalletResponse struct {
	Data struct {
		Wallet struct {
			InitialPublicKey string `json:"initialPublicKey"`
		} `json:"wallet"`
	} `json:"data"`
}

// GetWallet fetches a wallet by ID from Circle Programmable Wallets.
// The returned InitialPublicKey is a hex string without a 0x prefix.
func (c *CircleClient) GetWallet(ctx context.Context, walletID string) (*WalletResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/wallets/"+walletID, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("circle API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result WalletResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// entityPublicKeyResponse is the response from the get-entity-public-key endpoint.
type entityPublicKeyResponse struct {
	Data struct {
		PublicKey string `json:"publicKey"`
	} `json:"data"`
}

// getRSAKey fetches and caches Circle's RSA public key. The key is fetched once
// and reused for all subsequent encryptions (it rarely rotates).
func (c *CircleClient) getRSAKey(ctx context.Context) (*rsa.PublicKey, error) {
	c.rsaKeyMu.Lock()
	defer c.rsaKeyMu.Unlock()

	if c.rsaKey != nil {
		return c.rsaKey, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/config/entity/publicKey", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("circle API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var keyResp entityPublicKeyResponse
	if err := json.Unmarshal(respBody, &keyResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	block, _ := pem.Decode([]byte(keyResp.Data.PublicKey))
	if block == nil {
		return nil, fmt.Errorf("failed to decode Circle public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse Circle public key: %w", err)
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("Circle public key is not RSA")
	}

	c.rsaKey = rsaKey
	return rsaKey, nil
}

// EncryptEntitySecret returns the entity secret encrypted with Circle's RSA public key
// (base64-encoded). The RSA key is cached after the first call, but each encryption
// produces unique ciphertext due to RSA-OAEP random padding — this satisfies Circle's
// requirement for fresh ciphertext per API request.
func (c *CircleClient) EncryptEntitySecret(ctx context.Context, entitySecretHex string) (string, error) {
	secretBytes, err := hex.DecodeString(strings.TrimPrefix(entitySecretHex, "0x"))
	if err != nil {
		return "", fmt.Errorf("decode entity secret hex: %w", err)
	}

	rsaKey, err := c.getRSAKey(ctx)
	if err != nil {
		return "", fmt.Errorf("get Circle RSA key: %w", err)
	}

	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaKey, secretBytes, nil)
	if err != nil {
		return "", fmt.Errorf("encrypt entity secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// SignMessageRequest is the request body for the sign-message endpoint.
// Either WalletID or (WalletAddress + Blockchain) must be set.
type SignMessageRequest struct {
	WalletID               string `json:"walletId,omitempty"`
	WalletAddress          string `json:"walletAddress,omitempty"`
	Blockchain             string `json:"blockchain,omitempty"`
	Message                string `json:"message"`
	EntitySecretCiphertext string `json:"entitySecretCiphertext"`
	EncodedByHex           bool   `json:"encodedByHex"`
}

// SignMessageResponse is the response from the sign-message endpoint.
type SignMessageResponse struct {
	Data struct {
		Signature string `json:"signature"`
	} `json:"data"`
}

// SignMessage sends a sign-message request to Circle Programmable Wallets.
func (c *CircleClient) SignMessage(ctx context.Context, req *SignMessageRequest) (*SignMessageResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/developer/sign/message", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("circle API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result SignMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// SignTransactionRequest is the request body for the sign-transaction endpoint.
type SignTransactionRequest struct {
	WalletID               string `json:"walletId,omitempty"`
	WalletAddress          string `json:"walletAddress,omitempty"`
	Blockchain             string `json:"blockchain,omitempty"`
	RawTransaction         string `json:"rawTransaction"`
	EntitySecretCiphertext string `json:"entitySecretCiphertext"`
}

// SignTransactionResponse is the response from the sign-transaction endpoint.
type SignTransactionResponse struct {
	Data struct {
		Signature         string `json:"signature"`
		SignedTransaction string `json:"signedTransaction"`
	} `json:"data"`
}

// SignTransaction sends a sign-transaction request to Circle Programmable Wallets.
func (c *CircleClient) SignTransaction(ctx context.Context, req *SignTransactionRequest) (*SignTransactionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/developer/sign/transaction", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("circle API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result SignTransactionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
