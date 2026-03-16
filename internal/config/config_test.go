package config

import (
	"strings"
	"testing"
)

// setRequiredEnv sets the minimum required env vars for a valid config.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("API_KEY", "test-key")
	t.Setenv("APTOS_NODE_URL", "https://api.testnet.aptoslabs.com/v1")
	t.Setenv("SIGNER_PROVIDER", "local")
}

func TestLoad_MinimalValid(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "test-key")
	}
	if cfg.ServerPort != "8080" {
		t.Errorf("ServerPort = %q, want default %q", cfg.ServerPort, "8080")
	}
}

func TestLoad_MissingAPIKey(t *testing.T) {
	t.Setenv("API_KEY", "")
	t.Setenv("APTOS_NODE_URL", "https://example.com")
	t.Setenv("SIGNER_PROVIDER", "local")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing API_KEY")
	}
	if !strings.Contains(err.Error(), "API_KEY") {
		t.Errorf("error = %q, want mention of API_KEY", err.Error())
	}
}

func TestLoad_DefaultNodeURL(t *testing.T) {
	setRequiredEnv(t)
	// Unset APTOS_NODE_URL — should use the default.
	t.Setenv("APTOS_NODE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Default is the testnet URL.
	if cfg.AptosNodeURL != "https://api.testnet.aptoslabs.com/v1" {
		t.Errorf("AptosNodeURL = %q, want default testnet URL", cfg.AptosNodeURL)
	}
}

func TestLoad_InvalidSignerProvider(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SIGNER_PROVIDER", "bogus")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid SIGNER_PROVIDER")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %q, want mention of bogus", err.Error())
	}
}

func TestLoad_CircleMissingAPIKey(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SIGNER_PROVIDER", "circle")
	t.Setenv("CIRCLE_API_KEY", "")
	t.Setenv("CIRCLE_ENTITY_SECRET", "deadbeef")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing CIRCLE_API_KEY")
	}
}

func TestLoad_CircleMissingSecret(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SIGNER_PROVIDER", "circle")
	t.Setenv("CIRCLE_API_KEY", "key")
	t.Setenv("CIRCLE_ENTITY_SECRET", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing CIRCLE_ENTITY_SECRET")
	}
}

func TestLoad_CircleValid(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SIGNER_PROVIDER", "circle")
	t.Setenv("CIRCLE_API_KEY", "circle-key")
	t.Setenv("CIRCLE_ENTITY_SECRET", "deadbeef")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CircleAPIKey != "circle-key" {
		t.Errorf("CircleAPIKey = %q, want %q", cfg.CircleAPIKey, "circle-key")
	}
}

func TestLoad_CustomDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SERVER_PORT", "9090")
	t.Setenv("APTOS_CHAIN_ID", "4")
	t.Setenv("MAX_RETRIES", "5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ServerPort != "9090" {
		t.Errorf("ServerPort = %q, want %q", cfg.ServerPort, "9090")
	}
	if cfg.AptosChainID != 4 {
		t.Errorf("AptosChainID = %d, want 4", cfg.AptosChainID)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", cfg.MaxRetries)
	}
}

func TestLoad_InvalidChainID(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APTOS_CHAIN_ID", "abc")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-numeric APTOS_CHAIN_ID")
	}
}

func TestLoad_InvalidMaxRetries(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MAX_RETRIES", "abc")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-numeric MAX_RETRIES")
	}
}

func TestLoad_TestingModeDefaultFalse(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestingMode {
		t.Error("TestingMode should default to false")
	}
}

func TestLoad_TestingModeTrue(t *testing.T) {
	// With testing mode on, API_KEY should NOT be required.
	t.Setenv("TESTING_MODE", "true")
	t.Setenv("API_KEY", "")
	t.Setenv("APTOS_NODE_URL", "https://api.testnet.aptoslabs.com/v1")
	t.Setenv("SIGNER_PROVIDER", "local")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.TestingMode {
		t.Error("TestingMode should be true")
	}
	if cfg.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", cfg.APIKey)
	}
}

func TestLoad_TestingModeFalseRequiresAPIKey(t *testing.T) {
	t.Setenv("TESTING_MODE", "false")
	t.Setenv("API_KEY", "")
	t.Setenv("APTOS_NODE_URL", "https://api.testnet.aptoslabs.com/v1")
	t.Setenv("SIGNER_PROVIDER", "local")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing API_KEY when TESTING_MODE=false")
	}
	if !strings.Contains(err.Error(), "API_KEY") {
		t.Errorf("error = %q, want mention of API_KEY", err.Error())
	}
}

