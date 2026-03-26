//go:build e2e

package e2e

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/bcs"
	"github.com/aptos-labs/aptos-go-sdk/crypto"

	"github.com/aptos-labs/jc-contract-integration/internal/account"
	apiPkg "github.com/aptos-labs/jc-contract-integration/internal/api"
	"github.com/aptos-labs/jc-contract-integration/internal/api/handler"
	"github.com/aptos-labs/jc-contract-integration/internal/api/middleware"
	aptosint "github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/signer"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/aptos-labs/jc-contract-integration/internal/txn"
)

const (
	devnetNodeURL   = "https://api.devnet.aptoslabs.com/v1"
	devnetFaucetURL = "https://faucet.devnet.aptoslabs.com"
	testAPIKey      = "test-e2e-api-key"
)

// Package-level shared state populated during TestMain setup.
var (
	testServer *httptest.Server
	testClient *e2eClient
	cancelFunc context.CancelFunc

	// Key pairs for each role.
	ownerKey           *crypto.Ed25519PrivateKey
	masterMinterKey    *crypto.Ed25519PrivateKey
	minterKey          *crypto.Ed25519PrivateKey
	denylisterKey      *crypto.Ed25519PrivateKey
	metadataUpdaterKey *crypto.Ed25519PrivateKey

	// Derived addresses.
	ownerAddr           aptossdk.AccountAddress
	masterMinterAddr    aptossdk.AccountAddress
	minterAddr          aptossdk.AccountAddress
	denylisterAddr      aptossdk.AccountAddress
	metadataUpdaterAddr aptossdk.AccountAddress

	// Aptos clients.
	aptosClient *aptosint.Client
)

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("aptos"); err != nil {
		if isStrictE2E() {
			log.Println("FAIL: aptos CLI not found on PATH (strict e2e mode enabled)")
			os.Exit(1)
		}
		log.Println("SKIP: aptos CLI not found on PATH")
		os.Exit(0)
	}

	if err := setup(); err != nil {
		log.Fatalf("E2E setup failed: %v", err)
	}

	code := m.Run()
	teardown()
	os.Exit(code)
}

func isStrictE2E() bool {
	// E2E_STRICT takes precedence when explicitly set.
	if raw, ok := os.LookupEnv("E2E_STRICT"); ok {
		v, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err == nil {
			return v
		}
	}

	// Default to strict mode in CI environments.
	ci := strings.TrimSpace(strings.ToLower(os.Getenv("CI")))
	return ci == "true" || ci == "1"
}

