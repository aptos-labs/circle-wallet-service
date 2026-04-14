//go:build integration

package circle

import (
	"context"
	"encoding/base64"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/aptos-go-sdk/bcs"
	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
)

var circleErrStatusRE = regexp.MustCompile(`circle API error \(status (\d+)\)`)

func testCircleClient(t *testing.T) *Client {
	t.Helper()
	apiKey := os.Getenv("CIRCLE_API_KEY")
	if apiKey == "" {
		t.Skip("CIRCLE_API_KEY not set")
	}
	return NewClient(apiKey)
}

func testWalletID(t *testing.T) string {
	id := os.Getenv("TEST_CIRCLE_WALLET_ID")
	if id == "" {
		t.Skip("TEST_CIRCLE_WALLET_ID not set")
	}
	return id
}

func testEntitySecret(t *testing.T) string {
	s := os.Getenv("CIRCLE_ENTITY_SECRET")
	if s == "" {
		t.Skip("CIRCLE_ENTITY_SECRET not set")
	}
	return s
}

func integrationAptosClient(t *testing.T) *aptos.Client {
	t.Helper()
	nodeURL := os.Getenv("APTOS_NODE_URL")
	if nodeURL == "" {
		nodeURL = "https://api.testnet.aptoslabs.com/v1"
	}
	chainID := uint8(2)
	if v := strings.TrimSpace(os.Getenv("APTOS_CHAIN_ID")); v != "" {
		n, err := strconv.ParseUint(v, 10, 8)
		if err != nil {
			t.Fatalf("APTOS_CHAIN_ID: %v", err)
		}
		chainID = uint8(n)
	}
	c, err := aptos.NewClient(nodeURL, chainID, 600, 2_000_000)
	if err != nil {
		t.Fatalf("aptos client: %v", err)
	}
	return c
}

func circleStatus4xx(err error) bool {
	if err == nil {
		return false
	}
	m := circleErrStatusRE.FindStringSubmatch(err.Error())
	if len(m) < 2 {
		return false
	}
	code, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return false
	}
	return code >= 400 && code < 500
}

