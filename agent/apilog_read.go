package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"primeradiant.com/serf/llm/apilog"
)

const (
	defaultAPILogRecords  = 20
	maxAPILogRecords      = 100
	maxAPILogOutputBytes  = 64 << 10
	maxAPILogLineBytes    = 128 << 20
	defaultExpansionBytes = 16 << 10
)

type apiLogSettlementState string

const (
	apiLogSettled             apiLogSettlementState = "settled"
	apiLogUnsettled           apiLogSettlementState = "unsettled"
	apiLogUnknownOutsideRange apiLogSettlementState = "unknown_outside_range"
)

var openAPILogFile = func(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

type apiLogBodyEvidence struct {
	Encoding  apilog.BodyEncoding `json:"encoding"`
	ByteCount int                 `json:"byte_count"`
}

type apiLogRecordSummary struct {
	RecordNumber       int                        `json:"record_number"`
	Kind               string                     `json:"kind"`
	AttemptID          string                     `json:"attempt_id,omitempty"`
	AttemptGroupID     string                     `json:"attempt_group_id"`
	AttemptIndex       int                        `json:"attempt_index,omitempty"`
	Timestamp          *time.Time                 `json:"timestamp,omitempty"`
	LatencyMS          int64                      `json:"latency_ms,omitempty"`
	ProviderInstance   string                     `json:"provider_instance,omitempty"`
	RequestModel       string                     `json:"request_model,omitempty"`
	HistoryMode        string                     `json:"history_mode,omitempty"`
	Method             string                     `json:"method,omitempty"`
	Endpoint           string                     `json:"endpoint,omitempty"`
	RequestBody        *apiLogBodyEvidence        `json:"request_body,omitempty"`
	StatusCode         int                        `json:"status_code,omitempty"`
	ResponseModel      string                     `json:"response_model,omitempty"`
	FinishReason       string                     `json:"finish_reason,omitempty"`
	Usage              *apilog.Usage              `json:"usage,omitempty"`
	ResponseBody       *apiLogBodyEvidence        `json:"response_body,omitempty"`
	Outcome            apilog.AttemptOutcomeClass `json:"outcome"`
	ErrorClass         string                     `json:"error_class,omitempty"`
	FinalAttemptID     string                     `json:"final_attempt_id,omitempty"`
	FinalAttemptCount  *int                       `json:"final_attempt_count,omitempty"`
	ForensicIncomplete bool                       `json:"forensic_incomplete,omitempty"`
	SettledAt          *time.Time                 `json:"settled_at,omitempty"`
	SettlementState    apiLogSettlementState      `json:"settlement_state"`
	MetadataTruncated  bool                       `json:"metadata_truncated,omitempty"`
}

type apiLogReadMeta struct {
	RecordsTotal    int    `json:"records_total"`
	Range           string `json:"range"`
	RecordsReturned int    `json:"records_returned"`
	Truncated       bool   `json:"truncated"`
	PartialTail     bool   `json:"partial_tail"`
	RangeWarning    string `json:"range_warning,omitempty"`
}

type apiLogReadEnvelope struct {
	TranscriptRef            string                `json:"transcript_ref"`
	Source                   string                `json:"source"`
	CredentialValuesExcluded bool                  `json:"credential_values_excluded"`
	Records                  []apiLogRecordSummary `json:"records"`
	Meta                     apiLogReadMeta        `json:"meta"`
}

type apiLogAttemptEnvelope struct {
	TranscriptRef            string                  `json:"transcript_ref"`
	Source                   string                  `json:"source"`
	CredentialValuesExcluded bool                    `json:"credential_values_excluded"`
	Attempt                  apiLogRecordSummary     `json:"attempt"`
	Body                     *apiLogBodyExpansion    `json:"body,omitempty"`
	Continuation             *apiLogBodyContinuation `json:"continuation,omitempty"`
}

type apiLogBodyExpansion struct {
	Body          string              `json:"body"`
	OffsetBytes   int                 `json:"offset_bytes"`
	BytesReturned int                 `json:"bytes_returned"`
	TotalBytes    int                 `json:"total_bytes"`
	Encoding      apilog.BodyEncoding `json:"encoding"`
	Data          string              `json:"data"`
}

type apiLogBodyContinuation struct {
	AttemptID   string `json:"attempt_id"`
	Body        string `json:"body"`
	OffsetBytes int    `json:"offset_bytes"`
}

func apiLogPathForTranscript(path string) string {
	return strings.TrimSuffix(path, ".transcript.jsonl") + ".api.jsonl"
}

func readAPILogSummary(path, ref, rangeArg string) (any, error) {
	retained, totalRecords, partialTail, err := decodeAPILogSummaries(path, rangeArg)
	if err != nil {
		return nil, err
	}

	start, end, normalizedRange, rangeWarning := selectAPILogRange(rangeArg, totalRecords)
	records := make([]apiLogRecordSummary, 0, min(len(retained), maxAPILogRecords))
	for _, record := range retained {
		if record.RecordNumber >= start && record.RecordNumber <= end {
			records = append(records, record)
		}
	}
	envelope := apiLogReadEnvelope{
		TranscriptRef:            ref,
		Source:                   apiLogSource,
		CredentialValuesExcluded: true,
		Records:                  records,
		Meta: apiLogReadMeta{
			RecordsTotal:    totalRecords,
			Range:           normalizedRange,
			RecordsReturned: len(records),
			Truncated:       len(records) < totalRecords,
			PartialTail:     partialTail,
			RangeWarning:    rangeWarning,
		},
	}
	tailAnchored := strings.HasPrefix(normalizedRange, "last:")
	for {
		pageReachesCleanEOF := len(envelope.Records) > 0 && envelope.Records[len(envelope.Records)-1].RecordNumber == totalRecords-1 && !partialTail
		setAPILogSettlementStates(envelope.Records, pageReachesCleanEOF)
		envelope.Meta.RecordsReturned = len(envelope.Records)
		envelope.Meta.Truncated = envelope.Meta.Truncated || len(envelope.Records) < totalRecords
		encoded, err := json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode API-log summary: %w", err)
		}
		if len(encoded) <= maxAPILogOutputBytes {
			return envelope, nil
		}
		if len(envelope.Records) == 0 {
			return nil, fmt.Errorf("API-log summary metadata exceeds %d-byte output limit", maxAPILogOutputBytes)
		}
		if tailAnchored {
			envelope.Records = envelope.Records[1:]
		} else {
			envelope.Records = envelope.Records[:len(envelope.Records)-1]
		}
	}
}