func setup() error {
	log.Println("=== E2E Setup: Generating accounts ===")
	if err := generateTestAccounts(); err != nil {
		return fmt.Errorf("generate accounts: %w", err)
	}

	log.Println("=== E2E Setup: Funding accounts ===")
	if err := fundAccounts(); err != nil {
		return fmt.Errorf("fund accounts: %w", err)
	}

	log.Println("=== E2E Setup: Deploying contract ===")
	if err := deployContract(); err != nil {
		return fmt.Errorf("deploy contract: %w", err)
	}

	log.Println("=== E2E Setup: Initializing contract ===")
	if err := initializeContract(); err != nil {
		return fmt.Errorf("initialize contract: %w", err)
	}

	log.Println("=== E2E Setup: Assigning roles ===")
	if err := assignRoles(); err != nil {
		return fmt.Errorf("assign roles: %w", err)
	}

	log.Println("=== E2E Setup: Configuring minter ===")
	if err := setupMinter(); err != nil {
		return fmt.Errorf("configure minter: %w", err)
	}

	log.Println("=== E2E Setup: Starting server ===")
	if err := startTestServer(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	log.Println("=== E2E Setup complete ===")
	return nil
}

func teardown() {
	if testServer != nil {
		testServer.Close()
	}
	if cancelFunc != nil {
		cancelFunc()
	}
}

// generateTestAccounts creates 5 Ed25519 key pairs and derives their addresses.
func generateTestAccounts() error {
	keys := []*struct {
		name string
		key  **crypto.Ed25519PrivateKey
		addr *aptossdk.AccountAddress
	}{
		{"owner", &ownerKey, &ownerAddr},
		{"master_minter", &masterMinterKey, &masterMinterAddr},
		{"minter", &minterKey, &minterAddr},
		{"denylister", &denylisterKey, &denylisterAddr},
		{"metadata_updater", &metadataUpdaterKey, &metadataUpdaterAddr},
	}

	for _, k := range keys {
		key, err := crypto.GenerateEd25519PrivateKey()
		if err != nil {
			return fmt.Errorf("generate %s key: %w", k.name, err)
		}
		*k.key = key
		*k.addr = keyToAddress(key)
		log.Printf("  %-18s %s", k.name+":", k.addr.String())
	}

	return nil
}

// fundAccounts funds all test accounts on devnet via the faucet.
func fundAccounts() error {
	faucetClient, err := aptossdk.NewClient(aptossdk.NetworkConfig{
		NodeUrl:   devnetNodeURL,
		FaucetUrl: devnetFaucetURL,
	})
	if err != nil {
		return fmt.Errorf("create faucet client: %w", err)
	}

	aptosClient, err = aptosint.NewClient(devnetNodeURL, 0)
	if err != nil {
		return fmt.Errorf("create aptos client: %w", err)
	}

	addrs := []struct {
		name string
		addr aptossdk.AccountAddress
	}{
		{"owner", ownerAddr},
		{"master_minter", masterMinterAddr},
		{"minter", minterAddr},
		{"denylister", denylisterAddr},
		{"metadata_updater", metadataUpdaterAddr},
	}

	for _, a := range addrs {
		log.Printf("  Funding %s...", a.name)
		if err := faucetClient.Fund(a.addr, 100_000_000); err != nil {
			return fmt.Errorf("fund %s: %w", a.name, err)
		}
	}

	return nil
}

// deployContract compiles and publishes the contractInt contract via the Aptos CLI.
func deployContract() error {
	contractsDir, err := filepath.Abs("../contracts")
	if err != nil {
		return fmt.Errorf("resolve contracts dir: %w", err)
	}

	namedAddrs := fmt.Sprintf("contractInt=%s", ownerAddr.String())

	log.Println("  Compiling...")
	compile := exec.Command("aptos", "move", "compile",
		"--package-dir", contractsDir,
		"--named-addresses", namedAddrs,
	)
	compile.Stdout = os.Stdout
	compile.Stderr = os.Stderr
	if err := compile.Run(); err != nil {
		return fmt.Errorf("compile: %w", err)
	}

	log.Println("  Publishing...")
	publish := exec.Command("aptos", "move", "publish",
		"--package-dir", contractsDir,
		"--named-addresses", namedAddrs,
		"--private-key", privateKeySeedHex(ownerKey),
		"--url", devnetNodeURL,
		"--assume-yes",
		"--max-gas", "200000",
	)
	publish.Stdout = os.Stdout
	publish.Stderr = os.Stderr
	if err := publish.Run(); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	if err := aptosint.InitModule(ownerAddr.String()); err != nil {
		return fmt.Errorf("init module address: %w", err)
	}

	return nil
}

// initializeContract calls contractInt::initialize via the Go SDK.
func initializeContract() error {
	nameBytes, err := bcs.SerializeSingle(func(ser *bcs.Serializer) {
		ser.WriteString("contractInt")
	})
	if err != nil {
		return fmt.Errorf("serialize name: %w", err)
	}

	symbolBytes, err := bcs.SerializeSingle(func(ser *bcs.Serializer) {
		ser.WriteString("JIO")
	})
	if err != nil {
		return fmt.Errorf("serialize symbol: %w", err)
	}

	decimalsBytes, err := bcs.SerializeSingle(func(ser *bcs.Serializer) {
		ser.U8(6)
	})
	if err != nil {
		return fmt.Errorf("serialize decimals: %w", err)
	}

	iconBytes, err := bcs.SerializeSingle(func(ser *bcs.Serializer) {
		ser.WriteString("")
	})
	if err != nil {
		return fmt.Errorf("serialize icon_uri: %w", err)
	}

	projectBytes, err := bcs.SerializeSingle(func(ser *bcs.Serializer) {
		ser.WriteString("")
	})
	if err != nil {
		return fmt.Errorf("serialize project_uri: %w", err)
	}

	payload := aptossdk.TransactionPayload{
		Payload: &aptossdk.EntryFunction{
			Module: aptossdk.ModuleId{
				Address: ownerAddr,
				Name:    "contractInt",
			},
			Function: "initialize",
			ArgTypes: []aptossdk.TypeTag{},
			Args:     [][]byte{nameBytes, symbolBytes, decimalsBytes, iconBytes, projectBytes},
		},
	}

	return submitAndWait(ownerKey, ownerAddr, payload)
}

// assignRoles updates master_minter, denylister, and metadata_updater via the Go SDK.
func assignRoles() error {
	type roleUpdate struct {
		name    string
		addr    aptossdk.AccountAddress
		builder func(aptossdk.AccountAddress) (aptossdk.TransactionPayload, error)
	}

	updates := []roleUpdate{
		{"master_minter", masterMinterAddr, aptosint.UpdateMasterMinterPayload},
		{"denylister", denylisterAddr, aptosint.UpdateDenylisterPayload},
		{"metadata_updater", metadataUpdaterAddr, aptosint.UpdateMetadataUpdaterPayload},
	}

	for _, u := range updates {
		log.Printf("  Assigning %s to %s", u.name, u.addr.String())
		payload, err := u.builder(u.addr)
		if err != nil {
			return fmt.Errorf("build %s payload: %w", u.name, err)
		}
		if err := submitAndWait(ownerKey, ownerAddr, payload); err != nil {
			return fmt.Errorf("submit %s: %w", u.name, err)
		}
	}

	return nil
}

// setupMinter configures the minter role with 1,000,000 allowance.
func setupMinter() error {
	payload, err := aptosint.ConfigureMinterPayload(minterAddr, 1_000_000)
	if err != nil {
		return fmt.Errorf("build configure_minter payload: %w", err)
	}
	return submitAndWait(masterMinterKey, masterMinterAddr, payload)
}

// startTestServer starts an in-process API server backed by devnet.
func startTestServer() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	registry := account.NewRegistry()
	keys := []*crypto.Ed25519PrivateKey{
		ownerKey,
		masterMinterKey,
		minterKey,
		denylisterKey,
		metadataUpdaterKey,
	}
	for _, k := range keys {
		s, err := signer.NewLocalSigner(k.ToHex())
		if err != nil {
			return fmt.Errorf("create signer: %w", err)
		}
		registry.Register(s)
	}

	mgr := txn.NewManager(aptosClient, st, registry, 3, logger)
	poller := txn.NewPoller(aptosClient, st, 2*time.Second, logger)

	router := buildTestRouter(mgr, aptosClient.View, logger)
	testServer = httptest.NewServer(router)

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel
	go poller.Run(ctx)

	testClient = &e2eClient{
		baseURL: testServer.URL,
		apiKey:  testAPIKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}

	log.Printf("  Server running at %s", testServer.URL)
	return nil
}

