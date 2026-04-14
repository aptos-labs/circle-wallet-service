package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

func validTestWallet(t *testing.T) *CircleWallet {
	t.Helper()
	priv, err := crypto.GenerateEd25519PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := priv.PubKey().(*crypto.Ed25519PublicKey)
	if !ok {
		t.Fatal("expected Ed25519 public key")
	}
	var addr aptos.AccountAddress
	addr.FromAuthKey(pub.AuthKey())
	return &CircleWallet{
		WalletID:  "w1",
		Address:   addr.StringLong(),
		PublicKey: pub.ToHex(),
	}
}

func nonexistentConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "missing-config.yaml")
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		assert  func(t *testing.T, cfg *Config)
		wantErr string
	}{
		{
			name: "DefaultsApplied",
			setup: func(t *testing.T) {
				t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
				t.Setenv("MYSQL_DSN", "user:pass@tcp(localhost:3306)/db")
				t.Setenv("API_KEY", "api-key")
				t.Setenv("CIRCLE_API_KEY", "circle-key")
				t.Setenv("CIRCLE_ENTITY_SECRET", "entity-secret")
			},
			assert: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.Server.Port != 8080 {
					t.Fatalf("Server.Port: got %d want 8080", cfg.Server.Port)
				}
				if cfg.Transaction.MaxGasAmount != 2000000 {
					t.Fatalf("Transaction.MaxGasAmount: got %d want 2000000", cfg.Transaction.MaxGasAmount)
				}
				if cfg.Transaction.ExpirationSeconds != 60 {
					t.Fatalf("Transaction.ExpirationSeconds: got %d want 60", cfg.Transaction.ExpirationSeconds)
				}
				if cfg.Aptos.NodeURL != "https://api.testnet.aptoslabs.com/v1" {
					t.Fatalf("Aptos.NodeURL: got %q", cfg.Aptos.NodeURL)
				}
				if cfg.Aptos.ChainID != 2 {
					t.Fatalf("Aptos.ChainID: got %d want 2", cfg.Aptos.ChainID)
				}
				if cfg.Poller.IntervalSeconds != 5 {
					t.Fatalf("Poller.IntervalSeconds: got %d want 5", cfg.Poller.IntervalSeconds)
				}
				if cfg.Submitter.PollIntervalMs != 200 {
					t.Fatalf("Submitter.PollIntervalMs: got %d want 200", cfg.Submitter.PollIntervalMs)
				}
				if cfg.Webhook.MaxRetries != 5 {
					t.Fatalf("Webhook.MaxRetries: got %d want 5", cfg.Webhook.MaxRetries)
				}
				if cfg.RateLimit.RequestsPerSecond != 100 {
					t.Fatalf("RateLimit.RequestsPerSecond: got %d want 100", cfg.RateLimit.RequestsPerSecond)
				}
			},
		},
		{
			name: "YAMLOverridesDefaults",
			setup: func(t *testing.T) {
				f, err := os.CreateTemp(t.TempDir(), "cfg-*.yaml")
				if err != nil {
					t.Fatal(err)
				}
				path := f.Name()
				_, err = f.WriteString(strings.TrimSpace(`
server:
  port: 6000
transaction:
  max_gas_amount: 999
  expiration_seconds: 120
aptos:
  node_url: "https://example.com/v1"
  chain_id: 7
`))
				if err != nil {
					_ = f.Close()
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CONFIG_PATH", path)
				t.Setenv("MYSQL_DSN", "dsn")
				t.Setenv("API_KEY", "k")
				t.Setenv("CIRCLE_API_KEY", "ck")
				t.Setenv("CIRCLE_ENTITY_SECRET", "es")
			},
			assert: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.Server.Port != 6000 {
					t.Fatalf("Server.Port: got %d want 6000", cfg.Server.Port)
				}
				if cfg.Transaction.MaxGasAmount != 999 {
					t.Fatalf("Transaction.MaxGasAmount: got %d want 999", cfg.Transaction.MaxGasAmount)
				}
				if cfg.Transaction.ExpirationSeconds != 120 {
					t.Fatalf("Transaction.ExpirationSeconds: got %d want 120", cfg.Transaction.ExpirationSeconds)
				}
				if cfg.Aptos.NodeURL != "https://example.com/v1" {
					t.Fatalf("Aptos.NodeURL: got %q", cfg.Aptos.NodeURL)
				}
				if cfg.Aptos.ChainID != 7 {
					t.Fatalf("Aptos.ChainID: got %d want 7", cfg.Aptos.ChainID)
				}
			},
		},
		{
			name: "EnvOverridesYAML",
			setup: func(t *testing.T) {
				f, err := os.CreateTemp(t.TempDir(), "cfg-*.yaml")
				if err != nil {
					t.Fatal(err)
				}
				path := f.Name()
				_, err = f.WriteString("server:\n  port: 9090\n")
				if err != nil {
					_ = f.Close()
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CONFIG_PATH", path)
				t.Setenv("SERVER_PORT", "7070")
				t.Setenv("MYSQL_DSN", "dsn")
				t.Setenv("API_KEY", "k")
				t.Setenv("CIRCLE_API_KEY", "ck")
				t.Setenv("CIRCLE_ENTITY_SECRET", "es")
			},
			assert: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.Server.Port != 7070 {
					t.Fatalf("Server.Port: got %d want 7070", cfg.Server.Port)
				}
			},
		},
		{
			name: "MissingRequiredDSN",
			setup: func(t *testing.T) {
				t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
				t.Setenv("MYSQL_DSN", "")
				t.Setenv("API_KEY", "k")
				t.Setenv("CIRCLE_API_KEY", "ck")
				t.Setenv("CIRCLE_ENTITY_SECRET", "es")
			},
			wantErr: "MYSQL_DSN is required",
		},
		{
			name: "MissingAPIKeyNonTesting",
			setup: func(t *testing.T) {
				t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
				t.Setenv("MYSQL_DSN", "dsn")
				t.Setenv("API_KEY", "")
				t.Setenv("TESTING_MODE", "false")
				t.Setenv("CIRCLE_API_KEY", "ck")
				t.Setenv("CIRCLE_ENTITY_SECRET", "es")
			},
			wantErr: "API_KEY is required unless TESTING_MODE is enabled",
		},
		{
			name: "TestingModeSkipsAPIKey",
			setup: func(t *testing.T) {
				t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
				t.Setenv("MYSQL_DSN", "dsn")
				t.Setenv("API_KEY", "")
				t.Setenv("TESTING_MODE", "true")
			},
			assert: func(t *testing.T, cfg *Config) {
				t.Helper()
				if !cfg.TestingMode() {
					t.Fatalf("TestingMode: got false want true")
				}
				if cfg.APIKey() != "" {
					t.Fatalf("APIKey: got %q want empty", cfg.APIKey())
				}
			},
		},
		{
			name: "MalformedYAML",
			setup: func(t *testing.T) {
				f, err := os.CreateTemp(t.TempDir(), "bad-*.yaml")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("server: port: [broken"); err != nil {
					_ = f.Close()
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CONFIG_PATH", f.Name())
				t.Setenv("MYSQL_DSN", "dsn")
				t.Setenv("API_KEY", "k")
				t.Setenv("CIRCLE_API_KEY", "ck")
				t.Setenv("CIRCLE_ENTITY_SECRET", "es")
			},
			wantErr: "parse yaml",
		},
		{
			name: "MissingYAMLFileOk",
			setup: func(t *testing.T) {
				t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
				t.Setenv("MYSQL_DSN", "dsn")
				t.Setenv("API_KEY", "k")
				t.Setenv("CIRCLE_API_KEY", "ck")
				t.Setenv("CIRCLE_ENTITY_SECRET", "es")
			},
			assert: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.Server.Port != 8080 {
					t.Fatalf("Server.Port: got %d want 8080", cfg.Server.Port)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			cfg, err := Load()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("Load: expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load error: got %q want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if tt.assert != nil {
				tt.assert(t, cfg)
			}
		})
	}
}