func readAPILogAttempt(path, ref, attemptID, body string, offsetBytes, maxBytes int) (any, error) {
	attempt, summary, partialTail, settled, err := findAPILogAttempt(path, attemptID)
	if err != nil {
		return nil, err
	}
	switch {
	case settled:
		summary.SettlementState = apiLogSettled
	case partialTail:
		summary.SettlementState = apiLogUnknownOutsideRange
	default:
		summary.SettlementState = apiLogUnsettled
	}
	envelope := apiLogAttemptEnvelope{
		TranscriptRef:            ref,
		Source:                   apiLogSource,
		CredentialValuesExcluded: true,
		Attempt:                  summary,
	}
	if body == "" {
		return envelope, nil
	}

	encodedBody := attempt.Request.Body
	if body == "response" {
		if attempt.Response == nil {
			return nil, errors.New("invalid_request: attempt has no response body")
		}
		encodedBody = attempt.Response.Body
	}
	decodedBody, err := apilog.DecodeBody(encodedBody)
	if err != nil {
		return nil, fmt.Errorf("decode %s body for attempt %q: %w", body, attemptID, err)
	}
	if offsetBytes > len(decodedBody) {
		return nil, fmt.Errorf("invalid_request: offset_bytes %d exceeds %s body length %d", offsetBytes, body, len(decodedBody))
	}
	if maxBytes == 0 {
		maxBytes = defaultExpansionBytes
	}
	end := offsetBytes + maxBytes
	if end > len(decodedBody) {
		end = len(decodedBody)
	}
	chunk := decodedBody[offsetBytes:end]
	expansion := &apiLogBodyExpansion{
		Body:          body,
		OffsetBytes:   offsetBytes,
		BytesReturned: len(chunk),
		TotalBytes:    len(decodedBody),
	}
	if utf8.Valid(chunk) {
		expansion.Encoding = apilog.BodyUTF8
		expansion.Data = string(chunk)
	} else {
		expansion.Encoding = apilog.BodyBase64
		expansion.Data = base64.StdEncoding.EncodeToString(chunk)
	}
	envelope.Body = expansion
	if end < len(decodedBody) {
		envelope.Continuation = &apiLogBodyContinuation{AttemptID: attemptID, Body: body, OffsetBytes: end}
	}
	return envelope, nil
}

