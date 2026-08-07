package apilog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

// corruptLargeBody returns an EncodedBody claiming base64 encoding whose
// Data is deliberately not valid base64. It is sized to be non-trivial so a
// decode that actually materializes/validates it is not free.
func corruptLargeBody() EncodedBody {
	const size = 1 << 16 // 64KiB
	return EncodedBody{
		Encoding:  BodyBase64,
		Data:      strings.Repeat("!", size),
		ByteCount: size,
		Exact:     true,
	}
}

// TestMetadataOnlyDecodeSkipsBodyValidation pins: (1) the default strict
// decode still rejects a record whose body is corrupt base64 (existing
// behavior), and (2) a metadata-only decode accepts the same record,
// leaving EncodedBody fields in their encoded form untouched, while still
// surfacing the attempt's scalar fields.
func TestMetadataOnlyDecodeSkipsBodyValidation(t *testing.T) {
	record := validAPIAttemptRecord(t)
	corrupt := corruptLargeBody()
	record.Request.Body = corrupt
	record.Response.Body = corrupt

	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := DecodeRecord(line); err == nil {
		t.Fatal("DecodeRecord() accepted a record with corrupt base64 body")
	}

	data := append(append([]byte{}, line...), '\n')
	decoder := NewDecoder(bytes.NewReader(data), len(data), WithMetadataOnly())
	decoded, err := decoder.Next()
	if err != nil {
		t.Fatalf("metadata-only Next(): %v", err)
	}
	attempt, ok := decoded.(APIAttemptRecord)
	if !ok {
		t.Fatalf("decoded type = %T, want APIAttemptRecord", decoded)
	}
	if attempt.RequestModel != record.RequestModel {
		t.Fatalf("RequestModel = %q, want %q", attempt.RequestModel, record.RequestModel)
	}
	if attempt.Response == nil {
		t.Fatal("Response = nil, want non-nil")
	}
	if attempt.Response.TextLength == nil || *attempt.Response.TextLength != *record.Response.TextLength {
		t.Fatalf("TextLength = %v, want %v", attempt.Response.TextLength, record.Response.TextLength)
	}
	if attempt.Response.ToolCallCount == nil || *attempt.Response.ToolCallCount != *record.Response.ToolCallCount {
		t.Fatalf("ToolCallCount = %v, want %v", attempt.Response.ToolCallCount, record.Response.ToolCallCount)
	}
	if attempt.Response.Usage.InputTokens == nil || *attempt.Response.Usage.InputTokens != *record.Response.Usage.InputTokens {
		t.Fatalf("Usage.InputTokens = %v, want %v", attempt.Response.Usage.InputTokens, record.Response.Usage.InputTokens)
	}
	if attempt.Request.Body != corrupt {
		t.Fatalf("Request.Body = %+v, want untouched %+v", attempt.Request.Body, corrupt)
	}
	if attempt.Response.Body != corrupt {
		t.Fatalf("Response.Body = %+v, want untouched %+v", attempt.Response.Body, corrupt)
	}
}

// TestMetadataOnlyDecodeStillEnforcesStructuralFields proves metadata-only
// mode is not a blanket bypass: everything validateRecord checks other than
// body content (kind, schema version, required fields, ...) still rejects a
// malformed record.
func TestMetadataOnlyDecodeStillEnforcesStructuralFields(t *testing.T) {
	record := validAPIAttemptRecord(t)
	record.Outcome = "future_outcome"
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte{}, line...), '\n')
	decoder := NewDecoder(bytes.NewReader(data), len(data), WithMetadataOnly())
	if _, err := decoder.Next(); err == nil {
		t.Fatal("metadata-only Next() accepted an unknown attempt outcome")
	}
}

// benchmarkAPILogFixture builds a many-record synthetic API log with
// realistic (non-trivial) request/response bodies, approximating the
// multi-hundred-MB logs serf-doctor summarization runs against.
func benchmarkAPILogFixture(b *testing.B, recordCount int) []byte {
	b.Helper()
	requestBody := strings.Repeat("request payload byte content. ", 200)   // ~6.4KB
	responseBody := strings.Repeat("response payload byte content. ", 200) // ~6.4KB
	var buf bytes.Buffer
	for range recordCount {
		record := APIAttemptRecord{
			Kind:             attemptRecordKind,
			SchemaVersion:    recordSchemaVersion,
			AttemptID:        mustBenchmarkAttemptID(b),
			AttemptGroupID:   "ag_benchmark",
			AttemptIndex:     1,
			Timestamp:        recordTestTime,
			LatencyMS:        25,
			ProviderInstance: "openai-primary",
			RequestModel:     "gpt-test",
			Request: APIAttemptRequest{
				Method:   "POST",
				Endpoint: "https://provider.test/v1/responses",
				Body:     EncodeBody([]byte(requestBody)),
			},
			Response: &APIAttemptResponse{
				StatusCode:    recordTestInt(200),
				Body:          EncodeBody([]byte(responseBody)),
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
		line, err := json.Marshal(record)
		if err != nil {
			b.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func mustBenchmarkAttemptID(b *testing.B) string {
	b.Helper()
	id, err := identifier.NewAPIAttemptID()
	if err != nil {
		b.Fatal(err)
	}
	return id
}

func decodeAllBenchmark(b *testing.B, data []byte, opts ...DecoderOption) {
	b.Helper()
	decoder := NewDecoder(bytes.NewReader(data), len(data), opts...)
	for {
		if _, err := decoder.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			b.Fatal(err)
		}
	}
}

// BenchmarkDecoderStrict and BenchmarkDecoderMetadataOnly are the informal
// perf guard for the metadata-only decode mode (ws1/task-2): they measure
// full-log decode cost with and without body decode/revalidation over a
// many-record synthetic log. No CI perf gate; ratio recorded in the commit
// message.
func BenchmarkDecoderStrict(b *testing.B) {
	data := benchmarkAPILogFixture(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		decodeAllBenchmark(b, data)
	}
}

func BenchmarkDecoderMetadataOnly(b *testing.B) {
	data := benchmarkAPILogFixture(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		decodeAllBenchmark(b, data, WithMetadataOnly())
	}
}
