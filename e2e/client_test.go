//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// e2eClient wraps HTTP requests to the test API server.
type e2eClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// get sends GET and asserts 200.
func (c *e2eClient) get(t *testing.T, path string) map[string]any {
	t.Helper()
	status, body := c.do(t, http.MethodGet, path, nil)
	if status != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %v", path, status, body)
	}
	return body
}

// post sends POST and returns (status, body).
func (c *e2eClient) post(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	return c.do(t, http.MethodPost, path, body)
}

// put sends PUT and returns (status, body).
func (c *e2eClient) put(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	return c.do(t, http.MethodPut, path, body)
}

// do executes an HTTP request and returns (status, decoded JSON body).
func (c *e2eClient) do(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		t.Fatalf("build %s %s request: %v", method, path, err)
	}
	req.Header.Set("Authorization", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode %s %s response: %v", method, path, err)
	}

	return resp.StatusCode, result
}

// pollTransaction polls GET /v1/transactions/{id} until the status is terminal.
func (c *e2eClient) pollTransaction(t *testing.T, txnID string, timeout time.Duration) map[string]any {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result := c.get(t, "/v1/transactions/"+txnID)
		status, _ := result["status"].(string)
		switch status {
		case "confirmed", "failed", "permanently_failed":
			return result
		}
		time.Sleep(2 * time.Second)
	}

	t.Fatalf("transaction %s did not reach terminal status within %v", txnID, timeout)
	return nil
}
