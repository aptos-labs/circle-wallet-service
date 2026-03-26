package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- Testing Mode: auth bypass ---

func TestBuildRouter_TestingModeDisabled_RequiresAuth(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "secret-key", false, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/execute", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth + testingMode=false: status = %d, want 401", w.Code)
	}
}

func TestBuildRouter_TestingModeDisabled_ValidAuthPasses(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "secret-key", false, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/execute", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "secret-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("correct auth + testingMode=false: status = %d, want 400", w.Code)
	}
}

func TestBuildRouter_TestingModeEnabled_SkipsAuth(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "", true, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/execute", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("testingMode=true: status = %d, want 400", w.Code)
	}
}

func TestBuildRouter_TestingModeEnabled_EmptyAPIKey(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "", true, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/execute", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("testingMode=true + empty API key: status = %d, want 400", w.Code)
	}
}

// --- Unauthenticated endpoints ---

func TestBuildRouter_HealthNoAuth(t *testing.T) {
	t.Run("testing mode off requires auth", func(t *testing.T) {
		router := buildRouter(nil, nil, "http://localhost:8080", "secret-key", false, testLogger())
		req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("health (testing mode off, no auth): status = %d, want 401", w.Code)
		}
	})

	t.Run("testing mode off with valid auth", func(t *testing.T) {
		router := buildRouter(nil, nil, "http://localhost:8080", "secret-key", false, testLogger())
		req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		req.Header.Set("Authorization", "secret-key")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("health (testing mode off, valid auth): status = %d, want 200", w.Code)
		}
	})

	t.Run("testing mode on skips auth", func(t *testing.T) {
		router := buildRouter(nil, nil, "http://localhost:8080", "secret-key", true, testLogger())
		req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("health (testing mode on): status = %d, want 200", w.Code)
		}
	})
}

func TestBuildRouter_DocsEndpoint(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "", true, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/docs", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/v1/docs: status = %d, want 200", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "text/html" {
		t.Errorf("/v1/docs: Content-Type = %q, want text/html", contentType)
	}

	body := w.Body.String()
	if !strings.Contains(body, "@scalar/api-reference") {
		t.Error("/v1/docs: response body missing Scalar API reference script")
	}
	if !strings.Contains(body, "/v1/openapi.yaml") {
		t.Error("/v1/docs: response body missing openapi.yaml URL")
	}
	if !strings.Contains(body, "Contract") {
		t.Error("/v1/docs: response body missing Contract title")
	}
}

func TestBuildRouter_OpenAPIEndpoint(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "", true, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/openapi.yaml", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/v1/openapi.yaml: status = %d, want 200", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/x-yaml" {
		t.Errorf("/v1/openapi.yaml: Content-Type = %q, want application/x-yaml", contentType)
	}

	body := w.Body.String()
	if !strings.Contains(body, "openapi:") {
		t.Error("/v1/openapi.yaml: response body doesn't look like an OpenAPI spec")
	}
}

// --- Middleware ordering ---

func TestBuildRouter_RequestIDAlwaysPresent(t *testing.T) {
	tests := []struct {
		name        string
		testingMode bool
	}{
		{"testing mode off", false},
		{"testing mode on", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := buildRouter(nil, nil, "http://localhost:8080", "secret-key", tc.testingMode, testLogger())

			req := httptest.NewRequest(http.MethodPost, "/v1/contracts/execute", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if id := w.Header().Get("X-Request-ID"); id == "" {
				t.Error("expected X-Request-ID header in response")
			}
		})
	}
}

func TestBuildRouter_RecoveryMiddlewareActive(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "", true, testLogger())

	// POST /v1/contracts/execute with valid JSON but nil manager should cause a panic
	// that recovery middleware catches.
	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/execute",
		strings.NewReader(`{"function_id":"0x1::mod::func","signer":"0x1","arguments":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Either 400 (validation error) or 500 (panic recovered) — depends on whether
	// ABI cache is nil. With nil abiCache, we get a panic which recovery catches.
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 from recovery middleware, got %d", w.Code)
	}
}

// --- 404 handling ---

func TestBuildRouter_UnknownRoute(t *testing.T) {
	router := buildRouter(nil, nil, "http://localhost:8080", "", true, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("unknown route: status = %d, want 404", w.Code)
	}
}
