package apilog

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// TestValidateRecordKindError covers the kind validation error (line 111).
func TestValidateRecordKindError(t *testing.T) {
	r := APIAttemptRecord{Kind: "wrong"}
	err := r.validateRecord(DecodeStrict)
	if err == nil {
		t.Fatal("wrong kind should error")
	}
}

// TestValidateRecordSchemaVersionError covers the schema version error.
func TestValidateRecordSchemaVersionError(t *testing.T) {
	r := validRecordForValidation(t)
	r.SchemaVersion = 99
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("wrong schema version should error")
	}
}

// TestValidateRecordNegativeStatusCode covers the negative status code path
// (lines 193-194).
func TestValidateRecordNegativeStatusCode(t *testing.T) {
	negStatus := -1
	r := validRecordForValidation(t)
	r.Response = &APIAttemptResponse{StatusCode: &negStatus}
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("negative status code should error")
	}
}

// TestValidateRecordNegativeTextLength covers the negative text length path
// (lines 196-197).
func TestValidateRecordNegativeTextLength(t *testing.T) {
	negLen := -1
	r := validRecordForValidation(t)
	r.Response = &APIAttemptResponse{TextLength: &negLen}
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("negative text length should error")
	}
}

// TestValidateRecordNegativeToolCallCount covers the negative tool call count.
func TestValidateRecordNegativeToolCallCount(t *testing.T) {
	negCount := -1
	r := validRecordForValidation(t)
	r.Response = &APIAttemptResponse{ToolCallCount: &negCount}
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("negative tool call count should error")
	}
}

// TestValidateRecordNegativeCacheReadTokens covers the negative cache read
// token path (lines 216-218).
func TestValidateRecordNegativeCacheReadTokens(t *testing.T) {
	negTokens := -1
	r := validRecordForValidation(t)
	r.Response = &APIAttemptResponse{
		Usage: Usage{CacheReadTokens: &negTokens},
	}
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("negative cache read tokens should error")
	}
}

// TestValidateRecordNegativeCacheWriteTokens covers the negative cache write
// token path (lines 219-221).
func TestValidateRecordNegativeCacheWriteTokens(t *testing.T) {
	negTokens := -1
	r := validRecordForValidation(t)
	r.Response = &APIAttemptResponse{
		Usage: Usage{CacheWriteTokens: &negTokens},
	}
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("negative cache write tokens should error")
	}
}

// TestValidateRecordNegativeInputTokens covers the negative input token path
// (lines 211-214).
func TestValidateRecordNegativeInputTokens(t *testing.T) {
	negTokens := -1
	r := validRecordForValidation(t)
	r.Response = &APIAttemptResponse{
		Usage: Usage{InputTokens: &negTokens},
	}
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("negative input tokens should error")
	}
}

// TestValidateRecordNegativeOutputTokens covers negative output tokens.
func TestValidateRecordNegativeOutputTokens(t *testing.T) {
	negTokens := -1
	r := validRecordForValidation(t)
	r.Response = &APIAttemptResponse{
		Usage: Usage{OutputTokens: &negTokens},
	}
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("negative output tokens should error")
	}
}

// TestContainsForbiddenDurableStringEmpty covers the marshal error path
// (line 310-311) with an empty string.
func TestContainsForbiddenDurableStringEmpty(t *testing.T) {
	got := containsForbiddenDurableString("", nil, nil)
	if got {
		t.Fatal("empty string should not contain forbidden evidence")
	}
}

func validRecordForValidation(t *testing.T) APIAttemptRecord {
	t.Helper()
	return APIAttemptRecord{
		Kind:             attemptRecordKind,
		SchemaVersion:    recordSchemaVersion,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   "ag_test",
		AttemptIndex:     1,
		Timestamp:        time.Unix(1, 0).UTC(),
		ProviderInstance: "test",
		RequestModel:     "model",
		Request: APIAttemptRequest{
			Method:   "POST",
			Endpoint: "https://provider.test",
			Body:     EncodeBody([]byte(`{}`)),
		},
	}
}

// TestValidateRecordInvalidOutcome covers the invalid outcome path.
func TestValidateRecordInvalidOutcome(t *testing.T) {
	r := validRecordForValidation(t)
	r.Outcome = "invalid"
	if err := r.validateRecord(DecodeStrict); err == nil {
		t.Fatal("invalid outcome should error")
	}
}

// TestUsageValidateNegativeTotalTokens covers negative total tokens.
func TestUsageValidateNegativeTotalTokens(t *testing.T) {
	negTokens := -1
	u := Usage{TotalTokens: &negTokens}
	if err := u.validate(); err == nil {
		t.Fatal("negative total tokens should error")
	}
}

// suppress unused import warnings
var _ = json.Marshal
