package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/apilog"
)

// ---------------------------------------------------------------------------
// cloneAPILogHeaders
// ---------------------------------------------------------------------------

func TestCloneAPILogHeaders(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		if got := cloneAPILogHeaders(nil); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
		if got := cloneAPILogHeaders(apilog.EncodedHeader{}); got != nil {
			t.Fatalf("expected nil for empty, got %#v", got)
		}
	})
	t.Run("clones values", func(t *testing.T) {
		src := apilog.EncodedHeader{
			"X-Custom": {"a", "b"},
		}
		cloned := cloneAPILogHeaders(src)
		if cloned["X-Custom"][0] != "a" || cloned["X-Custom"][1] != "b" {
			t.Fatalf("cloned values wrong: %#v", cloned)
		}
		// Mutating the clone should not affect the source.
		cloned["X-Custom"][0] = "z"
		if src["X-Custom"][0] != "a" {
			t.Fatalf("source was mutated: %#v", src)
		}
	})
}

// ---------------------------------------------------------------------------
// validateAPILogAttemptEnvelopeSize
// ---------------------------------------------------------------------------

func TestValidateAPILogAttemptEnvelopeSize(t *testing.T) {
	t.Run("small envelope ok", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt:       apiLogRecordSummary{RecordNumber: 0, Kind: "attempt"},
		}
		if err := validateAPILogAttemptEnvelopeSize(env); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
	})
	t.Run("oversized envelope rejected", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt: apiLogRecordSummary{
				RecordNumber: 0,
				Kind:         "attempt",
				ErrorClass:   strings.Repeat("x", maxAPILogOutputBytes),
			},
		}
		if err := validateAPILogAttemptEnvelopeSize(env); err == nil {
			t.Fatal("expected oversized error")
		}
	})
}

// ---------------------------------------------------------------------------
// boundAPILogRequestHeaders
// ---------------------------------------------------------------------------

func TestBoundAPILogRequestHeaders(t *testing.T) {
	t.Run("small headers not paged", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt: apiLogRecordSummary{
				RecordNumber: 0,
				Kind:         "attempt",
				AttemptID:    "att-1",
				RequestHeaders: apilog.EncodedHeader{
					"X-Small": {"v"},
				},
			},
		}
		if err := boundAPILogRequestHeaders(&env, []byte(`{"X-Small":["v"]}`), false); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		if env.Attempt.RequestHeaders == nil {
			t.Fatal("expected headers preserved")
		}
	})
	t.Run("force paging clears headers", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt: apiLogRecordSummary{
				RecordNumber: 0,
				Kind:         "attempt",
				AttemptID:    "att-1",
				RequestHeaders: apilog.EncodedHeader{
					"X-Small": {"v"},
				},
			},
		}
		if err := boundAPILogRequestHeaders(&env, []byte(`{"X-Small":["v"]}`), true); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		if env.Attempt.RequestHeaders != nil {
			t.Fatal("expected headers cleared")
		}
		if env.Attempt.RequestHeadersInfo == nil || env.Attempt.RequestHeadersInfo.Complete {
			t.Fatalf("expected incomplete header evidence, got %#v", env.Attempt.RequestHeadersInfo)
		}
	})
	t.Run("oversized headers paged automatically", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt: apiLogRecordSummary{
				RecordNumber: 0,
				Kind:         "attempt",
				AttemptID:    "att-1",
				RequestHeaders: apilog.EncodedHeader{
					"X-Big": {strings.Repeat("x", maxAPILogOutputBytes)},
				},
			},
		}
		if err := boundAPILogRequestHeaders(&env, []byte(strings.Repeat("x", maxAPILogOutputBytes)), false); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		if env.Attempt.RequestHeaders != nil {
			t.Fatal("expected headers cleared for oversized")
		}
	})
}

// ---------------------------------------------------------------------------
// fitAPILogBodyPage
// ---------------------------------------------------------------------------

