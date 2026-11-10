package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
	apilog "primeradiant.com/evener/llm/apilog"
)

func TestBuildAPIAttemptRecordOmitsCredentialBearingProviderEvidence(t *testing.T) {
	const (
		secret           = "alpha/beta \"value\"\n"
		modelSecret      = "private-model-sentinel"
		customHeaderName = "X-Redirect-Credential-Sentinel"
		customQueryName  = "redirect_credential_sentinel"
		redirectSecret   = "redirect-value-sentinel"
	)
	jsonSecret := jsonStringContent(t, secret)
	request, err := http.NewRequest(http.MethodPost,
		"https://alice:password@provider.test/v1/responses?visible=yes&"+customQueryName+"="+url.QueryEscape(redirectSecret)+"#private-fragment",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header = http.Header{
		customHeaderName: {redirectSecret},
		"X-Path-Secret":  {url.PathEscape(secret)},
		"X-Visible":      {"visible"},
	}
	configured := NewAPILogCredentialMaterial(
		[]string{customHeaderName},
		[]string{customQueryName},
		secret, modelSecret,
	)
	material := APILogCredentialMaterialForRequest(request, configured)
	endpoint, headers := SanitizeRequestForAPILog(request, material)
	if endpoint != "https://provider.test/v1/responses" {
		t.Fatalf("endpoint = %q, want credential-free provenance", endpoint)
	}

	startedAt := time.Unix(10, 0).UTC()
	record := buildAPIAttemptRecord("ag_credential_omission", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance:   "provider",
		RequestModel:       "model",
		Method:             http.MethodPost,
		Endpoint:           endpoint,
		Headers:            headers,
		RequestBody:        append([]byte{0xff, 0x00}, []byte("request contains "+secret)...),
		StartedAt:          startedAt,
		CredentialMaterial: material,
	}, APIAttemptResult{
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: append([]byte{0xfe, 0x00}, []byte("response contains "+url.QueryEscape(secret))...),
		Response: &Response{
			Model:   modelSecret,
			Message: Assistant("safe"),
			Finish:  FinishReason{Reason: customHeaderName},
			Usage:   Usage{InputTokens: 123, OutputTokens: 45, TotalTokens: 168},
		},
		Outcome:    apilog.AttemptProviderReject,
		ErrorClass: customQueryName,
		Err:        errors.New("provider rejected " + jsonSecret),
		FinishedAt: startedAt.Add(time.Second),
	})

	assertCredentialExcludedBody(t, record.Request.Body)
	assertCredentialExcludedBody(t, record.Response.Body)
	visible := record.Request.Headers["X-Visible"]
	host := record.Request.Headers["Host"]
	if len(record.Request.Headers) != 2 || len(visible) != 1 || visible[0] != "visible" || len(host) != 1 || host[0] != "provider.test" {
		t.Fatalf("request headers = %#v, want Host and X-Visible", record.Request.Headers)
	}
	if record.ErrorClass != "" || record.ErrorMessage != "" {
		t.Fatalf("durable error = (%q, %q), want omitted", record.ErrorClass, record.ErrorMessage)
	}
	if record.Response.Model != "" || record.Response.FinishReason != "" {
		t.Fatalf("response strings = (%q, %q), want omitted", record.Response.Model, record.Response.FinishReason)
	}
	if record.Response.Usage.InputTokens == nil || *record.Response.Usage.InputTokens != 123 ||
		record.Response.Usage.OutputTokens == nil || *record.Response.Usage.OutputTokens != 45 {
		t.Fatalf("usage = %+v, want numeric metadata retained", record.Response.Usage)
	}
	line, err := apilog.MarshalRecord(record)
	if err != nil {
		t.Fatalf("MarshalRecord(): %v", err)
	}
	for _, forbidden := range []string{
		secret,
		url.QueryEscape(secret),
		url.PathEscape(secret),
		jsonSecret,
		modelSecret,
		customHeaderName,
		customQueryName,
		redirectSecret,
		"alice",
		"password",
		"private-fragment",
		"[credential excluded]",
	} {
		if strings.Contains(string(line), forbidden) {
			t.Fatalf("canonical record contains credential pattern %q: %s", forbidden, line)
		}
	}
}

