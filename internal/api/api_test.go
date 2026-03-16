package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Decode ---

func TestDecode_Success(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	body := `{"name":"alice","age":30}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))

	var got payload
	if err := Decode(req, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "alice" || got.Age != 30 {
		t.Errorf("got %+v, want {alice 30}", got)
	}
}

func TestDecode_NilBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = nil

	var target struct{}
	err := Decode(req, &target)
	if err == nil {
		t.Fatal("expected error for nil body")
	}
	if !strings.Contains(err.Error(), "request body is required") {
		t.Errorf("error = %q, want mention of body required", err.Error())
	}
}

func TestDecode_MalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{not json}`)))

	var target struct{}
	err := Decode(req, &target)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid request body") {
		t.Errorf("error = %q, want mention of invalid request body", err.Error())
	}
}

func TestDecode_UnknownFields(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	body := `{"name":"alice","extra":"field"}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))

	var got payload
	err := Decode(req, &got)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "invalid request body") {
		t.Errorf("error = %q, want mention of invalid request body", err.Error())
	}
}

func TestDecode_TypeMismatch(t *testing.T) {
	type payload struct {
		Count int `json:"count"`
	}

	body := `{"count":"not-a-number"}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))

	var got payload
	err := Decode(req, &got)
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
}

func TestDecode_EmptyObject(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))

	var got payload
	if err := Decode(req, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "" {
		t.Errorf("name = %q, want empty", got.Name)
	}
}

// --- JSON ---

func TestJSON_Success(t *testing.T) {
	w := httptest.NewRecorder()
	JSON(w, http.StatusOK, map[string]string{"hello": "world"})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["hello"] != "world" {
		t.Errorf("body = %v, want hello:world", got)
	}
}

func TestJSON_CustomStatusCode(t *testing.T) {
	w := httptest.NewRecorder()
	JSON(w, http.StatusCreated, map[string]int{"id": 42})

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
}

func TestJSON_NilData(t *testing.T) {
	w := httptest.NewRecorder()
	JSON(w, http.StatusOK, nil)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	// json.Encode(nil) produces "null\n"
	if body := strings.TrimSpace(w.Body.String()); body != "null" {
		t.Errorf("body = %q, want null", body)
	}
}

// --- Error ---

func TestError_Format(t *testing.T) {
	w := httptest.NewRecorder()
	Error(w, http.StatusBadRequest, "something went wrong")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var got AppError
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Message != "something went wrong" {
		t.Errorf("message = %q, want 'something went wrong'", got.Message)
	}
}

func TestError_DifferentCodes(t *testing.T) {
	tests := []struct {
		code int
		msg  string
	}{
		{http.StatusUnauthorized, "unauthorized"},
		{http.StatusNotFound, "not found"},
		{http.StatusInternalServerError, "internal error"},
	}

	for _, tc := range tests {
		t.Run(tc.msg, func(t *testing.T) {
			w := httptest.NewRecorder()
			Error(w, tc.code, tc.msg)

			if w.Code != tc.code {
				t.Errorf("status = %d, want %d", w.Code, tc.code)
			}

			var got map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got["error"] != tc.msg {
				t.Errorf("error = %q, want %q", got["error"], tc.msg)
			}
		})
	}
}

// --- AppError ---

func TestAppError_ErrorMethod(t *testing.T) {
	e := &AppError{Code: 400, Message: "bad input"}
	if e.Error() != "bad input" {
		t.Errorf("Error() = %q, want 'bad input'", e.Error())
	}
}

func TestAppError_CodeNotInJSON(t *testing.T) {
	e := &AppError{Code: 500, Message: "fail"}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["code"]; ok {
		t.Error("Code field should be excluded from JSON (json:\"-\" tag)")
	}
}
