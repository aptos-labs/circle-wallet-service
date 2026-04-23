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

// CircleWallet represents a resolved Circle wallet with its Aptos address and
// Ed25519 public key. Used by the submitter to verify address/key consistency.
type CircleWallet struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

// VerifyWallet confirms that the wallet's public key derives to the claimed
// Aptos address (authkey == address). Returns an error on mismatch.
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
	Port        int    `yaml:"port"`
	TestingMode bool   `yaml:"testing_mode"`
	APIKey      string `yaml:"api_key,omitempty"`
}

type MySQLConfig struct {
	DSN string `yaml:"dsn"`
}

type AptosConfig struct {
	NodeURL string `yaml:"node_url"`
	ChainID uint8  `yaml:"chain_id"`
	// APIKey, when set, is sent as `Authorization: Bearer <APIKey>` on every
	// request to NodeURL. Required in practice for any non-trivial load
	// against public Aptos endpoints (Geomi / Aptos Labs), which otherwise
	// rate-limit by IP. Empty = no auth header.
	APIKey string `yaml:"api_key"`
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
	PollIntervalMs          int `yaml:"poll_interval_ms"`
	MaxRetryDurationSeconds int `yaml:"max_retry_duration_seconds"`
	RetryIntervalSeconds    int `yaml:"retry_interval_seconds"`
	RetryJitterSeconds      int `yaml:"retry_jitter_seconds"`
	StaleProcessingSeconds  int `yaml:"stale_processing_seconds"`
	RecoveryTickSeconds     int `yaml:"recovery_tick_seconds"`
	SigningPipelineDepth    int `yaml:"signing_pipeline_depth"`
	// SimulateBeforeSubmit runs /transactions/simulate between build and sign.
	// On VM-level rejection (Success=false), the row is marked failed with
	// vm_status instead of burning a Circle signing round-trip and an on-chain
	// submission. Transient node errors (503/429/timeouts) fall through to the
	// normal requeue path. Defaults to true.
	SimulateBeforeSubmit bool `yaml:"simulate_before_submit"`
	// CalibrateGasFromSimulation uses the simulation's gas_used to shrink
	// max_gas_amount for the real submit (gas_used * 1.5, floored to the caller's
	// request when that's smaller). Reduces how much gas each sender reserves
	// per txn. Defaults to true; only takes effect when SimulateBeforeSubmit is
	// also enabled.
	CalibrateGasFromSimulation bool `yaml:"calibrate_gas_from_simulation"`
	// Per-call RPC timeouts. These exist so a single slow Circle or Aptos
	// response can't pin a per-sender worker past the stale-processing
	// threshold, which would otherwise let RecoverStaleProcessing race with
	// an in-flight signing pipeline. Values are in seconds; <= 0 disables
	// the timeout for that call.
	CircleSignTimeoutSeconds         int `yaml:"circle_sign_timeout_seconds"`
	AptosBuildTimeoutSeconds         int `yaml:"aptos_build_timeout_seconds"`
	AptosSimulateTimeoutSeconds      int `yaml:"aptos_simulate_timeout_seconds"`
	AptosSubmitTimeoutSeconds        int `yaml:"aptos_submit_timeout_seconds"`
	AptosAccountLookupTimeoutSeconds int `yaml:"aptos_account_lookup_timeout_seconds"`
}

type PollerConfig struct {
	IntervalSeconds int `yaml:"interval_seconds"`
	// RPCRequestsPerSecond caps the rate at which the poller makes
	// TransactionByHash calls against the Aptos node across all records it
	// sweeps in a single tick. 0 disables rate limiting. Defaults to 10.
	RPCRequestsPerSecond int `yaml:"rpc_requests_per_second"`
	// RPCBurst is the token-bucket burst size. Defaults to RPCRequestsPerSecond.
	RPCBurst int `yaml:"rpc_burst"`
	// PageSize caps how many rows a single ListByStatusPaged call pulls.
	// The poller loops pages within a tick so the sweep can cover large
	// backlogs without loading every unconfirmed row into memory. 500 keeps
	// the resident-set bounded to a few hundred KB per page.
	PageSize int `yaml:"page_size"`
	// SweepConcurrency is the size of the worker pool draining each page.
	// Parallelism under the rate limiter is free (the limiter is the real
	// throughput ceiling), so this lets one slow lookup not block every
	// other record in the same page. Defaults to RPCBurst (or 4 if unset).
	SweepConcurrency int `yaml:"sweep_concurrency"`
}

