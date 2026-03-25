package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	// Server
	ServerPort  string
	APIKey      string
	TestingMode bool

	// Aptos
	AptosNodeURL string
	AptosChainID uint8

	// Signer
	SignerProvider string // "local" or "circle"

	// Local signer keys (hex-encoded Ed25519 private keys)
	MinterPrivateKey          string
	DenylisterPrivateKey      string
	MasterMinterPrivateKey    string
	MetadataUpdaterPrivateKey string
	OwnerPrivateKey           string

	// Circle signer
	CircleAPIKey            string
	CircleEntitySecret      string
	MinterWalletID          string
	DenylisterWalletID      string
	MasterMinterWalletID    string
	MetadataUpdaterWalletID string
	OwnerWalletID           string

	// Per-role public keys (required for Circle signer)
	MinterPublicKey          string
	DenylisterPublicKey      string
	MasterMinterPublicKey    string
	MetadataUpdaterPublicKey string
	OwnerPublicKey           string

	// Per-role addresses (required for Circle, derived for local)
	MinterAddress          string
	DenylisterAddress      string
	MasterMinterAddress    string
	MetadataUpdaterAddress string
	OwnerAddress           string

	// SQLite
	SQLitePath string

	// Transaction settings
	MaxRetries              int
	PollIntervalSeconds     int
	MaxBatchSize            int
	MaxGasAmount            uint64
	GasPerRecipient         uint64
	TxnExpirationSeconds    int
	RetryBackoffBaseSeconds int
	RetryBackoffMaxSeconds  int
}

