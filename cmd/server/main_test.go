package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorized(t *testing.T) {
	apiKey := []byte("secret")

	t.Run("Bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer secret")
		if !authorized(req, apiKey) {
			t.Fatal("expected bearer token to authorize")
		}
	})

	t.Run("XAPIKey", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "secret")
		if !authorized(req, apiKey) {
			t.Fatal("expected X-API-Key to authorize")
		}
	})

	t.Run("WrongKey", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		if authorized(req, apiKey) {
			t.Fatal("expected wrong key to be rejected")
		}
	})
}
