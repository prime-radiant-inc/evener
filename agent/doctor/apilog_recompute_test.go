package doctor

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm/apilog"
)

// syntheticZeroedResponsesSSE mirrors the affected Responses-API wire shape
// pinned by llm/providers/openai/responses_recording_test.go and
// responses_recompute_test.go: a function_call and a text message item
// arrive via response.output_item.done events, but the terminal
// response.completed payload's "output" is empty. Pre-fix, this is exactly
// the stored body shape whose TextLength/ToolCalls were recorded as zero.
func syntheticZeroedResponsesSSE() string {
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: " + event + "\ndata: " + data + "\n\n")
	}
	write("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`)
	write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
	write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"path\":\"x\"}"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
	write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Hello"}]}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	return b.String()
}

// zeroedResponsesAttempt builds one successful api_attempt record recorded
// as empty (TextLength=0, ToolCalls=0) whose stored response body is
// syntheticZeroedResponsesSSE -- the historical-bug shape --recompute exists
// to re-extract.
func zeroedResponsesAttempt(group string) apilog.APIAttemptRecord {
	return apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   group,
		AttemptIndex:     1,
		Timestamp:        time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		LatencyMS:        900,
		ProviderInstance: "openai-primary",
		RequestModel:     "gpt-5.2",
		Request: apilog.APIAttemptRequest{
			Method:         "POST",
			Endpoint:       "https://provider.test/v1/responses",
			Body:           apilog.EncodeBody([]byte("{}")),
			Model:          "gpt-5.2",
			EndpointFamily: "openai_public",
		},
		Outcome: apilog.AttemptSuccess,
		Response: &apilog.APIAttemptResponse{
			StatusCode:    intp(200),
			Body:          apilog.EncodeBody([]byte(syntheticZeroedResponsesSSE())),
			Model:         "gpt-5.2",
			FinishReason:  "stop",
			TextLength:    intp(0),
			ToolCallCount: intp(0),
			Usage: apilog.Usage{
				InputTokens:  intp(1),
				OutputTokens: intp(2),
				TotalTokens:  intp(3),
			},
		},
	}
}

func TestAPILogRecomputeReExtractsZeroedResponsesRecord(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := zeroedResponsesAttempt("ag_zeroed")
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt, doctorSettlement(attempt, 1)}, schema.SessionMeta{})

	// Without --recompute, the row and totals report the recorded zeros
	// verbatim; recomputation must not run unbidden.
	plain, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(plain.Calls))
	}
	if plain.Calls[0].RecomputedTextLength != nil || plain.Calls[0].RecomputedToolCalls != nil {
		t.Fatalf("recomputed fields set without --recompute: %+v", plain.Calls[0])
	}
	if plain.Totals.RecomputedNonEmpty != 0 {
		t.Fatalf("recomputed_nonempty = %d without --recompute, want 0", plain.Totals.RecomputedNonEmpty)
	}

	result, err := APILog(base, sidA, APILogOpts{Recompute: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(result.Calls))
	}
	row := result.Calls[0]
	if row.TextLength == nil || *row.TextLength != 0 || row.ToolCalls == nil || *row.ToolCalls != 0 {
		t.Fatalf("recorded counts changed by --recompute: text=%v tools=%v", row.TextLength, row.ToolCalls)
	}
	if row.RecomputedTextLength == nil || *row.RecomputedTextLength != len("Hello") {
		t.Fatalf("recomputed text length = %v, want %d", row.RecomputedTextLength, len("Hello"))
	}
	if row.RecomputedToolCalls == nil || *row.RecomputedToolCalls != 1 {
		t.Fatalf("recomputed tool calls = %v, want 1", row.RecomputedToolCalls)
	}
	if result.Totals.RecomputedNonEmpty != 1 {
		t.Fatalf("totals.recomputed_nonempty = %d, want 1", result.Totals.RecomputedNonEmpty)
	}

	human := RenderAPILog(result, APILogOpts{Recompute: true})
	if !strings.Contains(human, "recomputed_nonempty=1") {
		t.Fatalf("summary omits recomputed_nonempty=1:\n%s", human)
	}
	if got := apilogHumanColumn(t, human, attempt.AttemptID, "recomputed_txt"); got != "5" {
		t.Fatalf("recomputed_txt column = %q, want %q\n%s", got, "5", human)
	}
	if got := apilogHumanColumn(t, human, attempt.AttemptID, "recomputed_tools"); got != "1" {
		t.Fatalf("recomputed_tools column = %q, want %q\n%s", got, "1", human)
	}
}
