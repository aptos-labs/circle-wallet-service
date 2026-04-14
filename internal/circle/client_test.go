package circle

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetWallet(t *testing.T) {
	const body = `{"data":{"wallet":{"id":"wid-1","address":"0x1","initialPublicKey":"0xdeadbeef"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/wallets/wid-1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	got, err := c.GetWallet(context.Background(), "wid-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Data.Wallet.ID != "wid-1" {
		t.Fatalf("ID: got %q", got.Data.Wallet.ID)
	}
	if got.Data.Wallet.Address != "0x1" {
		t.Fatalf("Address: got %q", got.Data.Wallet.Address)
	}
	if got.Data.Wallet.InitialPublicKey != "0xdeadbeef" {
		t.Fatalf("InitialPublicKey: got %q", got.Data.Wallet.InitialPublicKey)
	}
}

func TestGetWalletNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := testClient(t, srv)
	_, err := c.GetWallet(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error: %v", err)
	}
}

func TestGetWalletNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	c := &Client{
		apiKey:     "k",
		baseURL:    srv.URL,
		httpClient: http.DefaultClient,
	}
	_, err := c.GetWallet(context.Background(), "w")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "send request") {
		t.Fatalf("error: %v", err)
	}
}

func TestSignTransaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/developer/sign/transaction" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"signature":"0x` + strings.Repeat("aa", 64) + `"}}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	got, err := c.SignTransaction(context.Background(), &SignTransactionForDeveloperRequest{
		WalletID:               "w",
		RawTransaction:         "0x01",
		EntitySecretCiphertext: "abc",
		Memo:                   "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Data.Signature, "0x") {
		t.Fatalf("signature: %q", got.Data.Signature)
	}
}

func TestSignTransactionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":1}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := testClient(t, srv)
	_, err := c.SignTransaction(context.Background(), &SignTransactionForDeveloperRequest{
		WalletID: "w",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error: %v", err)
	}
}

func TestEncryptEntitySecret(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/entity/publicKey" {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{"data": map[string]string{"publicKey": pemStr}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	c := testClient(t, srv)
	out, err := c.EncryptEntitySecret(context.Background(), "0x0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("empty ciphertext")
	}

	_, err = c.EncryptEntitySecret(context.Background(), "not-hex")
	if err == nil {
		t.Fatal("expected decode error")
	}
}
