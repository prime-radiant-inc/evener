package doctor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
const doctorAPILogMaxCalls = 100
const doctorAPILogMaxSettlements = 100
const doctorAPILogMaxEndpointFamilies = 16
const doctorAPILogOtherEndpointFamily = "other"

const (
	SettlementSettled             = "settled"
	SettlementUnsettled           = "unsettled"
	SettlementUnknownOutsideRange = "unknown_outside_range"
)

// APICallRow is one canonical provider attempt's metrics and durable identity.
type APICallRow struct {
	AttemptID          string                     `json:"attempt_id"`
	AttemptGroupID     string                     `json:"attempt_group_id"`
	AttemptIndex       int                        `json:"attempt_index"`
	ProviderInstance   string                     `json:"provider_instance"`
	Model              string                     `json:"model"`
	LatencyMs          int64                      `json:"latency_ms"`
	InputTokens        *int                       `json:"input_tokens,omitempty"`
	OutputTokens       *int                       `json:"output_tokens,omitempty"`
	CacheRead          *int                       `json:"cache_read_tokens,omitempty"`
	UncachedInput      *int                       `json:"uncached_input_tokens,omitempty"`
	FinishReason       string                     `json:"finish_reason,omitempty"`
	TextLength         *int                       `json:"text_length,omitempty"`
	ToolCalls          *int                       `json:"tool_call_count,omitempty"`
	Empty              bool                       `json:"empty"`
	Outcome            apilog.AttemptOutcomeClass `json:"outcome"`
	StatusCode         *int                       `json:"status_code,omitempty"`
	ErrorClass         string                     `json:"error_class,omitempty"`
	Final              bool                       `json:"final"`
	SettlementState    string                     `json:"settlement_state"`
	FinalAttemptCount  *int                       `json:"final_attempt_count,omitempty"`
	ForensicIncomplete bool                       `json:"forensic_incomplete"`
}

// APIGroupSettlementRow is one outer model-call settlement. It remains
// independent of provider-attempt filters so zero-attempt and incomplete
// groups stay observable without fabricating a call row.
type APIGroupSettlementRow struct {
	AttemptGroupID     string                     `json:"attempt_group_id"`
	FinalAttemptID     string                     `json:"final_attempt_id"`
	FinalAttemptCount  int                        `json:"final_attempt_count"`
	Outcome            apilog.AttemptOutcomeClass `json:"outcome"`
	ForensicIncomplete bool                       `json:"forensic_incomplete"`
	SettledAt          time.Time                  `json:"settled_at"`
}

type APIGroupSettlements struct {
	Records   []APIGroupSettlementRow `json:"records"`
	Total     int                     `json:"total"`
	Truncated bool                    `json:"truncated"`
}

// APILogTotals aggregates available evidence from every attempt in the session,
// regardless of row filter. Optional totals remain absent when the log contains
// no corresponding provider evidence.
type APILogTotals struct {
	Calls                        int                                      `json:"calls"`
	Empties                      int                                      `json:"empties"`
	Errors                       int                                      `json:"errors"`
	InputTokens                  *int                                     `json:"input_tokens,omitempty"`
	OutputTokens                 *int                                     `json:"output_tokens,omitempty"`
	CacheReadTokens              *int                                     `json:"cache_read_tokens,omitempty"`
	TotalTokens                  *int                                     `json:"total_tokens,omitempty"`
	AvgLatencyMs                 int64                                    `json:"avg_latency_ms"`
	ContinuationByEndpointFamily map[string]ContinuationHistoryModeCounts `json:"continuation_by_endpoint_family,omitempty"`
}

type ContinuationHistoryModeCounts struct {
	ResponsesDelta      int `json:"responses_delta,omitempty"`
	FullHistory         int `json:"full_history,omitempty"`
	FullHistoryFallback int `json:"full_history_fallback,omitempty"`
}

