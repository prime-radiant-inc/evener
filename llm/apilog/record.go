package apilog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/identifier"
)

const (
	attemptRecordKind    = "api_attempt"
	settlementRecordKind = "attempt_group_settlement"
	recordSchemaVersion  = 1
)

type AttemptOutcomeClass string

const (
	AttemptSuccess         AttemptOutcomeClass = "success"
	AttemptProviderReject  AttemptOutcomeClass = "provider_rejection"
	AttemptTransportFail   AttemptOutcomeClass = "transport_failure"
	AttemptProviderTimeout AttemptOutcomeClass = "provider_timeout"
	AttemptCallerCancel    AttemptOutcomeClass = "caller_cancellation"
	AttemptDecodeFail      AttemptOutcomeClass = "response_decoding_failure"
)

type APIAttemptRequest struct {
	Method         string        `json:"method"`
	Endpoint       string        `json:"endpoint"`
	Headers        EncodedHeader `json:"headers,omitempty"`
	Body           EncodedBody   `json:"body"`
	Model          string        `json:"model,omitempty"`
	HistoryMode    string        `json:"history_mode,omitempty"`
	EndpointFamily string        `json:"endpoint_family,omitempty"`
}

type APIAttemptResponse struct {
	StatusCode    *int        `json:"status_code,omitempty"`
	Body          EncodedBody `json:"body"`
	Model         string      `json:"model,omitempty"`
	FinishReason  string      `json:"finish_reason,omitempty"`
	Usage         Usage       `json:"usage,omitempty"`
	TextLength    *int        `json:"text_length,omitempty"`
	ToolCallCount *int        `json:"tool_call_count,omitempty"`
}