func TestEnvOverrideAptosChainID(t *testing.T) {
	t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("API_KEY", "k")
	t.Setenv("CIRCLE_API_KEY", "ck")
	t.Setenv("CIRCLE_ENTITY_SECRET", "es")
	t.Setenv("APTOS_CHAIN_ID", " 9 ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AptosChainID() != 9 {
		t.Fatalf("ChainID: got %d want 9", cfg.AptosChainID())
	}
}

func TestEnvOverrideMaxGas(t *testing.T) {
	t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("API_KEY", "k")
	t.Setenv("CIRCLE_API_KEY", "ck")
	t.Setenv("CIRCLE_ENTITY_SECRET", "es")
	t.Setenv("MAX_GAS_AMOUNT", "5000000")
	t.Setenv("TXN_EXPIRATION_SECONDS", "90")
	t.Setenv("WEBHOOK_URL", " https://hooks.example/x ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxGasAmount() != 5000000 {
		t.Fatalf("MaxGasAmount: got %d", cfg.MaxGasAmount())
	}
	if cfg.TxnExpirationSeconds() != 90 {
		t.Fatalf("TxnExpirationSeconds: got %d", cfg.TxnExpirationSeconds())
	}
	if cfg.WebhookURL() != "https://hooks.example/x" {
		t.Fatalf("WebhookURL: got %q", cfg.WebhookURL())
	}
}