func TestBuildAPIAttemptRecordOmitsInvalidAndOpaqueEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
	}{
		{name: "malformed URL", endpoint: "://not-a-valid-endpoint"},
		{name: "missing host", endpoint: "https:/v1/responses"},
		{name: "opaque URL", endpoint: "mailto:provider@example.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startedAt := time.Unix(25, 0).UTC()
			record := buildAPIAttemptRecord("ag_endpoint_sanitize", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
				ProviderInstance: "provider",
				RequestModel:     "model",
				Method:           http.MethodPost,
				Endpoint:         tt.endpoint,
				RequestBody:      []byte("safe request"),
				StartedAt:        startedAt,
			}, APIAttemptResult{
				StatusCode:   http.StatusOK,
				ResponseBody: []byte("safe response"),
				Response: &Response{
					Model:   "model",
					Message: Assistant("safe"),
					Finish:  FinishReason{Reason: FinishReasonStop},
				},
				Outcome:    apilog.AttemptSuccess,
				FinishedAt: startedAt.Add(time.Millisecond),
			})
			if record.Request.Endpoint != "" {
				t.Fatalf("durable endpoint = %q, want invalid provenance omitted", record.Request.Endpoint)
			}
			if _, err := apilog.MarshalRecord(record); err == nil {
				t.Fatal("MarshalRecord() accepted an attempt without valid endpoint provenance")
			}
		})
	}
}

func TestMarshalRecordRejectsCredentialInsertedAfterRecordBuild(t *testing.T) {
	const secret = "post-build-credential-sentinel"
	record := validBuiltAPIAttemptRecord(t, NewAPILogCredentialMaterial(nil, nil, secret))
	record.Response.Model = secret
	if _, err := apilog.MarshalRecord(record); err == nil {
		t.Fatal("MarshalRecord() accepted credential-bearing provider evidence")
	}
}

func TestBuildAPIAttemptRecordOmitsCustomCredentialNamesCaseInsensitively(t *testing.T) {
	const (
		customHeader = "X-Custom-Credential-Name"
		customQuery  = "private_query_name"
		literalValue = "CaseSensitiveCredentialValue"
	)
	material := NewAPILogCredentialMaterial([]string{customHeader}, []string{customQuery}, literalValue)
	startedAt := time.Unix(24, 0).UTC()
	record := buildAPIAttemptRecord("ag_casefold_names", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance: "provider",
		RequestModel:     "model",
		Method:           http.MethodPost,
		Endpoint:         "https://provider.test/v1/responses",
		Headers: http.Header{
			"X-Ordinary": {"echo x-CuStOm-CrEdEnTiAl-NaMe"},
		},
		RequestBody:        []byte("echo PRIVATE_QUERY_NAME"),
		StartedAt:          startedAt,
		CredentialMaterial: material,
	}, APIAttemptResult{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte("casesensitivecredentialvalue"),
		Response: &Response{
			Model:   "echo X-cUsToM-cReDeNtIaL-nAmE",
			Message: Assistant("safe"),
			Finish:  FinishReason{Reason: "PRIVATE_QUERY_NAME"},
		},
		Outcome:    apilog.AttemptProviderReject,
		ErrorClass: "PRIVATE_QUERY_NAME",
		Err:        errors.New("echo x-CUSTOM-cREDENTIAL-nAME"),
		FinishedAt: startedAt.Add(time.Millisecond),
	})

	assertCredentialExcludedBody(t, record.Request.Body)
	if len(record.Request.Headers) != 0 {
		t.Fatalf("mixed-case credential name survived in header evidence: %#v", record.Request.Headers)
	}
	if record.Response.Model != "" || record.Response.FinishReason != "" || record.ErrorClass != "" || record.ErrorMessage != "" {
		t.Fatalf("mixed-case credential names survived: response=%+v error=(%q, %q)", record.Response, record.ErrorClass, record.ErrorMessage)
	}
	responseBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(responseBody); got != "casesensitivecredentialvalue" {
		t.Fatalf("literal-sensitive credential value changed case-insensitive evidence %q", got)
	}
	if _, err := apilog.MarshalRecord(record); err != nil {
		t.Fatalf("MarshalRecord(): %v", err)
	}
}

