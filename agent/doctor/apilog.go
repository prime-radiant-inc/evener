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
	"primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm/providers/openaicompat"
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
	// Recompute re-extracts text/tool-call counts from stored response
	// bodies for rows whose recorded TextLength and ToolCalls are both zero
	// (historical records from before the accumulated-item settlement fix
	// -- see llm/providers/openai.ExtractRecordedResponse). It reads bodies
	// on demand for those rows only; it does not change the decoder mode
	// APILog otherwise uses for the whole log.
	Recompute bool
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
	// RecomputedTextLength/RecomputedToolCalls hold --recompute's
	// re-extracted counts for a row whose recorded TextLength and ToolCalls
	// were both zero. They are set only when recomputation ran and produced
	// a value for this row (regardless of whether that value is itself
	// zero) -- their presence, not their value, is what distinguishes
	// "recomputed and still empty" from "not attempted".
	RecomputedTextLength *int `json:"recomputed_text_length,omitempty"`
	RecomputedToolCalls  *int `json:"recomputed_tool_calls,omitempty"`
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
	// RecomputedNonEmpty counts --recompute rows (across the whole session,
	// not just retained/displayed rows) whose re-extraction found nonzero
	// text or tool calls where the recorded counts were both zero. Zero
	// when --recompute was not requested.
	RecomputedNonEmpty int `json:"recomputed_nonempty,omitempty"`
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
//
// Summarization only reads scalar fields (model, tokens, TextLength, ...),
// never provider body content, so it decodes metadata-only to avoid paying
// for base64 body decode/revalidation on large logs. ValidateAPILog (the
// --validate path) keeps the strict default.
func APILog(stateBase, selector string, opts APILogOpts) (APILogResult, error) {
	return apiLog(stateBase, selector, opts, apilog.DecodeMetadataOnly)
}

