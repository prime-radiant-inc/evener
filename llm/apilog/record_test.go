package apilog

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
)

var recordTestTime = time.Date(2026, 7, 15, 12, 34, 56, 789, time.UTC)

func validAPIAttemptRecord(t *testing.T) APIAttemptRecord {
	t.Helper()
	return APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   "ag_test_group",
		AttemptIndex:     1,
		Timestamp:        recordTestTime,
		LatencyMS:        25,
		ProviderInstance: "openai-primary",
		RequestModel:     "gpt-test",
		Request: APIAttemptRequest{
			Method:      "POST",
			Endpoint:    "https://provider.test/v1/responses",
			Headers:     map[string][]string{"Content-Type": {"application/json"}},
			Body:        EncodeBody([]byte("{\"text\":\"line\\nquote\\\"\"}")),
			Model:       "gpt-test",
			HistoryMode: "messages",
		},
		Response: &APIAttemptResponse{
			StatusCode:    200,
			Body:          EncodeBody([]byte{0x00, 0xff, 0x80, '\n'}),
			Model:         "gpt-test-2026-07-15",
			FinishReason:  "stop",
			TextLength:    17,
			ToolCallCount: 2,
			Usage: Usage{
				InputTokens:  12,
				OutputTokens: 4,
				TotalTokens:  16,
			},
		},
		Outcome: AttemptSuccess,
	}
}

func validSettlement(t *testing.T) APIAttemptGroupSettlement {
	t.Helper()
	return APIAttemptGroupSettlement{
		Kind:              "attempt_group_settlement",
		SchemaVersion:     1,
		AttemptGroupID:    "ag_test_group",
		FinalAttemptID:    identifier.MustNewAPIAttemptID(),
		FinalAttemptCount: 1,
		Outcome:           AttemptSuccess,
		SettledAt:         recordTestTime.Add(time.Second),
	}
}