func TestBuildAPIAttemptRecordOmitsEncodedCustomCredentialNamesCaseInsensitively(t *testing.T) {
	const literalValue = "CaseSensitiveCredentialValue"
	material := NewAPILogCredentialMaterial(nil, []string{
		"private/name",
		"private query",
		"private path",
		`private"json`,
	}, literalValue)
	startedAt := time.Unix(27, 0).UTC()
	record := buildAPIAttemptRecord("ag_casefold_encoded_names", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance: "provider",
		RequestModel:     "model",
		Method:           http.MethodPost,
		Endpoint:         "https://provider.test/v1/responses",
		Headers: http.Header{
			"X-Ordinary": {"echo PrIvAtE+qUeRy"},
		},
		RequestBody:        []byte("echo PrIvAtE%20pAtH"),
		StartedAt:          startedAt,
		CredentialMaterial: material,
	}, APIAttemptResult{
		StatusCode:   http.StatusBadRequest,
		ResponseBody: []byte("casesensitivecredentialvalue"),
		Response: &Response{
			Model:   "echo PrIvAtE%2FnAmE",
			Message: Assistant("safe"),
			Finish:  FinishReason{Reason: `echo PrIvAtE\"JsOn`},
		},
		Outcome:    apilog.AttemptProviderReject,
		ErrorClass: `echo PrIvAtE\"JsOn`,
		Err:        errors.New("echo PrIvAtE%2FnAmE"),
		FinishedAt: startedAt.Add(time.Millisecond),
	})

	assertCredentialExcludedBody(t, record.Request.Body)
	if len(record.Request.Headers) != 0 {
		t.Fatalf("encoded credential name survived in header evidence: %#v", record.Request.Headers)
	}
	if record.Response.Model != "" || record.Response.FinishReason != "" || record.ErrorClass != "" || record.ErrorMessage != "" {
		t.Fatalf("encoded credential names survived: response=%+v error=(%q, %q)", record.Response, record.ErrorClass, record.ErrorMessage)
	}
	responseBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(responseBody); got != "casesensitivecredentialvalue" {
		t.Fatalf("literal-sensitive credential value changed case-insensitive evidence %q", got)
	}
	if _, err := apilog.MarshalRecord(record); err != nil {
		t.Fatalf("MarshalRecord(): %v", err)
	}
}

func TestMarshalRecordRejectsMixedCaseCustomCredentialNameInsertedAfterBuild(t *testing.T) {
	material := NewAPILogCredentialMaterial([]string{"X-Custom-Credential-Name"}, []string{"private_query_name"})
	for _, tt := range []struct {
		name   string
		mutate func(*apilog.APIAttemptRecord)
	}{
		{
			name: "ordinary header",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.Request.Headers = apilog.EncodedHeader{"X-Ordinary": {"x-CuStOm-CrEdEnTiAl-NaMe"}}
			},
		},
		{
			name: "request body",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.Request.Body = apilog.EncodeBody([]byte("PRIVATE_QUERY_NAME"))
			},
		},
		{
			name: "error text",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.ErrorMessage = "echo x-CUSTOM-cREDENTIAL-nAME"
			},
		},
		{
			name: "response model",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.Response.Model = "X-cUsToM-cReDeNtIaL-nAmE"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := validBuiltAPIAttemptRecord(t, material)
			tt.mutate(&record)
			if _, err := apilog.MarshalRecord(record); err == nil {
				t.Fatal("MarshalRecord() accepted mixed-case custom credential name")
			}
		})
	}
}

func TestMarshalRecordRejectsEncodedMixedCaseCustomCredentialNamesInsertedAfterBuild(t *testing.T) {
	const literalValue = "CaseSensitiveCredentialValue"
	material := NewAPILogCredentialMaterial(nil, []string{
		"private/name",
		"private query",
		"private path",
		`private"json`,
	}, literalValue)
	for _, tt := range []struct {
		name   string
		mutate func(*apilog.APIAttemptRecord)
	}{
		{
			name: "query-escaped name",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.Request.Headers = apilog.EncodedHeader{"X-Ordinary": {"PrIvAtE+qUeRy"}}
			},
		},
		{
			name: "path-escaped name",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.Request.Body = apilog.EncodeBody([]byte("PrIvAtE%20pAtH"))
			},
		},
		{
			name: "encoded slash name",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.Response.Model = "PrIvAtE%2FnAmE"
			},
		},
		{
			name: "JSON-content-escaped name",
			mutate: func(record *apilog.APIAttemptRecord) {
				record.ErrorMessage = `PrIvAtE\"JsOn`
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := validBuiltAPIAttemptRecord(t, material)
			tt.mutate(&record)
			if _, err := apilog.MarshalRecord(record); err == nil {
				t.Fatal("MarshalRecord() accepted encoded mixed-case custom credential name")
			}
		})
	}

	t.Run("credential value remains literal-case-sensitive", func(t *testing.T) {
		record := validBuiltAPIAttemptRecord(t, material)
		record.Response.Body = apilog.EncodeBody([]byte("casesensitivecredentialvalue"))
		if _, err := apilog.MarshalRecord(record); err != nil {
			t.Fatalf("MarshalRecord() rejected case-distinct credential value: %v", err)
		}
	})
}