func findAPILogAttempt(path, attemptID string) (apilog.APIAttemptRecord, apiLogRecordSummary, bool, bool, error) {
	f, err := openAPILogFile(path)
	if err != nil {
		return apilog.APIAttemptRecord{}, apiLogRecordSummary{}, false, false, fmt.Errorf("open API log: %w", err)
	}
	defer func() { _ = f.Close() }()

	decoder := apilog.NewDecoder(f, maxAPILogLineBytes)
	var found *apilog.APIAttemptRecord
	var summary apiLogRecordSummary
	settledGroups := make(map[string]bool)
	partialTail := false
	for recordNumber := 0; ; recordNumber++ {
		record, decodeErr := decoder.Next()
		switch {
		case errors.Is(decodeErr, io.EOF):
			if found == nil {
				return apilog.APIAttemptRecord{}, apiLogRecordSummary{}, false, false, fmt.Errorf("API attempt %q not found", attemptID)
			}
			return *found, summary, false, settledGroups[found.AttemptGroupID], nil
		case errors.Is(decodeErr, apilog.ErrPartialTail):
			partialTail = true
			if found == nil {
				return apilog.APIAttemptRecord{}, apiLogRecordSummary{}, true, false, fmt.Errorf("API attempt %q not found before partial API-log tail", attemptID)
			}
			return *found, summary, partialTail, settledGroups[found.AttemptGroupID], nil
		case decodeErr != nil:
			return apilog.APIAttemptRecord{}, apiLogRecordSummary{}, false, false, fmt.Errorf("read API log: %w", decodeErr)
		}
		switch typed := record.(type) {
		case apilog.APIAttemptRecord:
			if typed.AttemptID == attemptID {
				copy := typed
				found = &copy
				summary, err = summarizeAPILogRecord(recordNumber, typed)
				if err != nil {
					return apilog.APIAttemptRecord{}, apiLogRecordSummary{}, false, false, err
				}
			}
		case apilog.APIAttemptGroupSettlement:
			settledGroups[typed.AttemptGroupID] = true
		}
	}
}

type apiLogSummaryRetentionMode uint8

const (
	apiLogRetainTail apiLogSummaryRetentionMode = iota
	apiLogRetainStart
	apiLogRetainExact
)

type apiLogSummaryRetention struct {
	mode     apiLogSummaryRetentionMode
	start    int
	end      int
	records  []apiLogRecordSummary
	tailNext int
	last     *apiLogRecordSummary
}

