package doctor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

func intp(n int) *int { return &n }

func hasIntValue(value *int, want int) bool {
	return value != nil && *value == want
}

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
			StatusCode:    intp(200),
			Body:          apilog.EncodeBody([]byte("{}")),
			Model:         "gpt-5.2-codex",
			FinishReason:  "stop",
			TextLength:    intp(text),
			ToolCallCount: intp(tools),
			Usage: apilog.Usage{
				InputTokens:     intp(input),
				OutputTokens:    intp(output),
				TotalTokens:     intp(input + output),
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
	if !hasIntValue(tot.InputTokens, 81000) || !hasIntValue(tot.OutputTokens, 700) ||
		!hasIntValue(tot.CacheReadTokens, 21000) || !hasIntValue(tot.TotalTokens, 81700) {
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
	if err != nil || len(spikes.Calls) != 1 || !hasIntValue(spikes.Calls[0].UncachedInput, 60000) {
		t.Fatalf("spike filter = %+v, err %v", spikes.Calls, err)
	}
	low, err := APILog(base, sid, APILogOpts{CacheSpikes: true, SpikeThreshold: 500})
	if err != nil || len(low.Calls) != 3 {
		t.Fatalf("low spike filter rows = %d, err %v", len(low.Calls), err)
	}
}

func TestAPILogOmittedCompactCountsAreNotEmptyEvidence(t *testing.T) {
	attempt := doctorAttempt("ag_omitted_counts", 1, apilog.AttemptSuccess, 1, 0, 0, 0, 0, 0)
	attempt.Response.TextLength = nil
	attempt.Response.ToolCallCount = nil
	row := rowFromAttempt(attempt)
	if row.Empty {
		t.Fatal("successful attempt with omitted compact counts was classified as empty")
	}
}

func TestAPILogPreservesOptionalNumericPresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present bool
	}{
		{name: "omitted"},
		{name: "explicit zero", present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			bucket := stateHomeBucket(base, hash1)
			attempt := doctorAttempt("ag_numeric_presence", 1, apilog.AttemptSuccess, 1, 0, 0, 0, 0, 0)
			if tc.present {
				attempt.Response.StatusCode = intp(0)
			} else {
				attempt.Response.StatusCode = nil
				attempt.Response.TextLength = nil
				attempt.Response.ToolCallCount = nil
				attempt.Response.Usage = apilog.Usage{}
			}
			writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt}, schema.SessionMeta{})

			result, err := APILog(base, sidA, APILogOpts{})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				Calls  []map[string]json.RawMessage `json:"calls"`
				Totals map[string]json.RawMessage   `json:"totals"`
			}
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Calls) != 1 {
				t.Fatalf("JSON calls = %d, want 1", len(document.Calls))
			}
			for _, field := range []string{
				"status_code", "input_tokens", "output_tokens", "cache_read_tokens",
				"uncached_input_tokens", "text_length", "tool_call_count",
			} {
				_, got := document.Calls[0][field]
				if got != tc.present {
					t.Errorf("call field %q present = %v, want %v: %s", field, got, tc.present, encoded)
				}
			}
			for _, field := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "total_tokens"} {
				_, got := document.Totals[field]
				if got != tc.present {
					t.Errorf("totals field %q present = %v, want %v: %s", field, got, tc.present, encoded)
				}
			}

			human := RenderAPILog(result, APILogOpts{})
			want := "-"
			if tc.present {
				want = "0"
			}
			for _, column := range []string{"status", "in_tok", "out_tok", "uncached", "txt", "tools"} {
				if got := apilogHumanColumn(t, human, attempt.AttemptID, column); got != want {
					t.Errorf("human %s = %q, want %q\n%s", column, got, want, human)
				}
			}
			wantSummary := "tokens in=- out=- cache_read=- total=-"
			if tc.present {
				wantSummary = "tokens in=0 out=0 cache_read=0 total=0"
			}
			if !strings.Contains(human, wantSummary) {
				t.Errorf("human totals lost numeric presence; want %q\n%s", wantSummary, human)
			}
		})
	}
}

