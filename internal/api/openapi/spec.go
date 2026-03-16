package openapi

import (
	"encoding/json"
	"net/http"

	"gopkg.in/yaml.v3"
)

// OpenAPI 3.0.3 struct types — minimal subset for serialization.

type OpenAPI struct {
	OpenAPI    string                `json:"openapi"               yaml:"openapi"`
	Info       Info                  `json:"info"                  yaml:"info"`
	Servers    []Server              `json:"servers,omitempty"     yaml:"servers,omitempty"`
	Tags       []Tag                 `json:"tags,omitempty"        yaml:"tags,omitempty"`
	Paths      map[string]PathItem   `json:"paths"                yaml:"paths"`
	Components Components            `json:"components"            yaml:"components"`
	Security   []SecurityRequirement `json:"security"              yaml:"security"`
}

type Info struct {
	Title       string `json:"title"       yaml:"title"`
	Version     string `json:"version"     yaml:"version"`
	Description string `json:"description" yaml:"description"`
}

type Server struct {
	URL         string `json:"url"                   yaml:"url"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type Tag struct {
	Name        string `json:"name"        yaml:"name"`
	Description string `json:"description" yaml:"description"`
}

type PathItem map[string]*Operation // keyed by HTTP method (lowercase)

type Operation struct {
	OperationID string                 `json:"operationId"              yaml:"operationId"`
	Summary     string                 `json:"summary"                  yaml:"summary"`
	Description string                 `json:"description,omitempty"    yaml:"description,omitempty"`
	Tags        []string               `json:"tags"                     yaml:"tags"`
	Parameters  []Parameter            `json:"parameters,omitempty"     yaml:"parameters,omitempty"`
	RequestBody *RequestBody           `json:"requestBody,omitempty"    yaml:"requestBody,omitempty"`
	Responses   map[string]Response    `json:"responses"                yaml:"responses"`
	Security    *[]SecurityRequirement `json:"security,omitempty"       yaml:"security,omitempty"`
}

type Parameter struct {
	Name        string  `json:"name"                  yaml:"name"`
	In          string  `json:"in"                    yaml:"in"`
	Required    bool    `json:"required"              yaml:"required"`
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	Schema      *Schema `json:"schema"                yaml:"schema"`
}

type RequestBody struct {
	Required    bool                 `json:"required"                yaml:"required"`
	Description string               `json:"description,omitempty"   yaml:"description,omitempty"`
	Content     map[string]MediaType `json:"content"                 yaml:"content"`
}

type MediaType struct {
	Schema *Schema `json:"schema" yaml:"schema"`
}

type Response struct {
	Description string               `json:"description"         yaml:"description"`
	Content     map[string]MediaType `json:"content,omitempty"   yaml:"content,omitempty"`
}

type Schema struct {
	Ref              string             `json:"$ref,omitempty"             yaml:"$ref,omitempty"`
	Type             string             `json:"type,omitempty"             yaml:"type,omitempty"`
	Format           string             `json:"format,omitempty"           yaml:"format,omitempty"`
	Description      string             `json:"description,omitempty"      yaml:"description,omitempty"`
	Properties       map[string]*Schema `json:"properties,omitempty"       yaml:"properties,omitempty"`
	Required         []string           `json:"required,omitempty"         yaml:"required,omitempty"`
	Items            *Schema            `json:"items,omitempty"            yaml:"items,omitempty"`
	Enum             []any              `json:"enum,omitempty"             yaml:"enum,omitempty"`
	Example          any                `json:"example,omitempty"          yaml:"example,omitempty"`
	Minimum          *int64             `json:"minimum,omitempty"          yaml:"minimum,omitempty"`
	ExclusiveMinimum *bool              `json:"exclusiveMinimum,omitempty" yaml:"exclusiveMinimum,omitempty"`
	MinItems         *int               `json:"minItems,omitempty"         yaml:"minItems,omitempty"`
}

type Components struct {
	Schemas         map[string]*Schema        `json:"schemas"         yaml:"schemas"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes" yaml:"securitySchemes"`
}

