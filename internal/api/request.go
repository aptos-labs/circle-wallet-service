package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Decode reads and decodes a JSON request body into the target struct.
// Returns an error message suitable for the client if decoding fails.
func Decode(r *http.Request, target any) error {
	if r.Body == nil {
		return fmt.Errorf("request body is required")
	}
	defer func() { _ = r.Body.Close() }()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}
