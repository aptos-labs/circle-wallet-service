package handler

import (
	"fmt"
	"net/http"

	aptosint "github.com/aptos-labs/jc-contract-integration/rewrite/internal/aptos"
)

type queryRequest struct {
	FunctionID    string   `json:"function_id"`
	TypeArguments []string `json:"type_arguments"`
	Arguments     []any    `json:"arguments"`
}

// Query handles POST /v1/query.
func Query(client *aptosint.Client, abiCache *aptosint.ABICache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		if err := decodeJSON(r, &req); err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.FunctionID == "" {
			errorResponse(w, http.StatusBadRequest, "function_id is required")
			return
		}

		// Parse function ID.
		addr, module, function, err := aptosint.ParseFunctionID(req.FunctionID)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}

		// Resolve ABI params and validate argument count.
		params, err := abiCache.GetFunctionParams(addr, module, function)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "resolve ABI: "+err.Error())
			return
		}
		if len(req.Arguments) != len(params) {
			errorResponse(w, http.StatusBadRequest, "argument count mismatch: expected "+
				itoa(len(params))+", got "+itoa(len(req.Arguments)))
			return
		}

		// BCS-serialize each argument.
		args := make([][]byte, len(req.Arguments))
		for i, arg := range req.Arguments {
			b, err := aptosint.SerializeArgument(params[i], arg)
			if err != nil {
				errorResponse(w, http.StatusBadRequest, "argument["+itoa(i)+"]: "+err.Error())
				return
			}
			args[i] = b
		}

		// Default type_arguments to empty slice.
		typeArgs := req.TypeArguments
		if typeArgs == nil {
			typeArgs = []string{}
		}

		// Call view.
		result, err := client.View(req.FunctionID, typeArgs, args)
		if err != nil {
			errorResponse(w, http.StatusBadGateway, "view call failed: "+err.Error())
			return
		}

		jsonResponse(w, http.StatusOK, map[string]any{"result": result})
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
