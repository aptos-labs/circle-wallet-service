package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRequestID(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := GetRequestID(r.Context())
		if id == "" {
			t.Error("expected request ID in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Without X-Request-ID header — should generate one
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if id := w.Header().Get("X-Request-ID"); id == "" {
		t.Error("expected X-Request-ID in response")
	}

	// With X-Request-ID header — should preserve it
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Request-ID", "custom-id-123")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if id := w2.Header().Get("X-Request-ID"); id != "custom-id-123" {
		t.Errorf("X-Request-ID = %q, want custom-id-123", id)
	}
}

func TestGetRequestID_NoContext(t *testing.T) {
	id := GetRequestID(context.Background())
	if id != "" {
		t.Errorf("expected empty request ID from bare context, got %q", id)
	}
}

// --- Logging middleware ---

func TestLogging_RecordsStatusAndCallsNext(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	handler := Logging(logger)(inner)

	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/execute", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, `"method":"POST"`) {
		t.Error("log missing method field")
	}
	if !strings.Contains(logOutput, `"path":"/v1/contracts/execute"`) {
		t.Error("log missing path field")
	}
	if !strings.Contains(logOutput, `"status":201`) {
		t.Errorf("log missing correct status; got: %s", logOutput)
	}
}

func TestLogging_DefaultStatus200(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	// Handler that writes body but never calls WriteHeader explicitly.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	handler := Logging(logger)(inner)

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	logOutput := buf.String()
	if !strings.Contains(logOutput, `"status":200`) {
		t.Errorf("expected default status 200 in log; got: %s", logOutput)
	}
}

func TestLogging_IncludesDurationMs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Logging(logger)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(buf.String(), `"duration_ms"`) {
		t.Error("log missing duration_ms field")
	}
}

func TestResponseWriter_WriteHeaderCapturesStatus(t *testing.T) {
	w := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

	rw.WriteHeader(http.StatusNotFound)

	if rw.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rw.status)
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("underlying status = %d, want 404", w.Code)
	}
}

// --- Auth middleware ---

func TestAuth(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Auth("secret-key")(inner)

	// Without auth header
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth: status = %d, want 401", w.Code)
	}

	// Wrong key
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "wrong-key")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", w2.Code)
	}

	// Correct key
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.Header.Set("Authorization", "secret-key")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("correct key: status = %d, want 200", w3.Code)
	}
}

func TestAuth_EmptyAuthorizationHeader(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Auth("secret-key")(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty auth header: status = %d, want 401", w.Code)
	}
}

func TestAuth_BearerPrefixNotAccepted(t *testing.T) {
	// The auth middleware expects a raw key, not "Bearer <key>".
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Auth("secret-key")(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Bearer prefix: status = %d, want 401 (raw key expected)", w.Code)
	}
}

func TestAuth_CaseSensitive(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Auth("Secret-Key")(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "secret-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("case mismatch: status = %d, want 401", w.Code)
	}
}

func TestAuth_LongKey(t *testing.T) {
	longKey := strings.Repeat("a", 1024)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Auth(longKey)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", longKey)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("long key: status = %d, want 200", w.Code)
	}
}

// --- Recovery middleware ---

func TestRecovery(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	handler := Recovery(logger)(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("panic: status = %d, want 500", w.Code)
	}
}
