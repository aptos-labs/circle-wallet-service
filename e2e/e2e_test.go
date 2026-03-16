//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// Tests run sequentially in definition order, building cumulative on-chain state.
// Each mutating test submits via the HTTP API and polls until confirmed.

func TestE2E_HealthCheck(t *testing.T) {
	result := testClient.get(t, "/v1/health")
	if result["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", result["status"])
	}
}

func TestE2E_QueryInitialState(t *testing.T) {
	// Minter balance should be 0.
	result := testClient.get(t, "/v1/balance/"+minterAddr.String())
	if bal, _ := result["balance"].(float64); bal != 0 {
		t.Fatalf("expected balance=0, got %v", result["balance"])
	}

	// Master minter should match the assigned address.
	result = testClient.get(t, "/v1/master-minter")
	if result["master_minter"] != masterMinterAddr.String() {
		t.Fatalf("expected master_minter=%s, got %v", masterMinterAddr.String(), result["master_minter"])
	}

	// Minter should be configured.
	result = testClient.get(t, "/v1/minters/"+minterAddr.String())
	if result["is_minter"] != true {
		t.Fatalf("expected is_minter=true, got %v", result["is_minter"])
	}

	// Minter allowance should be 1,000,000.
	result = testClient.get(t, "/v1/minters/"+minterAddr.String()+"/allowance")
	if allowance, _ := result["allowance"].(float64); allowance != 1_000_000 {
		t.Fatalf("expected allowance=1000000, got %v", result["allowance"])
	}
}

func TestE2E_Mint(t *testing.T) {
	status, result := testClient.post(t, "/v1/mint", map[string]any{
		"to":     minterAddr.String(),
		"amount": 10000,
	})
	if status != 202 {
		t.Fatalf("expected 202, got %d: %v", status, result)
	}

	txnID, _ := result["transaction_id"].(string)
	if txnID == "" {
		t.Fatal("missing transaction_id")
	}

	txnResult := testClient.pollTransaction(t, txnID, 90*time.Second)
	if txnResult["status"] != "confirmed" {
		t.Fatalf("expected confirmed, got %v: %v", txnResult["status"], txnResult["error_message"])
	}

	// Verify balance.
	balResult := testClient.get(t, "/v1/balance/"+minterAddr.String())
	if bal, _ := balResult["balance"].(float64); bal != 10000 {
		t.Fatalf("expected balance=10000, got %v", balResult["balance"])
	}
}

func TestE2E_Burn(t *testing.T) {
	status, result := testClient.post(t, "/v1/burn", map[string]any{
		"from":   minterAddr.String(),
		"amount": 3000,
	})
	if status != 202 {
		t.Fatalf("expected 202, got %d: %v", status, result)
	}

	txnID, _ := result["transaction_id"].(string)
	txnResult := testClient.pollTransaction(t, txnID, 90*time.Second)
	if txnResult["status"] != "confirmed" {
		t.Fatalf("expected confirmed, got %v: %v", txnResult["status"], txnResult["error_message"])
	}

	// Balance: 10000 - 3000 = 7000.
	balResult := testClient.get(t, "/v1/balance/"+minterAddr.String())
	if bal, _ := balResult["balance"].(float64); bal != 7000 {
		t.Fatalf("expected balance=7000, got %v", balResult["balance"])
	}
}

func TestE2E_BatchMint(t *testing.T) {
	status, result := testClient.post(t, "/v1/batch-mint", map[string]any{
		"recipients": []map[string]any{
			{"to": denylisterAddr.String(), "amount": 5000},
			{"to": metadataUpdaterAddr.String(), "amount": 3000},
		},
	})
	if status != 202 {
		t.Fatalf("expected 202, got %d: %v", status, result)
	}

	txnID, _ := result["transaction_id"].(string)
	txnResult := testClient.pollTransaction(t, txnID, 90*time.Second)
	if txnResult["status"] != "confirmed" {
		t.Fatalf("expected confirmed, got %v: %v", txnResult["status"], txnResult["error_message"])
	}

	// Verify individual balances.
	r1 := testClient.get(t, "/v1/balance/"+denylisterAddr.String())
	if bal, _ := r1["balance"].(float64); bal != 5000 {
		t.Fatalf("expected denylister balance=5000, got %v", r1["balance"])
	}

	r2 := testClient.get(t, "/v1/balance/"+metadataUpdaterAddr.String())
	if bal, _ := r2["balance"].(float64); bal != 3000 {
		t.Fatalf("expected metadata_updater balance=3000, got %v", r2["balance"])
	}
}

