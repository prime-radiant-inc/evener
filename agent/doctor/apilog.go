package doctor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

// APILogOpts selects which calls to display. Filters narrow the rows shown; the
// totals always reflect the whole session.
type APILogOpts struct {
	EmptyOnly   bool
	ErrorsOnly  bool
	CacheSpikes bool
	// SpikeThreshold is the uncached-input-token floor for CacheSpikes. Zero
	// means use the default (defaultSpikeThreshold).
	SpikeThreshold int
	SummaryOnly    bool
}

const defaultSpikeThreshold = 50000
const doctorAPILogMaxLineBytes = 128 << 20

const (
	SettlementSettled             = "settled"
	SettlementUnsettled           = "unsettled"
	SettlementUnknownOutsideRange = "unknown_outside_range"
)

// APICallRow is one canonical provider attempt's metrics and durable identity.
type APICallRow struct {
	AttemptID         string                     `json:"attempt_id"`
	AttemptGroupID    string                     `json:"attempt_group_id"`
	AttemptIndex      int                        `json:"attempt_index"`
	ProviderInstance  string                     `json:"provider_instance"`
	Model             string                     `json:"model"`
	LatencyMs         int64                      `json:"latency_ms"`
	InputTokens       int                        `json:"input_tokens"`
	OutputTokens      int                        `json:"output_tokens"`
	CacheRead         int                        `json:"cache_read_tokens"`
	UncachedInput     int                        `json:"uncached_input_tokens"`
	FinishReason      string                     `json:"finish_reason,omitempty"`
	TextLength        int                        `json:"text_length"`
	ToolCalls         int                        `json:"tool_call_count"`
	Empty             bool                       `json:"empty"`
	Outcome           apilog.AttemptOutcomeClass `json:"outcome"`
	Error             string                     `json:"error,omitempty"`
	Final             bool                       `json:"final"`
	SettlementState   string                     `json:"settlement_state"`
	FinalAttemptCount *int                       `json:"final_attempt_count,omitempty"`
}

// APILogTotals aggregates every attempt in the session, regardless of row filter.
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

type APILogResult struct {
	SessionID string       `json:"session_id"`
	Calls     []APICallRow `json:"calls"`
	Totals    APILogTotals `json:"totals"`
}

// APILog decodes the private canonical API log and owns only its diagnostic
// projection. Provider bodies and headers never enter the result.
func APILog(stateBase, selector string, opts APILogOpts) (APILogResult, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return APILogResult{}, err
	}
	f, err := os.Open(paths.APILogPath)
	if err != nil {
		return APILogResult{}, fmt.Errorf("open API log %s: %w", paths.APILogPath, err)
	}
	defer func() { _ = f.Close() }()

	res := APILogResult{SessionID: paths.SessionID}
	decoder := apilog.NewDecoder(f, doctorAPILogMaxLineBytes)
	settlements := map[string]apilog.APIAttemptGroupSettlement{}
	partialTail := false
	var latencySum int64
	for {
		record, decodeErr := decoder.Next()
		if errors.Is(decodeErr, io.EOF) {
			break
		}
		if errors.Is(decodeErr, apilog.ErrPartialTail) {
			partialTail = true
			break
		}
		if decodeErr != nil {
			return APILogResult{}, fmt.Errorf("decode API log %s: %w", paths.APILogPath, decodeErr)
		}
		switch record := record.(type) {
		case apilog.APIAttemptRecord:
			row := rowFromAttempt(record)
			res.Calls = append(res.Calls, row)
			res.Totals.Calls++
			res.Totals.InputTokens += row.InputTokens
			res.Totals.OutputTokens += row.OutputTokens
			res.Totals.CacheReadTokens += row.CacheRead
			latencySum += row.LatencyMs
			if row.Outcome != apilog.AttemptSuccess {
				res.Totals.Errors++
			}
			if row.Empty {
				res.Totals.Empties++
			}
			recordContinuationHistoryMode(&res.Totals, record.Request.EndpointFamily, llm.HistoryMode(record.Request.HistoryMode))
		case apilog.APIAttemptGroupSettlement:
			settlements[record.AttemptGroupID] = record
		}
	}
	res.Totals.TotalTokens = res.Totals.InputTokens + res.Totals.OutputTokens
	if res.Totals.Calls > 0 {
		res.Totals.AvgLatencyMs = latencySum / int64(res.Totals.Calls)
	}

	threshold := opts.SpikeThreshold
	if threshold <= 0 {
		threshold = defaultSpikeThreshold
	}
	filtered := res.Calls[:0]
	for i := range res.Calls {
		row := &res.Calls[i]
		if settlement, ok := settlements[row.AttemptGroupID]; ok {
			count := settlement.FinalAttemptCount
			row.FinalAttemptCount = &count
			row.Final = row.AttemptID == settlement.FinalAttemptID
			row.SettlementState = SettlementSettled
		} else if partialTail {
			row.SettlementState = SettlementUnknownOutsideRange
		} else {
			row.SettlementState = SettlementUnsettled
		}
		if rowMatchesFilter(*row, opts, threshold) {
			filtered = append(filtered, *row)
		}
	}
	res.Calls = filtered
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

