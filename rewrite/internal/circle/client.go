package circle

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

type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	rsaKeyMu  sync.Mutex
	rsaKey     *rsa.PublicKey
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		baseURL:    "https://api.circle.com/v1/w3s",
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type WalletResponse struct {
	Data struct {
		Wallet struct {
			ID               string `json:"id"`
			Address          string `json:"address"`
			InitialPublicKey string `json:"initialPublicKey"`
		} `json:"wallet"`
	} `json:"data"`
}

func (c *Client) GetWallet(ctx context.Context, walletID string) (*WalletResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/wallets/"+walletID, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("circle API error (status %d): %s", resp.StatusCode, string(body))
	}
	var result WalletResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

func (c *Client) getRSAKey(ctx context.Context) (*rsa.PublicKey, error) {
	c.rsaKeyMu.Lock()
	defer c.rsaKeyMu.Unlock()
	if c.rsaKey != nil {
		return c.rsaKey, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/config/entity/publicKey", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("circle API error (status %d): %s", resp.StatusCode, string(body))
	}
	var keyResp struct {
		Data struct {
			PublicKey string `json:"publicKey"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &keyResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	block, _ := pem.Decode([]byte(keyResp.Data.PublicKey))
	if block == nil {
		return nil, fmt.Errorf("failed to decode Circle PEM key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse Circle public key: %w", err)
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("circle public key is not RSA")
	}
	c.rsaKey = rsaKey
	return rsaKey, nil
}

func (c *Client) EncryptEntitySecret(ctx context.Context, entitySecretHex string) (string, error) {
	secretBytes, err := hex.DecodeString(strings.TrimPrefix(entitySecretHex, "0x"))
	if err != nil {
		return "", fmt.Errorf("decode entity secret: %w", err)
	}
	rsaKey, err := c.getRSAKey(ctx)
	if err != nil {
		return "", err
	}
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaKey, secretBytes, nil)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

type SignTransactionRequest struct {
	WalletID               string `json:"walletId"`
	RawTransaction         string `json:"rawTransaction"`
	Memo                   string `json:"memo,omitempty"`
	EntitySecretCiphertext string `json:"entitySecretCiphertext"`
}

type SignTransactionResponse struct {
	Data struct {
		Signature         string `json:"signature,omitempty"`
		SignedTransaction string `json:"signedTransaction"`
		TxHash            string `json:"txHash,omitempty"`
	} `json:"data"`
}

// SignTransaction calls Circle's sign/transaction endpoint per the Aptos Signing APIs tutorial.
func (c *Client) SignTransaction(ctx context.Context, req *SignTransactionRequest) (*SignTransactionResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/developer/sign/transaction", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("circle sign/transaction error (status %d): %s", resp.StatusCode, string(body))
	}
	var result SignTransactionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