type SecurityScheme struct {
	Type        string `json:"type"                  yaml:"type"`
	Name        string `json:"name"                  yaml:"name"`
	In          string `json:"in"                    yaml:"in"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type SecurityRequirement map[string][]string

// Spec builds and returns the complete OpenAPI 3.0.3 specification for the Contract API.
func Spec() *OpenAPI {
	return &OpenAPI{
		OpenAPI: "3.0.3",
		Info: Info{
			Title:   "Contract API",
			Version: "2.0.0",
			Description: "Generic REST API for interacting with Aptos Move contracts.\n\n" +
				"## Endpoints\n\n" +
				"- **POST /v1/contracts/execute** — Submit an entry function transaction (async)\n" +
				"- **POST /v1/contracts/query** — Call a view function (sync)\n" +
				"- **GET /v1/transactions/{id}** — Poll transaction status\n\n" +
				"## Async Submission Model\n\n" +
				"Execute requests return `202 Accepted` with a `transaction_id`. The transaction is " +
				"submitted to the Aptos blockchain and processed in the background. Use " +
				"`GET /v1/transactions/{id}` to poll for the final status.\n\n" +
				"### Transaction Lifecycle\n\n" +
				"1. **pending** — Accepted by the API, queued for submission\n" +
				"2. **submitted** — Sent to the Aptos blockchain, awaiting confirmation\n" +
				"3. **confirmed** — Successfully committed on-chain\n" +
				"4. **failed** — Transaction failed (may be retried automatically)\n" +
				"5. **expired** — Transaction expired before confirmation\n" +
				"6. **permanently_failed** — Unrecoverable failure after all retries exhausted\n\n" +
				"## Authentication\n\n" +
				"All endpoints except `/v1/health` and `/v1/openapi.yaml` require an API key " +
				"passed in the `Authorization` header. Requests without a valid key receive `401 Unauthorized`.\n\n" +
				"## Error Handling\n\n" +
				"Errors return a JSON body with a single `error` field:\n" +
				"```json\n{\"error\": \"description of the problem\"}\n```\n\n" +
				"## Arguments\n\n" +
				"Arguments are untyped (strings, numbers, arrays) — matching the native Aptos REST API convention. " +
				"The server resolves argument types by fetching the module ABI from the Aptos node (cached per module) " +
				"and handles BCS serialization automatically.\n\n" +
				"Supported Move types: `address`, `bool`, `u8`–`u256`, `0x1::string::String`, " +
				"`vector<T>`, `0x1::object::Object<T>`.\n\n" +
				"## Address Format\n\n" +
				"All address parameters accept Aptos account addresses as hex strings (e.g., `0x1`). " +
				"Addresses are validated on the server side; invalid addresses return `400 Bad Request`.\n\n" +
				"## Request Validation\n\n" +
				"- Request bodies must be valid JSON with `Content-Type: application/json`\n" +
				"- Unknown fields in the request body are rejected (`400 Bad Request`)\n" +
				"- Missing required fields return `400 Bad Request` with a descriptive message",
		},
		Tags:       buildTags(),
		Paths:      buildPaths(),
		Components: buildComponents(),
		Security:   []SecurityRequirement{{"apiKeyAuth": {}}},
	}
}

// Handler returns an http.HandlerFunc that serves the OpenAPI spec as YAML.
func Handler() http.HandlerFunc {
	spec := Spec()
	out, err := yaml.Marshal(spec)
	if err != nil {
		panic("openapi: failed to marshal spec: " + err.Error())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write(out)
	}
}

// MarshalYAML returns the spec as YAML bytes.
func (o *OpenAPI) MarshalYAML() ([]byte, error) {
	return yaml.Marshal(o)
}

// JSON returns the spec as indented JSON bytes.
func JSON() ([]byte, error) {
	return json.MarshalIndent(Spec(), "", "  ")
}

// --- builders ---

