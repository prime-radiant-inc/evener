package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
	apilog "primeradiant.com/serf/llm/apilog"
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
		secret, modelSecret, "123",
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
	if record.Response.Usage.InputTokens != nil || record.Response.Usage.OutputTokens == nil || *record.Response.Usage.OutputTokens != 45 {
		t.Fatalf("usage = %+v, want matching value omitted and safe value retained", record.Response.Usage)
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

func TestBuildAPIAttemptRecordOmitsZeroNumericCredentialEvidence(t *testing.T) {
	zero := 0
	startedAt := time.Unix(25, 0).UTC()
	record := buildAPIAttemptRecord("ag_zero_numeric_omission", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance:   "provider",
		RequestModel:       "model",
		Method:             http.MethodPost,
		Endpoint:           "https://provider.test/v1/responses",
		RequestBody:        []byte("safe request"),
		StartedAt:          startedAt,
		CredentialMaterial: NewAPILogCredentialMaterial(nil, nil, "0"),
	}, APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte("safe response"),
		Response: &Response{
			Model:   "model",
			Message: Assistant(""),
			Finish:  FinishReason{Reason: FinishReasonStop},
			Usage: Usage{
				CacheReadTokens:  &zero,
				CacheWriteTokens: &zero,
			},
		},
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: startedAt.Add(time.Millisecond),
	})
	line, err := apilog.MarshalRecord(record)
	if err != nil {
		t.Fatalf("MarshalRecord(): %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(line, &object); err != nil {
		t.Fatal(err)
	}
	response := object["response"].(map[string]any)
	usage := response["usage"].(map[string]any)
	for _, field := range []string{"status_code", "text_length", "tool_call_count"} {
		if _, present := response[field]; present {
			t.Fatalf("credential-bearing response field %q persisted: %s", field, line)
		}
	}
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens", "cache_read_tokens", "cache_write_tokens"} {
		if _, present := usage[field]; present {
			t.Fatalf("credential-bearing usage field %q persisted: %s", field, line)
		}
	}
}

func TestBuildAPIAttemptRecordOmitsMatchingNonzeroNumericEvidence(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		container string
		field     string
	}{
		{name: "status", secret: "418", container: "response", field: "status_code"},
		{name: "text length", secret: "7", container: "response", field: "text_length"},
		{name: "tool-call count", secret: "2", container: "response", field: "tool_call_count"},
		{name: "input tokens", secret: "11", container: "usage", field: "input_tokens"},
		{name: "output tokens", secret: "13", container: "usage", field: "output_tokens"},
		{name: "total tokens", secret: "24", container: "usage", field: "total_tokens"},
		{name: "cache-read tokens", secret: "5", container: "usage", field: "cache_read_tokens"},
		{name: "cache-write tokens", secret: "6", container: "usage", field: "cache_write_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := numericEvidenceAttemptRecord(t, tt.secret)
			line, err := apilog.MarshalRecord(record)
			if err != nil {
				t.Fatalf("MarshalRecord(): %v", err)
			}
			var object map[string]any
			if err := json.Unmarshal(line, &object); err != nil {
				t.Fatal(err)
			}
			response := object["response"].(map[string]any)
			container := response
			if tt.container == "usage" {
				container = response["usage"].(map[string]any)
			}
			if _, present := container[tt.field]; present {
				t.Fatalf("credential-bearing numeric field %q persisted: %s", tt.field, line)
			}
		})
	}
}

func TestBuildAPIAttemptRecordOmitsCredentialCollisionsCreatedByCanonicalEncoding(t *testing.T) {
	startedAt := time.Unix(28, 0).UTC()
	material := NewAPILogCredentialMaterial(nil, nil, "/w==", `\u003c`, "17")
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
	if got := record.Request.Headers; len(got) != 1 || got["X-Safe"][0] != "visible" {
		t.Fatalf("request headers = %#v, want only safe evidence", got)
	}
	if record.Response.Model != "" || record.Response.FinishReason != "" || record.ErrorClass != "" || record.ErrorMessage != "" {
		t.Fatalf("JSON-escaped credential collision survived: response=%+v error=(%q, %q)", record.Response, record.ErrorClass, record.ErrorMessage)
	}

	for _, tt := range []struct {
		name   string
		mutate func(*APIAttemptMeta, *APIAttemptResult)
		body   func(apilog.APIAttemptRecord) apilog.EncodedBody
	}{
		{
			name: "request body byte count",
			mutate: func(meta *APIAttemptMeta, _ *APIAttemptResult) {
				meta.RequestBody = []byte(strings.Repeat("x", 17))
			},
			body: func(record apilog.APIAttemptRecord) apilog.EncodedBody { return record.Request.Body },
		},
		{
			name: "response body byte count",
			mutate: func(_ *APIAttemptMeta, result *APIAttemptResult) {
				result.ResponseBody = []byte(strings.Repeat("x", 17))
			},
			body: func(record apilog.APIAttemptRecord) apilog.EncodedBody { return record.Response.Body },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			meta := APIAttemptMeta{
				ProviderInstance:   "provider",
				RequestModel:       "model",
				Method:             http.MethodPost,
				Endpoint:           "https://provider.test/v1/responses",
				RequestBody:        []byte("safe request"),
				StartedAt:          startedAt,
				CredentialMaterial: NewAPILogCredentialMaterial(nil, nil, "17"),
			}
			result := APIAttemptResult{
				StatusCode:   http.StatusOK,
				ResponseBody: []byte("safe response"),
				Response: &Response{
					Model:   "model",
					Message: Assistant("safe"),
					Finish:  FinishReason{Reason: FinishReasonStop},
				},
				Outcome:    apilog.AttemptSuccess,
				FinishedAt: startedAt.Add(time.Millisecond),
			}
			tt.mutate(&meta, &result)
			record := buildAPIAttemptRecord("ag_encoded_count", identifier.MustNewAPIAttemptID(), 1, meta, result)
			assertCredentialExcludedBody(t, tt.body(record))
		})
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

func numericEvidenceAttemptRecord(t *testing.T, secret string) apilog.APIAttemptRecord {
	t.Helper()
	cacheRead, cacheWrite := 5, 6
	startedAt := time.Unix(26, 0).UTC()
	return buildAPIAttemptRecord("ag_numeric_omission", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance:   "provider",
		RequestModel:       "model",
		Method:             http.MethodPost,
		Endpoint:           "https://provider.test/v1/responses",
		RequestBody:        []byte("safe request"),
		StartedAt:          startedAt,
		CredentialMaterial: NewAPILogCredentialMaterial(nil, nil, secret),
	}, APIAttemptResult{
		StatusCode:   http.StatusTeapot,
		ResponseBody: []byte("safe response"),
		Response: &Response{
			Model: "model",
			Message: Message{Role: RoleAssistant, Content: []ContentPart{
				{Kind: ContentText, Text: "1234567"},
				{Kind: ContentToolCall, ToolCall: &ToolCallData{Name: "one"}},
				{Kind: ContentToolCall, ToolCall: &ToolCallData{Name: "two"}},
			}},
			Finish: FinishReason{Reason: FinishReasonStop},
			Usage: Usage{
				InputTokens:      11,
				OutputTokens:     13,
				TotalTokens:      24,
				CacheReadTokens:  &cacheRead,
				CacheWriteTokens: &cacheWrite,
			},
		},
		Outcome:    apilog.AttemptProviderReject,
		FinishedAt: startedAt.Add(time.Millisecond),
	})
}