func newAPILogSummaryRetention(rangeArg string) *apiLogSummaryRetention {
	retention := &apiLogSummaryRetention{
		mode:    apiLogRetainTail,
		records: make([]apiLogRecordSummary, 0, maxAPILogRecords),
	}
	switch {
	case strings.HasPrefix(rangeArg, "start:"):
		if _, ok := parsePositiveInt(strings.TrimPrefix(rangeArg, "start:")); ok {
			retention.mode = apiLogRetainStart
			retention.start = 0
			retention.end = maxAPILogRecords - 1
		}
	case strings.Contains(rangeArg, "-"):
		lo, hi, ok := parseDashRange(rangeArg)
		if ok {
			retention.mode = apiLogRetainExact
			retention.start = lo
			retention.end = hi
			if hi >= lo && hi-lo >= maxAPILogRecords {
				retention.end = lo + maxAPILogRecords - 1
			}
		}
	}
	return retention
}

func (r *apiLogSummaryRetention) add(recordNumber int, record apilog.APILogRecord) error {
	keep := r.mode == apiLogRetainTail || recordNumber >= r.start && recordNumber <= r.end
	if !keep && r.mode != apiLogRetainExact {
		return nil
	}
	summary, err := summarizeAPILogRecord(recordNumber, record)
	if err != nil {
		return err
	}
	if r.mode == apiLogRetainExact {
		last := summary
		r.last = &last
	}
	if !keep {
		return nil
	}
	if r.mode != apiLogRetainTail || len(r.records) < maxAPILogRecords {
		r.records = append(r.records, summary)
		return nil
	}
	r.records[r.tailNext] = summary
	r.tailNext = (r.tailNext + 1) % maxAPILogRecords
	return nil
}

func (r *apiLogSummaryRetention) result() []apiLogRecordSummary {
	if r.mode == apiLogRetainTail && len(r.records) == maxAPILogRecords && r.tailNext > 0 {
		ordered := make([]apiLogRecordSummary, 0, maxAPILogRecords)
		ordered = append(ordered, r.records[r.tailNext:]...)
		ordered = append(ordered, r.records[:r.tailNext]...)
		return ordered
	}
	result := append([]apiLogRecordSummary(nil), r.records...)
	if r.mode == apiLogRetainExact && r.last != nil && (len(result) == 0 || result[len(result)-1].RecordNumber != r.last.RecordNumber) {
		result = append(result, *r.last)
	}
	return result
}

func decodeAPILogSummaries(path, rangeArg string) ([]apiLogRecordSummary, int, bool, error) {
	f, err := openAPILogFile(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("open API log: %w", err)
	}
	defer func() { _ = f.Close() }()

	decoder := apilog.NewDecoder(f, maxAPILogLineBytes)
	retention := newAPILogSummaryRetention(rangeArg)
	for recordNumber := 0; ; recordNumber++ {
		record, err := decoder.Next()
		switch {
		case errors.Is(err, io.EOF):
			return retention.result(), recordNumber, false, nil
		case errors.Is(err, apilog.ErrPartialTail):
			return retention.result(), recordNumber, true, nil
		case err != nil:
			return nil, 0, false, fmt.Errorf("read API log: %w", err)
		}
		if err := retention.add(recordNumber, record); err != nil {
			return nil, 0, false, err
		}
	}
}

