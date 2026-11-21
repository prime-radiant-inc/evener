package doctor

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm/apilog"
)

// syntheticZeroedResponsesSSE mirrors the affected Responses-API wire shape
// pinned by llm/providers/responses/recompute_test.go: a function_call and
// a text message item
// arrive via response.output_item.done events, but the terminal
// response.completed payload's "output" is empty. Pre-fix, this is exactly
// the stored body shape whose TextLength/ToolCalls were recorded as zero.
func syntheticZeroedResponsesSSE() string {
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.WriteString(data)
		b.WriteString("\n\n")
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
			StatusCode:    new(200),
			Body:          apilog.EncodeBody([]byte(syntheticZeroedResponsesSSE())),
			Model:         "gpt-5.2",
			FinishReason:  "stop",
			TextLength:    new(0),
			ToolCallCount: new(0),
			Usage: apilog.Usage{
				InputTokens:  new(1),
				OutputTokens: new(2),
				TotalTokens:  new(3),
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
	plain, err := APILog(context.Background(), base, sidA, APILogOpts{})
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

	result, err := APILog(context.Background(), base, sidA, APILogOpts{Recompute: true})
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

// zeroedCompatChatAttempt builds a successful api_attempt recorded as empty
// under the retired openai_compatible_chat_completions family, whose stored
// body is an ordinary Chat Completions JSON response. Records like this are
// what --recompute exists for: they were written before the settlement fix
// and no new record will ever carry that family again.
func zeroedCompatChatAttempt(group string) apilog.APIAttemptRecord {
	const body = `{"id":"chatcmpl_1","model":"glm-5","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Hello"}}]}`
	return apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   group,
		AttemptIndex:     1,
		Timestamp:        time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		LatencyMS:        700,
		ProviderInstance: "compat-primary",
		RequestModel:     "glm-5",
		Request: apilog.APIAttemptRequest{
			Method:         "POST",
			Endpoint:       "https://provider.test/v1/chat/completions",
			Body:           apilog.EncodeBody([]byte("{}")),
			Model:          "glm-5",
			EndpointFamily: "openai_compatible_chat_completions",
		},
		Outcome: apilog.AttemptSuccess,
		Response: &apilog.APIAttemptResponse{
			StatusCode:    new(200),
			Body:          apilog.EncodeBody([]byte(body)),
			Model:         "glm-5",
			FinishReason:  "stop",
			TextLength:    new(0),
			ToolCallCount: new(0),
		},
	}
}

// TestAPILogRecomputeReExtractsRetiredCompatChatRecord pins that reading old
// data still works after the flag day: the registry emits one chat family
// now, but a log written before it carries the retired
// openai_compatible_chat_completions family over the same wire shape, and
// the merged chatcompletions extractor decodes it unchanged.
func TestAPILogRecomputeReExtractsRetiredCompatChatRecord(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := zeroedCompatChatAttempt("ag_zeroed_compat")
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt, doctorSettlement(attempt, 1)}, schema.SessionMeta{})

	result, err := APILog(context.Background(), base, sidA, APILogOpts{Recompute: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(result.Calls))
	}
	row := result.Calls[0]
	if row.RecomputedTextLength == nil || *row.RecomputedTextLength != len("Hello") {
		t.Fatalf("recomputed text length = %v, want %d", row.RecomputedTextLength, len("Hello"))
	}
	if result.Totals.RecomputedNonEmpty != 1 {
		t.Fatalf("totals.recomputed_nonempty = %d, want 1", result.Totals.RecomputedNonEmpty)
	}
}

