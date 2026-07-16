package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm/apilog"
)

func intp(n int) *int { return &n }

func doctorAttempt(group string, index int, outcome apilog.AttemptOutcomeClass, latency int64, input, output, cache, text, tools int) apilog.APIAttemptRecord {
	attempt := apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   group,
		AttemptIndex:     index,
		Timestamp:        time.Date(2026, 7, 15, 12, index, 0, 0, time.UTC),
		LatencyMS:        latency,
		ProviderInstance: "openai-primary",
		RequestModel:     "gpt-5.2-codex",
		Request: apilog.APIAttemptRequest{
			Method:         "POST",
			Endpoint:       "https://provider.test/v1/responses",
			Body:           apilog.EncodeBody([]byte("{}")),
			Model:          "gpt-5.2-codex",
			EndpointFamily: "openai_public",
		},
		Outcome: outcome,
	}
	if outcome == apilog.AttemptSuccess {
		attempt.Response = &apilog.APIAttemptResponse{
			StatusCode:    200,
			Body:          apilog.EncodeBody([]byte("{}")),
			Model:         "gpt-5.2-codex",
			FinishReason:  "stop",
			TextLength:    text,
			ToolCallCount: tools,
			Usage: apilog.Usage{
				InputTokens:     input,
				OutputTokens:    output,
				TotalTokens:     input + output,
				CacheReadTokens: intp(cache),
			},
		}
	} else {
		attempt.ErrorClass = "context_deadline"
		attempt.ErrorMessage = "context deadline exceeded"
	}
	return attempt
}

func doctorSettlement(attempt apilog.APIAttemptRecord, count int) apilog.APIAttemptGroupSettlement {
	return apilog.APIAttemptGroupSettlement{
		Kind:              "attempt_group_settlement",
		SchemaVersion:     1,
		AttemptGroupID:    attempt.AttemptGroupID,
		FinalAttemptID:    attempt.AttemptID,
		FinalAttemptCount: count,
		Outcome:           attempt.Outcome,
		SettledAt:         attempt.Timestamp.Add(time.Second),
	}
}

func apilogFixture(t *testing.T) (base, sid string, attempts []apilog.APIAttemptRecord) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	attempts = []apilog.APIAttemptRecord{
		doctorAttempt("ag_normal", 1, apilog.AttemptSuccess, 1200, 10000, 200, 9000, 40, 1),
		doctorAttempt("ag_empty", 1, apilog.AttemptSuccess, 800, 11000, 0, 11000, 0, 0),
		doctorAttempt("ag_error", 1, apilog.AttemptProviderTimeout, 50, 0, 0, 0, 0, 0),
		doctorAttempt("ag_spike", 1, apilog.AttemptSuccess, 3000, 60000, 500, 1000, 100, 0),
	}
	records := make([]apilog.APILogRecord, 0, len(attempts)*2)
	for _, attempt := range attempts {
		records = append(records, attempt, doctorSettlement(attempt, 1))
	}
	writeRichSession(t, bucket, sid, nil, records, schema.SessionMeta{})
	return base, sid, attempts
}

func TestAPILogCanonicalTotalsFiltersAndSettlementIdentity(t *testing.T) {
	base, sid, attempts := apilogFixture(t)
	res, err := APILog(base, sid, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	tot := res.Totals
	if tot.Calls != 4 || tot.Empties != 1 || tot.Errors != 1 {
		t.Fatalf("calls/empties/errors = %d/%d/%d, want 4/1/1", tot.Calls, tot.Empties, tot.Errors)
	}
	if tot.InputTokens != 81000 || tot.OutputTokens != 700 || tot.CacheReadTokens != 21000 || tot.TotalTokens != 81700 {
		t.Fatalf("token totals = %+v", tot)
	}
	if tot.AvgLatencyMs != 1262 {
		t.Fatalf("average latency = %d, want 1262", tot.AvgLatencyMs)
	}
	if len(res.Calls) != 4 {
		t.Fatalf("rows = %d, want 4", len(res.Calls))
	}
	first := res.Calls[0]
	if first.AttemptID != attempts[0].AttemptID || first.AttemptGroupID != "ag_normal" || first.AttemptIndex != 1 {
		t.Fatalf("attempt identity = %+v", first)
	}
	if first.ProviderInstance != "openai-primary" || first.Outcome != apilog.AttemptSuccess {
		t.Fatalf("provider/outcome = %q/%q", first.ProviderInstance, first.Outcome)
	}
	if !first.Final || first.SettlementState != SettlementSettled || first.FinalAttemptCount == nil || *first.FinalAttemptCount != 1 {
		t.Fatalf("finality = final %v state %q count %v", first.Final, first.SettlementState, first.FinalAttemptCount)
	}

	empty, err := APILog(base, sid, APILogOpts{EmptyOnly: true})
	if err != nil || len(empty.Calls) != 1 || !empty.Calls[0].Empty {
		t.Fatalf("empty filter = %+v, err %v", empty.Calls, err)
	}
	failures, err := APILog(base, sid, APILogOpts{ErrorsOnly: true})
	if err != nil || len(failures.Calls) != 1 || failures.Calls[0].Outcome != apilog.AttemptProviderTimeout {
		t.Fatalf("error filter = %+v, err %v", failures.Calls, err)
	}
	spikes, err := APILog(base, sid, APILogOpts{CacheSpikes: true})
	if err != nil || len(spikes.Calls) != 1 || spikes.Calls[0].UncachedInput != 60000 {
		t.Fatalf("spike filter = %+v, err %v", spikes.Calls, err)
	}
	low, err := APILog(base, sid, APILogOpts{CacheSpikes: true, SpikeThreshold: 500})
	if err != nil || len(low.Calls) != 3 {
		t.Fatalf("low spike filter rows = %d, err %v", len(low.Calls), err)
	}
}

func TestAPILogCacheSpikesUseNormalizedUncachedInput(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := doctorAttempt("ag_normalized", 1, apilog.AttemptSuccess, 1, 100, 0, 90, 1, 0)
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt, doctorSettlement(attempt, 1)}, schema.SessionMeta{})

	res, err := APILog(base, sidA, APILogOpts{CacheSpikes: true, SpikeThreshold: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Calls) != 1 {
		t.Fatalf("cache spike rows = %d, want 1: InputTokens is already normalized uncached input", len(res.Calls))
	}
	if got := res.Calls[0].UncachedInput; got != 100 {
		t.Fatalf("uncached_input_tokens = %d, want normalized input_tokens 100", got)
	}
}

