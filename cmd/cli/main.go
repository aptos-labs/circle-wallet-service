// cmd/cli is a simple CLI client for testing the contract integration API.
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
)

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "API base URL")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	var err error
	switch args[0] {
	case "execute":
		err = execute(*baseURL, args[1:])
	case "query":
		err = query(*baseURL, args[1:])
	case "status":
		err = status(*baseURL, args[1:])
	case "poll":
		err = poll(*baseURL, args[1:])
	case "health":
		err = health(*baseURL)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: cli [flags] <command> [args]

Commands:
  execute  -f <function_id> -s <signer_address> [-t type_arg]... [arg]...
           Submit an entry function transaction (async).

  query    -f <function_id> [-t type_arg]... [arg]...
           Call a view function (sync).

  status   <transaction_id>
           Get transaction status.

  poll     <transaction_id> [-interval 2s] [-timeout 60s]
           Poll until transaction completes or times out.

  health   Check API health.

Flags:
  -url string   API base URL (default "http://localhost:8080")

Examples:
  cli execute -f 0x1::aptos_account::transfer -s 0xYOUR_SIGNER_ADDRESS 0xBOB 1000
  cli query -f 0x1::coin::balance -t 0x1::aptos_coin::AptosCoin 0xALICE
  cli poll abc-123-def
  cli health`)
}

func execute(baseURL string, args []string) error {
	fs := flag.NewFlagSet("execute", flag.ExitOnError)
	funcID := fs.String("f", "", "function_id (required)")
	signer := fs.String("s", "", "signer address (required)")
	gas := fs.Uint64("gas", 0, "max_gas_amount (0 = default)")
	var typeArgs multiFlag
	fs.Var(&typeArgs, "t", "type argument (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *funcID == "" {
		return fmt.Errorf("-f function_id is required")
	}
	if *signer == "" {
		return fmt.Errorf("-s signer address is required")
	}

	fnArgs := make([]any, len(fs.Args()))
	for i, a := range fs.Args() {
		fnArgs[i] = a
	}

	body := map[string]any{
		"function_id":    *funcID,
		"type_arguments": typeArgs,
		"arguments":      fnArgs,
		"signer":         *signer,
	}
	if *gas > 0 {
		body["max_gas_amount"] = *gas
	}

	return doPost(baseURL+"/v1/contracts/execute", body)
}

func query(baseURL string, args []string) error {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	funcID := fs.String("f", "", "function_id (required)")
	var typeArgs multiFlag
	fs.Var(&typeArgs, "t", "type argument (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *funcID == "" {
		return fmt.Errorf("-f function_id is required")
	}

	fnArgs := make([]any, len(fs.Args()))
	for i, a := range fs.Args() {
		fnArgs[i] = a
	}

	body := map[string]any{
		"function_id":    *funcID,
		"type_arguments": typeArgs,
		"arguments":      fnArgs,
	}

	return doPost(baseURL+"/v1/contracts/query", body)
}

func status(baseURL string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("transaction_id is required")
	}
	return doGet(baseURL + "/v1/transactions/" + args[0])
}

func poll(baseURL string, args []string) error {
	fs := flag.NewFlagSet("poll", flag.ExitOnError)
	interval := fs.Duration("interval", 2*time.Second, "poll interval")
	timeout := fs.Duration("timeout", 60*time.Second, "poll timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) == 0 {
		return fmt.Errorf("transaction_id is required")
	}
	txnID := fs.Args()[0]
	url := baseURL + "/v1/transactions/" + txnID

	deadline := time.After(*timeout)
	tick := time.NewTicker(*interval)
	defer tick.Stop()

	fmt.Fprintf(os.Stderr, "polling %s every %s (timeout %s)...\n", txnID, *interval, *timeout)

	for {
		body, code, err := httpGet(url)
		if err != nil {
			return err
		}

		var rec map[string]any
		if err := json.Unmarshal(body, &rec); err == nil {
			s, _ := rec["status"].(string)
			fmt.Fprintf(os.Stderr, "  status: %s\n", s)
			if s == "confirmed" || s == "failed" || code == http.StatusNotFound {
				prettyPrint(body)
				return nil
			}
		}

		select {
		case <-deadline:
			fmt.Fprintln(os.Stderr, "timeout reached, last response:")
			prettyPrint(body)
			return fmt.Errorf("poll timed out after %s", *timeout)
		case <-tick.C:
		}
	}
}

func health(baseURL string) error {
	return doGet(baseURL + "/v1/health")
}

// --- HTTP helpers ---

func doPost(url string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "POST %s\n", url)
	fmt.Fprintf(os.Stderr, ">>> %s\n", string(data))

	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "<<< %d\n", resp.StatusCode)
	prettyPrint(respBody)
	return nil
}

func doGet(url string) error {
	fmt.Fprintf(os.Stderr, "GET %s\n", url)
	body, code, err := httpGet(url)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "<<< %d\n", code)
	prettyPrint(body)
	return nil
}

func httpGet(url string) ([]byte, int, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

func prettyPrint(data []byte) {
	var buf bytes.Buffer
	if json.Indent(&buf, data, "", "  ") == nil {
		fmt.Println(buf.String())
	} else {
		fmt.Println(string(data))
	}
}

// multiFlag collects repeated -t flags into a string slice.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprintf("%v", *m) }
func (m *multiFlag) Set(val string) error {
	*m = append(*m, val)
	return nil
}
