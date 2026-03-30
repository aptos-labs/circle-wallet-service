// Creates Circle developer wallets on Aptos testnet and prints the config
// needed for CIRCLE_WALLETS in your .env file.
//
// Usage:
//
//	export CIRCLE_API_KEY=your-api-key
//	export CIRCLE_ENTITY_SECRET=your-32-byte-hex
//	export CIRCLE_WALLET_SET_ID=your-wallet-set-id
//	cd rewrite && go run ./examples/create_wallets -count 1
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load("../../.env")

	count := flag.Int("count", 1, "number of wallets to create")
	blockchain := flag.String("blockchain", "APTOS-TESTNET", "blockchain (APTOS-DEVNET, APTOS-TESTNET, APTOS)")
	flag.Parse()

	apiKey := os.Getenv("CIRCLE_API_KEY")
	entitySecret := os.Getenv("CIRCLE_ENTITY_SECRET")
	walletSetID := os.Getenv("CIRCLE_WALLET_SET_ID")

	if apiKey == "" || entitySecret == "" || walletSetID == "" {
		log.Fatal("Set CIRCLE_API_KEY, CIRCLE_ENTITY_SECRET, and CIRCLE_WALLET_SET_ID")
	}

	circleClient := circle.NewClient(apiKey)

	// Encrypt entity secret for the create request
	ciphertext, err := circleClient.EncryptEntitySecret(context.Background(), entitySecret)
	if err != nil {
		log.Fatalf("encrypt entity secret: %v", err)
	}

	// Create wallets
	reqBody := map[string]any{
		"idempotencyKey":         uuid.New().String(),
		"accountType":            "EOA",
		"blockchains":            []string{*blockchain},
		"walletSetId":            walletSetID,
		"count":                  *count,
		"entitySecretCiphertext": ciphertext,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}

	req, err := http.NewRequest("POST", "https://api.circle.com/v1/w3s/developer/wallets", bytes.NewReader(jsonBody))
	if err != nil {
		log.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("send request: %v", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Fatalf("Circle API error (%d): %s", resp.StatusCode, body)
	}

	var result struct {
		Data struct {
			Wallets []struct {
				ID               string `json:"id"`
				Address          string `json:"address"`
				Blockchain       string `json:"blockchain"`
				InitialPublicKey string `json:"initialPublicKey"`
			} `json:"wallets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Fatalf("parse response: %v\n%s", err, body)
	}

	fmt.Printf("Created %d wallet(s):\n\n", len(result.Data.Wallets))

	// Build CIRCLE_WALLETS JSON
	var wallets []map[string]string
	for _, w := range result.Data.Wallets {
		fmt.Printf("  Wallet ID:   %s\n", w.ID)
		fmt.Printf("  Address:     %s\n", w.Address)
		fmt.Printf("  Public Key:  %s\n", w.InitialPublicKey)
		fmt.Printf("  Blockchain:  %s\n\n", w.Blockchain)

		wallets = append(wallets, map[string]string{
			"wallet_id":  w.ID,
			"address":    w.Address,
			"public_key": w.InitialPublicKey,
		})
	}

	walletsJSON, _ := json.Marshal(wallets)
	fmt.Println("Add this to your .env:")
	fmt.Printf("CIRCLE_WALLETS='%s'\n\n", walletsJSON)

	if *blockchain == "APTOS-TESTNET" || *blockchain == "APTOS-DEVNET" {
		fmt.Println("Fund your wallets on the Aptos faucet:")
		for _, w := range result.Data.Wallets {
			fmt.Printf("  https://aptos.dev/en/network/faucet?address=%s\n", w.Address)
		}
	}
}
