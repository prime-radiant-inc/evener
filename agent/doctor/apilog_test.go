package doctor

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func intp(n int) *int { return &n }

// apilogFixture writes a session with four representative api_calls: a normal
// call, an empty response, a failed call, and a cache-spike (large uncached
// input).
func apilogFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	apiCalls := []transcript.APICall{
		{ // normal: text + a tool call, mostly cached input
			Round:     0,
			LatencyMs: 1200,
			Request:   llm.APILogRequest{Model: "gpt-5.2-codex", Provider: "openai"},
			Response: &llm.APILogResponse{
				FinishReason: "stop", TextLength: 40, ToolCallCount: 1,
				Usage: llm.Usage{InputTokens: 10000, OutputTokens: 200, CacheReadTokens: intp(9000)},
			},
		},
		{ // empty: no text, no tool calls
			Round:     1,
			LatencyMs: 800,
			Request:   llm.APILogRequest{Model: "gpt-5.2-codex", Provider: "openai"},
			Response: &llm.APILogResponse{
				FinishReason: "stop", TextLength: 0, ToolCallCount: 0,
				Usage: llm.Usage{InputTokens: 11000, OutputTokens: 0, CacheReadTokens: intp(11000)},
			},
		},
		{ // error: no response
			Round:     2,
			LatencyMs: 50,
			Request:   llm.APILogRequest{Model: "gpt-5.2-codex", Provider: "openai"},
			Error:     "context deadline exceeded",
		},
		{ // cache spike: large uncached input (60000 - 1000 = 59000)
			Round:     3,
			LatencyMs: 3000,
			Request:   llm.APILogRequest{Model: "gpt-5.2-codex", Provider: "openai"},
			Response: &llm.APILogResponse{
				FinishReason: "stop", TextLength: 100, ToolCallCount: 0,
				Usage: llm.Usage{InputTokens: 60000, OutputTokens: 500, CacheReadTokens: intp(1000)},
			},
		},
	}
	writeRichSession(t, bucket, sid, nil, apiCalls, schema.SessionMeta{})
	return base, sid
}

func TestAPILog_Totals(t *testing.T) {
	base, sid := apilogFixture(t)
	res, err := APILog(base, sid, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	tot := res.Totals
	if tot.Calls != 4 {
		t.Errorf("calls = %d, want 4", tot.Calls)
	}
	if tot.Empties != 1 {
		t.Errorf("empties = %d, want 1", tot.Empties)
	}
	if tot.Errors != 1 {
		t.Errorf("errors = %d, want 1", tot.Errors)
	}
	wantIn := 10000 + 11000 + 0 + 60000
	if tot.InputTokens != wantIn {
		t.Errorf("input tokens = %d, want %d", tot.InputTokens, wantIn)
	}
	wantCache := 9000 + 11000 + 1000
	if tot.CacheReadTokens != wantCache {
		t.Errorf("cache_read = %d, want %d", tot.CacheReadTokens, wantCache)
	}
	if tot.TotalTokens != wantIn+(200+0+0+500) {
		t.Errorf("total tokens = %d, want %d", tot.TotalTokens, wantIn+700)
	}
	wantAvg := int64((1200 + 800 + 50 + 3000) / 4)
	if tot.AvgLatencyMs != wantAvg {
		t.Errorf("avg latency = %d, want %d", tot.AvgLatencyMs, wantAvg)
	}
	if len(res.Calls) != 4 {
		t.Errorf("rows = %d, want 4 (no filter)", len(res.Calls))
	}
}

func TestAPILogContinuationCountsByEndpointFamily(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	apiCalls := []transcript.APICall{
		{
			Round: 0,
			Request: llm.APILogRequest{
				Model:          "gpt-5.4",
				Provider:       "openai",
				HistoryMode:    llm.HistoryModeResponsesDelta,
				EndpointFamily: "openai_public",
			},
			Response: &llm.APILogResponse{Usage: llm.Usage{InputTokens: 10}},
		},
		{
			Round: 1,
			Request: llm.APILogRequest{
				Model:          "gpt-5.4",
				Provider:       "openai",
				HistoryMode:    llm.HistoryModeFullHistory,
				EndpointFamily: "openai_public",
			},
			Response: &llm.APILogResponse{Usage: llm.Usage{InputTokens: 20}},
		},
		{
			Round: 2,
			Request: llm.APILogRequest{
				Model:          "gpt-5.4",
				Provider:       "openai",
				HistoryMode:    llm.HistoryModeFullHistoryFallback,
				EndpointFamily: "openai_public",
			},
			Response: &llm.APILogResponse{Usage: llm.Usage{InputTokens: 30}},
		},
	}
	writeRichSession(t, bucket, sid, nil, apiCalls, schema.SessionMeta{})

	res, err := APILog(base, sid, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Totals.ContinuationByEndpointFamily["openai_public"]
	if got.ResponsesDelta != 1 ||
		got.FullHistory != 1 ||
		got.FullHistoryFallback != 1 {
		t.Fatalf("openai_public counts = %+v", got)
	}
}

func TestAPILog_EmptyFilter(t *testing.T) {
	base, sid := apilogFixture(t)
	res, err := APILog(base, sid, APILogOpts{EmptyOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Calls) != 1 {
		t.Fatalf("empty rows = %d, want 1", len(res.Calls))
	}
	if res.Calls[0].Round != 1 || !res.Calls[0].Empty {
		t.Errorf("empty filter returned wrong row: %+v", res.Calls[0])
	}
	// Totals still span the whole session.
	if res.Totals.Calls != 4 {
		t.Errorf("totals.calls = %d, want 4 even under filter", res.Totals.Calls)
	}
}

func TestAPILog_ErrorsFilter(t *testing.T) {
	base, sid := apilogFixture(t)
	res, err := APILog(base, sid, APILogOpts{ErrorsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Calls) != 1 || res.Calls[0].Error == "" {
		t.Fatalf("errors filter = %+v, want 1 error row", res.Calls)
	}
}

func TestAPILog_CacheSpikes(t *testing.T) {
	base, sid := apilogFixture(t)
	// Default threshold (50000) catches only the round-3 spike (59000 uncached).
	res, err := APILog(base, sid, APILogOpts{CacheSpikes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Calls) != 1 || res.Calls[0].Round != 3 {
		t.Fatalf("default-threshold spikes = %+v, want only round 3", res.Calls)
	}
	if res.Calls[0].UncachedInput != 59000 {
		t.Errorf("uncached input = %d, want 59000", res.Calls[0].UncachedInput)
	}
	// A low threshold catches the normal call too (1000 uncached).
	low, err := APILog(base, sid, APILogOpts{CacheSpikes: true, SpikeThreshold: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(low.Calls) != 2 {
		t.Errorf("low-threshold spikes = %d, want 2", len(low.Calls))
	}
}

func TestRenderAPILog_SummaryOnly(t *testing.T) {
	base, sid := apilogFixture(t)
	res, err := APILog(base, sid, APILogOpts{SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderAPILog(res, APILogOpts{SummaryOnly: true})
	if !strings.Contains(out, "calls=4") || !strings.Contains(out, "errors=1") {
		t.Errorf("summary missing aggregate line:\n%s", out)
	}
	if strings.Contains(out, "round") {
		t.Errorf("summary-only should not render the per-call table:\n%s", out)
	}
}