func TestGetWallet_Integration(t *testing.T) {
	ctx := context.Background()
	client := testCircleClient(t)
	walletID := testWalletID(t)

	resp, err := client.GetWallet(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Data.Wallet.ID != walletID {
		t.Fatalf("wallet id: got %q want %q", resp.Data.Wallet.ID, walletID)
	}
	if strings.TrimSpace(resp.Data.Wallet.Address) == "" {
		t.Fatal("empty address")
	}
	if strings.TrimSpace(resp.Data.Wallet.InitialPublicKey) == "" {
		t.Fatal("empty initialPublicKey")
	}
	t.Logf("wallet address=%s publicKey=%s", resp.Data.Wallet.Address, resp.Data.Wallet.InitialPublicKey)
}

func TestGetWallet_InvalidID(t *testing.T) {
	ctx := context.Background()
	client := testCircleClient(t)

	_, err := client.GetWallet(ctx, "nonexistent-wallet-id-00000")
	if err == nil {
		t.Fatal("expected error")
	}
	if !circleStatus4xx(err) {
		t.Fatalf("expected 4xx circle error, got: %v", err)
	}
}

func TestEncryptEntitySecret_Integration(t *testing.T) {
	ctx := context.Background()
	client := testCircleClient(t)
	entitySecret := testEntitySecret(t)

	out, err := client.EncryptEntitySecret(ctx, entitySecret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("empty ciphertext")
	}
	decoded, err := base64.StdEncoding.DecodeString(out)
	if err != nil || len(decoded) == 0 {
		t.Fatalf("ciphertext not valid base64 or empty decoded: %v", err)
	}

	out2, err := client.EncryptEntitySecret(ctx, entitySecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base64.StdEncoding.DecodeString(out2); err != nil {
		t.Fatal(err)
	}
}

func TestPublicKeyCache_Integration(t *testing.T) {
	ctx := context.Background()
	client := testCircleClient(t)
	walletID := testWalletID(t)

	cache := NewPublicKeyCache(client)
	pk1, err := cache.Resolve(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pk1, "0x") {
		t.Fatalf("public key should start with 0x: %q", pk1)
	}
	pk2, err := cache.Resolve(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if pk1 != pk2 {
		t.Fatalf("cache mismatch: %q vs %q", pk1, pk2)
	}
}

func walletPubKeyHex(t *testing.T, w *WalletResponse) string {
	t.Helper()
	raw := strings.TrimSpace(w.Data.Wallet.InitialPublicKey)
	if raw == "" {
		t.Fatal("empty initialPublicKey")
	}
	if strings.HasPrefix(raw, "0x") || strings.HasPrefix(raw, "0X") {
		return raw
	}
	return "0x" + raw
}

func buildSelfTransferOcta(t *testing.T, sender aptossdk.AccountAddress) aptossdk.TransactionPayload {
	t.Helper()
	amountBytes, err := bcs.SerializeU64(1)
	if err != nil {
		t.Fatal(err)
	}
	entry := &aptossdk.EntryFunction{
		Module:   aptossdk.ModuleId{Address: aptossdk.AccountOne, Name: "aptos_account"},
		Function: "transfer",
		ArgTypes: []aptossdk.TypeTag{},
		Args:     [][]byte{sender[:], amountBytes},
	}
	return aptossdk.TransactionPayload{Payload: entry}
}

func TestSignTransaction_Integration(t *testing.T) {
	ctx := context.Background()
	circleClient := testCircleClient(t)
	walletID := testWalletID(t)
	entitySecret := testEntitySecret(t)

	aptCli := integrationAptosClient(t)
	walletResp, err := circleClient.GetWallet(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	senderAddr, err := aptos.ParseAddress(walletResp.Data.Wallet.Address)
	if err != nil {
		t.Fatal(err)
	}
	info, err := aptCli.Inner.Account(senderAddr)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := info.SequenceNumber()
	if err != nil {
		t.Fatal(err)
	}

	payload := buildSelfTransferOcta(t, senderAddr)
	rawTxn, err := aptCli.BuildFeePayerTransaction(senderAddr, senderAddr, payload, 0, seq)
	if err != nil {
		t.Fatal(err)
	}

	signer := NewSigner(circleClient, entitySecret)
	pubKey := walletPubKeyHex(t, walletResp)
	auth, err := signer.SignTransaction(ctx, rawTxn, walletID, pubKey)
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("nil authenticator")
	}
	t.Logf("signature hex=%s", auth.Signature().ToHex())
}

func TestSignAndSubmit_Integration(t *testing.T) {
	ctx := context.Background()
	circleClient := testCircleClient(t)
	walletID := testWalletID(t)
	entitySecret := testEntitySecret(t)

	aptCli := integrationAptosClient(t)
	walletResp, err := circleClient.GetWallet(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	senderAddr, err := aptos.ParseAddress(walletResp.Data.Wallet.Address)
	if err != nil {
		t.Fatal(err)
	}
	info, err := aptCli.Inner.Account(senderAddr)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := info.SequenceNumber()
	if err != nil {
		t.Fatal(err)
	}

	payload := buildSelfTransferOcta(t, senderAddr)
	rawTxn, err := aptCli.BuildFeePayerTransaction(senderAddr, senderAddr, payload, 0, seq)
	if err != nil {
		t.Fatal(err)
	}

	signer := NewSigner(circleClient, entitySecret)
	pubKey := walletPubKeyHex(t, walletResp)
	senderAuth, err := signer.SignTransaction(ctx, rawTxn, walletID, pubKey)
	if err != nil {
		t.Fatal(err)
	}
	signedTxn, ok := rawTxn.ToFeePayerSignedTransaction(senderAuth, senderAuth, nil)
	if !ok {
		t.Fatal("ToFeePayerSignedTransaction failed")
	}

	submitResp, err := aptCli.SubmitTransaction(signedTxn)
	if err != nil {
		t.Fatal(err)
	}
	hash := submitResp.Hash
	t.Logf("submitted hash=%s", hash)

	deadline := time.Now().Add(2 * time.Minute)
	var final *api.Transaction
	for time.Now().Before(deadline) {
		txn, err := aptCli.TransactionByHash(hash)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		if s := txn.Success(); s != nil {
			final = txn
			t.Logf("committed success=%v", *s)
			if !*s {
				t.Fatalf("transaction failed on chain hash=%s", hash)
			}
			break
		}
		time.Sleep(time.Second)
	}
	if final == nil {
		t.Fatalf("timeout waiting for commitment hash=%s", hash)
	}
}