func TestAPIAttemptRecordRoundTripsExactBodies(t *testing.T) {
	want := validAPIAttemptRecord(t)
	line, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeRecord(line)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := record.(APIAttemptRecord)
	if !ok {
		t.Fatalf("DecodeRecord() type = %T, want APIAttemptRecord", record)
	}
	if got.RecordKind() != "api_attempt" {
		t.Fatalf("RecordKind() = %q", got.RecordKind())
	}
	requestBody, err := DecodeBody(got.Request.Body)
	if err != nil {
		t.Fatal(err)
	}
	wantRequestBody, err := DecodeBody(want.Request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requestBody, wantRequestBody) {
		t.Fatalf("request body = %v, want %v", requestBody, wantRequestBody)
	}
	responseBody, err := DecodeBody(got.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	wantResponseBody, err := DecodeBody(want.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(responseBody, wantResponseBody) {
		t.Fatalf("response body = %v, want %v", responseBody, wantResponseBody)
	}
	if got.AttemptID != want.AttemptID || got.AttemptGroupID != want.AttemptGroupID || got.AttemptIndex != want.AttemptIndex {
		t.Fatalf("attempt identity = (%q, %q, %d), want (%q, %q, %d)", got.AttemptID, got.AttemptGroupID, got.AttemptIndex, want.AttemptID, want.AttemptGroupID, want.AttemptIndex)
	}
	if got.Response.TextLength != 17 || got.Response.ToolCallCount != 2 {
		t.Fatalf("compact response counts = text %d tools %d, want text 17 tools 2", got.Response.TextLength, got.Response.ToolCallCount)
	}
}

func TestSettlementRecordRoundTripsAsDurableInterface(t *testing.T) {
	want := validSettlement(t)
	line, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeRecord(line)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := record.(APIAttemptGroupSettlement)
	if !ok {
		t.Fatalf("DecodeRecord() type = %T, want APIAttemptGroupSettlement", record)
	}
	if got.RecordKind() != "attempt_group_settlement" {
		t.Fatalf("RecordKind() = %q", got.RecordKind())
	}
	if got.AttemptGroupID != want.AttemptGroupID || got.FinalAttemptID != want.FinalAttemptID || got.FinalAttemptCount != want.FinalAttemptCount {
		t.Fatalf("settlement identity = (%q, %q, %d), want (%q, %q, %d)", got.AttemptGroupID, got.FinalAttemptID, got.FinalAttemptCount, want.AttemptGroupID, want.FinalAttemptID, want.FinalAttemptCount)
	}
}

func TestSettlementRecordAllowsExplicitZeroAttemptOutcome(t *testing.T) {
	record := validSettlement(t)
	record.FinalAttemptID = ""
	record.FinalAttemptCount = 0
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRecord(line); err != nil {
		t.Fatalf("DecodeRecord() rejected zero-attempt settlement: %v", err)
	}
}

func TestAPIAttemptRecordValidationRejectsInvalidDurableFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*APIAttemptRecord)
	}{
		{"kind", func(r *APIAttemptRecord) { r.Kind = "wrong" }},
		{"schema version", func(r *APIAttemptRecord) { r.SchemaVersion = 2 }},
		{"attempt ID", func(r *APIAttemptRecord) { r.AttemptID = "att_invalid" }},
		{"attempt group ID", func(r *APIAttemptRecord) { r.AttemptGroupID = "" }},
		{"attempt index", func(r *APIAttemptRecord) { r.AttemptIndex = 0 }},
		{"timestamp", func(r *APIAttemptRecord) { r.Timestamp = time.Time{} }},
		{"latency", func(r *APIAttemptRecord) { r.LatencyMS = -1 }},
		{"provider instance", func(r *APIAttemptRecord) { r.ProviderInstance = "" }},
		{"request model", func(r *APIAttemptRecord) { r.RequestModel = "" }},
		{"method", func(r *APIAttemptRecord) { r.Request.Method = "" }},
		{"endpoint", func(r *APIAttemptRecord) { r.Request.Endpoint = "" }},
		{"request body", func(r *APIAttemptRecord) { r.Request.Body.ByteCount++ }},
		{"response body", func(r *APIAttemptRecord) { r.Response.Body.ByteCount++ }},
		{"outcome", func(r *APIAttemptRecord) { r.Outcome = "unknown" }},
		{"negative input tokens", func(r *APIAttemptRecord) { r.Response.Usage.InputTokens = -1 }},
		{"negative output tokens", func(r *APIAttemptRecord) { r.Response.Usage.OutputTokens = -1 }},
		{"negative total tokens", func(r *APIAttemptRecord) { r.Response.Usage.TotalTokens = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validAPIAttemptRecord(t)
			tt.mutate(&record)
			line, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeRecord(line); err == nil {
				t.Fatal("DecodeRecord() accepted invalid attempt")
			}
		})
	}
}

func TestSettlementRecordValidationRejectsInvalidDurableFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*APIAttemptGroupSettlement)
	}{
		{"kind", func(r *APIAttemptGroupSettlement) { r.Kind = "wrong" }},
		{"schema version", func(r *APIAttemptGroupSettlement) { r.SchemaVersion = 2 }},
		{"attempt group ID", func(r *APIAttemptGroupSettlement) { r.AttemptGroupID = "" }},
		{"final attempt ID", func(r *APIAttemptGroupSettlement) { r.FinalAttemptID = "att_invalid" }},
		{"negative final attempt count", func(r *APIAttemptGroupSettlement) { r.FinalAttemptCount = -1 }},
		{"missing final attempt ID", func(r *APIAttemptGroupSettlement) { r.FinalAttemptID = "" }},
		{"ID on zero attempts", func(r *APIAttemptGroupSettlement) { r.FinalAttemptCount = 0 }},
		{"outcome", func(r *APIAttemptGroupSettlement) { r.Outcome = "unknown" }},
		{"settled at", func(r *APIAttemptGroupSettlement) { r.SettledAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validSettlement(t)
			tt.mutate(&record)
			line, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeRecord(line); err == nil {
				t.Fatal("DecodeRecord() accepted invalid settlement")
			}
		})
	}
}

func TestAPIAttemptRecordRequiresResponseForSuccess(t *testing.T) {
	record := validAPIAttemptRecord(t)
	record.Response = nil
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRecord(line); err == nil {
		t.Fatal("DecodeRecord() accepted successful attempt without response")
	}
}
