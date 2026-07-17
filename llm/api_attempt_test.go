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
	if got := record.Request.Headers["X-Visible"]; len(record.Request.Headers) != 1 || len(got) != 1 || got[0] != "visible" {
		t.Fatalf("request headers = %#v, want only X-Visible", record.Request.Headers)
	}
	if record.ErrorClass != "" || record.ErrorMessage != "" {
		t.Fatalf("durable error = (%q, %q), want omitted", record.ErrorClass, record.ErrorMessage)
	}
	if record.Response.Model != "" || record.Response.FinishReason != "" {
		t.Fatalf("response strings = (%q, %q), want omitted", record.Response.Model, record.Response.FinishReason)
	}
	if record.Response.Usage.InputTokens != 0 || record.Response.Usage.OutputTokens != 45 {
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

func TestMarshalRecordRejectsCredentialInsertedAfterRecordBuild(t *testing.T) {
	const secret = "post-build-credential-sentinel"
	record := validBuiltAPIAttemptRecord(t, NewAPILogCredentialMaterial(nil, nil, secret))
	record.Response.Model = secret
	if _, err := apilog.MarshalRecord(record); err == nil {
		t.Fatal("MarshalRecord() accepted credential-bearing provider evidence")
	}
}

func TestMarshalRecordIgnoresCredentialOverlapWithClosedStructuralFields(t *testing.T) {
	record := validBuiltAPIAttemptRecord(t, NewAPILogCredentialMaterial(nil, nil, "api"))
	line, err := apilog.MarshalRecord(record)
	if err != nil {
		t.Fatalf("MarshalRecord() rejected structural overlap: %v", err)
	}
	if !strings.Contains(string(line), `"kind":"api_attempt"`) {
		t.Fatalf("record does not contain expected structural overlap: %s", line)
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