func TestLoad_TestingModeInvalidValueDefaultsFalse(t *testing.T) {
	// An invalid boolean value should fall back to false (the safe default).
	t.Setenv("TESTING_MODE", "yep")
	t.Setenv("API_KEY", "")
	t.Setenv("APTOS_NODE_URL", "https://api.testnet.aptoslabs.com/v1")
	t.Setenv("SIGNER_PROVIDER", "local")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error: invalid TESTING_MODE should default to false, requiring API_KEY")
	}
}

func TestLoad_NewOperationalDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxBatchSize != 500 {
		t.Errorf("MaxBatchSize = %d, want 500", cfg.MaxBatchSize)
	}
	if cfg.MaxGasAmount != 100_000 {
		t.Errorf("MaxGasAmount = %d, want 100000", cfg.MaxGasAmount)
	}
	if cfg.GasPerRecipient != 0 {
		t.Errorf("GasPerRecipient = %d, want 0", cfg.GasPerRecipient)
	}
	if cfg.TxnExpirationSeconds != 60 {
		t.Errorf("TxnExpirationSeconds = %d, want 60", cfg.TxnExpirationSeconds)
	}
	if cfg.RetryBackoffBaseSeconds != 10 {
		t.Errorf("RetryBackoffBaseSeconds = %d, want 10", cfg.RetryBackoffBaseSeconds)
	}
	if cfg.RetryBackoffMaxSeconds != 300 {
		t.Errorf("RetryBackoffMaxSeconds = %d, want 300", cfg.RetryBackoffMaxSeconds)
	}
}

func TestLoad_CustomOperationalValues(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MAX_BATCH_SIZE", "100")
	t.Setenv("MAX_GAS_AMOUNT", "200000")
	t.Setenv("GAS_PER_RECIPIENT", "500")
	t.Setenv("TXN_EXPIRATION_SECONDS", "120")
	t.Setenv("RETRY_BACKOFF_BASE_SECONDS", "5")
	t.Setenv("RETRY_BACKOFF_MAX_SECONDS", "600")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxBatchSize != 100 {
		t.Errorf("MaxBatchSize = %d, want 100", cfg.MaxBatchSize)
	}
	if cfg.MaxGasAmount != 200_000 {
		t.Errorf("MaxGasAmount = %d, want 200000", cfg.MaxGasAmount)
	}
	if cfg.GasPerRecipient != 500 {
		t.Errorf("GasPerRecipient = %d, want 500", cfg.GasPerRecipient)
	}
	if cfg.TxnExpirationSeconds != 120 {
		t.Errorf("TxnExpirationSeconds = %d, want 120", cfg.TxnExpirationSeconds)
	}
	if cfg.RetryBackoffBaseSeconds != 5 {
		t.Errorf("RetryBackoffBaseSeconds = %d, want 5", cfg.RetryBackoffBaseSeconds)
	}
	if cfg.RetryBackoffMaxSeconds != 600 {
		t.Errorf("RetryBackoffMaxSeconds = %d, want 600", cfg.RetryBackoffMaxSeconds)
	}
}

func TestLoad_InvalidMaxBatchSize(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MAX_BATCH_SIZE", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for MAX_BATCH_SIZE=0")
	}
	if !strings.Contains(err.Error(), "MAX_BATCH_SIZE") {
		t.Errorf("error = %q, want mention of MAX_BATCH_SIZE", err.Error())
	}
}

func TestLoad_InvalidMaxGasAmount(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MAX_GAS_AMOUNT", "abc")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-numeric MAX_GAS_AMOUNT")
	}
}

func TestLoad_InvalidTxnExpiration(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TXN_EXPIRATION_SECONDS", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for TXN_EXPIRATION_SECONDS=0")
	}
}

func TestGetEnvBool(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		fallback bool
		want     bool
	}{
		{"true", "true", false, true},
		{"false", "false", true, false},
		{"1", "1", false, true},
		{"0", "0", true, false},
		{"TRUE", "TRUE", false, true},
		{"empty uses fallback true", "", true, true},
		{"empty uses fallback false", "", false, false},
		{"invalid uses fallback", "yep", false, false},
		{"invalid uses fallback true", "nope", true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_BOOL_VAR", tc.value)
			got := getEnvBool("TEST_BOOL_VAR", tc.fallback)
			if got != tc.want {
				t.Errorf("getEnvBool(%q, %v) = %v, want %v", tc.value, tc.fallback, got, tc.want)
			}
		})
	}
}