type WebhookConfig struct {
	GlobalURL      string `yaml:"global_url"`
	MaxRetries     int    `yaml:"max_retries"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	// SigningSecret is the shared secret used to compute the HMAC-SHA256
	// signature attached to every outbound delivery. When empty, deliveries
	// are sent without a signature header. Set via WEBHOOK_SIGNING_SECRET.
	SigningSecret string `yaml:"signing_secret"`
	// DeliveryConcurrency caps how many webhook deliveries run in parallel
	// within a single processBatch tick. The serial-per-batch default meant
	// one slow endpoint (full TimeoutSeconds to fail) would block every
	// other delivery behind it, stretching the effective latency of a batch
	// of N deliveries to N * TimeoutSeconds. A bounded pool keeps the
	// slowest customer from pinning the queue for everyone else. <=0
	// falls back to 1 (serial).
	DeliveryConcurrency int `yaml:"delivery_concurrency"`
}

// ArchiveConfig governs the background archive worker that trims old
// terminal-status rows from the transactions table.
//
// The worker runs in two stages per tick:
//  1. NULL idempotency_key on rows older than IdempotencyRetentionDays
//     (frees UNIQUE slots while keeping the audit row).
//  2. DELETE rows older than RetentionDays (cascades to webhook_deliveries
//     via fk_webhook_txn ON DELETE CASCADE).
//
// Without an archive sweep the transactions table grows unbounded — a
// production backlog of millions of confirmed rows eventually hurts every
// query that touches the table (plus the UNIQUE(idempotency_key) index).
type ArchiveConfig struct {
	// Enabled opts into the archive worker. Off by default because the right
	// retention window is operator-specific (compliance / audit needs).
	Enabled bool `yaml:"enabled"`
	// TickSeconds sets how often the archive loop wakes up. One sweep is
	// cheap when nothing's aged past retention, so a low-frequency tick
	// (5 min) is fine.
	TickSeconds int `yaml:"tick_seconds"`
	// RetentionDays is how long a terminal row (confirmed/failed/expired)
	// sticks around before being deleted. Default 30 days.
	RetentionDays int `yaml:"retention_days"`
	// IdempotencyRetentionDays is the shorter window after which only the
	// idempotency_key is NULLed on a terminal row. Must be <= RetentionDays
	// to make sense; values above RetentionDays are ignored (the DELETE step
	// would have already removed the row, taking its key with it).
	// Default 7 days.
	IdempotencyRetentionDays int `yaml:"idempotency_retention_days"`
	// BatchSize caps each DELETE/UPDATE per tick so one sweep cannot hold
	// the table's row locks for long. The worker loops within a tick until
	// a batch comes back short. Default 1000.
	BatchSize int `yaml:"batch_size"`
}

type RateLimitConfig struct {
	Enabled           bool `yaml:"enabled"`
	RequestsPerSecond int  `yaml:"requests_per_second"`
	Burst             int  `yaml:"burst"`
	PerWallet         bool `yaml:"per_wallet"`
}

// Config holds all application settings. Created by [Load].
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	MySQL       MySQLConfig       `yaml:"mysql"`
	Aptos       AptosConfig       `yaml:"aptos"`
	Circle      CircleConfig      `yaml:"circle"`
	Transaction TransactionConfig `yaml:"transaction"`
	Submitter   SubmitterConfig   `yaml:"submitter"`
	Poller      PollerConfig      `yaml:"poller"`
	Webhook     WebhookConfig     `yaml:"webhook"`
	Archive     ArchiveConfig     `yaml:"archive"`
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
			PollIntervalMs:                   200,
			MaxRetryDurationSeconds:          300,
			RetryIntervalSeconds:             5,
			RetryJitterSeconds:               2,
			StaleProcessingSeconds:           120,
			RecoveryTickSeconds:              30,
			SigningPipelineDepth:             4,
			SimulateBeforeSubmit:             true,
			CalibrateGasFromSimulation:       true,
			CircleSignTimeoutSeconds:         15,
			AptosBuildTimeoutSeconds:         10,
			AptosSimulateTimeoutSeconds:      30,
			AptosSubmitTimeoutSeconds:        10,
			AptosAccountLookupTimeoutSeconds: 10,
		},
		Poller: PollerConfig{
			IntervalSeconds:      5,
			RPCRequestsPerSecond: 10,
			RPCBurst:             10,
			PageSize:             500,
			SweepConcurrency:     10,
		},
		Webhook: WebhookConfig{
			GlobalURL:           "",
			MaxRetries:          5,
			TimeoutSeconds:      10,
			DeliveryConcurrency: 4,
		},
		Archive: ArchiveConfig{
			Enabled:                  false,
			TickSeconds:              300,
			RetentionDays:            30,
			IdempotencyRetentionDays: 7,
			BatchSize:                1000,
		},
		RateLimit: RateLimitConfig{
			Enabled:           false,
			RequestsPerSecond: 100,
			Burst:             200,
			PerWallet:         false,
		},
	}
}

// Load reads config.yaml (or CONFIG_PATH), applies environment variable
// overrides, validates required fields, and returns the final [Config].
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
	if v, ok := os.LookupEnv("APTOS_API_KEY"); ok {
		c.Aptos.APIKey = strings.TrimSpace(v)
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
	if v, ok := os.LookupEnv("WEBHOOK_SIGNING_SECRET"); ok {
		c.Webhook.SigningSecret = v
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
	if v, ok := os.LookupEnv("SIMULATE_BEFORE_SUBMIT"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("SIMULATE_BEFORE_SUBMIT: %w", err)
		}
		c.Submitter.SimulateBeforeSubmit = b
	}
	if v, ok := os.LookupEnv("CALIBRATE_GAS_FROM_SIMULATION"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("CALIBRATE_GAS_FROM_SIMULATION: %w", err)
		}
		c.Submitter.CalibrateGasFromSimulation = b
	}
	// Submitter retry budget is checked against time.Since(CreatedAt). Useful to
	// override in environments with long sign/submit latency (E2E against real
	// Circle + testnet) without touching YAML.
	if v, ok := os.LookupEnv("SUBMITTER_MAX_RETRY_DURATION_SECONDS"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("SUBMITTER_MAX_RETRY_DURATION_SECONDS: %w", err)
		}
		c.Submitter.MaxRetryDurationSeconds = n
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
	if c.AptosChainID() == 0 {
		return fmt.Errorf("APTOS_CHAIN_ID must be greater than 0")
	}
	// Per-wallet rate limiting is not yet implemented; the middleware only
	// enforces a global token bucket. Accepting rate_limit.per_wallet=true
	// would silently no-op, so reject it explicitly until the feature lands.
	if c.RateLimit.PerWallet {
		return fmt.Errorf("rate_limit.per_wallet is not yet implemented; leave it false")
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

func (c *Config) AptosAPIKey() string {
	return c.Aptos.APIKey
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

func (c *Config) PollerRPCRequestsPerSecond() int {
	return c.Poller.RPCRequestsPerSecond
}

func (c *Config) PollerRPCBurst() int {
	if c.Poller.RPCBurst > 0 {
		return c.Poller.RPCBurst
	}
	return c.Poller.RPCRequestsPerSecond
}

func (c *Config) PollerPageSize() int {
	if c.Poller.PageSize > 0 {
		return c.Poller.PageSize
	}
	return 500
}

func (c *Config) ArchiveEnabled() bool {
	return c.Archive.Enabled
}

func (c *Config) ArchiveTickSeconds() int {
	if c.Archive.TickSeconds > 0 {
		return c.Archive.TickSeconds
	}
	return 300
}

func (c *Config) ArchiveRetentionDays() int {
	if c.Archive.RetentionDays > 0 {
		return c.Archive.RetentionDays
	}
	return 30
}

func (c *Config) ArchiveIdempotencyRetentionDays() int {
	if c.Archive.IdempotencyRetentionDays > 0 {
		return c.Archive.IdempotencyRetentionDays
	}
	return 7
}

func (c *Config) ArchiveBatchSize() int {
	if c.Archive.BatchSize > 0 {
		return c.Archive.BatchSize
	}
	return 1000
}

func (c *Config) PollerSweepConcurrency() int {
	if c.Poller.SweepConcurrency > 0 {
		return c.Poller.SweepConcurrency
	}
	if burst := c.PollerRPCBurst(); burst > 0 {
		return burst
	}
	return 4
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

func (c *Config) SimulateBeforeSubmit() bool {
	return c.Submitter.SimulateBeforeSubmit
}

func (c *Config) CalibrateGasFromSimulation() bool {
	return c.Submitter.CalibrateGasFromSimulation
}

func (c *Config) CircleSignTimeoutSeconds() int {
	return c.Submitter.CircleSignTimeoutSeconds
}

func (c *Config) AptosBuildTimeoutSeconds() int {
	return c.Submitter.AptosBuildTimeoutSeconds
}

func (c *Config) AptosSimulateTimeoutSeconds() int {
	return c.Submitter.AptosSimulateTimeoutSeconds
}

func (c *Config) AptosSubmitTimeoutSeconds() int {
	return c.Submitter.AptosSubmitTimeoutSeconds
}

func (c *Config) AptosAccountLookupTimeoutSeconds() int {
	return c.Submitter.AptosAccountLookupTimeoutSeconds
}

func (c *Config) WebhookMaxRetries() int {
	return c.Webhook.MaxRetries
}

func (c *Config) WebhookTimeoutSeconds() int {
	return c.Webhook.TimeoutSeconds
}

func (c *Config) WebhookSigningSecret() string {
	return c.Webhook.SigningSecret
}

func (c *Config) WebhookDeliveryConcurrency() int {
	if c.Webhook.DeliveryConcurrency <= 0 {
		return 1
	}
	return c.Webhook.DeliveryConcurrency
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