func summarizeAPILogRecord(recordNumber int, record apilog.APILogRecord) (apiLogRecordSummary, error) {
	summary := apiLogRecordSummary{RecordNumber: recordNumber, Kind: record.RecordKind()}
	switch typed := record.(type) {
	case apilog.APIAttemptRecord:
		summary.AttemptID = typed.AttemptID
		summary.AttemptGroupID = typed.AttemptGroupID
		summary.AttemptIndex = typed.AttemptIndex
		timestamp := typed.Timestamp
		summary.Timestamp = &timestamp
		summary.LatencyMS = typed.LatencyMS
		summary.ProviderInstance, summary.MetadataTruncated = boundedAPILogMetadata(typed.ProviderInstance, summary.MetadataTruncated)
		summary.RequestModel, summary.MetadataTruncated = boundedAPILogMetadata(typed.RequestModel, summary.MetadataTruncated)
		summary.HistoryMode, summary.MetadataTruncated = boundedAPILogMetadata(typed.Request.HistoryMode, summary.MetadataTruncated)
		summary.Method, summary.MetadataTruncated = boundedAPILogMetadata(typed.Request.Method, summary.MetadataTruncated)
		summary.Endpoint, summary.MetadataTruncated = boundedAPILogMetadata(typed.Request.Endpoint, summary.MetadataTruncated)
		summary.RequestBody = &apiLogBodyEvidence{Encoding: typed.Request.Body.Encoding, ByteCount: typed.Request.Body.ByteCount}
		summary.Outcome = typed.Outcome
		summary.ErrorClass, summary.MetadataTruncated = boundedAPILogMetadata(typed.ErrorClass, summary.MetadataTruncated)
		if typed.Response != nil {
			summary.StatusCode = typed.Response.StatusCode
			summary.ResponseModel, summary.MetadataTruncated = boundedAPILogMetadata(typed.Response.Model, summary.MetadataTruncated)
			summary.FinishReason, summary.MetadataTruncated = boundedAPILogMetadata(typed.Response.FinishReason, summary.MetadataTruncated)
			summary.Usage = &typed.Response.Usage
			summary.ResponseBody = &apiLogBodyEvidence{Encoding: typed.Response.Body.Encoding, ByteCount: typed.Response.Body.ByteCount}
		}
	case apilog.APIAttemptGroupSettlement:
		summary.AttemptGroupID = typed.AttemptGroupID
		summary.FinalAttemptID = typed.FinalAttemptID
		finalAttemptCount := typed.FinalAttemptCount
		summary.FinalAttemptCount = &finalAttemptCount
		summary.Outcome = typed.Outcome
		summary.ForensicIncomplete = typed.ForensicIncomplete
		settledAt := typed.SettledAt
		summary.SettledAt = &settledAt
	default:
		return apiLogRecordSummary{}, fmt.Errorf("unsupported API-log record type %T", record)
	}
	return summary, nil
}

const maxAPILogMetadataBytes = 2048

func boundedAPILogMetadata(value string, alreadyTruncated bool) (string, bool) {
	if len(value) <= maxAPILogMetadataBytes {
		return value, alreadyTruncated
	}
	for max := maxAPILogMetadataBytes; max > 0; max-- {
		if value[max]&0xc0 != 0x80 {
			return value[:max], true
		}
	}
	return "", true
}

func selectAPILogRange(spec string, recordCount int) (start, end int, normalized, warning string) {
	normalized = spec
	if normalized == "" {
		normalized = fmt.Sprintf("last:%d", defaultAPILogRecords)
	}
	start, end, err := parseRangeErr(normalized, recordCount)
	if err != nil {
		warning = fmt.Sprintf("invalid range %q; rendered the default instead. Accepted: %s", spec, rangeAcceptedGrammar)
		normalized = fmt.Sprintf("last:%d", defaultAPILogRecords)
		start, end, _ = parseRangeErr(normalized, recordCount)
	}
	if end-start+1 > maxAPILogRecords {
		if strings.HasPrefix(normalized, "last:") {
			start = end - maxAPILogRecords + 1
		} else {
			end = start + maxAPILogRecords - 1
		}
	}
	return start, end, normalized, warning
}

func setAPILogSettlementStates(records []apiLogRecordSummary, pageReachesCleanEOF bool) {
	settled := make(map[string]bool)
	for i := range records {
		if records[i].Kind == "attempt_group_settlement" {
			settled[records[i].AttemptGroupID] = true
		}
	}
	for i := range records {
		switch {
		case settled[records[i].AttemptGroupID]:
			records[i].SettlementState = apiLogSettled
		case pageReachesCleanEOF && records[i].Kind == "api_attempt":
			records[i].SettlementState = apiLogUnsettled
		default:
			records[i].SettlementState = apiLogUnknownOutsideRange
		}
	}
}