// Load reads configuration from environment variables, optionally loading a .env file first.
func Load() (*Config, error) {
	// Best-effort .env load — missing file is fine
	_ = godotenv.Load()

	chainID, err := getEnvUint8("APTOS_CHAIN_ID", 2)
	if err != nil {
		return nil, fmt.Errorf("invalid APTOS_CHAIN_ID: %w", err)
	}

	maxRetries, err := getEnvInt("MAX_RETRIES", 3)
	if err != nil {
		return nil, fmt.Errorf("invalid MAX_RETRIES: %w", err)
	}

	pollInterval, err := getEnvInt("POLL_INTERVAL_SECONDS", 5)
	if err != nil {
		return nil, fmt.Errorf("invalid POLL_INTERVAL_SECONDS: %w", err)
	}

	maxBatchSize, err := getEnvInt("MAX_BATCH_SIZE", 500)
	if err != nil {
		return nil, fmt.Errorf("invalid MAX_BATCH_SIZE: %w", err)
	}

	maxGasAmount, err := getEnvUint64("MAX_GAS_AMOUNT", 100_000)
	if err != nil {
		return nil, fmt.Errorf("invalid MAX_GAS_AMOUNT: %w", err)
	}

	gasPerRecipient, err := getEnvUint64("GAS_PER_RECIPIENT", 0)
	if err != nil {
		return nil, fmt.Errorf("invalid GAS_PER_RECIPIENT: %w", err)
	}

	txnExpiration, err := getEnvInt("TXN_EXPIRATION_SECONDS", 60)
	if err != nil {
		return nil, fmt.Errorf("invalid TXN_EXPIRATION_SECONDS: %w", err)
	}

	backoffBase, err := getEnvInt("RETRY_BACKOFF_BASE_SECONDS", 10)
	if err != nil {
		return nil, fmt.Errorf("invalid RETRY_BACKOFF_BASE_SECONDS: %w", err)
	}

	backoffMax, err := getEnvInt("RETRY_BACKOFF_MAX_SECONDS", 300)
	if err != nil {
		return nil, fmt.Errorf("invalid RETRY_BACKOFF_MAX_SECONDS: %w", err)
	}

	cfg := &Config{
		ServerPort:                getEnv("SERVER_PORT", "8080"),
		APIKey:                    getEnv("API_KEY", ""),
		TestingMode:               getEnvBool("TESTING_MODE", false),
		AptosNodeURL:              getEnv("APTOS_NODE_URL", "https://api.testnet.aptoslabs.com/v1"),
		AptosChainID:              chainID,
		SignerProvider:            getEnv("SIGNER_PROVIDER", "local"),
		MinterPrivateKey:          getEnv("MINTER_PRIVATE_KEY", ""),
		DenylisterPrivateKey:      getEnv("DENYLISTER_PRIVATE_KEY", ""),
		MasterMinterPrivateKey:    getEnv("MASTER_MINTER_PRIVATE_KEY", ""),
		MetadataUpdaterPrivateKey: getEnv("METADATA_UPDATER_PRIVATE_KEY", ""),
		OwnerPrivateKey:           getEnv("OWNER_PRIVATE_KEY", ""),
		CircleAPIKey:              getEnv("CIRCLE_API_KEY", ""),
		CircleEntitySecret:        getEnv("CIRCLE_ENTITY_SECRET", ""),
		MinterWalletID:            getEnv("MINTER_WALLET_ID", ""),
		DenylisterWalletID:        getEnv("DENYLISTER_WALLET_ID", ""),
		MasterMinterWalletID:      getEnv("MASTER_MINTER_WALLET_ID", ""),
		MetadataUpdaterWalletID:   getEnv("METADATA_UPDATER_WALLET_ID", ""),
		OwnerWalletID:             getEnv("OWNER_WALLET_ID", ""),
		MinterPublicKey:           getEnv("MINTER_PUBLIC_KEY", ""),
		DenylisterPublicKey:       getEnv("DENYLISTER_PUBLIC_KEY", ""),
		MasterMinterPublicKey:     getEnv("MASTER_MINTER_PUBLIC_KEY", ""),
		MetadataUpdaterPublicKey:  getEnv("METADATA_UPDATER_PUBLIC_KEY", ""),
		OwnerPublicKey:            getEnv("OWNER_PUBLIC_KEY", ""),
		MinterAddress:             getEnv("MINTER_ADDRESS", ""),
		DenylisterAddress:         getEnv("DENYLISTER_ADDRESS", ""),
		MasterMinterAddress:       getEnv("MASTER_MINTER_ADDRESS", ""),
		MetadataUpdaterAddress:    getEnv("METADATA_UPDATER_ADDRESS", ""),
		OwnerAddress:              getEnv("OWNER_ADDRESS", ""),
		SQLitePath:                getEnv("SQLITE_PATH", "./contractInt.db"),
		MaxRetries:                maxRetries,
		PollIntervalSeconds:       pollInterval,
		MaxBatchSize:              maxBatchSize,
		MaxGasAmount:              maxGasAmount,
		GasPerRecipient:           gasPerRecipient,
		TxnExpirationSeconds:      txnExpiration,
		RetryBackoffBaseSeconds:   backoffBase,
		RetryBackoffMaxSeconds:    backoffMax,
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.APIKey == "" && !c.TestingMode {
		return fmt.Errorf("API_KEY is required")
	}
	if c.AptosNodeURL == "" {
		return fmt.Errorf("APTOS_NODE_URL is required")
	}

	if c.MaxBatchSize <= 0 {
		return fmt.Errorf("MAX_BATCH_SIZE must be > 0, got %d", c.MaxBatchSize)
	}
	if c.MaxGasAmount == 0 {
		return fmt.Errorf("MAX_GAS_AMOUNT must be > 0")
	}
	if c.TxnExpirationSeconds <= 0 {
		return fmt.Errorf("TXN_EXPIRATION_SECONDS must be > 0, got %d", c.TxnExpirationSeconds)
	}

	switch c.SignerProvider {
	case "local":
		// At least one role key should be configured, but we don't enforce all
	case "circle":
		if c.CircleAPIKey == "" {
			return fmt.Errorf("CIRCLE_API_KEY is required when SIGNER_PROVIDER=circle")
		}
		if c.CircleEntitySecret == "" {
			return fmt.Errorf("CIRCLE_ENTITY_SECRET is required when SIGNER_PROVIDER=circle")
		}
	default:
		return fmt.Errorf("SIGNER_PROVIDER must be 'local' or 'circle', got %q", c.SignerProvider)
	}

	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvUint8(key string, fallback uint8) (uint8, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseUint(v, 10, 8)
	if err != nil {
		return 0, err
	}
	return uint8(n), nil
}

func getEnvUint64(key string, fallback uint64) (uint64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return strconv.ParseUint(v, 10, 64)
}

func getEnvInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return strconv.Atoi(v)
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