func TestMarshalRecordIgnoresCredentialOverlapWithClosedStructuralFields(t *testing.T) {
	record := validBuiltAPIAttemptRecord(t, APILogCredentialMaterial{})
	tests := []struct {
		name   string
		secret string
		mutate func(*apilog.APIAttemptRecord)
	}{
		{name: "schema key", secret: "schema_version"},
		{name: "kind", secret: "api"},
		{name: "outcome", secret: string(apilog.AttemptSuccess)},
		{name: "body encoding", secret: string(apilog.BodyUTF8)},
		{name: "truth boolean", secret: "true"},
		{name: "JSON delimiter", secret: ","},
		{name: "generated attempt ID", secret: record.AttemptID},
		{name: "generated group ID", secret: record.AttemptGroupID},
		{name: "timestamp", secret: record.Timestamp.Format(time.RFC3339Nano)},
		{
			name:   "binary body encoding",
			secret: string(apilog.BodyBase64),
			mutate: func(record *apilog.APIAttemptRecord) { record.Request.Body = apilog.EncodeBody([]byte{0xff}) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testRecord := record
			if tt.mutate != nil {
				tt.mutate(&testRecord)
			}
			testRecord = testRecord.WithForbiddenProviderEvidence(credentialValueVariants([]string{tt.secret}), nil)
			if _, err := apilog.MarshalRecord(testRecord); err != nil {
				t.Fatalf("MarshalRecord() rejected closed structural overlap: %v", err)
			}
		})
	}
}

func TestBuildAPIAttemptRecordPreservesNumericCredentialCoincidences(t *testing.T) {
	one := 1
	startedAt := time.Unix(25, 0).UTC()
	record := buildAPIAttemptRecord("ag_numeric_coincidence", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance: "provider",
		RequestModel:     "model",
		Method:           http.MethodPost,
		Endpoint:         "https://provider.test/responses",
		Headers: http.Header{
			"X-Safe": {"abcdefghij"},
		},
		RequestBody:        []byte("abcdefghij"),
		StartedAt:          startedAt,
		CredentialMaterial: NewAPILogCredentialMaterial(nil, nil, "1"),
	}, APIAttemptResult{
		StatusCode:   http.StatusCreated,
		ResponseBody: []byte("abcdefghij"),
		Response: &Response{
			Model: "model",
			Message: Message{Role: RoleAssistant, Content: []ContentPart{
				{Kind: ContentText, Text: "x"},
				{Kind: ContentToolCall, ToolCall: &ToolCallData{Name: "tool"}},
			}},
			Finish: FinishReason{Reason: FinishReasonStop},
			Usage: Usage{
				InputTokens:      1,
				OutputTokens:     1,
				TotalTokens:      1,
				CacheReadTokens:  &one,
				CacheWriteTokens: &one,
			},
		},
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: startedAt.Add(time.Millisecond),
	})

	if got := record.Request.Headers["X-Safe"]; len(got) != 1 || got[0] != "abcdefghij" {
		t.Fatalf("request header = %#v, want safe 10-byte value retained", got)
	}
	for name, body := range map[string]apilog.EncodedBody{
		"request":  record.Request.Body,
		"response": record.Response.Body,
	} {
		if body.CredentialValuesExcluded || body.ByteCount != 10 {
			t.Fatalf("%s body = %+v, want exact 10-byte evidence retained", name, body)
		}
	}
	for name, value := range map[string]*int{
		"status":             record.Response.StatusCode,
		"text length":        record.Response.TextLength,
		"tool-call count":    record.Response.ToolCallCount,
		"input tokens":       record.Response.Usage.InputTokens,
		"output tokens":      record.Response.Usage.OutputTokens,
		"total tokens":       record.Response.Usage.TotalTokens,
		"cache-read tokens":  record.Response.Usage.CacheReadTokens,
		"cache-write tokens": record.Response.Usage.CacheWriteTokens,
	} {
		if value == nil {
			t.Fatalf("%s omitted because it coincides with credential text", name)
		}
	}
	if got := *record.Response.StatusCode; got != http.StatusCreated {
		t.Fatalf("status code = %d, want %d", got, http.StatusCreated)
	}
	if _, err := apilog.MarshalRecord(record); err != nil {
		t.Fatalf("MarshalRecord(): %v", err)
	}
}

