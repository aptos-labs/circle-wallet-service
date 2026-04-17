package handler

import (
	"encoding/json"
	"net/http"
)

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	err := json.NewEncoder(w).Encode(v)
	if err != nil {
		return
	}
}

func errorResponse(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

const maxRequestBodySize = 1 << 20 // 1 MB

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	limited := http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