// buildTestRouter replicates the production router from cmd/server/main.go.
func buildTestRouter(mgr *txn.Manager, view aptosint.Viewer, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Mutating endpoints (async, return 202).
	mux.HandleFunc("POST /v1/mint", handler.Mint(mgr))
	mux.HandleFunc("POST /v1/batch-mint", handler.BatchMint(mgr))
	mux.HandleFunc("POST /v1/burn", handler.Burn(mgr))
	mux.HandleFunc("POST /v1/denylist", handler.Denylist(mgr))
	mux.HandleFunc("POST /v1/undenylist", handler.Undenylist(mgr))
	mux.HandleFunc("POST /v1/minters", handler.ConfigureMinter(mgr))
	mux.HandleFunc("DELETE /v1/minters/{address}", handler.RemoveMinter(mgr))
	mux.HandleFunc("POST /v1/minters/{address}/increment-allowance", handler.IncrementMinterAllowance(mgr))
	mux.HandleFunc("POST /v1/minters/{address}/decrement-allowance", handler.DecrementMinterAllowance(mgr))
	mux.HandleFunc("PUT /v1/metadata", handler.UpdateMetadata(mgr))
	mux.HandleFunc("PUT /v1/roles/denylister", handler.UpdateDenylister(mgr))
	mux.HandleFunc("PUT /v1/roles/master-minter", handler.UpdateMasterMinter(mgr))
	mux.HandleFunc("PUT /v1/roles/metadata-updater", handler.UpdateMetadataUpdater(mgr))

	// Query endpoints (synchronous).
	mux.HandleFunc("GET /v1/balance/{address}", handler.Balance(view))
	mux.HandleFunc("GET /v1/minters/{address}", handler.IsMinter(view))
	mux.HandleFunc("GET /v1/minters/{address}/allowance", handler.MintAllowance(view))
	mux.HandleFunc("GET /v1/master-minter", handler.MasterMinter(view))
	mux.HandleFunc("GET /v1/contractInt-address", handler.contractIntAddress(view))
	mux.HandleFunc("GET /v1/contractInt-object", handler.contractIntObject(view))

	// Transaction tracking.
	mux.HandleFunc("GET /v1/transactions/{id}", handler.GetTransaction(mgr))

	// Health check.
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		apiPkg.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Middleware stack (same order as production).
	var h http.Handler = mux
	h = middleware.Auth(testAPIKey)(h)
	h = middleware.Recovery(logger)(h)
	h = middleware.Logging(logger)(h)
	h = middleware.RequestID(h)

	return h
}

