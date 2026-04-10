package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"github.com/joho/godotenv"
)

// CircleWallet represents a Circle programmable wallet.
type CircleWallet struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

func (wallet *CircleWallet) VerifyWallet() error {
	// Verify address
	address := &aptos.AccountAddress{}
	err := address.ParseStringRelaxed(wallet.Address)
	if err != nil {
		return fmt.Errorf("failed to load wallet address %w", err)
	}

	// Validate and public key for sender (this prevents INVALID_SIGNATURE)
	pubKey := crypto.Ed25519PublicKey{}
	err = pubKey.FromHex(wallet.PublicKey)
	if err != nil {
		return fmt.Errorf("failed to load wallet public key %w", err)
	}

	addressStr := address.StringLong()
	authkeyStr := pubKey.AuthKey().ToHex()

	if addressStr != authkeyStr {
		return fmt.Errorf("address != authkey %s : %s", addressStr, authkeyStr)
	}
	return nil
}

// Config holds all application configuration.
type Config struct {
	// Server
	ServerPort  string
	APIKey      string
	TestingMode bool

	// Aptos
	AptosNodeURL string
	AptosChainID uint8

	// Circle
	CircleAPIKey       string
	CircleEntitySecret string
	CircleWallets      []CircleWallet

	// Webhook
	WebhookURL string

	// Transaction
	MaxGasAmount         uint64
	TxnExpirationSeconds int
	PollIntervalSeconds  int
	StoreTTLSeconds      int

	// Orderless transactions (replay-protection nonce mode)
	OrderlessEnabled bool
	NonceTTLSeconds  int

	// Idempotency
	IdempotencyTTLSeconds int
}

// Load reads configuration from environment variables with .env fallback.
func Load() (*Config, error) {
	// Load .env file if present; ignore error if missing.
	_ = godotenv.Load()

	walletsJSON := os.Getenv("CIRCLE_WALLETS")
	var wallets []CircleWallet
	if walletsJSON != "" {
		if err := json.Unmarshal([]byte(walletsJSON), &wallets); err != nil {
			return nil, fmt.Errorf("parsing CIRCLE_WALLETS: %w", err)
		}
		for i, wallet := range wallets {
			err := wallet.VerifyWallet()
			if err != nil {
				return nil, fmt.Errorf("failed to load wallet %d: %w", i, err)
			}
		}
	}

	cfg := &Config{
		ServerPort:  env("SERVER_PORT", "8080"),
		APIKey:      os.Getenv("API_KEY"),
		TestingMode: envBool("TESTING_MODE", false),

		AptosNodeURL: env("APTOS_NODE_URL", ""),
		AptosChainID: envUint8("APTOS_CHAIN_ID", 0),

		CircleAPIKey:       os.Getenv("CIRCLE_API_KEY"),
		CircleEntitySecret: os.Getenv("CIRCLE_ENTITY_SECRET"),
		CircleWallets:      wallets,

		WebhookURL: os.Getenv("WEBHOOK_URL"),

		MaxGasAmount:         envUint64("MAX_GAS_AMOUNT", 100000),
		TxnExpirationSeconds: envInt("TXN_EXPIRATION_SECONDS", 60),
		PollIntervalSeconds:  envInt("POLL_INTERVAL_SECONDS", 5),
		StoreTTLSeconds:      envInt("STORE_TTL_SECONDS", 180),

		OrderlessEnabled: envBool("ORDERLESS_ENABLED", true),
		NonceTTLSeconds:  envInt("NONCE_TTL_SECONDS", 120),

		IdempotencyTTLSeconds: envInt("IDEMPOTENCY_TTL_SECONDS", 300),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.APIKey == "" && !c.TestingMode {
		return fmt.Errorf("API_KEY is required unless TESTING_MODE is enabled")
	}
	if c.CircleAPIKey == "" {
		return fmt.Errorf("CIRCLE_API_KEY is required")
	}
	if c.CircleEntitySecret == "" {
		return fmt.Errorf("CIRCLE_ENTITY_SECRET is required")
	}
	if c.MaxGasAmount == 0 {
		return fmt.Errorf("MAX_GAS_AMOUNT must be greater than 0")
	}
	if c.TxnExpirationSeconds <= 0 {
		return fmt.Errorf("TXN_EXPIRATION_SECONDS must be greater than 0")
	}
	return nil
}

// WalletByID returns the CircleWallet with the given wallet ID, if any.
func (c *Config) WalletByID(id string) (CircleWallet, bool) {
	id = strings.TrimSpace(id)
	for _, w := range c.CircleWallets {
		if strings.TrimSpace(w.WalletID) == id {
			return w, true
		}
	}
	return CircleWallet{}, false
}

// WalletByAddress returns the CircleWallet with the given Aptos address, if any.
func (c *Config) WalletByAddress(addr string) (CircleWallet, bool) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	for _, w := range c.CircleWallets {
		if strings.ToLower(strings.TrimSpace(w.Address)) == addr {
			return w, true
		}
	}
	return CircleWallet{}, false
}

// LookupWallet finds a wallet by wallet_id or address (tries both).
func (c *Config) LookupWallet(idOrAddr string) (CircleWallet, bool) {
	if w, ok := c.WalletByID(idOrAddr); ok {
		return w, true
	}
	return c.WalletByAddress(idOrAddr)
}

// env returns the environment variable value or a default.
func env(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// envBool parses a boolean environment variable.
func envBool(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultVal
	}
	return b
}

// envInt parses an integer environment variable.
func envInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

// envUint8 parses a uint8 environment variable.
func envUint8(key string, defaultVal uint8) uint8 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseUint(v, 10, 8)
	if err != nil {
		return defaultVal
	}
	return uint8(n)
}

// envUint64 parses a uint64 environment variable.
func envUint64(key string, defaultVal uint64) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return defaultVal
	}
	return n
}