func zeroedGoogleAttempt(group, family string, body string) apilog.APIAttemptRecord {
	return apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   group,
		AttemptIndex:     1,
		Timestamp:        time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		LatencyMS:        500,
		ProviderInstance: "google",
		RequestModel:     "gemini-4-neon",
		Request: apilog.APIAttemptRequest{
			Method:         "POST",
			Endpoint:       "https://generativelanguage.googleapis.com/v1beta/interactions",
			Body:           apilog.EncodeBody([]byte("{}")),
			Model:          "gemini-4-neon",
			EndpointFamily: family,
		},
		Outcome: apilog.AttemptSuccess,
		Response: &apilog.APIAttemptResponse{
			StatusCode:    new(200),
			Body:          apilog.EncodeBody([]byte(body)),
			Model:         "gemini-4-neon",
			FinishReason:  "stop",
			TextLength:    new(0),
			ToolCallCount: new(0),
		},
	}
}

func TestAPILogRecompute_GoogleInteractionsAndMixedHistoricalSkip(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)

	interactionsJSON := `{"id":"int_1","model":"gemini-4-neon","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"Hello"}]},{"type":"function_call","id":"fc_1","name":"read_file","arguments":{"path":"a.go"}}]}`
	interactionsSSE := "event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\n" +
		"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"World\"}}\n\n" +
		"event: step.stop\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\n" +
		"event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"int_2\",\"status\":\"completed\"}}\n\n"
	legacyJSON := `{"candidates":[{"content":{"parts":[{"text":"legacy"}]}}]}`
	legacySSE := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"legacy\"}]}}]}\n\n"
	misfiledChoices := `{"id":"c1","choices":[{"message":{"content":"wrong"}}]}`
	misfiledCandidates := `{"candidates":[{"content":{"parts":[{"text":"wrong"}]}}]}`

	a1 := zeroedGoogleAttempt("ag_1", "google_interactions", interactionsJSON)
	a2 := zeroedGoogleAttempt("ag_2", "google_interactions_stream", interactionsSSE)
	a3 := zeroedGoogleAttempt("ag_3", "google_generate_content", legacyJSON)
	a4 := zeroedGoogleAttempt("ag_4", "google_stream_generate_content", legacySSE)
	a5 := zeroedGoogleAttempt("ag_5", "google_interactions", misfiledChoices)
	a6 := zeroedGoogleAttempt("ag_6", "google_interactions", misfiledCandidates)

	records := []apilog.APILogRecord{
		a1, doctorSettlement(a1, 1),
		a2, doctorSettlement(a2, 2),
		a3, doctorSettlement(a3, 3),
		a4, doctorSettlement(a4, 4),
		a5, doctorSettlement(a5, 5),
		a6, doctorSettlement(a6, 6),
	}
	writeRichSession(t, bucket, sidA, nil, records, schema.SessionMeta{})

	result, err := APILog(context.Background(), base, sidA, APILogOpts{Recompute: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 6 {
		t.Fatalf("calls = %d, want 6", len(result.Calls))
	}
	if result.Calls[0].RecomputedTextLength == nil || *result.Calls[0].RecomputedTextLength != len("Hello") ||
		result.Calls[0].RecomputedToolCalls == nil || *result.Calls[0].RecomputedToolCalls != 1 {
		t.Fatalf("google_interactions JSON row = %+v", result.Calls[0])
	}
	if result.Calls[1].RecomputedTextLength == nil || *result.Calls[1].RecomputedTextLength != len("World") ||
		result.Calls[1].RecomputedToolCalls == nil || *result.Calls[1].RecomputedToolCalls != 0 {
		t.Fatalf("google_interactions_stream SSE row = %+v", result.Calls[1])
	}
	for i := 2; i < 6; i++ {
		if result.Calls[i].RecomputedTextLength != nil || result.Calls[i].RecomputedToolCalls != nil {
			t.Fatalf("row %d (%s) should not recompute: %+v", i, records[i*2].(apilog.APIAttemptRecord).Request.EndpointFamily, result.Calls[i])
		}
	}
	if result.Totals.RecomputedNonEmpty != 2 {
		t.Fatalf("recomputed_nonempty = %d, want 2", result.Totals.RecomputedNonEmpty)
	}
}
