package anthropic

import (
	"testing"
)

// TestInbandErrorStatusAllTypes covers all the status code branches in
// inbandErrorStatus (lines 181, 185, 187, 189).
func TestInbandErrorStatusAllTypes(t *testing.T) {
	tests := []struct {
		errType string
		want    int
	}{
		{"invalid_request_error", 400},
		{"authentication_error", 401},
		{"permission_error", 403},
		{"not_found_error", 404},
		{"request_too_large", 413},
		{"unknown_error", 0},
	}
	for _, tt := range tests {
		got := inbandErrorStatus(tt.errType)
		if got != tt.want {
			t.Errorf("inbandErrorStatus(%q) = %d, want %d", tt.errType, got, tt.want)
		}
	}
}

// TestInbandStreamErrorEmptyMessage covers the empty message fallback
// (line 166-167).
func TestInbandStreamErrorEmptyMessage(t *testing.T) {
	payload := map[string]any{
		"error": map[string]any{
			"type": "invalid_request_error",
		},
	}
	err := inbandStreamError(payload)
	if err == nil {
		t.Fatal("inbandStreamError should return an error")
	}
}

// TestInbandStreamErrorNoErrorObject covers the case where the error object
// is missing.
func TestInbandStreamErrorNoErrorObject(t *testing.T) {
	payload := map[string]any{}
	err := inbandStreamError(payload)
	if err == nil {
		t.Fatal("inbandStreamError with no error object should return an error")
	}
}