func TestBuildAPIAttemptRecordOmitsCredentialCollisionsCreatedByCanonicalEncoding(t *testing.T) {
	startedAt := time.Unix(28, 0).UTC()
	material := NewAPILogCredentialMaterial(nil, nil, "/w==", `\u003c`)
	record := buildAPIAttemptRecord("ag_encoded_omission", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance: "provider",
		RequestModel:     "model",
		Method:           http.MethodPost,
		Endpoint:         "https://provider.test/v1/responses",
		Headers: http.Header{
			"X-Binary": {string([]byte{0xff})},
			"X-Count":  {strings.Repeat("x", 17)},
			"X-JSON":   {"<"},
			"X-Safe":   {"visible"},
		},
		RequestBody:        []byte{0xff},
		StartedAt:          startedAt,
		CredentialMaterial: material,
	}, APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte("<"),
		Response: &Response{
			Model:   "<",
			Message: Assistant("safe"),
			Finish:  FinishReason{Reason: "<"},
		},
		Outcome:    apilog.AttemptSuccess,
		ErrorClass: "<",
		Err:        errors.New("<"),
		FinishedAt: startedAt.Add(time.Millisecond),
	})

	assertCredentialExcludedBody(t, record.Request.Body)
	assertCredentialExcludedBody(t, record.Response.Body)
	if got := record.Request.Headers; len(got) != 2 || got["X-Count"][0] != strings.Repeat("x", 17) || got["X-Safe"][0] != "visible" {
		t.Fatalf("request headers = %#v, want safe content and numeric coincidence retained", got)
	}
	if record.Response.Model != "" || record.Response.FinishReason != "" || record.ErrorClass != "" || record.ErrorMessage != "" {
		t.Fatalf("JSON-escaped credential collision survived: response=%+v error=(%q, %q)", record.Response, record.ErrorClass, record.ErrorMessage)
	}

}

func TestBuildAPIAttemptRecordRendersErrorsOnceAndContainsPanic(t *testing.T) {
	err := &countingAPILogError{text: "provider failure"}
	record := builtAPIAttemptRecordWithError(t, err)
	if err.calls != 1 {
		t.Fatalf("Error() calls = %d, want 1", err.calls)
	}
	if record.ErrorMessage != err.text {
		t.Fatalf("error message = %q, want %q", record.ErrorMessage, err.text)
	}

	panicking := &countingAPILogError{panicOnError: true}
	record = builtAPIAttemptRecordWithError(t, panicking)
	if panicking.calls != 1 {
		t.Fatalf("panicking Error() calls = %d, want 1", panicking.calls)
	}
	if record.ErrorMessage == "" {
		t.Fatal("panic-safe durable error text is empty")
	}
}

func TestBuildAPIAttemptRecordReplacesOversizedErrorText(t *testing.T) {
	const (
		maxErrorTextBytes = 64 << 10
		omittedErrorText  = "API-log error details omitted because they exceed the size limit"
	)
	rawText := strings.Repeat("s", maxErrorTextBytes+1)
	err := &countingAPILogError{text: rawText}

	record := builtAPIAttemptRecordWithError(t, err)

	if err.calls != 1 {
		t.Fatalf("Error() calls = %d, want 1", err.calls)
	}
	if record.ErrorMessage != omittedErrorText {
		t.Fatalf("error message bytes = %d, want generic omission text", len(record.ErrorMessage))
	}
	if strings.Contains(record.ErrorMessage, rawText[:32]) {
		t.Fatal("oversized error prefix survived in durable evidence")
	}
	if _, err := apilog.MarshalRecord(record); err != nil {
		t.Fatalf("MarshalRecord(): %v", err)
	}
}