func TestAPILogRetainsLatestBoundedCallRowsAndScansAllTotals(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	const maxCallRows = 100
	const callCount = maxCallRows + 3
	records := make([]apilog.APILogRecord, 0, callCount)
	for i := 0; i < callCount; i++ {
		records = append(records, doctorAttempt(fmt.Sprintf("ag_call_%03d", i), 1, apilog.AttemptSuccess, 1, 1, 2, 0, 1, 0))
	}
	writeRichSession(t, bucket, sidA, nil, records, schema.SessionMeta{})

	result, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != maxCallRows {
		t.Fatalf("retained calls = %d, want %d", len(result.Calls), maxCallRows)
	}
	if result.Calls[0].AttemptGroupID != "ag_call_003" || result.Calls[len(result.Calls)-1].AttemptGroupID != "ag_call_102" {
		t.Fatalf("retained call range = %q..%q, want latest 100", result.Calls[0].AttemptGroupID, result.Calls[len(result.Calls)-1].AttemptGroupID)
	}
	if result.Totals.Calls != callCount {
		t.Fatalf("totals.calls = %d, want all %d scanned calls", result.Totals.Calls, callCount)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if string(document["matching_calls"]) != "103" || string(document["calls_truncated"]) != "true" {
		t.Fatalf("bounded call metadata missing from %s", encoded)
	}
	if human := RenderAPILog(result, APILogOpts{}); !strings.Contains(human, "call_rows=100/103 (latest; truncated)") {
		t.Fatalf("human output omits bounded call evidence:\n%s", human)
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
	if got := res.Calls[0].UncachedInput; !hasIntValue(got, 100) {
		t.Fatalf("uncached_input_tokens = %v, want normalized input_tokens 100", got)
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

func TestAPILogContinuationEndpointFamiliesAreBounded(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	records := make([]apilog.APILogRecord, 0, doctorAPILogMaxEndpointFamilies*2)
	for i := 0; i < doctorAPILogMaxEndpointFamilies*2; i++ {
		attempt := doctorAttempt(fmt.Sprintf("ag_family_%03d", i), 1, apilog.AttemptSuccess, 1, 1, 0, 0, 1, 0)
		attempt.Request.EndpointFamily = fmt.Sprintf("family_%03d", i)
		attempt.Request.HistoryMode = string(llm.HistoryModeResponsesDelta)
		records = append(records, attempt)
	}
	writeRichSession(t, bucket, sidA, nil, records, schema.SessionMeta{})

	result, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Totals.ContinuationByEndpointFamily
	if len(got) != doctorAPILogMaxEndpointFamilies {
		t.Fatalf("continuation endpoint families = %d, want bounded %d", len(got), doctorAPILogMaxEndpointFamilies)
	}
	overflow := got[doctorAPILogOtherEndpointFamily]
	wantOverflow := doctorAPILogMaxEndpointFamilies + 1
	if overflow.ResponsesDelta != wantOverflow {
		t.Fatalf("overflow responses_delta = %d, want %d", overflow.ResponsesDelta, wantOverflow)
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

func TestRenderAPILogKeepsStructuredFailureColumnsSeparate(t *testing.T) {
	attemptID := identifier.MustNewAPIAttemptID()
	result := APILogResult{
		SessionID: sidA,
		Calls: []APICallRow{{
			AttemptID:        attemptID,
			AttemptGroupID:   "ag_error_columns",
			AttemptIndex:     1,
			ProviderInstance: "openai-primary",
			Model:            "gpt-5.2-codex",
			Outcome:          apilog.AttemptProviderTimeout,
			StatusCode:       intp(504),
			ErrorClass:       "provider_timeout",
			SettlementState:  SettlementUnsettled,
		}},
	}

	out := RenderAPILog(result, APILogOpts{})
	if got := apilogHumanColumn(t, out, attemptID, "outcome"); got != string(apilog.AttemptProviderTimeout) {
		t.Fatalf("outcome column = %q, want exact class %q\n%s", got, apilog.AttemptProviderTimeout, out)
	}
	if got := apilogHumanColumn(t, out, attemptID, "status"); got != "504" {
		t.Fatalf("status column = %q, want 504\n%s", got, out)
	}
	if got := apilogHumanColumn(t, out, attemptID, "error_class"); got != "provider_timeout" {
		t.Fatalf("error_class column = %q, want provider_timeout\n%s", got, out)
	}
}

func TestAPILogProjectsStructuredFailureWithoutProviderBodyMessage(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := doctorAttempt("ag_structured_failure", 1, apilog.AttemptProviderReject, 25, 0, 0, 0, 0, 0)
	attempt.ErrorClass = "rate_limit"
	attempt.ErrorMessage = "provider-body-sentinel: quota detail"
	attempt.Response = &apilog.APIAttemptResponse{
		StatusCode: intp(429),
		Body:       apilog.EncodeBody([]byte("provider-body-sentinel")),
	}
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt, doctorSettlement(attempt, 1)}, schema.SessionMeta{})

	result, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(result.Calls))
	}
	row := result.Calls[0]
	if !hasIntValue(row.StatusCode, http.StatusTooManyRequests) || row.ErrorClass != "rate_limit" || row.Outcome != apilog.AttemptProviderReject {
		t.Fatalf("structured failure = status %v class %q outcome %q", row.StatusCode, row.ErrorClass, row.Outcome)
	}
	jsonResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	humanResult := RenderAPILog(result, APILogOpts{})
	for name, output := range map[string]string{"json": string(jsonResult), "human": humanResult} {
		if strings.Contains(output, "provider-body-sentinel") || strings.Contains(output, "quota detail") {
			t.Fatalf("%s doctor output exposed provider body-derived error text: %s", name, output)
		}
	}
	if got := apilogHumanColumn(t, humanResult, attempt.AttemptID, "status"); got != "429" {
		t.Fatalf("status column = %q, want 429\n%s", got, humanResult)
	}
	if got := apilogHumanColumn(t, humanResult, attempt.AttemptID, "error_class"); got != "rate_limit" {
		t.Fatalf("error_class column = %q, want rate_limit\n%s", got, humanResult)
	}
}

func TestAPILogSettlementCollectionIsBoundedAndIndependentOfCallFilters(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	const fillerSettlements = 103
	records := make([]apilog.APILogRecord, 0, fillerSettlements+3)
	for i := 0; i < fillerSettlements; i++ {
		records = append(records, apilog.APIAttemptGroupSettlement{
			Kind:              "attempt_group_settlement",
			SchemaVersion:     1,
			AttemptGroupID:    fmt.Sprintf("ag_filler_%03d", i),
			FinalAttemptCount: 0,
			Outcome:           apilog.AttemptCallerCancel,
			SettledAt:         time.Date(2026, 7, 15, 13, 0, i, 0, time.UTC),
		})
	}
	filteredAttempt := doctorAttempt("ag_filtered_success", 1, apilog.AttemptSuccess, 1, 1, 1, 0, 1, 0)
	filteredSettlement := doctorSettlement(filteredAttempt, 1)
	filteredSettlement.ForensicIncomplete = true
	zeroAttemptSettlement := apilog.APIAttemptGroupSettlement{
		Kind:               "attempt_group_settlement",
		SchemaVersion:      1,
		AttemptGroupID:     "ag_zero_attempt",
		FinalAttemptCount:  0,
		Outcome:            apilog.AttemptCallerCancel,
		ForensicIncomplete: true,
		SettledAt:          time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC),
	}
	records = append(records, filteredAttempt, filteredSettlement, zeroAttemptSettlement)
	writeRichSession(t, bucket, sidA, nil, records, schema.SessionMeta{})

	result, err := APILog(base, sidA, APILogOpts{ErrorsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 0 {
		t.Fatalf("errors filter retained success calls: %+v", result.Calls)
	}
	if result.Settlements.Total != fillerSettlements+2 || !result.Settlements.Truncated || len(result.Settlements.Records) != 100 {
		t.Fatalf("bounded settlements = %+v", result.Settlements)
	}
	wantGroups := make([]string, 0, doctorAPILogMaxSettlements)
	for i := 5; i < fillerSettlements; i++ {
		wantGroups = append(wantGroups, fmt.Sprintf("ag_filler_%03d", i))
	}
	wantGroups = append(wantGroups, "ag_filtered_success", "ag_zero_attempt")
	for i, settlement := range result.Settlements.Records {
		if settlement.AttemptGroupID != wantGroups[i] {
			t.Fatalf("settlement[%d].attempt_group_id = %q, want %q", i, settlement.AttemptGroupID, wantGroups[i])
		}
		if i >= len(wantGroups)-2 && !settlement.ForensicIncomplete {
			t.Errorf("settlement[%d] %s lost forensic_incomplete", i, settlement.AttemptGroupID)
		}
		if settlement.AttemptGroupID == "ag_zero_attempt" && (settlement.FinalAttemptID != "" || settlement.FinalAttemptCount != 0) {
			t.Fatalf("zero-attempt settlement fabricated provider identity: %+v", settlement)
		}
	}
	human := RenderAPILog(result, APILogOpts{ErrorsOnly: true})
	for _, group := range []string{"ag_filtered_success", "ag_zero_attempt"} {
		if !strings.Contains(human, group) {
			t.Errorf("filtered human output erased settlement %s:\n%s", group, human)
		}
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
	end := len(row)
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