func TestAPILogContinuationCountsByEndpointFamily(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	modes := []string{"responses_delta", "full_history", "full_history_fallback"}
	var records []apilog.APILogRecord
	for i, mode := range modes {
		attempt := doctorAttempt("ag_mode_"+mode, 1, apilog.AttemptSuccess, 1, 10*(i+1), 0, 0, 1, 0)
		attempt.Request.HistoryMode = mode
		records = append(records, attempt, doctorSettlement(attempt, 1))
	}
	writeRichSession(t, bucket, sidA, nil, records, schema.SessionMeta{})
	res, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Totals.ContinuationByEndpointFamily["openai_public"]
	if got.ResponsesDelta != 1 || got.FullHistory != 1 || got.FullHistoryFallback != 1 {
		t.Fatalf("openai_public counts = %+v", got)
	}
}

func TestAPILogCleanEOFMakesMissingSettlementUnsettled(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := doctorAttempt("ag_crash", 1, apilog.AttemptSuccess, 1, 1, 1, 0, 1, 0)
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt}, schema.SessionMeta{})

	res, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	row := res.Calls[0]
	if row.SettlementState != SettlementUnsettled || row.Final || row.FinalAttemptCount != nil {
		t.Fatalf("unsettled row = %+v", row)
	}
}

func TestAPILogPartialTailMakesUnsettledGroupUnknown(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := doctorAttempt("ag_partial", 1, apilog.AttemptSuccess, 1, 1, 1, 0, 1, 0)
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt}, schema.SessionMeta{})
	path := filepath.Join(bucket, "sessions", sidA+".api.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"kind\":\"attempt_group_settlement\""); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	row := res.Calls[0]
	if row.SettlementState != SettlementUnknownOutsideRange || row.Final || row.FinalAttemptCount != nil {
		t.Fatalf("partial-tail row = %+v", row)
	}
}

func TestAPILogInteriorCorruptionIsError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := doctorAttempt("ag_corrupt", 1, apilog.AttemptSuccess, 1, 1, 1, 0, 1, 0)
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt}, schema.SessionMeta{})
	path := filepath.Join(bucket, "sessions", sidA+".api.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{bad json}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := APILog(base, sidA, APILogOpts{}); err == nil {
		t.Fatal("APILog accepted interior corruption")
	}
}

func TestRenderAPILogSummaryAndIdentity(t *testing.T) {
	base, sid, attempts := apilogFixture(t)
	res, err := APILog(base, sid, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	summary := RenderAPILog(res, APILogOpts{SummaryOnly: true})
	if !strings.Contains(summary, "calls=4") || strings.Contains(summary, "attempt_id") {
		t.Fatalf("summary output = %q", summary)
	}
	out := RenderAPILog(res, APILogOpts{})
	if !strings.Contains(out, "attempt_id") || !strings.Contains(out, "settled") {
		t.Fatalf("detailed output missing canonical identity/finality:\n%s", out)
	}
	if got := apilogHumanColumn(t, out, attempts[0].AttemptID, "attempt_group_id"); got != "ag_normal" {
		t.Fatalf("attempt_group_id column = %q, want ag_normal\n%s", got, out)
	}
	if got := apilogHumanColumn(t, out, attempts[0].AttemptID, "final_attempt_count"); got != "1" {
		t.Fatalf("final_attempt_count column = %q, want 1\n%s", got, out)
	}

	unsettled := RenderAPILog(APILogResult{
		SessionID: sid,
		Calls: []APICallRow{{
			AttemptID:       attempts[0].AttemptID,
			AttemptGroupID:  "ag_unsettled",
			AttemptIndex:    1,
			SettlementState: SettlementUnsettled,
		}},
	}, APILogOpts{})
	if got := apilogHumanColumn(t, unsettled, attempts[0].AttemptID, "final_attempt_count"); got != "-" {
		t.Fatalf("unsettled final_attempt_count column = %q, want -\n%s", got, unsettled)
	}
}

func apilogHumanColumn(t *testing.T, output, attemptID, column string) string {
	t.Helper()
	lines := strings.Split(output, "\n")
	var header, row string
	for _, line := range lines {
		if strings.HasPrefix(line, "attempt_id ") {
			header = line
		}
		if strings.HasPrefix(line, attemptID+" ") {
			row = line
		}
	}
	if header == "" {
		t.Fatalf("API-log output has no table header:\n%s", output)
	}
	if row == "" {
		t.Fatalf("API-log output has no row for %s:\n%s", attemptID, output)
	}
	start := strings.Index(header, column)
	if start < 0 {
		t.Fatalf("API-log table has no %s column:\n%s", column, output)
	}
	end := len(header)
	for _, field := range strings.Fields(header) {
		fieldStart := strings.Index(header, field)
		if fieldStart > start && fieldStart < end {
			end = fieldStart
		}
	}
	if start >= len(row) {
		return ""
	}
	if end > len(row) {
		end = len(row)
	}
	return strings.TrimSpace(row[start:end])
}
