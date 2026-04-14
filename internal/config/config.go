package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type CircleWallet struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

func (wallet *CircleWallet) VerifyWallet() error {
	address := &aptos.AccountAddress{}
	err := address.ParseStringRelaxed(wallet.Address)
	if err != nil {
		return fmt.Errorf("failed to load wallet address %w", err)
	}

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

type ServerConfig struct {
	Port         int    `yaml:"port"`
	TestingMode  bool   `yaml:"testing_mode"`
	APIKey       string `yaml:"api_key,omitempty"`
}

type MySQLConfig struct {
	DSN string `yaml:"dsn"`
}

type AptosConfig struct {
	NodeURL string `yaml:"node_url"`
	ChainID uint8  `yaml:"chain_id"`
}

type CircleConfig struct {
	APIKey       string `yaml:"api_key"`
	EntitySecret string `yaml:"entity_secret"`
}

type TransactionConfig struct {
	MaxGasAmount      uint64 `yaml:"max_gas_amount"`
	ExpirationSeconds int    `yaml:"expiration_seconds"`
}

type SubmitterConfig struct {
	PollIntervalMs             int `yaml:"poll_interval_ms"`
	MaxRetryDurationSeconds    int `yaml:"max_retry_duration_seconds"`
	RetryIntervalSeconds       int `yaml:"retry_interval_seconds"`
	RetryJitterSeconds         int `yaml:"retry_jitter_seconds"`
	StaleProcessingSeconds     int `yaml:"stale_processing_seconds"`
	RecoveryTickSeconds        int `yaml:"recovery_tick_seconds"`
	SigningPipelineDepth       int `yaml:"signing_pipeline_depth"`
}

type PollerConfig struct {
	IntervalSeconds int `yaml:"interval_seconds"`
}

type WebhookConfig struct {
	GlobalURL      string `yaml:"global_url"`
	MaxRetries     int    `yaml:"max_retries"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type RateLimitConfig struct {
	Enabled           bool `yaml:"enabled"`
	RequestsPerSecond int  `yaml:"requests_per_second"`
	Burst             int  `yaml:"burst"`
	PerWallet         bool `yaml:"per_wallet"`
}

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	MySQL       MySQLConfig       `yaml:"mysql"`
	Aptos       AptosConfig       `yaml:"aptos"`
	Circle      CircleConfig      `yaml:"circle"`
	Transaction TransactionConfig `yaml:"transaction"`
	Submitter   SubmitterConfig   `yaml:"submitter"`
	Poller      PollerConfig      `yaml:"poller"`
	Webhook     WebhookConfig     `yaml:"webhook"`
	RateLimit   RateLimitConfig   `yaml:"rate_limit"`
}

func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:        8080,
			TestingMode: false,
		},
		MySQL: MySQLConfig{DSN: ""},
		Aptos: AptosConfig{
			NodeURL: "https://api.testnet.aptoslabs.com/v1",
			ChainID: 2,
		},
		Circle: CircleConfig{},
		Transaction: TransactionConfig{
			MaxGasAmount:      2000000,
			ExpirationSeconds: 60,
		},
		Submitter: SubmitterConfig{
			PollIntervalMs:             200,
			MaxRetryDurationSeconds:    300,
			RetryIntervalSeconds:       5,
			RetryJitterSeconds:         2,
			StaleProcessingSeconds:     120,
			RecoveryTickSeconds:        30,
			SigningPipelineDepth:       4,
		},
		Poller: PollerConfig{IntervalSeconds: 5},
		Webhook: WebhookConfig{
			GlobalURL:      "",
			MaxRetries:     5,
			TimeoutSeconds: 10,
		},
		RateLimit: RateLimitConfig{
			Enabled:           false,
			RequestsPerSecond: 100,
			Burst:             200,
			PerWallet:         false,
		},
	}
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := defaultConfig()

	path := env("CONFIG_PATH", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config file %q: %w", path, err)
		}
	} else {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse yaml %q: %w", path, err)
		}
	}

	if err := applyEnvOverrides(cfg); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnvOverrides(c *Config) error {
	if v, ok := os.LookupEnv("API_KEY"); ok {
		c.Server.APIKey = v
	}
	if v, ok := os.LookupEnv("MYSQL_DSN"); ok {
		c.MySQL.DSN = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("CIRCLE_API_KEY"); ok {
		c.Circle.APIKey = v
	}
	if v, ok := os.LookupEnv("CIRCLE_ENTITY_SECRET"); ok {
		c.Circle.EntitySecret = v
	}
	if v, ok := os.LookupEnv("SERVER_PORT"); ok {
		v = strings.TrimSpace(v)
		if v == "" {
			c.Server.Port = 0
		} else {
			p, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("SERVER_PORT: %w", err)
			}
			c.Server.Port = p
		}
	}
	if v, ok := os.LookupEnv("TESTING_MODE"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("TESTING_MODE: %w", err)
		}
		c.Server.TestingMode = b
	}
	if v, ok := os.LookupEnv("APTOS_NODE_URL"); ok {
		c.Aptos.NodeURL = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("APTOS_CHAIN_ID"); ok {
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 8)
		if err != nil {
			return fmt.Errorf("APTOS_CHAIN_ID: %w", err)
		}
		c.Aptos.ChainID = uint8(n)
	}
	if v, ok := os.LookupEnv("WEBHOOK_URL"); ok {
		c.Webhook.GlobalURL = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("MAX_GAS_AMOUNT"); ok {
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return fmt.Errorf("MAX_GAS_AMOUNT: %w", err)
		}
		c.Transaction.MaxGasAmount = n
	}
	if v, ok := os.LookupEnv("TXN_EXPIRATION_SECONDS"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("TXN_EXPIRATION_SECONDS: %w", err)
		}
		c.Transaction.ExpirationSeconds = n
	}
	return nil
}

func (c *Config) validate() error {
	if c.MySQLDSN() == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}
	if c.APIKey() == "" && !c.TestingMode() {
		return fmt.Errorf("API_KEY is required unless TESTING_MODE is enabled")
	}
	if !c.TestingMode() {
		if c.CircleAPIKey() == "" {
			return fmt.Errorf("CIRCLE_API_KEY is required")
		}
		if c.CircleEntitySecret() == "" {
			return fmt.Errorf("CIRCLE_ENTITY_SECRET is required")
		}
	}
	if c.MaxGasAmount() == 0 {
		return fmt.Errorf("MAX_GAS_AMOUNT must be greater than 0")
	}
	if c.TxnExpirationSeconds() <= 0 {
		return fmt.Errorf("TXN_EXPIRATION_SECONDS must be greater than 0")
	}
	return nil
}

func (c *Config) ServerPort() string {
	if c.Server.Port == 0 {
		return "8080"
	}
	return strconv.Itoa(c.Server.Port)
}

func (c *Config) APIKey() string {
	return c.Server.APIKey
}

func (c *Config) TestingMode() bool {
	return c.Server.TestingMode
}

func (c *Config) MySQLDSN() string {
	return c.MySQL.DSN
}

func (c *Config) AptosNodeURL() string {
	return c.Aptos.NodeURL
}

func (c *Config) AptosChainID() uint8 {
	return c.Aptos.ChainID
}

func (c *Config) CircleAPIKey() string {
	return c.Circle.APIKey
}

func (c *Config) CircleEntitySecret() string {
	return c.Circle.EntitySecret
}

func (c *Config) WebhookURL() string {
	return c.Webhook.GlobalURL
}

func (c *Config) MaxGasAmount() uint64 {
	return c.Transaction.MaxGasAmount
}

func (c *Config) TxnExpirationSeconds() int {
	return c.Transaction.ExpirationSeconds
}

func (c *Config) PollIntervalSeconds() int {
	return c.Poller.IntervalSeconds
}

func (c *Config) SubmitterPollIntervalMs() int {
	return c.Submitter.PollIntervalMs
}

func (c *Config) SubmitterMaxRetryDurationSeconds() int {
	return c.Submitter.MaxRetryDurationSeconds
}

func (c *Config) SubmitterRetryIntervalSeconds() int {
	return c.Submitter.RetryIntervalSeconds
}

func (c *Config) SubmitterRetryJitterSeconds() int {
	return c.Submitter.RetryJitterSeconds
}

func (c *Config) SubmitterStaleProcessingSeconds() int {
	return c.Submitter.StaleProcessingSeconds
}

func (c *Config) SubmitterRecoveryTickSeconds() int {
	return c.Submitter.RecoveryTickSeconds
}

func (c *Config) SubmitterSigningPipelineDepth() int {
	return c.Submitter.SigningPipelineDepth
}

func (c *Config) WebhookMaxRetries() int {
	return c.Webhook.MaxRetries
}

func (c *Config) WebhookTimeoutSeconds() int {
	return c.Webhook.TimeoutSeconds
}

func (c *Config) RateLimitEnabled() bool {
	return c.RateLimit.Enabled
}

func (c *Config) RateLimitRequestsPerSecond() int {
	return c.RateLimit.RequestsPerSecond
}

func (c *Config) RateLimitBurst() int {
	return c.RateLimit.Burst
}

func (c *Config) RateLimitPerWallet() bool {
	return c.RateLimit.PerWallet
}

func env(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

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