func buildTags() []Tag {
	return []Tag{
		{Name: "Execute", Description: "Submit entry function transactions to any Move contract"},
		{Name: "Query", Description: "Call view functions on any Move contract"},
		{Name: "Transactions", Description: "Track transaction status"},
		{Name: "Health", Description: "Server health check"},
	}
}

func buildPaths() map[string]PathItem {
	return map[string]PathItem{
		"/v1/contracts/execute": {
			"post": executeOp(),
		},
		"/v1/contracts/query": {
			"post": queryOp(),
		},
		"/v1/transactions/{id}": {
			"get": getTransactionOp(),
		},
		"/v1/health": {
			"get": healthOp(),
		},
		"/v1/docs": {
			"get": docsOp(),
		},
	}
}

func executeOp() *Operation {
	return &Operation{
		OperationID: "executeContract",
		Summary:     "Execute an entry function",
		Description: "Submits a transaction calling the specified Move entry function. " +
			"Arguments are untyped — the server resolves types from the on-chain module ABI " +
			"and handles BCS serialization. Returns 202 with a transaction_id for polling.\n\n" +
			"**Example: Mint tokens**\n" +
			"```json\n" +
			"{\n" +
			"  \"function_id\": \"0x1234::contractInt::mint\",\n" +
			"  \"type_arguments\": [],\n" +
			"  \"arguments\": [\"0x5678abcd...\", \"10000\"],\n" +
			"  \"signer\": \"minter\",\n" +
			"  \"max_gas_amount\": 200000\n" +
			"}\n" +
			"```",
		Tags: []string{"Execute"},
		RequestBody: &RequestBody{
			Required:    true,
			Description: "Entry function call parameters",
			Content: map[string]MediaType{
				"application/json": {
					Schema: &Schema{Ref: "#/components/schemas/ExecuteRequest"},
				},
			},
		},
		Responses: map[string]Response{
			"202": {
				Description: "Transaction accepted and queued for submission",
				Content: map[string]MediaType{
					"application/json": {
						Schema: &Schema{Ref: "#/components/schemas/SubmitResponse"},
					},
				},
			},
			"400": errorResponse("Invalid request (bad function_id format, wrong argument count, unsupported type, etc.)"),
			"401": errorResponse("Missing or invalid API key"),
			"500": errorResponse("Internal server error (ABI fetch failure, signing error, etc.)"),
		},
	}
}

func queryOp() *Operation {
	return &Operation{
		OperationID: "queryContract",
		Summary:     "Call a view function",
		Description: "Calls a Move view function and returns the result synchronously. " +
			"The request is proxied to the Aptos node's /view endpoint, which handles " +
			"ABI resolution and argument serialization internally.\n\n" +
			"**Example: Check balance**\n" +
			"```json\n" +
			"{\n" +
			"  \"function_id\": \"0x1234::contractInt::balance_of\",\n" +
			"  \"type_arguments\": [],\n" +
			"  \"arguments\": [\"0x5678abcd...\"]\n" +
			"}\n" +
			"```\n\n" +
			"Returns: `{\"result\": [\"10000\"]}`",
		Tags: []string{"Query"},
		RequestBody: &RequestBody{
			Required:    true,
			Description: "View function call parameters",
			Content: map[string]MediaType{
				"application/json": {
					Schema: &Schema{Ref: "#/components/schemas/QueryRequest"},
				},
			},
		},
		Responses: map[string]Response{
			"200": {
				Description: "View function executed successfully",
				Content: map[string]MediaType{
					"application/json": {
						Schema: &Schema{Ref: "#/components/schemas/QueryResponse"},
					},
				},
			},
			"400": errorResponse("Invalid request or Aptos view function error (bad function_id, wrong args, on-chain abort, etc.)"),
			"401": errorResponse("Missing or invalid API key"),
			"502": errorResponse("Aptos node unreachable or returned an unexpected error"),
		},
	}
}

