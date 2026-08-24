package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestNormalizeClientMutationError covers all branches of
// NormalizeClientMutationError (session_client_mutation.go:29-51).
func TestNormalizeClientMutationError(t *testing.T) {
	t.Parallel()
	// nil error -> nil.
	if err := NormalizeClientMutationError("id_1", nil); err != nil {
		t.Fatalf("nil error: %v", err)
	}
	// WireError passes through.
	wireErr := appwire.InvalidParams("bad input")
	if err := NormalizeClientMutationError("id_1", wireErr); !errors.Is(err, wireErr) {
		t.Fatalf("WireError should pass through: got %v", err)
	}
	// Mismatch error -> InvalidRequest.
	if err := NormalizeClientMutationError("id_1", errClientMutationMismatch); err == nil {
		t.Fatal("mismatch should return error")
	}
	// Generic error -> InternalError.
	genericErr := errors.New("disk full")
	resultErr := NormalizeClientMutationError("id_1", genericErr)
	if resultErr == nil {
		t.Fatal("generic error should return error")
	}
	// Verify the internal error is a WireError with CodeInternalError.
	we, ok := errors.AsType[appwire.WireError](resultErr)
	if !ok {
		t.Fatal("generic error should be a WireError")
	}
	if we.Code != appwire.CodeInternalError {
		t.Fatalf("code = %v, want %v", we.Code, appwire.CodeInternalError)
	}
	// The Data field should carry the client mutation ID.
	data, ok := we.Data.(appwire.ErrorData)
	if !ok {
		t.Fatal("data should be ErrorData")
	}
	if data.ClientMutationID != "id_1" {
		t.Fatalf("client mutation ID = %q, want id_1", data.ClientMutationID)
	}
}

// TestSessionIsQuiesced covers sessionIsQuiesced
// (session_client_mutation.go:496-498).
func TestSessionIsQuiesced(t *testing.T) {
	t.Parallel()
	// Not running and no claimed turn -> quiesced.
	if !sessionIsQuiesced(false, "") {
		t.Fatal("not running with no turn should be quiesced")
	}
	// Running -> not quiesced.
	if sessionIsQuiesced(true, "") {
		t.Fatal("running should not be quiesced")
	}
	// Not running but has claimed turn -> not quiesced.
	if sessionIsQuiesced(false, "turn_1") {
		t.Fatal("has claimed turn should not be quiesced")
	}
	// Running with claimed turn -> not quiesced.
	if sessionIsQuiesced(true, "turn_1") {
		t.Fatal("running with turn should not be quiesced")
	}
	// Whitespace-only turn ID counts as empty.
	if !sessionIsQuiesced(false, "  ") {
		t.Fatal("whitespace-only turn ID should count as quiesced")
	}
}

// TestNewClientMutationRequest covers all branches of newClientMutationRequest
// (session_client_mutation.go:225-243).
func TestNewClientMutationRequest(t *testing.T) {
	t.Parallel()
	// Empty method -> error.
	_, err := newClientMutationRequest("", "id_1", nil)
	if err == nil {
		t.Fatal("empty method should error")
	}
	// Empty ID -> error.
	_, err = newClientMutationRequest("turn/start", "", nil)
	if err == nil {
		t.Fatal("empty ID should error")
	}
	// Valid request with nil payload.
	req, err := newClientMutationRequest("turn/start", "id_1", nil)
	if err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if req.ClientMutationID != "id_1" || req.Method != "turn/start" {
		t.Fatalf("request = %+v", req)
	}
	if req.PayloadHash == "" {
		t.Fatal("payload hash should not be empty")
	}
	// Valid request with a payload.
	req, err = newClientMutationRequest("turn/steer", "id_2", map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("valid request with payload: %v", err)
	}
	if len(req.Payload) == 0 {
		t.Fatal("payload should not be empty")
	}
	// Same payload should produce same hash.
	req2, _ := newClientMutationRequest("turn/steer", "id_3", map[string]any{"key": "value"})
	if req.PayloadHash != req2.PayloadHash {
		t.Fatal("same payload should produce same hash")
	}
	// Unmarshalable payload (channel) -> error.
	_, err = newClientMutationRequest("turn/start", "id_4", make(chan int))
	if err == nil {
		t.Fatal("unmarshalable payload should error")
	}
}