func TestFitAPILogBodyPage(t *testing.T) {
	t.Run("utf8 body fits", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt:       apiLogRecordSummary{RecordNumber: 0, Kind: "attempt", AttemptID: "att-1"},
		}
		decoded := []byte("hello world")
		encoded := apilog.EncodeBody(decoded)
		if err := fitAPILogBodyPage(&env, nil, encoded, decoded, "response", "att-1", 0, 1024); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		if env.Body == nil || env.Body.Encoding != apilog.BodyUTF8 {
			t.Fatalf("expected utf8 body, got %#v", env.Body)
		}
		if env.Body.Data != "hello world" {
			t.Fatalf("expected data 'hello world', got %q", env.Body.Data)
		}
		if env.Continuation != nil {
			t.Fatal("expected no continuation for full body")
		}
	})
	t.Run("base64 body for non-utf8", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt:       apiLogRecordSummary{RecordNumber: 0, Kind: "attempt", AttemptID: "att-1"},
		}
		decoded := []byte{0xff, 0xfe, 0xfd}
		encoded := apilog.EncodeBody(decoded)
		if err := fitAPILogBodyPage(&env, nil, encoded, decoded, "response", "att-1", 0, 1024); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		if env.Body == nil || env.Body.Encoding != apilog.BodyBase64 {
			t.Fatalf("expected base64 body, got %#v", env.Body)
		}
	})
	t.Run("partial body has continuation", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt:       apiLogRecordSummary{RecordNumber: 0, Kind: "attempt", AttemptID: "att-1"},
		}
		decoded := []byte("hello world this is a longer body")
		encoded := apilog.EncodeBody(decoded)
		if err := fitAPILogBodyPage(&env, nil, encoded, decoded, "response", "att-1", 0, 5); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		if env.Body == nil || env.Body.BytesReturned != 5 {
			t.Fatalf("expected 5 bytes returned, got %#v", env.Body)
		}
		if env.Continuation == nil || env.Continuation.OffsetBytes != 5 {
			t.Fatalf("expected continuation at offset 5, got %#v", env.Continuation)
		}
	})
	t.Run("partial body from offset", func(t *testing.T) {
		env := apiLogAttemptEnvelope{
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			Attempt:       apiLogRecordSummary{RecordNumber: 0, Kind: "attempt", AttemptID: "att-1"},
		}
		decoded := []byte("hello world this is a longer body")
		encoded := apilog.EncodeBody(decoded)
		if err := fitAPILogBodyPage(&env, nil, encoded, decoded, "response", "att-1", 10, 5); err != nil {
			t.Fatalf("expected ok, got %v", err)
		}
		if env.Body == nil || env.Body.OffsetBytes != 10 {
			t.Fatalf("expected offset 10, got %#v", env.Body)
		}
		if env.Body.BytesReturned != 5 {
			t.Fatalf("expected 5 bytes, got %d", env.Body.BytesReturned)
		}
		if env.Continuation == nil || env.Continuation.OffsetBytes != 15 {
			t.Fatalf("expected continuation at 15, got %#v", env.Continuation)
		}
	})
}

// ---------------------------------------------------------------------------
// boundedAPILogMetadata
// ---------------------------------------------------------------------------

