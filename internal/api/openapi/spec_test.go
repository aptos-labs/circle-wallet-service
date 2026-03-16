package openapi

import (
	"encoding/json"
	"testing"
)

func TestSpec_ValidJSON(t *testing.T) {
	spec := Spec()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("failed to marshal spec to JSON: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("marshaled JSON is empty")
	}
}

func TestSpec_ValidYAML(t *testing.T) {
	spec := Spec()
	data, err := spec.MarshalYAML()
	if err != nil {
		t.Fatalf("failed to marshal spec to YAML: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("marshaled YAML is empty")
	}
}

func TestSpec_Version(t *testing.T) {
	spec := Spec()
	if spec.OpenAPI != "3.0.3" {
		t.Errorf("expected openapi version 3.0.3, got %s", spec.OpenAPI)
	}
}

func TestSpec_HasAllPaths(t *testing.T) {
	spec := Spec()

	expectedPaths := []string{
		"/v1/contracts/execute",
		"/v1/contracts/query",
		"/v1/transactions/{id}",
		"/v1/health",
		"/v1/docs",
	}

	for _, p := range expectedPaths {
		if _, ok := spec.Paths[p]; !ok {
			t.Errorf("missing path: %s", p)
		}
	}

	if len(spec.Paths) != len(expectedPaths) {
		t.Errorf("expected %d paths, got %d", len(expectedPaths), len(spec.Paths))
	}
}

func TestSpec_HasAllSchemas(t *testing.T) {
	spec := Spec()

	expectedSchemas := []string{
		"ExecuteRequest",
		"QueryRequest",
		"SubmitResponse",
		"QueryResponse",
		"TransactionRecord",
		"ErrorResponse",
		"HealthResponse",
	}

	for _, s := range expectedSchemas {
		if _, ok := spec.Components.Schemas[s]; !ok {
			t.Errorf("missing schema: %s", s)
		}
	}

	if len(spec.Components.Schemas) != len(expectedSchemas) {
		t.Errorf("expected %d schemas, got %d", len(expectedSchemas), len(spec.Components.Schemas))
	}
}

func TestSpec_HealthNoSecurity(t *testing.T) {
	spec := Spec()
	healthPath, ok := spec.Paths["/v1/health"]
	if !ok {
		t.Fatal("missing /v1/health path")
	}
	healthOp := healthPath["get"]
	if healthOp == nil {
		t.Fatal("missing GET operation on /v1/health")
	}
	if healthOp.Security == nil {
		t.Fatal("expected security override on health endpoint")
	}
	if len(*healthOp.Security) != 0 {
		t.Errorf("expected empty security on health endpoint, got %d requirements", len(*healthOp.Security))
	}
}

func TestSpec_DocsNoSecurity(t *testing.T) {
	spec := Spec()
	docsPath, ok := spec.Paths["/v1/docs"]
	if !ok {
		t.Fatal("missing /v1/docs path")
	}
	docsOp := docsPath["get"]
	if docsOp == nil {
		t.Fatal("missing GET operation on /v1/docs")
	}
	if docsOp.Security == nil {
		t.Fatal("expected security override on docs endpoint")
	}
	if len(*docsOp.Security) != 0 {
		t.Errorf("expected empty security on docs endpoint, got %d requirements", len(*docsOp.Security))
	}
}

func TestSpec_SecurityScheme(t *testing.T) {
	spec := Spec()
	scheme, ok := spec.Components.SecuritySchemes["apiKeyAuth"]
	if !ok {
		t.Fatal("missing apiKeyAuth security scheme")
	}
	if scheme.Type != "apiKey" {
		t.Errorf("expected type apiKey, got %s", scheme.Type)
	}
	if scheme.Name != "Authorization" {
		t.Errorf("expected name Authorization, got %s", scheme.Name)
	}
	if scheme.In != "header" {
		t.Errorf("expected in header, got %s", scheme.In)
	}
}

func TestSpec_HasAllTags(t *testing.T) {
	spec := Spec()

	expectedTags := []string{
		"Execute", "Query", "Transactions", "Health",
	}

	tagNames := make(map[string]bool)
	for _, tag := range spec.Tags {
		tagNames[tag.Name] = true
		if tag.Description == "" {
			t.Errorf("tag %q has empty description", tag.Name)
		}
	}

	for _, name := range expectedTags {
		if !tagNames[name] {
			t.Errorf("missing tag: %s", name)
		}
	}

	if len(spec.Tags) != len(expectedTags) {
		t.Errorf("expected %d tags, got %d", len(expectedTags), len(spec.Tags))
	}
}

func TestSpec_AllOperationsHaveDescriptions(t *testing.T) {
	spec := Spec()

	for path, item := range spec.Paths {
		for method, op := range item {
			if op.Description == "" {
				t.Errorf("%s %s: operation has empty description", method, path)
			}
			if op.Summary == "" {
				t.Errorf("%s %s: operation has empty summary", method, path)
			}
		}
	}
}

func TestSpec_AllSchemasHaveDescriptions(t *testing.T) {
	spec := Spec()

	for name, schema := range spec.Components.Schemas {
		if schema.Description == "" {
			t.Errorf("schema %q has empty description", name)
		}
		for propName, prop := range schema.Properties {
			if prop.Description == "" {
				t.Errorf("schema %q property %q has empty description", name, propName)
			}
		}
	}
}

func TestSpec_TransactionStatusEnum(t *testing.T) {
	spec := Spec()
	txnSchema := spec.Components.Schemas["TransactionRecord"]
	if txnSchema == nil {
		t.Fatal("missing TransactionRecord schema")
	}
	statusProp := txnSchema.Properties["status"]
	if statusProp == nil {
		t.Fatal("missing status property on TransactionRecord")
	}
	if len(statusProp.Enum) != 6 {
		t.Errorf("expected 6 status enum values, got %d", len(statusProp.Enum))
	}
}

func TestSpec_InfoDescription(t *testing.T) {
	spec := Spec()
	if len(spec.Info.Description) < 100 {
		t.Error("expected a detailed info description")
	}
}

func TestSpec_ExecuteRequestRequired(t *testing.T) {
	spec := Spec()
	schema := spec.Components.Schemas["ExecuteRequest"]
	if schema == nil {
		t.Fatal("missing ExecuteRequest schema")
	}
	required := make(map[string]bool)
	for _, r := range schema.Required {
		required[r] = true
	}
	if !required["function_id"] {
		t.Error("ExecuteRequest: function_id should be required")
	}
	if !required["signer"] {
		t.Error("ExecuteRequest: signer should be required")
	}
}

func TestSpec_QueryRequestRequired(t *testing.T) {
	spec := Spec()
	schema := spec.Components.Schemas["QueryRequest"]
	if schema == nil {
		t.Fatal("missing QueryRequest schema")
	}
	required := make(map[string]bool)
	for _, r := range schema.Required {
		required[r] = true
	}
	if !required["function_id"] {
		t.Error("QueryRequest: function_id should be required")
	}
}

func TestSpec_ExecuteHas202Response(t *testing.T) {
	spec := Spec()
	path := spec.Paths["/v1/contracts/execute"]
	if path == nil {
		t.Fatal("missing /v1/contracts/execute path")
	}
	op := path["post"]
	if op == nil {
		t.Fatal("missing POST operation on /v1/contracts/execute")
	}
	if _, ok := op.Responses["202"]; !ok {
		t.Error("execute endpoint should have 202 response")
	}
}

func TestSpec_QueryHas200Response(t *testing.T) {
	spec := Spec()
	path := spec.Paths["/v1/contracts/query"]
	if path == nil {
		t.Fatal("missing /v1/contracts/query path")
	}
	op := path["post"]
	if op == nil {
		t.Fatal("missing POST operation on /v1/contracts/query")
	}
	if _, ok := op.Responses["200"]; !ok {
		t.Error("query endpoint should have 200 response")
	}
}

func TestSpec_TransactionRecordFields(t *testing.T) {
	spec := Spec()
	schema := spec.Components.Schemas["TransactionRecord"]
	if schema == nil {
		t.Fatal("missing TransactionRecord schema")
	}

	expectedFields := []string{
		"id", "operation_type", "status", "txn_hash", "nonce",
		"sender_address", "attempt", "max_retries", "request_payload",
		"error_message", "created_at", "updated_at",
	}

	for _, f := range expectedFields {
		if _, ok := schema.Properties[f]; !ok {
			t.Errorf("TransactionRecord missing field: %s", f)
		}
	}
}
