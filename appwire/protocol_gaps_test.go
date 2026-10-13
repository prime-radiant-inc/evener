package appwire

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestValidateMutationParamsInvalidJSON covers the json.Unmarshal error
// path (line 184) where raw is not valid JSON.
func TestValidateMutationParamsInvalidJSON(t *testing.T) {
	err := ValidateMutationParams(MethodTurnStart, json.RawMessage(`not-json`))
	if err == nil {
		t.Fatal("invalid JSON should return error")
	}
}

// TestValidateMutationParamsEmptyStringField covers the branch where a
// required string field exists but unmarshals to an empty/whitespace
// string (line 200).
func TestValidateMutationParamsEmptyStringField(t *testing.T) {
	err := ValidateMutationParams(MethodTurnStart, json.RawMessage(`{"clientMutationId":"  "}`))
	if err == nil {
		t.Fatal("empty string field should return error")
	}
	missing, ok := errors.AsType[MissingMutationFieldError](err)
	if !ok || missing.Field != "clientMutationId" {
		t.Fatalf("error = %v, want clientMutationId reported missing", err)
	}
}

// TestValidateMutationParamsNonStringField covers the branch where a
// required string field is a non-string JSON value that fails to
// unmarshal into a string (line 200).
func TestValidateMutationParamsNonStringField(t *testing.T) {
	err := ValidateMutationParams(MethodTurnStart, json.RawMessage(`{"clientMutationId":123}`))
	if err == nil {
		t.Fatal("non-string field should return error")
	}
}