func TestBuildAPIAttemptRecordClassifiesExplicitAndUnknownResults(t *testing.T) {
	for _, tt := range []struct {
		name    string
		outcome apilog.AttemptOutcomeClass
		err     error
		want    apilog.AttemptOutcomeClass
	}{
		{name: "provider rejection", outcome: apilog.AttemptProviderReject, err: errors.New("rejected"), want: apilog.AttemptProviderReject},
		{name: "decode failure", outcome: apilog.AttemptDecodeFail, err: errors.New("decode"), want: apilog.AttemptDecodeFail},
		{name: "provider timeout", outcome: apilog.AttemptProviderTimeout, err: errors.New("timeout"), want: apilog.AttemptProviderTimeout},
		{name: "unknown error", err: errors.New("unknown"), want: apilog.AttemptTransportFail},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := validBuiltAPIAttemptRecord(t, APILogCredentialMaterial{})
			record = buildAPIAttemptRecord(record.AttemptGroupID, record.AttemptID, record.AttemptIndex, APIAttemptMeta{
				ProviderInstance: record.ProviderInstance,
				RequestModel:     record.RequestModel,
				Method:           record.Request.Method,
				Endpoint:         record.Request.Endpoint,
				StartedAt:        record.Timestamp,
			}, APIAttemptResult{
				Outcome:    tt.outcome,
				Err:        tt.err,
				FinishedAt: record.Timestamp.Add(time.Millisecond),
			})
			if record.Outcome != tt.want {
				t.Fatalf("outcome = %q, want %q", record.Outcome, tt.want)
			}
		})
	}
}

func TestSettleResultUsesOwningContextInsteadOfErrorGraph(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_explicit_context")
	ctx := WithAPIAttemptSink(context.Background(), sink)
	group.SettleResult(ctx, errors.Join(errors.New("outer"), context.Canceled))
	_, settlements, _ := sink.snapshot()
	if got := settlements[0].Outcome; got != apilog.AttemptTransportFail {
		t.Fatalf("live-context settlement = %q, want %q", got, apilog.AttemptTransportFail)
	}

	sink = &recordingAPIAttemptSink{}
	group = NewAPIAttemptGroup("ag_canceled_context")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = WithAPIAttemptSink(canceled, sink)
	group.SettleResult(ctx, errors.New("unknown"))
	_, settlements, _ = sink.snapshot()
	if got := settlements[0].Outcome; got != apilog.AttemptCallerCancel {
		t.Fatalf("canceled-context settlement = %q, want %q", got, apilog.AttemptCallerCancel)
	}
}

type countingAPILogError struct {
	text         string
	calls        int
	panicOnError bool
}

func (e *countingAPILogError) Error() string {
	e.calls++
	if e.panicOnError {
		panic("untrusted Error panic")
	}
	return e.text
}

func jsonStringContent(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded[1 : len(encoded)-1])
}

func assertCredentialExcludedBody(t *testing.T, body apilog.EncodedBody) {
	t.Helper()
	if body.Exact || !body.CredentialValuesExcluded || body.Encoding != "" || body.Data != "" || body.ByteCount != 0 {
		t.Fatalf("credential-bearing body = %+v, want omitted inexact body", body)
	}
}

func validBuiltAPIAttemptRecord(t *testing.T, material APILogCredentialMaterial) apilog.APIAttemptRecord {
	t.Helper()
	startedAt := time.Unix(20, 0).UTC()
	return buildAPIAttemptRecord("ag_build_test", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance:   "provider",
		RequestModel:       "model",
		Method:             http.MethodPost,
		Endpoint:           "https://provider.test/v1/responses",
		RequestBody:        []byte("safe request"),
		StartedAt:          startedAt,
		CredentialMaterial: material,
	}, APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte("safe response"),
		Response: &Response{
			Model:   "model",
			Message: Assistant("safe"),
			Finish:  FinishReason{Reason: FinishReasonStop},
		},
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: startedAt.Add(time.Millisecond),
	})
}

func builtAPIAttemptRecordWithError(t *testing.T, err error) apilog.APIAttemptRecord {
	t.Helper()
	startedAt := time.Unix(30, 0).UTC()
	return buildAPIAttemptRecord("ag_error_test", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance: "provider",
		RequestModel:     "model",
		Method:           http.MethodPost,
		Endpoint:         "https://provider.test/v1/responses",
		StartedAt:        startedAt,
	}, APIAttemptResult{
		Outcome:    apilog.AttemptTransportFail,
		Err:        err,
		FinishedAt: startedAt.Add(time.Millisecond),
	})
}