// submitAndWait builds, signs, submits a transaction and polls until confirmed.
func submitAndWait(key *crypto.Ed25519PrivateKey, sender aptossdk.AccountAddress, payload aptossdk.TransactionPayload) error {
	rawTxn, _, err := aptosClient.BuildOrderlessTransaction(sender, payload)
	if err != nil {
		return fmt.Errorf("build transaction: %w", err)
	}

	signingMsg, err := rawTxn.SigningMessage()
	if err != nil {
		return fmt.Errorf("signing message: %w", err)
	}

	auth, err := key.Sign(signingMsg)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	signedTxn, err := rawTxn.SignedTransactionWithAuthenticator(auth)
	if err != nil {
		return fmt.Errorf("build signed txn: %w", err)
	}

	submitResp, err := aptosClient.Inner.SubmitTransaction(signedTxn)
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}

	log.Printf("  Submitted txn: %s", submitResp.Hash)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		result, err := aptosClient.Inner.TransactionByHash(submitResp.Hash)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		success := result.Success()
		if success == nil {
			time.Sleep(2 * time.Second)
			continue
		}

		if *success {
			log.Printf("  Confirmed: %s", submitResp.Hash)
			return nil
		}

		return fmt.Errorf("transaction failed on-chain: %s", submitResp.Hash)
	}

	return fmt.Errorf("transaction timed out: %s", submitResp.Hash)
}

// keyToAddress derives the Aptos account address from an Ed25519 private key.
func keyToAddress(key *crypto.Ed25519PrivateKey) aptossdk.AccountAddress {
	authKey := key.AuthKey()
	var addr aptossdk.AccountAddress
	copy(addr[:], authKey[:])
	return addr
}

// privateKeySeedHex returns the 32-byte Ed25519 seed as hex for the Aptos CLI.
func privateKeySeedHex(key *crypto.Ed25519PrivateKey) string {
	seed := key.Inner.Seed()
	return "0x" + hex.EncodeToString(seed)
}