func TestAccessorMethods(t *testing.T) {
	cfg := defaultConfig()
	cfg.Server.Port = 3000
	cfg.Server.APIKey = "a"
	cfg.Server.TestingMode = true
	cfg.MySQL.DSN = "m"
	cfg.Aptos.NodeURL = "https://n"
	cfg.Aptos.ChainID = 3
	cfg.Circle.APIKey = "c"
	cfg.Circle.EntitySecret = "e"
	cfg.Transaction.MaxGasAmount = 111
	cfg.Transaction.ExpirationSeconds = 45
	cfg.Poller.IntervalSeconds = 7
	cfg.Submitter.PollIntervalMs = 11
	cfg.Submitter.MaxRetryDurationSeconds = 12
	cfg.Submitter.RetryIntervalSeconds = 13
	cfg.Submitter.RetryJitterSeconds = 14
	cfg.Submitter.StaleProcessingSeconds = 15
	cfg.Submitter.RecoveryTickSeconds = 16
	cfg.Submitter.SigningPipelineDepth = 17
	cfg.Webhook.GlobalURL = "https://w"
	cfg.Webhook.MaxRetries = 18
	cfg.Webhook.TimeoutSeconds = 19
	cfg.RateLimit.Enabled = true
	cfg.RateLimit.RequestsPerSecond = 20
	cfg.RateLimit.Burst = 21
	cfg.RateLimit.PerWallet = true

	if cfg.ServerPort() != "3000" {
		t.Fatalf("ServerPort: %q", cfg.ServerPort())
	}
	if cfg.APIKey() != "a" || !cfg.TestingMode() || cfg.MySQLDSN() != "m" {
		t.Fatal("server/mysql accessors")
	}
	if cfg.AptosNodeURL() != "https://n" || cfg.AptosChainID() != 3 {
		t.Fatal("aptos accessors")
	}
	if cfg.CircleAPIKey() != "c" || cfg.CircleEntitySecret() != "e" {
		t.Fatal("circle accessors")
	}
	if cfg.WebhookURL() != "https://w" {
		t.Fatalf("WebhookURL: %q", cfg.WebhookURL())
	}
	if cfg.MaxGasAmount() != 111 || cfg.TxnExpirationSeconds() != 45 || cfg.PollIntervalSeconds() != 7 {
		t.Fatal("transaction/poller accessors")
	}
	if cfg.SubmitterPollIntervalMs() != 11 ||
		cfg.SubmitterMaxRetryDurationSeconds() != 12 ||
		cfg.SubmitterRetryIntervalSeconds() != 13 ||
		cfg.SubmitterRetryJitterSeconds() != 14 ||
		cfg.SubmitterStaleProcessingSeconds() != 15 ||
		cfg.SubmitterRecoveryTickSeconds() != 16 ||
		cfg.SubmitterSigningPipelineDepth() != 17 {
		t.Fatal("submitter accessors")
	}
	if cfg.WebhookMaxRetries() != 18 || cfg.WebhookTimeoutSeconds() != 19 {
		t.Fatal("webhook accessors")
	}
	if !cfg.RateLimitEnabled() ||
		cfg.RateLimitRequestsPerSecond() != 20 ||
		cfg.RateLimitBurst() != 21 ||
		!cfg.RateLimitPerWallet() {
		t.Fatal("rate limit accessors")
	}
}

func TestServerPortDefault(t *testing.T) {
	cfg := defaultConfig()
	cfg.Server.Port = 0
	if cfg.ServerPort() != "8080" {
		t.Fatalf("got %q want 8080", cfg.ServerPort())
	}
}

