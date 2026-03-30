// CLI for interacting with the Aptos Contract API server.
//
// Usage:
//
//	go run ./cmd/cli <command> [flags]
//
// Commands:
//
//	health                         Check server health
//	query                          Call a view function
//	execute                        Submit a transaction
//	status <transaction_id>        Poll transaction status
//	watch  <transaction_id>        Poll until terminal status
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	// Strip the command from os.Args so flag.Parse works on subcommand flags
	os.Args = append(os.Args[:1], os.Args[2:]...)

	switch cmd {
	case "health":
		cmdHealth()
	case "query":
		cmdQuery()
	case "execute":
		cmdExecute()
	case "status":
		cmdStatus()
	case "watch":
		cmdWatch()
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Aptos Contract API CLI

Usage: go run ./cmd/cli <command> [flags]

Commands:
  health                           Check server health
  query    -f <function_id> ...    Call a view function
  execute  -w <wallet_id|address> -f <function_id> ...    Submit a transaction
  status   -id <transaction_id>    Get transaction status
  watch    -id <transaction_id>    Poll until terminal status

Global env vars:
  API_BASE_URL    Server URL (default http://localhost:8080)
  API_KEY         Auth key

Examples:
  go run ./cmd/cli health

  go run ./cmd/cli query \
    -f "0x1::coin::balance" \
    -t "0x1::aptos_coin::AptosCoin" \
    -a "0xYOUR_ADDRESS"

  go run ./cmd/cli execute \
    -w "circle-wallet-uuid-or-0xADDRESS" \
    -f "0x1::aptos_account::transfer" \
    -a "0xRECIPIENT" -a "100"

  go run ./cmd/cli watch -id "transaction-uuid"`)
}

// --- helpers ---

func baseURL() string {
	if v := os.Getenv("API_BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func apiKey() string {
	return os.Getenv("API_KEY")
}

func doRequest(method, path string, body any) (int, []byte) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			fatal("marshal request: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, baseURL()+path, bodyReader)
	if err != nil {
		fatal("create request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("request failed: %v", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("read response: %v", err)
	}
	return resp.StatusCode, respBody
}

func prettyPrint(data []byte) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Println(string(data))
		return
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}

// --- array flag for repeated -a / -t values ---

type stringSlice []string

func (s *stringSlice) String() string { return fmt.Sprintf("%v", *s) }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// --- commands ---

func cmdHealth() {
	status, body := doRequest("GET", "/v1/health", nil)
	fmt.Printf("HTTP %d\n", status)
	prettyPrint(body)
}

func cmdQuery() {
	var functionID string
	var typeArgs stringSlice
	var args stringSlice

	fs := flag.NewFlagSet("query", flag.ExitOnError)
	fs.StringVar(&functionID, "f", "", "function_id (e.g. 0x1::coin::balance)")
	fs.Var(&typeArgs, "t", "type argument (repeatable)")
	fs.Var(&args, "a", "argument (repeatable)")
	err := fs.Parse(os.Args[1:])
	if err != nil {
		os.Exit(1)
	}

	if functionID == "" {
		_, err2 := fmt.Fprintln(os.Stderr, "Error: -f (function_id) is required")
		if err2 != nil {
			os.Exit(1)
		}
		fs.Usage()
		os.Exit(1)
	}

	// Convert string args to []any for JSON
	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = a
	}

	reqBody := map[string]any{
		"function_id":    functionID,
		"type_arguments": []string(typeArgs),
		"arguments":      anyArgs,
	}

	status, body := doRequest("POST", "/v1/query", reqBody)
	fmt.Printf("HTTP %d\n", status)
	prettyPrint(body)
}

func cmdExecute() {
	var walletID, functionID, webhookURL string
	var typeArgs stringSlice
	var args stringSlice
	var maxGas uint64

	fs := flag.NewFlagSet("execute", flag.ExitOnError)
	fs.StringVar(&walletID, "w", "", "wallet_id or Aptos address")
	fs.StringVar(&functionID, "f", "", "function_id (e.g. 0x1::aptos_account::transfer)")
	fs.Var(&typeArgs, "t", "type argument (repeatable)")
	fs.Var(&args, "a", "argument (repeatable)")
	fs.Uint64Var(&maxGas, "gas", 0, "max gas amount (0 = server default)")
	fs.StringVar(&webhookURL, "webhook", "", "webhook URL for status callback")
	err := fs.Parse(os.Args[1:])
	if err != nil {
		os.Exit(1)
	}

	if walletID == "" || functionID == "" {
		_, err2 := fmt.Fprintln(os.Stderr, "Error: -w (wallet_id) and -f (function_id) are required")
		if err2 != nil {
			os.Exit(1)
		}
		fs.Usage()
		os.Exit(1)
	}

	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = a
	}

	reqBody := map[string]any{
		"wallet_id":      walletID,
		"function_id":    functionID,
		"type_arguments": []string(typeArgs),
		"arguments":      anyArgs,
	}
	if maxGas > 0 {
		reqBody["max_gas_amount"] = maxGas
	}
	if webhookURL != "" {
		reqBody["webhook_url"] = webhookURL
	}

	status, body := doRequest("POST", "/v1/execute", reqBody)
	fmt.Printf("HTTP %d\n", status)
	prettyPrint(body)

	// If successful, offer to watch
	if status == 202 {
		var resp struct {
			TransactionID string `json:"transaction_id"`
		}
		if json.Unmarshal(body, &resp) == nil && resp.TransactionID != "" {
			fmt.Printf("\nPoll with:  go run ./cmd/cli watch -id %s\n", resp.TransactionID)
		}
	}
}

func cmdStatus() {
	var txnID string

	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.StringVar(&txnID, "id", "", "transaction ID")
	err := fs.Parse(os.Args[1:])
	if err != nil {
		os.Exit(1)
	}

	if txnID == "" {
		_, err := fmt.Fprintln(os.Stderr, "Error: -id (transaction ID) is required")
		if err != nil {
			os.Exit(1)
		}
		fs.Usage()
		os.Exit(1)
	}

	status, body := doRequest("GET", "/v1/transactions/"+txnID, nil)
	fmt.Printf("HTTP %d\n", status)
	prettyPrint(body)
}

func cmdWatch() {
	var txnID string
	var interval int

	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	fs.StringVar(&txnID, "id", "", "transaction ID")
	fs.IntVar(&interval, "interval", 5, "poll interval in seconds")
	err := fs.Parse(os.Args[1:])
	if err != nil {
		os.Exit(1)
	}

	if txnID == "" {
		_, err := fmt.Fprintln(os.Stderr, "Error: -id (transaction ID) is required")
		if err != nil {
			os.Exit(1)
		}
		fs.Usage()
		os.Exit(1)
	}

	fmt.Printf("Watching transaction %s (every %ds)...\n\n", txnID, interval)

	for i := range 60 {
		if i > 0 {
			time.Sleep(time.Duration(interval) * time.Second)
		}

		status, body := doRequest("GET", "/v1/transactions/"+txnID, nil)
		if status == 404 {
			fmt.Printf("  [%s] not found (may have been evicted)\n", time.Now().Format("15:04:05"))
			return
		}

		var resp struct {
			Status       string `json:"status"`
			TxnHash      string `json:"txn_hash"`
			ErrorMessage string `json:"error_message"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			fmt.Printf("  [%s] HTTP %d — parse error: %v\n", time.Now().Format("15:04:05"), status, err)
			continue
		}

		fmt.Printf("  [%s] status=%s", time.Now().Format("15:04:05"), resp.Status)
		if resp.TxnHash != "" {
			fmt.Printf("  hash=%s", resp.TxnHash)
		}
		if resp.ErrorMessage != "" {
			fmt.Printf("  error=%s", resp.ErrorMessage)
		}
		fmt.Println()

		switch resp.Status {
		case "confirmed":
			fmt.Println("\nTransaction confirmed!")
			return
		case "failed":
			fmt.Printf("\nTransaction failed: %s\n", resp.ErrorMessage)
			os.Exit(1)
		case "expired":
			fmt.Println("\nTransaction expired.")
			os.Exit(1)
		}
	}

	fmt.Println("\nTimed out waiting for terminal status.")
	os.Exit(1)
}
