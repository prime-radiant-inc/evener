package doctor

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// APILogOpts selects which calls to display. Filters narrow the rows shown; the
// totals always reflect the whole session.
type APILogOpts struct {
	EmptyOnly   bool // only responses with no text and no tool calls
	ErrorsOnly  bool // only failed calls
	CacheSpikes bool // only calls whose uncached input >= SpikeThreshold
	// SpikeThreshold is the uncached-input-token floor for CacheSpikes. Zero
	// means use the default (defaultSpikeThreshold).
	SpikeThreshold int
	SummaryOnly    bool // render only the per-session aggregate
}

const defaultSpikeThreshold = 50000

// APICallRow is one LLM round's key metrics, flattened from transcript.APICall
// for legible per-call output.
type APICallRow struct {
	Round         int    `json:"round"`
	Model         string `json:"model"`
	Provider      string `json:"provider,omitempty"`
	LatencyMs     int64  `json:"latency_ms"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	CacheRead     int    `json:"cache_read_tokens"`
	UncachedInput int    `json:"uncached_input_tokens"`
	FinishReason  string `json:"finish_reason,omitempty"`
	TextLength    int    `json:"text_length"`
	ToolCalls     int    `json:"tool_call_count"`
	Empty         bool   `json:"empty"`
	Error         string `json:"error,omitempty"`
}

// APILogTotals aggregates every call in the session, regardless of row filter.
type APILogTotals struct {
	Calls                        int                                      `json:"calls"`
	Empties                      int                                      `json:"empties"`
	Errors                       int                                      `json:"errors"`
	InputTokens                  int                                      `json:"input_tokens"`
	OutputTokens                 int                                      `json:"output_tokens"`
	CacheReadTokens              int                                      `json:"cache_read_tokens"`
	TotalTokens                  int                                      `json:"total_tokens"`
	AvgLatencyMs                 int64                                    `json:"avg_latency_ms"`
	ContinuationByEndpointFamily map[string]ContinuationHistoryModeCounts `json:"continuation_by_endpoint_family,omitempty"`
}

type ContinuationHistoryModeCounts struct {
	ResponsesDelta      int `json:"responses_delta,omitempty"`
	FullHistory         int `json:"full_history,omitempty"`
	FullHistoryFallback int `json:"full_history_fallback,omitempty"`
}

// APILogResult is the apilog command's output: the (filtered) per-call rows and
// the whole-session totals.
type APILogResult struct {
	SessionID string       `json:"session_id"`
	Calls     []APICallRow `json:"calls"`
	Totals    APILogTotals `json:"totals"`
}

// APILog reads a session's api_call lines and reports per-call metrics plus a
// session aggregate — empties, errors, cache spikes, token spend. The data is
// the same in-transcript api_call snapshot the runtime writes, parsed through
// serf's own transcript.APICall type.
func APILog(stateBase, selector string, opts APILogOpts) (APILogResult, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return APILogResult{}, err
	}
	doc, err := loadTranscript(paths.TranscriptPath)
	if err != nil {
		return APILogResult{}, err
	}
	res := APILogResult{SessionID: paths.SessionID}
	threshold := opts.SpikeThreshold
	if threshold <= 0 {
		threshold = defaultSpikeThreshold
	}
	var latencySum int64
	for _, line := range doc.apiLines {
		var call transcript.APICall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			continue // diagnostic data; never load-bearing
		}
		row := rowFromCall(call)
		// Totals span the whole session, before any display filter.
		res.Totals.Calls++
		res.Totals.InputTokens += row.InputTokens
		res.Totals.OutputTokens += row.OutputTokens
		res.Totals.CacheReadTokens += row.CacheRead
		latencySum += row.LatencyMs
		if row.Error != "" {
			res.Totals.Errors++
		}
		if row.Empty {
			res.Totals.Empties++
		}
		recordContinuationHistoryMode(&res.Totals, call.Request.EndpointFamily, call.Request.HistoryMode)
		if !rowMatchesFilter(row, opts, threshold) {
			continue
		}
		res.Calls = append(res.Calls, row)
	}
	res.Totals.TotalTokens = res.Totals.InputTokens + res.Totals.OutputTokens
	if res.Totals.Calls > 0 {
		res.Totals.AvgLatencyMs = latencySum / int64(res.Totals.Calls)
	}
	return res, nil
}

func recordContinuationHistoryMode(totals *APILogTotals, endpointFamily string, mode llm.HistoryMode) {
	endpointFamily = strings.TrimSpace(endpointFamily)
	if endpointFamily == "" {
		return
	}
	counts := totals.ContinuationByEndpointFamily
	if counts == nil {
		counts = map[string]ContinuationHistoryModeCounts{}
		totals.ContinuationByEndpointFamily = counts
	}
	next := counts[endpointFamily]
	switch mode {
	case llm.HistoryModeResponsesDelta:
		next.ResponsesDelta++
	case llm.HistoryModeFullHistory:
		next.FullHistory++
	case llm.HistoryModeFullHistoryFallback:
		next.FullHistoryFallback++
	default:
		return
	}
	counts[endpointFamily] = next
}

func rowFromCall(call transcript.APICall) APICallRow {
	row := APICallRow{
		Round:     call.Round,
		Model:     call.Request.Model,
		Provider:  call.Request.Provider,
		LatencyMs: call.LatencyMs,
		Error:     call.Error,
	}
	if call.Response != nil {
		row.FinishReason = call.Response.FinishReason
		row.TextLength = call.Response.TextLength
		row.ToolCalls = call.Response.ToolCallCount
		u := call.Response.Usage
		row.InputTokens = u.InputTokens
		row.OutputTokens = u.OutputTokens
		if u.CacheReadTokens != nil {
			row.CacheRead = *u.CacheReadTokens
		}
		// A call that returned neither text nor a tool call did no useful work.
		row.Empty = row.TextLength == 0 && row.ToolCalls == 0
	}
	row.UncachedInput = row.InputTokens - row.CacheRead
	return row
}

func rowMatchesFilter(row APICallRow, opts APILogOpts, threshold int) bool {
	if opts.EmptyOnly && !row.Empty {
		return false
	}
	if opts.ErrorsOnly && row.Error == "" {
		return false
	}
	if opts.CacheSpikes && row.UncachedInput < threshold {
		return false
	}
	return true
}

// RenderAPILog renders the human summary: a totals block, then a per-call table
// unless SummaryOnly was requested.
func RenderAPILog(r APILogResult, opts APILogOpts) string {
	var b strings.Builder
	t := r.Totals
	fmt.Fprintf(&b, "session %s\n", r.SessionID)
	fmt.Fprintf(&b, "calls=%d empties=%d errors=%d  tokens in=%d out=%d cache_read=%d total=%d  avg_latency=%dms\n",
		t.Calls, t.Empties, t.Errors, t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.TotalTokens, t.AvgLatencyMs)
	if opts.SummaryOnly {
		return b.String()
	}
	if len(r.Calls) == 0 {
		fmt.Fprintln(&b, "(no calls match)")
		return b.String()
	}
	fmt.Fprintf(&b, "%-5s %-22s %8s %8s %8s %9s %-14s %6s %5s\n",
		"round", "model", "latency", "in_tok", "out_tok", "uncached", "finish", "txt", "tools")
	for _, c := range r.Calls {
		finish := c.FinishReason
		if c.Error != "" {
			finish = "ERROR: " + oneLine(c.Error)
		} else if c.Empty {
			finish = "(empty) " + finish
		}
		fmt.Fprintf(&b, "%-5d %-22s %7dms %8d %8d %9d %-14s %6d %5d\n",
			c.Round, truncate(c.Model, 22), c.LatencyMs, c.InputTokens, c.OutputTokens,
			c.UncachedInput, truncate(finish, 14), c.TextLength, c.ToolCalls)
	}
	return b.String()
}