type APILogResult struct {
	SessionID      string              `json:"session_id"`
	Calls          []APICallRow        `json:"calls"`
	MatchingCalls  int                 `json:"matching_calls"`
	CallsTruncated bool                `json:"calls_truncated"`
	Settlements    APIGroupSettlements `json:"settlements"`
	Totals         APILogTotals        `json:"totals"`
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
	threshold := opts.SpikeThreshold
	if threshold <= 0 {
		threshold = defaultSpikeThreshold
	}
	var retainedCalls apiCallRetention
	var retainedSettlements apiGroupSettlementRetention
	partialTail := false
	var latencySum int64
	var inputTokens, outputTokens, cacheReadTokens, totalTokens optionalIntSum
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
			res.Totals.Calls++
			inputTokens.add(row.InputTokens)
			outputTokens.add(row.OutputTokens)
			cacheReadTokens.add(row.CacheRead)
			if record.Response != nil {
				totalTokens.add(record.Response.Usage.TotalTokens)
			}
			latencySum += row.LatencyMs
			if row.Outcome != apilog.AttemptSuccess {
				res.Totals.Errors++
			}
			if row.Empty {
				res.Totals.Empties++
			}
			recordContinuationHistoryMode(&res.Totals, record.Request.EndpointFamily, llm.HistoryMode(record.Request.HistoryMode))
			if rowMatchesFilter(row, opts, threshold) {
				retainedCalls.add(row)
			}
		case apilog.APIAttemptGroupSettlement:
			retainedCalls.settle(record)
			retainedSettlements.add(record)
		}
	}
	res.Calls, res.MatchingCalls, res.CallsTruncated = retainedCalls.result(partialTail)
	res.Settlements = retainedSettlements.result()
	res.Totals.InputTokens = inputTokens.result()
	res.Totals.OutputTokens = outputTokens.result()
	res.Totals.CacheReadTokens = cacheReadTokens.result()
	res.Totals.TotalTokens = totalTokens.result()
	if res.Totals.Calls > 0 {
		res.Totals.AvgLatencyMs = latencySum / int64(res.Totals.Calls)
	}
	return res, nil
}

type optionalIntSum struct {
	total int
	seen  bool
}

func (s *optionalIntSum) add(value *int) {
	if value == nil {
		return
	}
	s.total += *value
	s.seen = true
}

func (s optionalIntSum) result() *int {
	if !s.seen {
		return nil
	}
	value := s.total
	return &value
}

type apiCallRetention struct {
	records []APICallRow
	total   int
	next    int
}

func (r *apiCallRetention) add(row APICallRow) {
	r.total++
	if len(r.records) < doctorAPILogMaxCalls {
		r.records = append(r.records, row)
		return
	}
	r.records[r.next] = row
	r.next = (r.next + 1) % doctorAPILogMaxCalls
}

func (r *apiCallRetention) settle(settlement apilog.APIAttemptGroupSettlement) {
	for i := range r.records {
		row := &r.records[i]
		if row.AttemptGroupID != settlement.AttemptGroupID {
			continue
		}
		count := settlement.FinalAttemptCount
		row.FinalAttemptCount = &count
		row.Final = row.AttemptID == settlement.FinalAttemptID
		row.SettlementState = SettlementSettled
		row.ForensicIncomplete = settlement.ForensicIncomplete
	}
}

func (r *apiCallRetention) result(partialTail bool) ([]APICallRow, int, bool) {
	records := append([]APICallRow(nil), r.records...)
	if len(records) == doctorAPILogMaxCalls && r.next > 0 {
		ordered := make([]APICallRow, 0, doctorAPILogMaxCalls)
		ordered = append(ordered, records[r.next:]...)
		ordered = append(ordered, records[:r.next]...)
		records = ordered
	}
	for i := range records {
		if records[i].SettlementState != "" {
			continue
		}
		if partialTail {
			records[i].SettlementState = SettlementUnknownOutsideRange
		} else {
			records[i].SettlementState = SettlementUnsettled
		}
	}
	return records, r.total, r.total > len(records)
}

type apiGroupSettlementRetention struct {
	records []APIGroupSettlementRow
	total   int
	next    int
}

func (r *apiGroupSettlementRetention) add(settlement apilog.APIAttemptGroupSettlement) {
	row := APIGroupSettlementRow{
		AttemptGroupID:     settlement.AttemptGroupID,
		FinalAttemptID:     settlement.FinalAttemptID,
		FinalAttemptCount:  settlement.FinalAttemptCount,
		Outcome:            settlement.Outcome,
		ForensicIncomplete: settlement.ForensicIncomplete,
		SettledAt:          settlement.SettledAt,
	}
	r.total++
	if len(r.records) < doctorAPILogMaxSettlements {
		r.records = append(r.records, row)
		return
	}
	r.records[r.next] = row
	r.next = (r.next + 1) % doctorAPILogMaxSettlements
}

