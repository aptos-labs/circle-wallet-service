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

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

func circleMockRSAHandler(t *testing.T, priv *rsa.PrivateKey) http.HandlerFunc {
	t.Helper()
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	sigHex := "0x" + strings.Repeat("ab", 64)

	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config/entity/publicKey":
			resp := map[string]any{"data": map[string]string{"publicKey": pemStr}}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)
		case r.Method == http.MethodPost && r.URL.Path == "/developer/sign/transaction":
			_, _ = w.Write([]byte(`{"data":{"signature":"` + sigHex + `"}}`))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestSign(t *testing.T) {
	privRSA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(circleMockRSAHandler(t, privRSA))
	defer srv.Close()

	edPriv, err := crypto.GenerateEd25519PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	edPub, ok := edPriv.PubKey().(*crypto.Ed25519PublicKey)
	if !ok {
		t.Fatal("expected Ed25519 public key")
	}

	client := testClient(t, srv)
	signer := NewSigner(client, "0x0102030405060708090a0b0c0d0e0f10")

	raw := &aptossdk.RawTransaction{
		Sender:         aptossdk.AccountOne,
		SequenceNumber: 1,
		Payload: aptossdk.TransactionPayload{
			Payload: &aptossdk.EntryFunction{
				Module:   aptossdk.ModuleId{Address: aptossdk.AccountZero, Name: "m"},
				Function: "f",
			},
		},
		MaxGasAmount:               100,
		GasUnitPrice:               1,
		ExpirationTimestampSeconds: 1,
		ChainId:                    2,
	}

	auth, err := signer.SignTransaction(context.Background(), raw, "wallet-id", edPub.ToHex())
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("nil authenticator")
	}
}

func TestSignError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	defer srv.Close()

	edPriv, err := crypto.GenerateEd25519PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	edPub, ok := edPriv.PubKey().(*crypto.Ed25519PublicKey)
	if !ok {
		t.Fatal("expected Ed25519 public key")
	}

	client := testClient(t, srv)
	signer := NewSigner(client, "0x0102030405060708090a0b0c0d0e0f10")

	raw := &aptossdk.RawTransaction{
		Sender:         aptossdk.AccountOne,
		SequenceNumber: 0,
		Payload: aptossdk.TransactionPayload{
			Payload: &aptossdk.EntryFunction{
				Module:   aptossdk.ModuleId{Address: aptossdk.AccountZero, Name: "m"},
				Function: "f",
			},
		},
		MaxGasAmount:               1,
		GasUnitPrice:               1,
		ExpirationTimestampSeconds: 1,
		ChainId:                    2,
	}

	_, err = signer.SignTransaction(context.Background(), raw, "w", edPub.ToHex())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "encrypt entity secret") && !strings.Contains(err.Error(), "circle") {
		t.Fatalf("error: %v", err)
	}
}
