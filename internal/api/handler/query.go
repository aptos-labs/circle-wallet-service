package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aptos-labs/jc-contract-integration/internal/api"
)

type queryRequest struct {
	FunctionID    string   `json:"function_id"`
	TypeArguments []string `json:"type_arguments"`
	Arguments     []any    `json:"arguments"`
}

// Query returns a handler for POST /v1/contracts/query.
// It proxies the request to the Aptos node's /view endpoint.
func Query(nodeURL string) http.HandlerFunc {
	viewURL := strings.TrimRight(nodeURL, "/") + "/view"

	return func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		if err := api.Decode(r, &req); err != nil {
			api.Error(w, http.StatusBadRequest, err.Error())
			return
		}

		if req.FunctionID == "" {
			api.Error(w, http.StatusBadRequest, "function_id is required")
			return
		}

		// Build the Aptos view request body.
		viewBody := map[string]any{
			"function":       req.FunctionID,
			"type_arguments": req.TypeArguments,
			"arguments":      req.Arguments,
		}
		if viewBody["type_arguments"] == nil {
			viewBody["type_arguments"] = []string{}
		}
		if viewBody["arguments"] == nil {
			viewBody["arguments"] = []any{}
		}

		bodyBytes, err := json.Marshal(viewBody)
		if err != nil {
			api.Error(w, http.StatusInternalServerError, "marshal view request: "+err.Error())
			return
		}

		resp, err := http.Post(viewURL, "application/json", strings.NewReader(string(bodyBytes)))
		if err != nil {
			api.Error(w, http.StatusBadGateway, "aptos node request failed: "+err.Error())
			return
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			api.Error(w, http.StatusBadGateway, "read aptos node response: "+err.Error())
			return
		}

		// If the Aptos node returned an error, forward it.
		if resp.StatusCode != http.StatusOK {
			api.Error(w, http.StatusBadRequest,
				fmt.Sprintf("aptos view function error: %s", string(respBody)))
			return
		}

		// The node returns a JSON array; wrap it in {"result": ...}.
		var result json.RawMessage
		if err := json.Unmarshal(respBody, &result); err != nil {
			api.Error(w, http.StatusInternalServerError, "parse aptos response: "+err.Error())
			return
		}

		api.JSON(w, http.StatusOK, map[string]json.RawMessage{"result": result})
	}
}