func TestVerifyWalletValid(t *testing.T) {
	w := validTestWallet(t)
	if err := w.VerifyWallet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyWalletBadAddress(t *testing.T) {
	w := validTestWallet(t)
	w.Address = "not-an-address"
	if err := w.VerifyWallet(); err == nil {
		t.Fatal("expected error")
	}
}

func TestVerifyWalletBadPubKey(t *testing.T) {
	w := validTestWallet(t)
	w.PublicKey = "0x00"
	if err := w.VerifyWallet(); err == nil {
		t.Fatal("expected error")
	}
}

func TestVerifyWalletAddressAuthKeyMismatch(t *testing.T) {
	a := validTestWallet(t)
	b := validTestWallet(t)
	w := &CircleWallet{
		WalletID:  "w",
		Address:   a.Address,
		PublicKey: b.PublicKey,
	}
	err := w.VerifyWallet()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "address != authkey") {
		t.Fatalf("error: %v", err)
	}
}

func TestValidationZeroMaxGasAmount(t *testing.T) {
	t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("API_KEY", "k")
	t.Setenv("CIRCLE_API_KEY", "ck")
	t.Setenv("CIRCLE_ENTITY_SECRET", "es")
	t.Setenv("MAX_GAS_AMOUNT", "0")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_GAS_AMOUNT") {
		t.Fatalf("Load: %v", err)
	}
}

func TestValidationZeroExpiration(t *testing.T) {
	t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("API_KEY", "k")
	t.Setenv("CIRCLE_API_KEY", "ck")
	t.Setenv("CIRCLE_ENTITY_SECRET", "es")
	t.Setenv("TXN_EXPIRATION_SECONDS", "0")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "TXN_EXPIRATION_SECONDS") {
		t.Fatalf("Load: %v", err)
	}
}

func TestApplyEnvOverridesErrors(t *testing.T) {
	t.Run("BadServerPort", func(t *testing.T) {
		t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
		t.Setenv("MYSQL_DSN", "dsn")
		t.Setenv("API_KEY", "k")
		t.Setenv("CIRCLE_API_KEY", "ck")
		t.Setenv("CIRCLE_ENTITY_SECRET", "es")
		t.Setenv("SERVER_PORT", "not-int")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "SERVER_PORT") {
			t.Fatalf("Load: %v", err)
		}
	})
	t.Run("BadTestingMode", func(t *testing.T) {
		t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
		t.Setenv("MYSQL_DSN", "dsn")
		t.Setenv("API_KEY", "k")
		t.Setenv("CIRCLE_API_KEY", "ck")
		t.Setenv("CIRCLE_ENTITY_SECRET", "es")
		t.Setenv("TESTING_MODE", "maybe")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "TESTING_MODE") {
			t.Fatalf("Load: %v", err)
		}
	})
	t.Run("BadAptosChainID", func(t *testing.T) {
		t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
		t.Setenv("MYSQL_DSN", "dsn")
		t.Setenv("API_KEY", "k")
		t.Setenv("CIRCLE_API_KEY", "ck")
		t.Setenv("CIRCLE_ENTITY_SECRET", "es")
		t.Setenv("APTOS_CHAIN_ID", "x")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "APTOS_CHAIN_ID") {
			t.Fatalf("Load: %v", err)
		}
	})
	t.Run("BadMaxGasAmount", func(t *testing.T) {
		t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
		t.Setenv("MYSQL_DSN", "dsn")
		t.Setenv("API_KEY", "k")
		t.Setenv("CIRCLE_API_KEY", "ck")
		t.Setenv("CIRCLE_ENTITY_SECRET", "es")
		t.Setenv("MAX_GAS_AMOUNT", "x")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "MAX_GAS_AMOUNT") {
			t.Fatalf("Load: %v", err)
		}
	})
	t.Run("BadTxnExpiration", func(t *testing.T) {
		t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
		t.Setenv("MYSQL_DSN", "dsn")
		t.Setenv("API_KEY", "k")
		t.Setenv("CIRCLE_API_KEY", "ck")
		t.Setenv("CIRCLE_ENTITY_SECRET", "es")
		t.Setenv("TXN_EXPIRATION_SECONDS", "nope")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "TXN_EXPIRATION_SECONDS") {
			t.Fatalf("Load: %v", err)
		}
	})
}

func TestServerPortEmptyEnv(t *testing.T) {
	t.Setenv("CONFIG_PATH", nonexistentConfigPath(t))
	t.Setenv("MYSQL_DSN", "dsn")
	t.Setenv("API_KEY", "k")
	t.Setenv("CIRCLE_API_KEY", "ck")
	t.Setenv("CIRCLE_ENTITY_SECRET", "es")
	t.Setenv("SERVER_PORT", "   ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 0 {
		t.Fatalf("Port: got %d want 0", cfg.Server.Port)
	}
	if cfg.ServerPort() != "8080" {
		t.Fatalf("ServerPort: got %q", cfg.ServerPort())
	}
}