// SetWireRequestMetadata replaces the preliminary request snapshot with what
// the transport wrote, and its credential material both scrubs this attempt's
// record and carries into later attempts of the group.
func TestSetWireRequestMetadataOnActiveAttempt(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_wire_meta")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	attempt.SetWireRequestMetadata(http.MethodPut, "https://provider.test/v1/updated",
		http.Header{"X-After": []string{"value"}, "X-Wire-Credential": []string{"bearer wire-secret"}},
		NewAPILogCredentialMaterial(nil, nil, "wire-secret"))
	attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))

	attempts, _, _ := sink.snapshot()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Request.Method != http.MethodPut {
		t.Fatalf("method = %q, want %q", attempts[0].Request.Method, http.MethodPut)
	}
	if attempts[0].Request.Endpoint != "https://provider.test/v1/updated" {
		t.Fatalf("endpoint = %q, want updated", attempts[0].Request.Endpoint)
	}
	want := apilog.EncodedHeader{"X-After": []string{"value"}}
	if !reflect.DeepEqual(attempts[0].Request.Headers, want) {
		t.Fatalf("headers = %v, want %v (the credential-bearing header scrubbed)", attempts[0].Request.Headers, want)
	}

	next := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	defer next.Complete(testAPIAttemptResult(startedAt.Add(time.Second), apilog.AttemptSuccess, nil))
	if !slices.Contains(next.CredentialMaterial().Values, "wire-secret") {
		t.Fatalf("next attempt's credential material = %+v, want the wire credential", next.CredentialMaterial())
	}
}

// An attempt without both a group and a sink is inert: it ignores every
// update a transport would make to an active one.
func TestInertAPIAttemptIgnoresUpdates(t *testing.T) {
	t.Run("credential material", func(t *testing.T) {
		a := &APIAttempt{meta: APIAttemptMeta{CredentialMaterial: NewAPILogCredentialMaterial([]string{"X-Key"}, nil, "secret")}}
		if got := a.CredentialMaterial(); !reflect.DeepEqual(got, APILogCredentialMaterial{}) {
			t.Fatalf("inactive CredentialMaterial = %+v, want empty", got)
		}
	})
	t.Run("request body", func(t *testing.T) {
		wantBody := []byte("original")
		a := &APIAttempt{meta: APIAttemptMeta{RequestBody: append([]byte(nil), wantBody...), RequestBodyInexact: true}}
		a.SetRequestBody([]byte("body"), true)
		if !reflect.DeepEqual(a.meta.RequestBody, wantBody) || !a.meta.RequestBodyInexact {
			t.Fatalf("inactive SetRequestBody mutated metadata to body %q, inexact %v", a.meta.RequestBody, a.meta.RequestBodyInexact)
		}
	})
	t.Run("merged credential material", func(t *testing.T) {
		want := NewAPILogCredentialMaterial([]string{"X-Original"}, nil, "original-secret")
		a := &APIAttempt{meta: APIAttemptMeta{CredentialMaterial: want}}
		a.MergeCredentialMaterial(NewAPILogCredentialMaterial(nil, nil, "secret"))
		if !reflect.DeepEqual(a.meta.CredentialMaterial, want) {
			t.Fatalf("inactive MergeCredentialMaterial mutated metadata to %+v, want %+v", a.meta.CredentialMaterial, want)
		}
	})
	t.Run("wire request metadata", func(t *testing.T) {
		a := &APIAttempt{}
		a.SetWireRequestMetadata(http.MethodGet, "https://noop.test", http.Header{}, APILogCredentialMaterial{})
		if a.Active() || a.meta.Method != "" {
			t.Fatalf("inert attempt took wire metadata: %+v", a.meta)
		}
	})
}

// The attempt helpers are called on every provider path, coordinated or not,
// so each tolerates a missing context, group or error.
func TestAPIAttemptHelpersTolerateMissingCoordination(t *testing.T) {
	var noContext context.Context
	t.Run("context active without a context", func(t *testing.T) {
		if APIAttemptContextActive(noContext) {
			t.Fatal("APIAttemptContextActive(nil) = true, want false")
		}
	})
	t.Run("group lookup without a context", func(t *testing.T) {
		if got := apiAttemptGroupFromContext(noContext); got != nil {
			t.Fatalf("apiAttemptGroupFromContext(nil) = %v, want nil", got)
		}
	})
	t.Run("credential material without a context", func(t *testing.T) {
		if _, ok := apiLogCredentialMaterialFromContext(noContext); ok {
			t.Fatal("apiLogCredentialMaterialFromContext(nil) reports material")
		}
	})
	t.Run("begin without a group", func(t *testing.T) {
		if BeginAPIAttempt(context.Background(), testAPIAttemptMeta(time.Unix(1_700_000_000, 0).UTC())).Active() {
			t.Fatal("attempt with no group is active")
		}
	})
	t.Run("settle without a group", func(t *testing.T) {
		var group *APIAttemptGroup
		group.Settle(context.Background(), apilog.AttemptSuccess)
	})
	t.Run("wait without a group", func(t *testing.T) {
		WaitForPriorAPIAttempts(context.Background())
	})
	t.Run("sanitize without an error", func(t *testing.T) {
		if err := sanitizeAPILogError(nil, APILogCredentialMaterial{}); err != nil {
			t.Fatalf("sanitizeAPILogError(nil) = %v, want nil", err)
		}
	})
	t.Run("absent optional int", func(t *testing.T) {
		if got := optionalAPILogInt(42, false); got != nil {
			t.Fatalf("optionalAPILogInt(42, false) = %v, want nil", got)
		}
	})
}

