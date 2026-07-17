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
			StatusCode:    recordTestInt(200),
			Body:          EncodeBody([]byte{0x00, 0xff, 0x80, '\n'}),
			Model:         "gpt-test-2026-07-15",
			FinishReason:  "stop",
			TextLength:    recordTestInt(17),
			ToolCallCount: recordTestInt(2),
			Usage: Usage{
				InputTokens:  recordTestInt(12),
				OutputTokens: recordTestInt(4),
				TotalTokens:  recordTestInt(16),
			},
		},
		Outcome: AttemptSuccess,
	}
}

func recordTestInt(value int) *int { return &value }

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
	if got.Response.TextLength == nil || *got.Response.TextLength != 17 || got.Response.ToolCallCount == nil || *got.Response.ToolCallCount != 2 {
		t.Fatalf("compact response counts = text %v tools %v, want text 17 tools 2", got.Response.TextLength, got.Response.ToolCallCount)
	}
}

func TestMarshalRecordValidatesCanonicalAttemptAndSettlement(t *testing.T) {
	for _, record := range []APILogRecord{validAPIAttemptRecord(t), validSettlement(t)} {
		line, err := MarshalRecord(record)
		if err != nil {
			t.Fatalf("MarshalRecord(%s): %v", record.RecordKind(), err)
		}
		decoded, err := DecodeRecord(line)
		if err != nil {
			t.Fatalf("DecodeRecord(MarshalRecord(%s)): %v", record.RecordKind(), err)
		}
		if decoded.RecordKind() != record.RecordKind() {
			t.Fatalf("decoded kind = %q, want %q", decoded.RecordKind(), record.RecordKind())
		}
	}

	invalid := validAPIAttemptRecord(t)
	invalid.Outcome = "future_outcome"
	if _, err := MarshalRecord(invalid); err == nil {
		t.Fatal("MarshalRecord() accepted an invalid durable enum")
	}
}

func TestAPIAttemptNumericEvidencePreservesPresenceAcrossStrictCodec(t *testing.T) {
	base, err := MarshalRecord(validAPIAttemptRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		present bool
	}{
		{name: "present zero", present: true},
		{name: "absent", present: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var object map[string]any
			if err := json.Unmarshal(base, &object); err != nil {
				t.Fatal(err)
			}
			response := object["response"].(map[string]any)
			usage := response["usage"].(map[string]any)
			for _, field := range []string{"status_code", "text_length", "tool_call_count"} {
				if tt.present {
					response[field] = float64(0)
				} else {
					delete(response, field)
				}
			}
			for _, field := range []string{"input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens"} {
				if tt.present {
					usage[field] = float64(0)
				} else {
					delete(usage, field)
				}
			}
			line, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			record, err := DecodeRecord(line)
			if err != nil {
				t.Fatalf("DecodeRecord(): %v", err)
			}
			canonical, err := MarshalRecord(record)
			if err != nil {
				t.Fatalf("MarshalRecord(): %v", err)
			}
			var roundTrip map[string]any
			if err := json.Unmarshal(canonical, &roundTrip); err != nil {
				t.Fatal(err)
			}
			response = roundTrip["response"].(map[string]any)
			usage = response["usage"].(map[string]any)
			for _, field := range []string{"status_code", "text_length", "tool_call_count"} {
				_, present := response[field]
				if present != tt.present {
					t.Fatalf("response field %q presence = %t, want %t: %s", field, present, tt.present, canonical)
				}
			}
			for _, field := range []string{"input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens"} {
				_, present := usage[field]
				if present != tt.present {
					t.Fatalf("usage field %q presence = %t, want %t: %s", field, present, tt.present, canonical)
				}
			}
		})
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
		{"negative input tokens", func(r *APIAttemptRecord) { *r.Response.Usage.InputTokens = -1 }},
		{"negative output tokens", func(r *APIAttemptRecord) { *r.Response.Usage.OutputTokens = -1 }},
		{"negative total tokens", func(r *APIAttemptRecord) { *r.Response.Usage.TotalTokens = -1 }},
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
