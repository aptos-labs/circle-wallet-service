package signer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"golang.org/x/crypto/sha3"
)

// testEntitySecret is a valid 32-byte hex entity secret for tests.
const testEntitySecret = "0000000000000000000000000000000000000000000000000000000000000001"

// testSigningMessage builds a fake Aptos signing message:
// sha3_256("APTOS::RawTransaction") || payload
// This matches the format that CircleSigner.Sign expects.
func testSigningMessage(payload []byte) []byte {
	prefix := sha3.Sum256([]byte("APTOS::RawTransaction"))
	msg := make([]byte, len(prefix)+len(payload))
	copy(msg, prefix[:])
	copy(msg[len(prefix):], payload)
	return msg
}

// newTestCircleServer creates a mock Circle API server that handles the
// entity public key endpoint (for EncryptEntitySecret) and the sign-message endpoint.
func newTestCircleServer(t *testing.T, signHandler http.HandlerFunc) (*httptest.Server, *CircleClient) {
	t.Helper()

	// Generate a test RSA key for entity secret encryption.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pubKeyBytes, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal RSA public key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubKeyBytes})

	mux := http.NewServeMux()
	mux.HandleFunc("/config/entity/publicKey", func(w http.ResponseWriter, r *http.Request) {
		resp := entityPublicKeyResponse{}
		resp.Data.PublicKey = string(pemBytes)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/developer/sign/message", signHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := NewCircleClient("test-api-key")
	client.baseURL = server.URL
	return server, client
}

func TestCircleSigner_Sign_Success(t *testing.T) {
	// Generate a test key and produce a known signature.
	key, err := crypto.GenerateEd25519PrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	testMsg := testSigningMessage([]byte("test signing payload"))
	sig, err := key.SignMessage(testMsg)
	if err != nil {
		t.Fatalf("sign message: %v", err)
	}
	sigHex := "0x" + hex.EncodeToString(sig.Bytes())

	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := SignMessageResponse{}
		resp.Data.Signature = sigHex
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	auth, err := signer.Sign(context.Background(), testMsg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authenticator")
	}
	if auth.Variant != crypto.AccountAuthenticatorEd25519 {
		t.Errorf("variant = %d, want Ed25519", auth.Variant)
	}
}

func TestCircleSigner_Sign_HTTPError(t *testing.T) {
	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})

	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	_, err = signer.Sign(context.Background(), testSigningMessage([]byte("payload")))
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestCircleSigner_Sign_BadSignatureHex(t *testing.T) {
	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := SignMessageResponse{}
		resp.Data.Signature = "not-valid-hex"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	_, err = signer.Sign(context.Background(), testSigningMessage([]byte("payload")))
	if err == nil {
		t.Fatal("expected error for bad hex")
	}
}

func TestCircleSigner_Sign_WrongSigLength(t *testing.T) {
	// Return a valid hex string but only 32 bytes (not 64).
	shortSig := "0x" + hex.EncodeToString(make([]byte, 32))

	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := SignMessageResponse{}
		resp.Data.Signature = shortSig
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	_, err = signer.Sign(context.Background(), testSigningMessage([]byte("payload")))
	if err == nil {
		t.Fatal("expected error for wrong signature length")
	}
}

func TestCircleSigner_Sign_MessageTooShort(t *testing.T) {
	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach Circle API with short message")
	})

	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	// Message shorter than 32-byte prehash prefix
	_, err = signer.Sign(context.Background(), []byte("short"))
	if err == nil {
		t.Fatal("expected error for short message")
	}
}