func getTransactionOp() *Operation {
	return &Operation{
		OperationID: "getTransaction",
		Summary:     "Get transaction status",
		Description: "Returns the current status and details of a previously submitted transaction. " +
			"Use this endpoint to poll for transaction confirmation after calling the execute endpoint. " +
			"Terminal statuses are: confirmed, failed, expired, permanently_failed.",
		Tags: []string{"Transactions"},
		Parameters: []Parameter{
			{
				Name:        "id",
				In:          "path",
				Required:    true,
				Description: "Transaction ID (UUID) returned by the execute endpoint",
				Schema:      &Schema{Type: "string", Format: "uuid"},
			},
		},
		Responses: map[string]Response{
			"200": {
				Description: "Transaction record found",
				Content: map[string]MediaType{
					"application/json": {
						Schema: &Schema{Ref: "#/components/schemas/TransactionRecord"},
					},
				},
			},
			"400": errorResponse("Missing or empty transaction ID"),
			"401": errorResponse("Missing or invalid API key"),
			"404": errorResponse("Transaction not found"),
			"500": errorResponse("Internal server error (database failure)"),
		},
	}
}

func healthOp() *Operation {
	noAuth := []SecurityRequirement{}
	return &Operation{
		OperationID: "healthCheck",
		Summary:     "Health check",
		Description: "Returns the server health status. This endpoint does not require authentication.",
		Tags:        []string{"Health"},
		Responses: map[string]Response{
			"200": {
				Description: "Server is healthy",
				Content: map[string]MediaType{
					"application/json": {
						Schema: &Schema{Ref: "#/components/schemas/HealthResponse"},
					},
				},
			},
		},
		Security: &noAuth,
	}
}

func docsOp() *Operation {
	noAuth := []SecurityRequirement{}
	return &Operation{
		OperationID: "apiDocs",
		Summary:     "Interactive API documentation",
		Description: "Serves an interactive API documentation page powered by Scalar, " +
			"which reads from the OpenAPI spec at /v1/openapi.yaml.",
		Tags: []string{"Health"},
		Responses: map[string]Response{
			"200": {
				Description: "HTML page with interactive API documentation",
			},
		},
		Security: &noAuth,
	}
}

func errorResponse(desc string) Response {
	return Response{
		Description: desc,
		Content: map[string]MediaType{
			"application/json": {
				Schema: &Schema{Ref: "#/components/schemas/ErrorResponse"},
			},
		},
	}
}