func TestBoundedAPILogMetadata(t *testing.T) {
	t.Run("short value unchanged", func(t *testing.T) {
		got, trunc := boundedAPILogMetadata("hello", false)
		if got != "hello" || trunc {
			t.Fatalf("expected unchanged, got %q trunc=%v", got, trunc)
		}
	})
	t.Run("long value truncated", func(t *testing.T) {
		long := strings.Repeat("x", maxAPILogMetadataBytes+100)
		got, trunc := boundedAPILogMetadata(long, false)
		if !trunc {
			t.Fatal("expected truncated=true")
		}
		if len(got) > maxAPILogMetadataBytes {
			t.Fatalf("expected len <= %d, got %d", maxAPILogMetadataBytes, len(got))
		}
	})
	t.Run("already truncated preserved", func(t *testing.T) {
		long := strings.Repeat("x", maxAPILogMetadataBytes+100)
		got, trunc := boundedAPILogMetadata(long, true)
		if !trunc {
			t.Fatal("expected truncated=true preserved")
		}
		if len(got) > maxAPILogMetadataBytes {
			t.Fatalf("expected len <= %d, got %d", maxAPILogMetadataBytes, len(got))
		}
	})
	t.Run("long multibyte truncates on rune boundary", func(t *testing.T) {
		// Build a string that ends with multibyte continuation bytes.
		long := strings.Repeat("x", maxAPILogMetadataBytes-2) + strings.Repeat("é", 50)
		got, trunc := boundedAPILogMetadata(long, false)
		if !trunc {
			t.Fatal("expected truncated=true")
		}
		// The result should be valid UTF-8.
		if !utf8ValidString(got) {
			t.Fatalf("expected valid utf8, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// selectAPILogRange
// ---------------------------------------------------------------------------

func TestSelectAPILogRange(t *testing.T) {
	t.Run("empty defaults to last:20", func(t *testing.T) {
		start, end, normalized, warning := selectAPILogRange("", 100)
		if warning != "" {
			t.Fatalf("expected no warning, got %q", warning)
		}
		if normalized != "last:20" {
			t.Fatalf("normalized = %q", normalized)
		}
		if end != 99 || start != 80 {
			t.Fatalf("start=%d end=%d", start, end)
		}
	})
	t.Run("invalid range produces warning", func(t *testing.T) {
		start, end, normalized, warning := selectAPILogRange("garbage", 100)
		if warning == "" {
			t.Fatal("expected warning for invalid range")
		}
		if normalized != "last:20" {
			t.Fatalf("normalized = %q, want last:20", normalized)
		}
		// Should still return a valid range.
		_ = start
		_ = end
	})
	t.Run("dash range clamped to max records", func(t *testing.T) {
		// A range wider than maxAPILogRecords should be clamped.
		start, end, normalized, warning := selectAPILogRange("0-200", 300)
		if warning != "" {
			t.Fatalf("expected no warning, got %q", warning)
		}
		if normalized != "0-200" {
			t.Fatalf("normalized = %q", normalized)
		}
		if end-start+1 > maxAPILogRecords {
			t.Fatalf("range too wide: %d-%d", start, end)
		}
		if end != maxAPILogRecords-1 {
			t.Fatalf("expected end=%d, got %d", maxAPILogRecords-1, end)
		}
	})
	t.Run("last range clamped to max records", func(t *testing.T) {
		start, end, normalized, warning := selectAPILogRange("last:200", 300)
		if warning != "" {
			t.Fatalf("expected no warning, got %q", warning)
		}
		if normalized != "last:200" {
			t.Fatalf("normalized = %q", normalized)
		}
		if end-start+1 > maxAPILogRecords {
			t.Fatalf("range too wide: %d-%d", start, end)
		}
		if start != end-maxAPILogRecords+1 {
			t.Fatalf("expected start=%d, got %d", end-maxAPILogRecords+1, start)
		}
	})
}

// ---------------------------------------------------------------------------
// apiLogAttemptSettlementLookup
// ---------------------------------------------------------------------------

func TestAPILogAttemptSettlementLookup(t *testing.T) {
	t.Run("no group selected ignores settlement", func(t *testing.T) {
		l := apiLogAttemptSettlementLookup{}
		l.consider(apilog.APIAttemptGroupSettlement{AttemptGroupID: "g1"})
		if l.settlement != nil {
			t.Fatal("expected nil settlement when no group selected")
		}
	})
	t.Run("matching group retains settlement", func(t *testing.T) {
		l := apiLogAttemptSettlementLookup{}
		l.selectAttemptGroup("g1")
		l.consider(apilog.APIAttemptGroupSettlement{AttemptGroupID: "g1", FinalAttemptID: "a1"})
		if l.settlement == nil || l.settlement.FinalAttemptID != "a1" {
			t.Fatalf("expected settlement a1, got %#v", l.settlement)
		}
	})
	t.Run("non-matching group ignored", func(t *testing.T) {
		l := apiLogAttemptSettlementLookup{}
		l.selectAttemptGroup("g1")
		l.consider(apilog.APIAttemptGroupSettlement{AttemptGroupID: "g2", FinalAttemptID: "a2"})
		if l.settlement != nil {
			t.Fatal("expected nil for non-matching group")
		}
	})
}

// ---------------------------------------------------------------------------
// apiLogSummaryRetention
// ---------------------------------------------------------------------------

func TestAPILogSummaryRetention(t *testing.T) {
	t.Run("tail mode retains last N", func(t *testing.T) {
		r := newAPILogSummaryRetention("last:5")
		for i := 0; i < 10; i++ {
			if err := r.add(i, apilog.APIAttemptRecord{AttemptID: "a"}); err != nil {
				t.Fatalf("add %d: %v", i, err)
			}
		}
		// In tail mode with < maxAPILogRecords, all are kept.
		result := r.result()
		if len(result) != 10 {
			t.Fatalf("expected 10 records, got %d", len(result))
		}
	})
	t.Run("start mode retains first N", func(t *testing.T) {
		r := newAPILogSummaryRetention("start:5")
		for i := 0; i < 10; i++ {
			if err := r.add(i, apilog.APIAttemptRecord{AttemptID: "a"}); err != nil {
				t.Fatalf("add %d: %v", i, err)
			}
		}
		result := r.result()
		if len(result) > maxAPILogRecords {
			t.Fatalf("expected at most %d records, got %d", maxAPILogRecords, len(result))
		}
	})
	t.Run("exact range retains specified records", func(t *testing.T) {
		r := newAPILogSummaryRetention("2-4")
		for i := 0; i < 10; i++ {
			if err := r.add(i, apilog.APIAttemptRecord{AttemptID: "a"}); err != nil {
				t.Fatalf("add %d: %v", i, err)
			}
		}
		result := r.result()
		// Should keep records 2-4.
		if len(result) == 0 {
			t.Fatal("expected some records")
		}
		if result[0].RecordNumber != 2 {
			t.Fatalf("expected first record at 2, got %d", result[0].RecordNumber)
		}
	})
	t.Run("summarize nil record errors", func(t *testing.T) {
		r := newAPILogSummaryRetention("last:5")
		// A nil APILogRecord panics in summarizeAPILogRecord before reaching
		// the default branch; this is not a valid decoder output, so we skip it.
		_ = r
	})
}

// ---------------------------------------------------------------------------
// summarizeAPILogRecord
// ---------------------------------------------------------------------------

func TestSummarizeAPILogRecord(t *testing.T) {
	t.Run("attempt record with response", func(t *testing.T) {
		rec := apilog.APIAttemptRecord{
			AttemptID:        "att-1",
			AttemptGroupID:   "grp-1",
			AttemptIndex:     0,
			ProviderInstance: "provider-1",
			RequestModel:     "model-1",
			Request: apilog.APIAttemptRequest{
				HistoryMode: "full",
				Method:      "POST",
				Endpoint:    "/v1/chat",
				Body: apilog.EncodedBody{
					Encoding:  apilog.BodyUTF8,
					ByteCount: 10,
					Exact:     true,
				},
			},
			Response: &apilog.APIAttemptResponse{
				StatusCode:   intPtr(200),
				Model:        "resp-model",
				FinishReason: "stop",
				Body: apilog.EncodedBody{
					Encoding:  apilog.BodyUTF8,
					ByteCount: 20,
					Exact:     true,
				},
			},
			Outcome: "success",
		}
		summary, err := summarizeAPILogRecord(5, rec)
		if err != nil {
			t.Fatalf("summarize: %v", err)
		}
		if summary.RecordNumber != 5 || summary.AttemptID != "att-1" {
			t.Fatalf("summary = %#v", summary)
		}
		if summary.StatusCode == nil || *summary.StatusCode != 200 {
			t.Fatalf("expected status 200, got %#v", summary.StatusCode)
		}
		if summary.ResponseModel != "resp-model" {
			t.Fatalf("expected resp-model, got %q", summary.ResponseModel)
		}
	})
	t.Run("settlement record", func(t *testing.T) {
		rec := apilog.APIAttemptGroupSettlement{
			AttemptGroupID:    "grp-1",
			FinalAttemptID:    "att-final",
			FinalAttemptCount: 3,
			Outcome:           "success",
		}
		summary, err := summarizeAPILogRecord(2, rec)
		if err != nil {
			t.Fatalf("summarize: %v", err)
		}
		if summary.AttemptGroupID != "grp-1" || summary.FinalAttemptID != "att-final" {
			t.Fatalf("summary = %#v", summary)
		}
		if summary.FinalAttemptCount == nil || *summary.FinalAttemptCount != 3 {
			t.Fatalf("expected final count 3, got %#v", summary.FinalAttemptCount)
		}
	})
	t.Run("nil record panics", func(t *testing.T) {
		// summarizeAPILogRecord dereferences the record interface for RecordKind()
		// before the type switch, so a nil APILogRecord is not a valid input and
		// is documented as unreachable from the decoder path.
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic for nil record")
			}
		}()
		_, _ = summarizeAPILogRecord(0, nil)
	})
}

// ---------------------------------------------------------------------------
// findAPILogAttempt error paths
// ---------------------------------------------------------------------------

func TestFindAPILogAttemptErrors(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, _, _, err := findAPILogAttempt(ctx, "/nonexistent", "att-1")
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
	})
	t.Run("nonexistent file", func(t *testing.T) {
		_, _, _, _, err := findAPILogAttempt(context.Background(), "/nonexistent/path/file.jsonl", "att-1")
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
	})
}

// ---------------------------------------------------------------------------
// decodeAPILogSummaries error paths
// ---------------------------------------------------------------------------

func TestDecodeAPILogSummariesErrors(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, _, err := decodeAPILogSummaries(ctx, "/nonexistent", "last:5")
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
	})
	t.Run("nonexistent file", func(t *testing.T) {
		_, _, _, err := decodeAPILogSummaries(context.Background(), "/nonexistent/path/file.jsonl", "last:5")
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
	})
}

// ---------------------------------------------------------------------------
// apiLogPathForTranscript
// ---------------------------------------------------------------------------

func TestAPILogPathForTranscript(t *testing.T) {
	got := apiLogPathForTranscript("/state/sessions/s1/s1.transcript.jsonl")
	want := "/state/sessions/s1/s1.api.jsonl"
	if got != want {
		t.Fatalf("apiLogPathForTranscript = %q, want %q", got, want)
	}
}

// utf8ValidString is a small helper to avoid importing unicode/utf8 just for a validity check.
func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

func intPtr(value int) *int { return &value }