func rowFromAttempt(attempt apilog.APIAttemptRecord) APICallRow {
	row := APICallRow{
		AttemptID:        attempt.AttemptID,
		AttemptGroupID:   attempt.AttemptGroupID,
		AttemptIndex:     attempt.AttemptIndex,
		ProviderInstance: attempt.ProviderInstance,
		Model:            attempt.RequestModel,
		LatencyMs:        attempt.LatencyMS,
		Outcome:          attempt.Outcome,
		Error:            attempt.ErrorMessage,
	}
	if row.Error == "" && row.Outcome != apilog.AttemptSuccess {
		row.Error = string(row.Outcome)
	}
	if attempt.Response != nil {
		row.FinishReason = attempt.Response.FinishReason
		row.TextLength = attempt.Response.TextLength
		row.ToolCalls = attempt.Response.ToolCallCount
		row.InputTokens = attempt.Response.Usage.InputTokens
		row.OutputTokens = attempt.Response.Usage.OutputTokens
		if attempt.Response.Usage.CacheReadTokens != nil {
			row.CacheRead = *attempt.Response.Usage.CacheReadTokens
		}
		row.Empty = attempt.Outcome == apilog.AttemptSuccess && row.TextLength == 0 && row.ToolCalls == 0
	}
	// llm.Usage.InputTokens is already normalized to uncached input by the
	// provider adapter. CacheRead is reported separately and must not be
	// subtracted a second time.
	row.UncachedInput = row.InputTokens
	return row
}

func rowMatchesFilter(row APICallRow, opts APILogOpts, threshold int) bool {
	if opts.EmptyOnly && !row.Empty {
		return false
	}
	if opts.ErrorsOnly && row.Outcome == apilog.AttemptSuccess {
		return false
	}
	if opts.CacheSpikes && row.UncachedInput < threshold {
		return false
	}
	return true
}

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
	fmt.Fprintf(&b, "%-26s %-26s %-8s %-18s %-18s %-25s %-7s %-24s %-19s %8s %8s %8s %9s %6s %-5s %s\n",
		"attempt_id", "attempt_group_id", "index", "provider", "model", "outcome", "empty", "settlement", "final_attempt_count", "latency", "in_tok", "out_tok", "uncached", "txt", "tools", "error")
	for _, c := range r.Calls {
		settlement := c.SettlementState
		if c.Final {
			settlement += " final"
		}
		finalAttemptCount := "-"
		if c.FinalAttemptCount != nil {
			finalAttemptCount = fmt.Sprintf("%d", *c.FinalAttemptCount)
		}
		fmt.Fprintf(&b, "%-26s %-26s %-8d %-18s %-18s %-25s %-7t %-24s %-19s %7dms %8d %8d %9d %6d %-5d %s\n",
			truncate(c.AttemptID, 26), truncate(c.AttemptGroupID, 26), c.AttemptIndex, truncate(c.ProviderInstance, 18),
			truncate(c.Model, 18), c.Outcome, c.Empty, truncate(settlement, 24), finalAttemptCount, c.LatencyMs,
			c.InputTokens, c.OutputTokens, c.UncachedInput, c.TextLength, c.ToolCalls, humanAPILogError(c.Error))
	}
	return b.String()
}

func humanAPILogError(message string) string {
	message = strings.Map(func(r rune) rune {
		if r == '\x1b' {
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		return "-"
	}
	runes := []rune(message)
	if len(runes) > 48 {
		return string(runes[:48]) + "…"
	}
	return message
}
