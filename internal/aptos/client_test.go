//go:build integration

package aptos

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/bcs"
)

func testAptosClient(t *testing.T) *Client {
	t.Helper()
	nodeURL := os.Getenv("APTOS_NODE_URL")
	if nodeURL == "" {
		nodeURL = "https://api.testnet.aptoslabs.com/v1"
	}
	chainIDStr := os.Getenv("APTOS_CHAIN_ID")
	if chainIDStr == "" {
		chainIDStr = "2"
	}
	chainID, err := strconv.ParseUint(chainIDStr, 10, 8)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(nodeURL, uint8(chainID), 60, 2000000, os.Getenv("APTOS_API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testWalletAddress(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TEST_WALLET_ADDRESS")
	if addr == "" {
		t.Skip("TEST_WALLET_ADDRESS not set")
	}
	return addr
}

func TestNewClient(t *testing.T) {
	c, err := NewClient("https://api.testnet.aptoslabs.com/v1", 2, 60, 2000000, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Inner == nil {
		t.Fatal("Inner is nil")
	}
}

func TestAccountInfo(t *testing.T) {
	wallet := testWalletAddress(t)
	c := testAptosClient(t)
	var addr aptossdk.AccountAddress
	if err := addr.ParseStringRelaxed(wallet); err != nil {
		t.Fatal(err)
	}
	info, err := c.Inner.Account(addr)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := info.SequenceNumber()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sequence_number=%d authentication_key=%s", seq, info.AuthenticationKeyHex)
}

func TestBuildTransaction(t *testing.T) {
	wallet := testWalletAddress(t)
	c := testAptosClient(t)
	var addr aptossdk.AccountAddress
	if err := addr.ParseStringRelaxed(wallet); err != nil {
		t.Fatal(err)
	}
	toAddr := addr
	amount := uint64(1)
	amountBytes, err := bcs.SerializeU64(amount)
	if err != nil {
		t.Fatal(err)
	}
	recvBytes, err := bcs.Serialize(&toAddr)
	if err != nil {
		t.Fatal(err)
	}
	payload := aptossdk.TransactionPayload{
		Payload: &aptossdk.EntryFunction{
			Module: aptossdk.ModuleId{
				Address: aptossdk.AccountOne,
				Name:    "aptos_account",
			},
			Function: "transfer",
			ArgTypes: []aptossdk.TypeTag{},
			Args:     [][]byte{recvBytes, amountBytes},
		},
	}
	rawTxn, err := c.BuildTransaction(addr, payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	chainIDStr := os.Getenv("APTOS_CHAIN_ID")
	if chainIDStr == "" {
		chainIDStr = "2"
	}
	wantChain, err := strconv.ParseUint(chainIDStr, 10, 8)
	if err != nil {
		t.Fatal(err)
	}
	if rawTxn.Sender != addr {
		t.Fatalf("sender got %s want %s", rawTxn.Sender.String(), addr.String())
	}
	if rawTxn.ChainId != uint8(wantChain) {
		t.Fatalf("chain_id got %d want %d", rawTxn.ChainId, wantChain)
	}
	if rawTxn.MaxGasAmount != 2000000 {
		t.Fatalf("max_gas_amount got %d want 2000000", rawTxn.MaxGasAmount)
	}
	if rawTxn.GasUnitPrice == 0 {
		t.Fatal("gas_unit_price is zero")
	}
	now := time.Now().Unix()
	exp := int64(rawTxn.ExpirationTimestampSeconds)
	if exp < now || exp > now+120 {
		t.Fatalf("expiration_timestamp_seconds=%d now=%d", rawTxn.ExpirationTimestampSeconds, now)
	}
	t.Logf("raw_txn sequence=%d max_gas=%d gas_unit_price=%d expiration=%d hash_inputs_sender=%s",
		rawTxn.SequenceNumber, rawTxn.MaxGasAmount, rawTxn.GasUnitPrice, rawTxn.ExpirationTimestampSeconds, rawTxn.Sender.String())
}

func TestBuildFeePayerTransaction(t *testing.T) {
	wallet := testWalletAddress(t)
	c := testAptosClient(t)
	var addr aptossdk.AccountAddress
	if err := addr.ParseStringRelaxed(wallet); err != nil {
		t.Fatal(err)
	}
	toAddr := addr
	amountBytes, err := bcs.SerializeU64(uint64(1))
	if err != nil {
		t.Fatal(err)
	}
	recvBytes, err := bcs.Serialize(&toAddr)
	if err != nil {
		t.Fatal(err)
	}
	payload := aptossdk.TransactionPayload{
		Payload: &aptossdk.EntryFunction{
			Module: aptossdk.ModuleId{
				Address: aptossdk.AccountOne,
				Name:    "aptos_account",
			},
			Function: "transfer",
			ArgTypes: []aptossdk.TypeTag{},
			Args:     [][]byte{recvBytes, amountBytes},
		},
	}
	rawWithData, err := c.BuildFeePayerTransaction(addr, addr, payload, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rawWithData == nil || rawWithData.Inner == nil {
		t.Fatal("nil RawTransactionWithData")
	}
	inner, ok := rawWithData.Inner.(*aptossdk.MultiAgentWithFeePayerRawTransactionWithData)
	if !ok {
		t.Fatalf("inner type %T", rawWithData.Inner)
	}
	if inner.RawTxn.SequenceNumber != 0 {
		t.Fatalf("sequence got %d want 0", inner.RawTxn.SequenceNumber)
	}
	t.Logf("fee_payer_raw variant=%d sender=%s", rawWithData.Variant, inner.RawTxn.Sender.String())
}

func TestView(t *testing.T) {
	c := testAptosClient(t)
	out, err := c.View(context.Background(), "0x1::coin::symbol", []string{"0x1::aptos_coin::AptosCoin"}, [][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("len=%d %+v", len(out), out)
	}
	s, ok := out[0].(string)
	if !ok || s != "APT" {
		t.Fatalf("got %#v want APT", out[0])
	}
	t.Logf("view symbol=%q", s)
}

func TestViewInvalidFunction(t *testing.T) {
	c := testAptosClient(t)
	_, err := c.View(context.Background(), "0x1::coin::this_function_does_not_exist_ever", []string{"0x1::aptos_coin::AptosCoin"}, [][]byte{})
	if err == nil {
		t.Fatal("expected error")
	}
	t.Logf("view error: %v", err)
}

func TestTransactionByHash(t *testing.T) {
	c := testAptosClient(t)
	hash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	txn, err := c.TransactionByHash(hash)
	if err != nil {
		t.Logf("transaction_by_hash error (expected for invalid hash): %v", err)
		return
	}
	if txn == nil {
		t.Fatal("nil transaction without error")
	}
	t.Logf("transaction_by_hash: %+v", txn)
}