func (r *apiGroupSettlementRetention) result() APIGroupSettlements {
	records := append([]APIGroupSettlementRow(nil), r.records...)
	if len(records) == doctorAPILogMaxSettlements && r.next > 0 {
		ordered := make([]APIGroupSettlementRow, 0, doctorAPILogMaxSettlements)
		ordered = append(ordered, records[r.next:]...)
		ordered = append(ordered, records[:r.next]...)
		records = ordered
	}
	return APIGroupSettlements{
		Records:   records,
		Total:     r.total,
		Truncated: r.total > len(records),
	}
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
	if _, exists := counts[endpointFamily]; !exists && len(counts) >= doctorAPILogMaxEndpointFamilies-1 {
		endpointFamily = doctorAPILogOtherEndpointFamily
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
		ErrorClass:       attempt.ErrorClass,
	}
	if attempt.Response != nil {
		row.StatusCode = attempt.Response.StatusCode
		row.FinishReason = attempt.Response.FinishReason
		row.TextLength = attempt.Response.TextLength
		row.ToolCalls = attempt.Response.ToolCallCount
		row.InputTokens = attempt.Response.Usage.InputTokens
		row.OutputTokens = attempt.Response.Usage.OutputTokens
		row.CacheRead = attempt.Response.Usage.CacheReadTokens
		row.Empty = attempt.Outcome == apilog.AttemptSuccess &&
			row.TextLength != nil && *row.TextLength == 0 &&
			row.ToolCalls != nil && *row.ToolCalls == 0
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
	if opts.CacheSpikes && (row.UncachedInput == nil || *row.UncachedInput < threshold) {
		return false
	}
	return true
}

func RenderAPILog(r APILogResult, opts APILogOpts) string {
	var b strings.Builder
	t := r.Totals
	fmt.Fprintf(&b, "session %s\n", r.SessionID)
	fmt.Fprintf(&b, "calls=%d empties=%d errors=%d  tokens in=%s out=%s cache_read=%s total=%s  avg_latency=%dms\n",
		t.Calls, t.Empties, t.Errors, optionalIntString(t.InputTokens), optionalIntString(t.OutputTokens),
		optionalIntString(t.CacheReadTokens), optionalIntString(t.TotalTokens), t.AvgLatencyMs)
	if opts.SummaryOnly {
		return b.String()
	}
	if len(r.Calls) == 0 {
		fmt.Fprintln(&b, "(no calls match)")
	} else {
		fmt.Fprintf(&b, "%-26s %-26s %-8s %-18s %-18s %-25s %6s %-24s %-7s %-24s %-19s %-19s %8s %8s %8s %9s %6s %-5s\n",
			"attempt_id", "attempt_group_id", "index", "provider", "model", "outcome", "status", "error_class", "empty", "settlement", "final_attempt_count", "forensic_incomplete", "latency", "in_tok", "out_tok", "uncached", "txt", "tools")
		for _, c := range r.Calls {
			settlement := c.SettlementState
			if c.Final {
				settlement += " final"
			}
			finalAttemptCount := "-"
			if c.FinalAttemptCount != nil {
				finalAttemptCount = strconv.Itoa(*c.FinalAttemptCount)
			}
			status := optionalIntString(c.StatusCode)
			errorClass := c.ErrorClass
			if errorClass == "" {
				errorClass = "-"
			}
			fmt.Fprintf(&b, "%-26s %-26s %-8d %-18s %-18s %-25s %6s %-24s %-7t %-24s %-19s %-19t %7dms %8s %8s %9s %6s %-5s\n",
				truncate(c.AttemptID, 26), truncate(c.AttemptGroupID, 26), c.AttemptIndex, truncate(c.ProviderInstance, 18),
				truncate(c.Model, 18), c.Outcome, status, truncate(errorClass, 24), c.Empty, truncate(settlement, 24), finalAttemptCount,
				c.ForensicIncomplete, c.LatencyMs, optionalIntString(c.InputTokens), optionalIntString(c.OutputTokens),
				optionalIntString(c.UncachedInput), optionalIntString(c.TextLength), optionalIntString(c.ToolCalls))
		}
	}
	fmt.Fprintf(&b, "call_rows=%d/%d", len(r.Calls), r.MatchingCalls)
	if r.CallsTruncated {
		fmt.Fprint(&b, " (latest; truncated)")
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "settlements=%d/%d", len(r.Settlements.Records), r.Settlements.Total)
	if r.Settlements.Truncated {
		fmt.Fprint(&b, " (latest; truncated)")
	}
	fmt.Fprintln(&b)
	if len(r.Settlements.Records) == 0 {
		fmt.Fprintln(&b, "(no settlements)")
		return b.String()
	}
	fmt.Fprintf(&b, "%-26s %-26s %-19s %-25s %-19s %s\n",
		"attempt_group_id", "final_attempt_id", "final_attempt_count", "outcome", "forensic_incomplete", "settled_at")
	for _, settlement := range r.Settlements.Records {
		finalAttemptID := settlement.FinalAttemptID
		if finalAttemptID == "" {
			finalAttemptID = "-"
		}
		fmt.Fprintf(&b, "%-26s %-26s %-19d %-25s %-19t %s\n",
			truncate(settlement.AttemptGroupID, 26), truncate(finalAttemptID, 26), settlement.FinalAttemptCount,
			settlement.Outcome, settlement.ForensicIncomplete, settlement.SettledAt.Format(time.RFC3339Nano))
	}
	return b.String()
}

func optionalIntString(value *int) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}