func TestE2E_Denylist(t *testing.T) {
	status, result := testClient.post(t, "/v1/denylist", map[string]any{
		"address": metadataUpdaterAddr.String(),
	})
	if status != 202 {
		t.Fatalf("expected 202, got %d: %v", status, result)
	}

	txnID, _ := result["transaction_id"].(string)
	txnResult := testClient.pollTransaction(t, txnID, 90*time.Second)
	if txnResult["status"] != "confirmed" {
		t.Fatalf("expected confirmed, got %v: %v", txnResult["status"], txnResult["error_message"])
	}
}

func TestE2E_Undenylist(t *testing.T) {
	status, result := testClient.post(t, "/v1/undenylist", map[string]any{
		"address": metadataUpdaterAddr.String(),
	})
	if status != 202 {
		t.Fatalf("expected 202, got %d: %v", status, result)
	}

	txnID, _ := result["transaction_id"].(string)
	txnResult := testClient.pollTransaction(t, txnID, 90*time.Second)
	if txnResult["status"] != "confirmed" {
		t.Fatalf("expected confirmed, got %v: %v", txnResult["status"], txnResult["error_message"])
	}
}

func TestE2E_ConfigureMinter(t *testing.T) {
	// Configure denylister address as a new minter.
	status, result := testClient.post(t, "/v1/minters", map[string]any{
		"address":   denylisterAddr.String(),
		"allowance": 5000,
	})
	if status != 202 {
		t.Fatalf("expected 202, got %d: %v", status, result)
	}

	txnID, _ := result["transaction_id"].(string)
	txnResult := testClient.pollTransaction(t, txnID, 90*time.Second)
	if txnResult["status"] != "confirmed" {
		t.Fatalf("expected confirmed, got %v: %v", txnResult["status"], txnResult["error_message"])
	}

	// Verify the new minter.
	mResult := testClient.get(t, "/v1/minters/"+denylisterAddr.String())
	if mResult["is_minter"] != true {
		t.Fatalf("expected is_minter=true, got %v", mResult["is_minter"])
	}
}

func TestE2E_MintAllowance(t *testing.T) {
	// Original allowance: 1,000,000
	// Mints: 10,000 (single) + 5,000 + 3,000 (batch) = 18,000
	// Remaining: 982,000
	result := testClient.get(t, "/v1/minters/"+minterAddr.String()+"/allowance")
	if allowance, _ := result["allowance"].(float64); allowance != 982_000 {
		t.Fatalf("expected allowance=982000, got %v", result["allowance"])
	}
}

func TestE2E_TransactionLifecycle(t *testing.T) {
	// Submit a mint and observe status progression.
	status, result := testClient.post(t, "/v1/mint", map[string]any{
		"to":     ownerAddr.String(),
		"amount": 100,
	})
	if status != 202 {
		t.Fatalf("expected 202, got %d: %v", status, result)
	}

	txnID, _ := result["transaction_id"].(string)

	// Immediate check — should have some status.
	immediate := testClient.get(t, "/v1/transactions/"+txnID)
	txnStatus, _ := immediate["status"].(string)
	if txnStatus == "" {
		t.Fatal("expected non-empty status")
	}
	t.Logf("Immediate status: %s", txnStatus)

	// Wait for final confirmation.
	final := testClient.pollTransaction(t, txnID, 90*time.Second)
	if final["status"] != "confirmed" {
		t.Fatalf("expected confirmed, got %v: %v", final["status"], final["error_message"])
	}

	// Verify transaction record fields.
	if _, ok := final["id"].(string); !ok {
		t.Fatal("missing id field")
	}
	if _, ok := final["txn_hash"].(string); !ok {
		t.Fatal("missing txn_hash field")
	}
	if final["operation_type"] != "mint" {
		t.Fatalf("expected operation_type=mint, got %v", final["operation_type"])
	}
}