type Usage struct {
	InputTokens      *int `json:"input_tokens,omitempty"`
	OutputTokens     *int `json:"output_tokens,omitempty"`
	TotalTokens      *int `json:"total_tokens,omitempty"`
	CacheReadTokens  *int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`
}

type APIAttemptRecord struct {
	Kind                     string              `json:"kind"`
	SchemaVersion            int                 `json:"schema_version"`
	AttemptID                string              `json:"attempt_id"`
	AttemptGroupID           string              `json:"attempt_group_id"`
	AttemptIndex             int                 `json:"attempt_index"`
	Timestamp                time.Time           `json:"timestamp"`
	LatencyMS                int64               `json:"latency_ms"`
	ProviderInstance         string              `json:"provider_instance"`
	RequestModel             string              `json:"request_model"`
	Request                  APIAttemptRequest   `json:"request"`
	Response                 *APIAttemptResponse `json:"response,omitempty"`
	Outcome                  AttemptOutcomeClass `json:"outcome"`
	ErrorClass               string              `json:"error_class,omitempty"`
	ErrorMessage             string              `json:"error_message,omitempty"`
	forbiddenEvidence        []string
	forbiddenCredentialNames []string
}

type APIAttemptGroupSettlement struct {
	Kind               string              `json:"kind"`
	SchemaVersion      int                 `json:"schema_version"`
	AttemptGroupID     string              `json:"attempt_group_id"`
	FinalAttemptID     string              `json:"final_attempt_id,omitempty"`
	FinalAttemptCount  int                 `json:"final_attempt_count"`
	Outcome            AttemptOutcomeClass `json:"outcome"`
	ForensicIncomplete bool                `json:"forensic_incomplete,omitempty"`
	SettledAt          time.Time           `json:"settled_at"`
}

type APILogRecord interface {
	RecordKind() string
	validateRecord() error
}

func (APIAttemptRecord) RecordKind() string {
	return attemptRecordKind
}

func (APIAttemptGroupSettlement) RecordKind() string {
	return settlementRecordKind
}

// WithForbiddenProviderEvidence binds credential values and custom names that
// must be absent from provider-derived fields at the final marshal boundary.
func (r APIAttemptRecord) WithForbiddenProviderEvidence(patterns, credentialNames []string) APIAttemptRecord {
	r.forbiddenEvidence = append([]string(nil), patterns...)
	r.forbiddenCredentialNames = append([]string(nil), credentialNames...)
	return r
}

func (r APIAttemptRecord) validateRecord() error {
	if r.Kind != attemptRecordKind {
		return fmt.Errorf("kind must be %q", attemptRecordKind)
	}
	if r.SchemaVersion != recordSchemaVersion {
		return fmt.Errorf("schema version must be %d", recordSchemaVersion)
	}
	if err := identifier.ValidateAPIAttemptID(r.AttemptID); err != nil {
		return fmt.Errorf("invalid attempt ID: %w", err)
	}
	if r.AttemptGroupID == "" {
		return fmt.Errorf("attempt group ID is required")
	}
	if r.AttemptIndex < 1 {
		return fmt.Errorf("attempt index must be at least 1")
	}
	if r.Timestamp.IsZero() {
		return fmt.Errorf("timestamp is required")
	}
	if r.LatencyMS < 0 {
		return fmt.Errorf("latency must be non-negative")
	}
	if r.ProviderInstance == "" {
		return fmt.Errorf("provider instance is required")
	}
	if r.RequestModel == "" {
		return fmt.Errorf("request model is required")
	}
	if r.Request.Method == "" {
		return fmt.Errorf("request method is required")
	}
	if r.Request.Endpoint == "" {
		return fmt.Errorf("request endpoint is required")
	}
	if _, err := DecodeBody(r.Request.Body); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	if !validAttemptOutcome(r.Outcome) {
		return fmt.Errorf("unknown attempt outcome %q", r.Outcome)
	}
	if r.Outcome == AttemptSuccess && r.Response == nil {
		return fmt.Errorf("successful attempt requires a response")
	}
	if r.Response != nil {
		if err := r.Response.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r APIAttemptGroupSettlement) validateRecord() error {
	if r.Kind != settlementRecordKind {
		return fmt.Errorf("kind must be %q", settlementRecordKind)
	}
	if r.SchemaVersion != recordSchemaVersion {
		return fmt.Errorf("schema version must be %d", recordSchemaVersion)
	}
	if r.AttemptGroupID == "" {
		return fmt.Errorf("attempt group ID is required")
	}
	if r.FinalAttemptCount < 0 {
		return fmt.Errorf("final attempt count must be non-negative")
	}
	if r.FinalAttemptCount == 0 && r.FinalAttemptID != "" {
		return fmt.Errorf("zero-attempt settlement cannot name a final attempt")
	}
	if r.FinalAttemptCount > 0 {
		if err := identifier.ValidateAPIAttemptID(r.FinalAttemptID); err != nil {
			return fmt.Errorf("invalid final attempt ID: %w", err)
		}
	}
	if !validAttemptOutcome(r.Outcome) {
		return fmt.Errorf("unknown attempt outcome %q", r.Outcome)
	}
	if r.SettledAt.IsZero() {
		return fmt.Errorf("settled timestamp is required")
	}
	return nil
}

func (r APIAttemptResponse) validate() error {
	if r.StatusCode != nil && *r.StatusCode < 0 {
		return fmt.Errorf("response status code must be non-negative")
	}
	if r.TextLength != nil && *r.TextLength < 0 || r.ToolCallCount != nil && *r.ToolCallCount < 0 {
		return fmt.Errorf("response compact counts must be non-negative")
	}
	if _, err := DecodeBody(r.Body); err != nil {
		return fmt.Errorf("invalid response body: %w", err)
	}
	if err := r.Usage.validate(); err != nil {
		return err
	}
	return nil
}

func (u Usage) validate() error {
	if u.InputTokens != nil && *u.InputTokens < 0 ||
		u.OutputTokens != nil && *u.OutputTokens < 0 ||
		u.TotalTokens != nil && *u.TotalTokens < 0 {
		return fmt.Errorf("token usage must be non-negative")
	}
	if u.CacheReadTokens != nil && *u.CacheReadTokens < 0 {
		return fmt.Errorf("cache-read token usage must be non-negative")
	}
	if u.CacheWriteTokens != nil && *u.CacheWriteTokens < 0 {
		return fmt.Errorf("cache-write token usage must be non-negative")
	}
	return nil
}

func validAttemptOutcome(outcome AttemptOutcomeClass) bool {
	switch outcome {
	case AttemptSuccess, AttemptProviderReject, AttemptTransportFail, AttemptProviderTimeout, AttemptCallerCancel, AttemptDecodeFail:
		return true
	default:
		return false
	}
}

func (r APIAttemptRecord) validateProviderEvidence() error {
	if len(r.forbiddenEvidence) == 0 && len(r.forbiddenCredentialNames) == 0 {
		return nil
	}
	stringsToCheck := []struct {
		name  string
		value string
	}{
		{name: "provider instance", value: r.ProviderInstance},
		{name: "request model", value: r.RequestModel},
		{name: "request method", value: r.Request.Method},
		{name: "request endpoint", value: r.Request.Endpoint},
		{name: "request body model", value: r.Request.Model},
		{name: "request history mode", value: r.Request.HistoryMode},
		{name: "request endpoint family", value: r.Request.EndpointFamily},
		{name: "error class", value: r.ErrorClass},
		{name: "error message", value: r.ErrorMessage},
	}
	if r.Response != nil {
		stringsToCheck = append(stringsToCheck,
			struct {
				name  string
				value string
			}{name: "response model", value: r.Response.Model},
			struct {
				name  string
				value string
			}{name: "response finish reason", value: r.Response.FinishReason},
		)
	}
	for _, field := range stringsToCheck {
		if containsForbiddenEvidence([]byte(field.value), r.forbiddenEvidence, r.forbiddenCredentialNames) {
			return fmt.Errorf("%s contains credential material", field.name)
		}
	}
	for name, values := range r.Request.Headers {
		if containsForbiddenEvidence([]byte(name), r.forbiddenEvidence, r.forbiddenCredentialNames) {
			return fmt.Errorf("request header name contains credential material")
		}
		for _, value := range values {
			if containsForbiddenEvidence([]byte(value), r.forbiddenEvidence, r.forbiddenCredentialNames) {
				return fmt.Errorf("request header %q contains credential material", name)
			}
		}
	}
	if body, err := DecodeBody(r.Request.Body); err == nil && containsForbiddenEvidence(body, r.forbiddenEvidence, r.forbiddenCredentialNames) {
		return fmt.Errorf("request body contains credential material")
	}
	if r.Response == nil {
		return nil
	}
	if body, err := DecodeBody(r.Response.Body); err == nil && containsForbiddenEvidence(body, r.forbiddenEvidence, r.forbiddenCredentialNames) {
		return fmt.Errorf("response body contains credential material")
	}
	numbers := []struct {
		name  string
		value *int
	}{
		{name: "response status code", value: r.Response.StatusCode},
		{name: "response text length", value: r.Response.TextLength},
		{name: "response tool call count", value: r.Response.ToolCallCount},
		{name: "input token usage", value: r.Response.Usage.InputTokens},
		{name: "output token usage", value: r.Response.Usage.OutputTokens},
		{name: "total token usage", value: r.Response.Usage.TotalTokens},
		{name: "cache-read token usage", value: r.Response.Usage.CacheReadTokens},
		{name: "cache-write token usage", value: r.Response.Usage.CacheWriteTokens},
	}
	for _, field := range numbers {
		if field.value != nil && containsForbiddenEvidence([]byte(strconv.Itoa(*field.value)), r.forbiddenEvidence, r.forbiddenCredentialNames) {
			return fmt.Errorf("%s contains credential material", field.name)
		}
	}
	return nil
}

func containsForbiddenEvidence(value []byte, patterns, credentialNames []string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(string(value), pattern) {
			return true
		}
	}
	lowerValue := strings.ToLower(string(value))
	for _, name := range credentialNames {
		if name != "" && strings.Contains(lowerValue, name) {
			return true
		}
	}
	return false
}