func TestCircleSigner_Sign_StripsPrehashPrefix(t *testing.T) {
	key, _ := crypto.GenerateEd25519PrivateKey()
	payload := []byte("test bcs payload bytes")
	fullMsg := testSigningMessage(payload)

	sig, _ := key.SignMessage(fullMsg)
	sigHex := "0x" + hex.EncodeToString(sig.Bytes())

	var gotMessage string
	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req SignMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMessage = req.Message

		resp := SignMessageResponse{}
		resp.Data.Signature = sigHex
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	s, err := NewCircleSigner(client, "w1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	if _, err := s.Sign(context.Background(), fullMsg); err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Verify that only the BCS payload (without prehash) was sent to Circle
	expectedHex := "0x" + hex.EncodeToString(payload)
	if gotMessage != expectedHex {
		t.Errorf("message sent to Circle = %q, want %q (BCS only, no prehash prefix)", gotMessage, expectedHex)
	}
}

func TestCircleSigner_PublicKey(t *testing.T) {
	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	client := NewCircleClient("key")
	signer, err := NewCircleSigner(client, "w1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	got := signer.PublicKey()
	if got == nil {
		t.Fatal("expected non-nil public key")
	}
}

func TestCircleSigner_Address(t *testing.T) {
	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	client := NewCircleClient("key")
	signer, err := NewCircleSigner(client, "w1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	got := signer.Address()
	// Verify the address is not all zeros
	allZero := true
	for _, b := range got {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("expected non-zero address")
	}
}

func TestCircleClient_SignMessage_RequestFormat(t *testing.T) {
	var gotReq SignMessageRequest
	var gotAuthHeader string
	var gotContentType string

	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)

		resp := SignMessageResponse{}
		resp.Data.Signature = "0x" + hex.EncodeToString(make([]byte, 64))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	ctx := context.Background()
	_, _ = client.SignMessage(ctx, &SignMessageRequest{
		WalletID:               "test-wallet",
		Message:                "deadbeef",
		EntitySecretCiphertext: "cipher",
		EncodedByHex:           true,
	})

	if gotAuthHeader != "Bearer test-api-key" {
		t.Errorf("Authorization = %q, want %q", gotAuthHeader, "Bearer test-api-key")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "application/json")
	}
	if gotReq.WalletID != "test-wallet" {
		t.Errorf("WalletID = %q, want %q", gotReq.WalletID, "test-wallet")
	}
	if !gotReq.EncodedByHex {
		t.Error("expected EncodedByHex=true")
	}
}

func TestCircleSigner_Sign_FreshCiphertextPerRequest(t *testing.T) {
	key, _ := crypto.GenerateEd25519PrivateKey()
	msg := testSigningMessage([]byte("payload"))
	sig, _ := key.SignMessage(msg)
	sigHex := "0x" + hex.EncodeToString(sig.Bytes())

	var ciphertexts []string
	_, client := newTestCircleServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req SignMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		ciphertexts = append(ciphertexts, req.EntitySecretCiphertext)

		resp := SignMessageResponse{}
		resp.Data.Signature = sigHex
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	s, err := NewCircleSigner(client, "w1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	// Sign twice and verify ciphertexts differ (RSA-OAEP random padding).
	if _, err := s.Sign(context.Background(), msg); err != nil {
		t.Fatalf("first sign: %v", err)
	}
	if _, err := s.Sign(context.Background(), msg); err != nil {
		t.Fatalf("second sign: %v", err)
	}
	if len(ciphertexts) != 2 {
		t.Fatalf("expected 2 sign calls, got %d", len(ciphertexts))
	}
	if ciphertexts[0] == ciphertexts[1] {
		t.Error("entity secret ciphertext was reused across requests; expected fresh ciphertext each time")
	}
}

func TestNewCircleSigner_EmptyPubKey(t *testing.T) {
	client := NewCircleClient("key")
	_, err := NewCircleSigner(client, "w1", testEntitySecret, "", "0x1")
	if err == nil {
		t.Fatal("expected error for empty public key")
	}
}

func TestNewCircleSigner_InvalidPubKey(t *testing.T) {
	client := NewCircleClient("key")
	_, err := NewCircleSigner(client, "w1", testEntitySecret, "not-hex", "0x1")
	if err == nil {
		t.Fatal("expected error for invalid public key hex")
	}
}

func TestNewCircleSigner_InvalidAddress(t *testing.T) {
	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())

	client := NewCircleClient("key")
	_, err := NewCircleSigner(client, "w1", testEntitySecret, pubKeyHex, "invalid-address-!!")
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
}

// testRawTxn builds a minimal RawTransaction for use in SignTransaction tests.
func testRawTxn() *aptossdk.RawTransaction {
	return &aptossdk.RawTransaction{
		Sender: aptossdk.AccountAddress{},
		Payload: aptossdk.TransactionPayload{
			Payload: &aptossdk.EntryFunction{
				Module: aptossdk.ModuleId{
					Address: aptossdk.AccountAddress{},
					Name:    "test",
				},
				Function: "noop",
				ArgTypes: []aptossdk.TypeTag{},
				Args:     [][]byte{},
			},
		},
	}
}

func TestCircleSigner_SignTransaction_Success(t *testing.T) {
	key, err := crypto.GenerateEd25519PrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Produce a 64-byte fake signature (the content doesn't matter for this test —
	// we just verify the flow and authenticator structure).
	fakeSig := make([]byte, 64)
	for i := range fakeSig {
		fakeSig[i] = byte(i)
	}
	sigHex := "0x" + hex.EncodeToString(fakeSig)

	var gotReq SignMessageRequest
	signMsgHandler := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		resp := SignMessageResponse{}
		resp.Data.Signature = sigHex
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}

	_, client := newTestCircleServer(t, signMsgHandler)

	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	signedTxn, err := signer.SignTransaction(context.Background(), testRawTxn())
	if err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	if signedTxn == nil {
		t.Fatal("expected non-nil signed transaction")
	}

	// Verify the request sent to Circle used sign/message with hex encoding
	if gotReq.WalletID != "wallet-1" {
		t.Errorf("walletId = %q, want %q", gotReq.WalletID, "wallet-1")
	}
	if gotReq.Message == "" {
		t.Error("message should not be empty")
	}
	if !gotReq.EncodedByHex {
		t.Error("expected EncodedByHex=true")
	}
	if gotReq.EntitySecretCiphertext == "" {
		t.Error("entitySecretCiphertext should not be empty")
	}

	// Verify the signed transaction has a fee-payer authenticator
	if signedTxn.Authenticator == nil {
		t.Fatal("expected non-nil authenticator")
	}
	if signedTxn.Authenticator.Variant != aptossdk.TransactionAuthenticatorFeePayer {
		t.Errorf("authenticator variant = %d, want TransactionAuthenticatorFeePayer (%d)",
			signedTxn.Authenticator.Variant, aptossdk.TransactionAuthenticatorFeePayer)
	}
}

func TestCircleSigner_SignTransaction_CircleError(t *testing.T) {
	signMsgHandler := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":156025,"message":"error signing"}`, http.StatusBadRequest)
	}

	_, client := newTestCircleServer(t, signMsgHandler)

	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	_, err = signer.SignTransaction(context.Background(), testRawTxn())
	if err == nil {
		t.Fatal("expected error for Circle API error")
	}
}

func TestCircleSigner_SignTransaction_BadSignature(t *testing.T) {
	signMsgHandler := func(w http.ResponseWriter, r *http.Request) {
		resp := SignMessageResponse{}
		resp.Data.Signature = "not-valid-hex"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}

	_, client := newTestCircleServer(t, signMsgHandler)

	key, _ := crypto.GenerateEd25519PrivateKey()
	pubKey := key.PubKey().(*crypto.Ed25519PublicKey)
	pubKeyHex := "0x" + hex.EncodeToString(pubKey.Bytes())
	addr := key.AuthKey()
	addrHex := "0x" + hex.EncodeToString(addr[:])

	signer, err := NewCircleSigner(client, "wallet-1", testEntitySecret, pubKeyHex, addrHex)
	if err != nil {
		t.Fatalf("new circle signer: %v", err)
	}

	_, err = signer.SignTransaction(context.Background(), testRawTxn())
	if err == nil {
		t.Fatal("expected error for bad signature hex")
	}
}

// Verify CircleSigner implements TransactionSigner at compile time.
var _ TransactionSigner = (*CircleSigner)(nil)
