package aptos

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
)

// stringErr is a plain error so tests can assert the string-sniff fallback.
type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

// fakeNetErr implements net.Error so IsTransientSimulationError's errors.As
// path fires without making real network calls.
type fakeNetErr struct{ timeout bool }

func (fakeNetErr) Error() string   { return "fake network error" }
func (e fakeNetErr) Timeout() bool { return e.timeout }
func (fakeNetErr) Temporary() bool { return true }

var _ net.Error = fakeNetErr{}

func TestIsTransientSimulationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429 rate limited", &SimulateHTTPError{StatusCode: http.StatusTooManyRequests}, true},
		{"503 unavailable", &SimulateHTTPError{StatusCode: http.StatusServiceUnavailable}, true},
		{"400 bad request", &SimulateHTTPError{StatusCode: http.StatusBadRequest}, false},
		{"404 not found", &SimulateHTTPError{StatusCode: http.StatusNotFound}, false},
		{"500 server error", &SimulateHTTPError{StatusCode: http.StatusInternalServerError}, false},
		{"net.Error timeout", fakeNetErr{timeout: true}, true},
		{"net.Error non-timeout", fakeNetErr{}, true},
		{"wrapped 503", fmt.Errorf("simulate: %w", &SimulateHTTPError{StatusCode: 503}), true},
		{"wrapped 400", fmt.Errorf("simulate: %w", &SimulateHTTPError{StatusCode: 400}), false},
		{"timeout substring", &stringErr{"context deadline exceeded: timeout"}, true},
		{"connection refused substring", &stringErr{"dial tcp 127.0.0.1:80: connection refused"}, true},
		{"eof substring", &stringErr{"read tcp: EOF"}, true},
		{"unrelated error", errors.New("INSUFFICIENT_BALANCE"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTransientSimulationError(tc.err); got != tc.want {
				t.Fatalf("IsTransientSimulationError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestSimulateHTTPError_Error(t *testing.T) {
	t.Parallel()
	e := &SimulateHTTPError{StatusCode: 503, Body: "node unavailable"}
	if got := e.Error(); got != "simulate HTTP 503: node unavailable" {
		t.Fatalf("unexpected: %q", got)
	}
}

func TestIsSequenceVmStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		vmStatus string
		want     bool
	}{
		{"empty", "", false},
		{"too old bare", "SEQUENCE_NUMBER_TOO_OLD", true},
		{"too new bare", "SEQUENCE_NUMBER_TOO_NEW", true},
		{"too big bare", "SEQUENCE_NUMBER_TOO_BIG", true},
		{"too old lowercase", "sequence_number_too_old", true},
		{"wrapped in VMStatus prefix", "Discard(StatusCode=SEQUENCE_NUMBER_TOO_OLD)", true},
		{"insufficient balance", "Move abort: EINSUFFICIENT_BALANCE", false},
		{"out of gas", "Out of gas", false},
		{"unknown status", "SOMETHING_ELSE", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSequenceVmStatus(tc.vmStatus); got != tc.want {
				t.Fatalf("IsSequenceVmStatus(%q) = %v, want %v", tc.vmStatus, got, tc.want)
			}
		})
	}
}