// TestBeginAPIAttemptAfterSettlingReturnsInert covers the branch where
// group.settling is true, so BeginAPIAttempt returns an inert attempt.
func TestBeginAPIAttemptAfterSettlingReturnsInert(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_settling")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	first := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	first.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	group.Settle(ctx, apilog.AttemptSuccess)

	// After settlement, a new attempt must be inert.
	late := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	if late.Active() {
		t.Fatal("attempt begun after settlement should be inert")
	}
	late.Complete(testAPIAttemptResult(startedAt.Add(time.Second), apilog.AttemptSuccess, nil))

	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 1 || len(settlements) != 1 {
		t.Fatalf("attempts/settlements = %d/%d, want 1/1", len(attempts), len(settlements))
	}
}

// TestCanonicalAPIAttemptOutcomeDefaultSuccess covers the default branch where
// the outcome is empty, there is no error, and the function returns AttemptSuccess.
func TestCanonicalAPIAttemptOutcomeDefaultSuccess(t *testing.T) {
	if got := canonicalAPIAttemptOutcome(APIAttemptResult{}); got != apilog.AttemptSuccess {
		t.Fatalf("canonicalAPIAttemptOutcome(empty) = %q, want %q", got, apilog.AttemptSuccess)
	}
}

// TestRecordFailureWithNilErr covers the branch in recordFailure where
// failure.Err is nil, so the observer is never invoked.
func TestRecordFailureWithNilErr(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	observerCalled := false
	sink.failureObserverFn = func(APILogFailure) { observerCalled = true }
	group := NewAPIAttemptGroup("ag_nil_failure_err")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	group.Settle(ctx, apilog.AttemptSuccess)

	group.recordFailure(APILogFailure{Operation: "test", AttemptGroupID: group.ID})
	if observerCalled {
		t.Fatal("observer called for nil-error failure")
	}
}

// TestMergeCredentialMaterialCoversBothSides exercises APILogCredentialMaterial.Merge
// with non-empty left and right to cover the merge loops.
func TestMergeCredentialMaterialCoversBothSides(t *testing.T) {
	left := NewAPILogCredentialMaterial([]string{"X-Left"}, []string{"left_q"}, "left-secret")
	right := NewAPILogCredentialMaterial([]string{"X-Right"}, []string{"right_q"}, "right-secret")
	merged := left.Merge(right)
	if _, ok := merged.HeaderNames["X-Left"]; !ok {
		t.Fatal("merged missing left header name")
	}
	if _, ok := merged.HeaderNames["X-Right"]; !ok {
		t.Fatal("merged missing right header name")
	}
	if _, ok := merged.QueryNames["left_q"]; !ok {
		t.Fatal("merged missing left query name")
	}
	if _, ok := merged.QueryNames["right_q"]; !ok {
		t.Fatal("merged missing right query name")
	}
	for _, secret := range []string{"left-secret", "right-secret"} {
		if !slices.Contains(merged.Values, secret) {
			t.Fatalf("merged missing value %q", secret)
		}
	}
}

// A header whose name itself carries a credential value is dropped from the
// credential-free clone.
func TestCloneCredentialFreeHTTPHeaderDropsACredentialName(t *testing.T) {
	material := NewAPILogCredentialMaterial(nil, nil, "secret")
	header := http.Header{
		"X-Safe":          []string{"ok"},
		"X-secret-header": []string{"value"},
	}
	cloned := cloneCredentialFreeHTTPHeader(header, material.patterns, material.secretNames)
	want := apilog.EncodedHeader{"X-Safe": []string{"ok"}}
	if !reflect.DeepEqual(cloned, want) {
		t.Fatalf("credential-free headers = %#v, want %#v", cloned, want)
	}
}
