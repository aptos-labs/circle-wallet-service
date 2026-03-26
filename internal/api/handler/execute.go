package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"

	"github.com/aptos-labs/jc-contract-integration/internal/api"
	aptosint "github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/txn"
)

type executeRequest struct {
	FunctionID    string   `json:"function_id"`
	TypeArguments []string `json:"type_arguments"`
	Arguments     []any    `json:"arguments"`
	Signer        string   `json:"signer"`
	MaxGasAmount  *uint64  `json:"max_gas_amount,omitempty"`
}

// executeOp implements txn.Operation for a generic contract execution.
type executeOp struct {
	functionID  string
	signer      string
	payload     aptossdk.TransactionPayload
	reqJSON     []byte
	gasOverride uint64
}

func (o *executeOp) Name() string                                       { return "execute" }
func (o *executeOp) SignerAddress() string                              { return o.signer }
func (o *executeOp) BuildPayload() (aptossdk.TransactionPayload, error) { return o.payload, nil }
func (o *executeOp) RequestJSON() []byte                                { return o.reqJSON }

// GasOverride returns the per-request gas override (0 means use default).
func (o *executeOp) GasOverride() uint64 { return o.gasOverride }

// Execute returns a handler for POST /v1/contracts/execute.
func Execute(mgr txn.Submitter, abiCache *aptosint.ABICache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req executeRequest
		if err := api.Decode(r, &req); err != nil {
			api.Error(w, http.StatusBadRequest, err.Error())
			return
		}

		op, err := buildExecuteOp(abiCache, &req)
		if err != nil {
			api.Error(w, http.StatusBadRequest, err.Error())
			return
		}

		submitAndRespond(w, r, mgr, op)
	}
}

// RebuildExecute reconstructs an executeOp from its stored JSON request payload.
// It must be called with a live ABICache to re-resolve parameter types.
func RebuildExecute(abiCache *aptosint.ABICache) txn.OperationFactory {
	return func(requestJSON []byte) (txn.Operation, error) {
		var req executeRequest
		if err := json.Unmarshal(requestJSON, &req); err != nil {
			return nil, fmt.Errorf("unmarshal execute request: %w", err)
		}
		return buildExecuteOp(abiCache, &req)
	}
}

func buildExecuteOp(abiCache *aptosint.ABICache, req *executeRequest) (*executeOp, error) {
	if req.FunctionID == "" {
		return nil, fmt.Errorf("function_id is required")
	}
	if req.Signer == "" {
		return nil, fmt.Errorf("signer is required")
	}

	addr, module, function, err := aptosint.ParseFunctionID(req.FunctionID)
	if err != nil {
		return nil, err
	}

	// Resolve parameter types from the on-chain ABI.
	paramTypes, err := abiCache.GetFunctionParams(addr, module, function)
	if err != nil {
		return nil, fmt.Errorf("resolve ABI: %w", err)
	}

	if len(req.Arguments) != len(paramTypes) {
		return nil, fmt.Errorf("expected %d arguments, got %d", len(paramTypes), len(req.Arguments))
	}

	// BCS-serialize each argument.
	args := make([][]byte, len(req.Arguments))
	for i, arg := range req.Arguments {
		b, err := aptosint.SerializeArgument(paramTypes[i], arg)
		if err != nil {
			return nil, fmt.Errorf("arguments[%d] (%s): %w", i, paramTypes[i], err)
		}
		args[i] = b
	}

	// Parse type arguments.
	typeArgs := []aptossdk.TypeTag{}
	if len(req.TypeArguments) > 0 {
		typeArgs, err = aptosint.ParseTypeTags(req.TypeArguments)
		if err != nil {
			return nil, err
		}
	}

	// Build the address for the module ID.
	modAddr, _ := aptosint.ParseAddress(addr) // already validated by ParseFunctionID

	payload := aptossdk.TransactionPayload{
		Payload: &aptossdk.EntryFunction{
			Module: aptossdk.ModuleId{
				Address: modAddr,
				Name:    module,
			},
			Function: function,
			ArgTypes: typeArgs,
			Args:     args,
		},
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var gasOverride uint64
	if req.MaxGasAmount != nil {
		gasOverride = *req.MaxGasAmount
	}

	return &executeOp{
		functionID:  req.FunctionID,
		signer:      req.Signer,
		payload:     payload,
		reqJSON:     reqJSON,
		gasOverride: gasOverride,
	}, nil
}