func buildComponents() Components {
	return Components{
		Schemas: map[string]*Schema{
			"ExecuteRequest": {
				Type:        "object",
				Description: "Request body for executing a Move entry function as a blockchain transaction.",
				Required:    []string{"function_id", "signer"},
				Properties: map[string]*Schema{
					"function_id": {
						Type:        "string",
						Description: "Fully qualified Move function ID in the format addr::module::function",
						Example:     "0x1234::contractInt::mint",
					},
					"type_arguments": {
						Type:        "array",
						Description: "Move type arguments for generic functions (e.g., [\"0x1::aptos_coin::AptosCoin\"]). Pass an empty array if the function has no type parameters.",
						Items:       &Schema{Type: "string"},
					},
					"arguments": {
						Type:        "array",
						Description: "Function arguments as untyped values (strings, numbers, arrays). The server resolves the expected types from the on-chain module ABI and serializes via BCS.",
						Items:       &Schema{Description: "Argument value — type is inferred from the on-chain ABI"},
					},
					"signer": {
						Type:        "string",
						Description: "Name of the configured signer role to sign the transaction (e.g., minter, owner, denylister)",
						Example:     "minter",
					},
					"max_gas_amount": {
						Type:        "integer",
						Format:      "uint64",
						Description: "Optional gas limit override for this transaction. If omitted, the server's default max_gas_amount is used.",
					},
				},
			},
			"QueryRequest": {
				Type:        "object",
				Description: "Request body for calling a Move view function (read-only, no transaction).",
				Required:    []string{"function_id"},
				Properties: map[string]*Schema{
					"function_id": {
						Type:        "string",
						Description: "Fully qualified Move view function ID in the format addr::module::function",
						Example:     "0x1234::contractInt::balance_of",
					},
					"type_arguments": {
						Type:        "array",
						Description: "Move type arguments for generic view functions. Pass an empty array if the function has no type parameters.",
						Items:       &Schema{Type: "string"},
					},
					"arguments": {
						Type:        "array",
						Description: "View function arguments as untyped values. The Aptos node resolves types from the on-chain ABI.",
						Items:       &Schema{Description: "Argument value — type is inferred by the Aptos node"},
					},
				},
			},
			"SubmitResponse": {
				Type:        "object",
				Description: "Response returned when a transaction is accepted for processing.",
				Properties: map[string]*Schema{
					"transaction_id": {
						Type:        "string",
						Format:      "uuid",
						Description: "Unique identifier for tracking the transaction via GET /v1/transactions/{id}",
						Example:     "550e8400-e29b-41d4-a716-446655440000",
					},
					"status": {
						Type:        "string",
						Description: "Initial transaction status (always \"pending\" on creation)",
						Example:     "pending",
					},
				},
			},
			"QueryResponse": {
				Type:        "object",
				Description: "Response from a successful view function call.",
				Properties: map[string]*Schema{
					"result": {
						Type:        "array",
						Description: "View function return values as a JSON array. Element types depend on the Move function's return signature (e.g., [\"10000\"] for a u64 return).",
						Items:       &Schema{Description: "Return value — type depends on the Move function signature"},
					},
				},
			},
			"TransactionRecord": {
				Type:        "object",
				Description: "Full record of a submitted transaction, including its current lifecycle status.",
				Properties: map[string]*Schema{
					"id": {
						Type:        "string",
						Format:      "uuid",
						Description: "Unique transaction identifier assigned by the API",
					},
					"operation_type": {
						Type:        "string",
						Description: "The type of operation (e.g., \"execute\")",
					},
					"status": {
						Type:        "string",
						Description: "Current lifecycle status of the transaction",
						Enum:        []any{"pending", "submitted", "confirmed", "failed", "expired", "permanently_failed"},
					},
					"txn_hash": {
						Type:        "string",
						Description: "On-chain transaction hash (populated after submission to the Aptos node)",
					},
					"nonce": {
						Type:        "integer",
						Description: "Orderless replay-protection nonce used for this transaction",
					},
					"sender_address": {
						Type:        "string",
						Description: "Aptos account address of the signer who submitted the transaction",
					},
					"attempt": {
						Type:        "integer",
						Description: "Current attempt number (0-indexed). Incremented on each automatic retry.",
					},
					"max_retries": {
						Type:        "integer",
						Description: "Maximum number of retry attempts before the transaction is marked permanently_failed",
					},
					"request_payload": {
						Type:        "string",
						Description: "Original JSON request body stored for audit logging and transaction resubmission",
					},
					"error_message": {
						Type:        "string",
						Description: "Error message from the most recent failure, if any",
					},
					"created_at": {
						Type:        "string",
						Format:      "date-time",
						Description: "Timestamp when the transaction was first accepted by the API",
					},
					"updated_at": {
						Type:        "string",
						Format:      "date-time",
						Description: "Timestamp of the most recent status update",
					},
				},
			},
			"ErrorResponse": {
				Type:        "object",
				Description: "Standard error response returned for all 4xx and 5xx status codes.",
				Properties: map[string]*Schema{
					"error": {
						Type:        "string",
						Description: "Human-readable error description",
						Example:     "invalid function_id: expected format addr::module::function",
					},
				},
			},
			"HealthResponse": {
				Type:        "object",
				Description: "Health check response indicating server status.",
				Properties: map[string]*Schema{
					"status": {
						Type:        "string",
						Description: "Server health status",
						Example:     "ok",
					},
				},
			},
		},
		SecuritySchemes: map[string]SecurityScheme{
			"apiKeyAuth": {
				Type:        "apiKey",
				Name:        "Authorization",
				In:          "header",
				Description: "API key for authentication. Pass the raw key value in the Authorization header.",
			},
		},
	}
}