// apiLog is APILog's implementation, parameterized on decode mode so tests
// can pin that metadata-only summarization is identical to strict
// summarization over the same log.
func apiLog(stateBase, selector string, opts APILogOpts, mode apilog.DecodeMode) (APILogResult, error) {
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
	var decoderOpts []apilog.DecoderOption
	if mode == apilog.DecodeMetadataOnly {
		decoderOpts = append(decoderOpts, apilog.WithMetadataOnly())
	}
	decoder := apilog.NewDecoder(f, doctorAPILogMaxLineBytes, decoderOpts...)
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
			if opts.Recompute && recomputeRow(&row, record) {
				res.Totals.RecomputedNonEmpty++
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

// apiHealthRetryStormThreshold is the attempt-group size ("this many
// attempts before it's providers or budget worth inspecting, not routine
// retry") that RetryStormGroups counts against -- matching the plan's own
// "attempt groups with >=3 attempts" definition.
const apiHealthRetryStormThreshold = 3

const (
	apiErrorClassQuota     = "quota"
	apiErrorClassPermanent = "permanent"
	apiErrorClassRetryable = "retryable"
)

// apiHealthRecordedEmptyCaveat documents why RecordedEmpty may not be the
// final word: it counts a call as empty from the compact counts recorded at
// call time (text_length==0 && tool_call_count==0), not from a re-decode of
// the response body. WS1's `--recompute` (still unmerged as of this task)
// will add a `recomputed_nonempty` figure derived from the raw body instead
// -- see docs/superpowers/plans/2026-08-06-ws1-responses-recording.md. This
// field is emitted unconditionally rather than gated behind a WS1 check so
// the caveat is always visible next to the count it qualifies.
const apiHealthRecordedEmptyCaveat = "recorded_empty reflects the compact counts (text_length/tool_call_count) recorded at call time, not a re-decode of the response body; WS1's --recompute (docs/superpowers/plans/2026-08-06-ws1-responses-recording.md, not yet merged) will add a recomputed_nonempty figure alongside this one"

// APIHealthResult is apilog --health's one-line verdict: every attempt group
// in a session's whole API log (never truncated the way APILog's row/
// settlement caps are -- there is nothing here to page through) reduced to
// the counts a batch study needs to decide "does this session's provider
// traffic deserve a closer look."
type APIHealthResult struct {
	SessionID string `json:"session_id"`

	// Attempts is the total provider-attempt count across the whole log.
	Attempts int `json:"attempts"`

	// RecordedEmpty is the count of successful attempts recorded as empty
	// (no text, no tool calls) -- see RecordedEmptyCaveat.
	RecordedEmpty       int    `json:"recorded_empty"`
	RecordedEmptyCaveat string `json:"recorded_empty_caveat"`

	// RetryStormGroups is the count of attempt groups with
	// apiHealthRetryStormThreshold or more recorded attempts, whether or
	// not the group ever settled.
	RetryStormGroups int `json:"retry_storm_groups"`
	// UnsettledGroups is the count of attempt groups with no
	// attempt_group_settlement record anywhere in the log. A partial-tail
	// EOF (an in-flight append) can inflate this by one for whichever group
	// was mid-write -- routine, not a defect; see APILog's SettlementState
	// handling of the same condition for the row-level analogue.
	UnsettledGroups int `json:"unsettled_groups"`

	// ErrorsByClass buckets every non-success attempt into one of three
	// retry-disposition classes -- see classifyAPIErrorClass's doc comment
	// for the recorded-field mapping and its judgment calls. Always carries
	// all three keys ("quota", "permanent", "retryable"), zero or not, so a
	// consumer never has to guess whether an absent key means zero or
	// "not computed."
	ErrorsByClass map[string]int `json:"errors_by_class"`
}

// apiHealthGroup is one attempt group's running tally while APIHealth scans
// the log once, independent of the row/settlement retention caps APILog
// applies for interactive display.
type apiHealthGroup struct {
	attempts int
	settled  bool
}

// APIHealth computes APIHealthResult for one session: a single pass over the
// whole canonical API log, independent of APILog's row/settlement caps so a
// batch study's verdict never silently drops evidence from a long session.
func APIHealth(stateBase, selector string) (APIHealthResult, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return APIHealthResult{}, err
	}
	f, err := os.Open(paths.APILogPath)
	if err != nil {
		return APIHealthResult{}, fmt.Errorf("open API log %s: %w", paths.APILogPath, err)
	}
	defer func() { _ = f.Close() }()

	res := APIHealthResult{
		SessionID:           paths.SessionID,
		RecordedEmptyCaveat: apiHealthRecordedEmptyCaveat,
		ErrorsByClass: map[string]int{
			apiErrorClassQuota:     0,
			apiErrorClassPermanent: 0,
			apiErrorClassRetryable: 0,
		},
	}
	groups := map[string]*apiHealthGroup{}
	groupFor := func(id string) *apiHealthGroup {
		g := groups[id]
		if g == nil {
			g = &apiHealthGroup{}
			groups[id] = g
		}
		return g
	}

	decoder := apilog.NewDecoder(f, doctorAPILogMaxLineBytes)
	for {
		record, decodeErr := decoder.Next()
		if errors.Is(decodeErr, io.EOF) {
			break
		}
		if errors.Is(decodeErr, apilog.ErrPartialTail) {
			break
		}
		if decodeErr != nil {
			return APIHealthResult{}, fmt.Errorf("decode API log %s: %w", paths.APILogPath, decodeErr)
		}
		switch record := record.(type) {
		case apilog.APIAttemptRecord:
			res.Attempts++
			groupFor(record.AttemptGroupID).attempts++
			row := rowFromAttempt(record)
			if row.Empty {
				res.RecordedEmpty++
			}
			if row.Outcome != apilog.AttemptSuccess {
				res.ErrorsByClass[classifyAPIErrorClass(row.Outcome, row.StatusCode, row.ErrorClass)]++
			}
		case apilog.APIAttemptGroupSettlement:
			groupFor(record.AttemptGroupID).settled = true
		}
	}

	for _, g := range groups {
		if g.attempts >= apiHealthRetryStormThreshold {
			res.RetryStormGroups++
		}
		if !g.settled {
			res.UnsettledGroups++
		}
	}
	return res, nil
}

// classifyAPIErrorClass maps one attempt's *recorded* fields -- outcome,
// status_code, and error_class (despite the field name, this is
// llm.Kind(err).String(), the category axis, not llm.ErrorClass's
// retry-disposition axis -- see APIAttemptRecord.ErrorClass and
// llm/errorkind.go's doc comment) -- to one of three retry-disposition
// buckets. It mirrors llm.Classify's status-code/Kind table (llm/classify.go)
// over durable evidence instead of a live error value, and documents where
// that substitution is a judgment call rather than an exact reconstruction:
//
//   - quota: llm.Classify reaches this only via a typed *quotaExceededError
//     match (llm/errors.go) that inspects the response BODY for
//     provider-specific quota signals -- evidence the apilog schema does
//     not persist. The transport layer's own recorded-field fallback
//     (explicitAPIAttemptErrorClass,
//     llm/providers/internal/transport/api_attempt.go) never distinguishes
//     a quota-exhausted 429 from an ordinary rate-limit 429: both land as
//     status_code=429, error_class="rate_limit". So this bucket is reachable
//     today only via an explicit error_class=="quota_exceeded"
//     (llm.KindQuotaExceeded.String()) -- forward-compatible with a future
//     logging fix, but always zero against today's real logs. This is
//     deliberate: guessing quota from the error message's text would
//     misclassify ordinary rate limits as quota.
//   - permanent: outcome==caller_cancellation (mirrors Classify's
//     context.Canceled/*AbortError branch), or status_code in
//     {400,401,403,404,413,422}, or -- when no status_code is recorded --
//     error_class in {invalid_request, authentication, access_denied,
//     not_found, context_length, content_filter}.
//   - retryable: status_code in {408,429,500,502,503,504}, or -- when no
//     status_code is recorded -- error_class in {timeout, rate_limit,
//     server}, or anything else. This default (not permanent) mirrors
//     Classify's own conservative fallback for an unclassified error.
func classifyAPIErrorClass(outcome apilog.AttemptOutcomeClass, statusCode *int, errorClass string) string {
	if errorClass == llm.KindQuotaExceeded.String() {
		return apiErrorClassQuota
	}
	if outcome == apilog.AttemptCallerCancel {
		return apiErrorClassPermanent
	}
	if statusCode != nil {
		switch *statusCode {
		case 400, 401, 403, 404, 413, 422:
			return apiErrorClassPermanent
		case 408, 429, 500, 502, 503, 504:
			return apiErrorClassRetryable
		}
	}
	switch errorClass {
	case llm.KindInvalidRequest.String(), llm.KindAuthentication.String(), llm.KindAccessDenied.String(),
		llm.KindNotFound.String(), llm.KindContextLength.String(), llm.KindContentFilter.String():
		return apiErrorClassPermanent
	case llm.KindTimeout.String(), llm.KindRateLimit.String(), llm.KindServer.String():
		return apiErrorClassRetryable
	}
	return apiErrorClassRetryable
}

// RenderAPIHealth renders an APIHealthResult as a single line: the compact
// verdict a batch study scans across many sessions at once.
func RenderAPIHealth(r APIHealthResult) string {
	return fmt.Sprintf("session %s: attempts=%d recorded_empty=%d retry_storm_groups=%d unsettled_groups=%d errors_by_class(quota=%d permanent=%d retryable=%d)\n",
		r.SessionID, r.Attempts, r.RecordedEmpty, r.RetryStormGroups, r.UnsettledGroups,
		r.ErrorsByClass[apiErrorClassQuota], r.ErrorsByClass[apiErrorClassPermanent], r.ErrorsByClass[apiErrorClassRetryable])
}

// doctorAPILogValidationMaxProblems bounds retained validation problems, like
// doctorAPILogMaxCalls/doctorAPILogMaxSettlements bound APILog's rows. Unlike
// those (which retain the latest activity for live debugging), validation
// retains the FIRST N problems: for a corruption scan the earliest trouble is
// the most diagnostic, and everything after it may just be cascade.
const doctorAPILogValidationMaxProblems = 100

// APILogValidationIssue is one complete or interior record that Next rejected
// during whole-history validation: corrupt bytes, a malformed shape, an
// oversized record, or an unsupported record kind. Offset and Line pinpoint
// it in the file; Message is the decoder's own error text verbatim (it
// already carries the offset/line plus the specific defect, so this avoids
// re-deriving or hand-parsing a second description of the same failure).
type APILogValidationIssue struct {
	Offset  int64  `json:"offset"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// APILogPartialTail reports a final record fragment that hit EOF before a
// terminating newline. It is not a defect: an in-flight append can leave
// exactly this shape. Its presence means whole-history validation could not
// reach a clean EOF -- it does not, by itself, mean anything is wrong, and it
// is never counted as a Problem.
type APILogPartialTail struct {
	Offset int64 `json:"offset"`
	Line   int   `json:"line"`
}

// APILogValidationResult is the whole-history structural-integrity outcome:
// every complete record from offset zero through clean EOF (or a trailing
// partial fragment) was strictly decoded with apilog.Decoder.
type APILogValidationResult struct {
	SessionID         string                  `json:"session_id"`
	APILogPath        string                  `json:"api_log_path"`
	FileSize          int64                   `json:"file_size"`
	RecordsOK         int                     `json:"records_ok"`
	Problems          []APILogValidationIssue `json:"problems"`
	ProblemCount      int                     `json:"problem_count"`
	ProblemsTruncated bool                    `json:"problems_truncated"`
	PartialTail       *APILogPartialTail      `json:"partial_tail,omitempty"`
	// Clean is true iff ProblemCount is zero. A bare partial tail does not
	// clear it: that is routine (an in-flight append), not corruption -- see
	// APILogPartialTail.
	Clean bool `json:"clean"`
}

// ValidateAPILog strictly decodes every complete record in a session's
// canonical API log from offset zero through clean EOF, reusing
// apilog.Decoder as the sole decode authority -- no second decoder. Unlike
// APILog (which stops at the first decode error), this scan never stops
// early: it keeps decoding past a corrupt/malformed/oversized/unsupported
// record and reports every one it finds, each with its byte offset, instead
// of surfacing only the first. This is explicit operator diagnostics, so the
// scan is allowed to be proportional to file size; it is never run at logger
// open (see llm/apilog.ScanRecovery for the bounded open-time recovery scan
// this deliberately does not replace).
func ValidateAPILog(stateBase, selector string) (APILogValidationResult, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return APILogValidationResult{}, err
	}
	f, err := os.Open(paths.APILogPath)
	if err != nil {
		return APILogValidationResult{}, fmt.Errorf("open API log %s: %w", paths.APILogPath, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return APILogValidationResult{}, fmt.Errorf("stat API log %s: %w", paths.APILogPath, err)
	}

	res := APILogValidationResult{
		SessionID:  paths.SessionID,
		APILogPath: paths.APILogPath,
		FileSize:   info.Size(),
	}
	decoder := apilog.NewDecoder(f, doctorAPILogMaxLineBytes)
	for {
		_, decodeErr := decoder.Next()
		if errors.Is(decodeErr, io.EOF) {
			break
		}
		if errors.Is(decodeErr, apilog.ErrPartialTail) {
			res.PartialTail = &APILogPartialTail{Offset: decoder.RecordOffset(), Line: decoder.RecordLine()}
			break
		}
		if decodeErr != nil {
			res.ProblemCount++
			if len(res.Problems) < doctorAPILogValidationMaxProblems {
				res.Problems = append(res.Problems, APILogValidationIssue{
					Offset:  decoder.RecordOffset(),
					Line:    decoder.RecordLine(),
					Message: decodeErr.Error(),
				})
			}
			continue
		}
		res.RecordsOK++
	}
	res.ProblemsTruncated = res.ProblemCount > len(res.Problems)
	res.Clean = res.ProblemCount == 0
	return res, nil
}

// RenderAPILogValidation renders a whole-history validation result as fixed
// human text: a summary line, a problems table (offset/line/message) when any
// were found, a truncation footer when the retained list was capped, a
// partial-tail note when the file ended mid-record, and a final clean/dirty
// line.
func RenderAPILogValidation(r APILogValidationResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s\n", r.SessionID)
	fmt.Fprintf(&b, "api log %s\n", r.APILogPath)
	fmt.Fprintf(&b, "file_size=%d records_ok=%d problems=%d\n", r.FileSize, r.RecordsOK, r.ProblemCount)
	if len(r.Problems) == 0 {
		fmt.Fprintln(&b, "(no structural problems)")
	} else {
		fmt.Fprintf(&b, "%-12s %-6s %s\n", "offset", "line", "message")
		for _, p := range r.Problems {
			fmt.Fprintf(&b, "%-12d %-6d %s\n", p.Offset, p.Line, p.Message)
		}
	}
	if r.ProblemsTruncated {
		fmt.Fprintf(&b, "problems=%d/%d (earliest; truncated)\n", len(r.Problems), r.ProblemCount)
	}
	if r.PartialTail != nil {
		fmt.Fprintf(&b, "partial tail at offset %d (line %d): incomplete final fragment, unknown finality -- not counted as a problem\n",
			r.PartialTail.Offset, r.PartialTail.Line)
	}
	if r.Clean {
		fmt.Fprintln(&b, "clean: every complete record decoded through EOF")
	} else {
		fmt.Fprintln(&b, "not clean: see problems above")
	}
	return b.String()
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

// recomputeExtractors maps an EndpointFamily to the provider-package
// function that re-extracts a settled llm.Response from a stored body of
// that family's own wire shape -- each reusing that family's real live
// parser (see the referenced functions' docs for which live decoder each
// one shares state or logic with), never a second hand-rolled one:
//   - openai_public / openai_codex: the Responses API. openaicompat's
//     codex-continuation family delegates to the same OpenAI Responses
//     adapter (openairesponses.Adapter, called via responsesAdapter()), so
//     its records carry these same EndpointFamily values too -- one
//     extractor covers both origins.
//   - openai_chat_completions: this adapter's own Chat Completions
//     fallback (always streamed in this codebase).
//   - openai_compatible_chat_completions: openaicompat's Chat Completions
//     adapter (JSON only -- see openaicompat.ExtractRecordedResponse).
//
// Other providers' bodies are a different wire shape entirely and are left
// alone.
var recomputeExtractors = map[string]func(body []byte, requestedModel string) (llm.Response, error){
	"openai_public":                      openai.ExtractRecordedResponse,
	"openai_codex":                       openai.ExtractRecordedResponse,
	"openai_chat_completions":            openai.ExtractRecordedChatCompletionsResponse,
	"openai_compatible_chat_completions": openaicompat.ExtractRecordedResponse,
}

// recomputeRow re-extracts TextLength/ToolCalls from record's stored
// response body for a row whose recorded counts are both zero, setting
// row.RecomputedTextLength/RecomputedToolCalls when it succeeds. It reports
// whether recomputation found nonzero text or tool calls, for the caller's
// recomputed_nonempty tally; it leaves the row's recorded fields untouched
// either way -- "recorded" and "recomputed" are reported side by side; see
// APICallRow.
func recomputeRow(row *APICallRow, record apilog.APIAttemptRecord) bool {
	if record.Response == nil {
		return false
	}
	if row.TextLength == nil || *row.TextLength != 0 || row.ToolCalls == nil || *row.ToolCalls != 0 {
		return false
	}
	extract, ok := recomputeExtractors[record.Request.EndpointFamily]
	if !ok {
		return false
	}
	body, err := apilog.DecodeBody(record.Response.Body)
	if err != nil || len(body) == 0 {
		return false
	}
	resp, err := extract(body, record.RequestModel)
	if err != nil {
		return false
	}
	textLen := len(resp.Text())
	toolCalls := len(resp.ToolCalls())
	row.RecomputedTextLength = &textLen
	row.RecomputedToolCalls = &toolCalls
	return textLen > 0 || toolCalls > 0
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
	if opts.Recompute {
		fmt.Fprintf(&b, "recomputed_nonempty=%d\n", t.RecomputedNonEmpty)
	}
	if opts.SummaryOnly {
		return b.String()
	}
	if len(r.Calls) == 0 {
		fmt.Fprintln(&b, "(no calls match)")
	} else {
		header := "%-26s %-26s %-8s %-18s %-18s %-25s %6s %-24s %-7s %-24s %-19s %-19s %8s %8s %8s %9s %6s %-5s"
		row := "%-26s %-26s %-8d %-18s %-18s %-25s %6s %-24s %-7t %-24s %-19s %-19t %7dms %8s %8s %9s %6s %-5s"
		if opts.Recompute {
			header += " %-15s %-16s"
			row += " %-15s %-16s"
		}
		headerArgs := []any{
			"attempt_id", "attempt_group_id", "index", "provider", "model", "outcome", "status", "error_class", "empty", "settlement", "final_attempt_count", "forensic_incomplete", "latency", "in_tok", "out_tok", "uncached", "txt", "tools",
		}
		if opts.Recompute {
			headerArgs = append(headerArgs, "recomputed_txt", "recomputed_tools")
		}
		fmt.Fprintf(&b, header+"\n", headerArgs...)
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
			rowArgs := []any{
				truncate(c.AttemptID, 26), truncate(c.AttemptGroupID, 26), c.AttemptIndex, truncate(c.ProviderInstance, 18),
				truncate(c.Model, 18), c.Outcome, status, truncate(errorClass, 24), c.Empty, truncate(settlement, 24), finalAttemptCount,
				c.ForensicIncomplete, c.LatencyMs, optionalIntString(c.InputTokens), optionalIntString(c.OutputTokens),
				optionalIntString(c.UncachedInput), optionalIntString(c.TextLength), optionalIntString(c.ToolCalls),
			}
			if opts.Recompute {
				rowArgs = append(rowArgs, optionalIntString(c.RecomputedTextLength), optionalIntString(c.RecomputedToolCalls))
			}
			fmt.Fprintf(&b, row+"\n", rowArgs...)
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
	return strconv.Itoa(*value)
}
