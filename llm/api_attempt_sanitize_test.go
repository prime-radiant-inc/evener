package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	apilog "primeradiant.com/evener/llm/apilog"
)

func TestSanitizeRequestForAPILogExcludesCredentialMaterial(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://alice:password@example.test/v1?keep=visible&api_key=secret+token&opaque=secret+token&ordered=first&ordered=second", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{
		"x-visible":         {"first", "second"},
		"X-Gateway-Key":     {"secret token"},
		"X-Contains-Token":  {"prefix secret token suffix"},
		"Authorization":     {"Bearer built-in-secret"},
		"Trailer":           {"X-Gateway-Key, X-Visible-Trailer"},
		"X-Visible-Trailer": {"visible trailer"},
	}
	material := NewAPILogCredentialMaterial(
		[]string{"x-gateway-key"},
		[]string{"API_KEY"},
		"alice", "password", "secret token", "built-in-secret",
	)

	endpoint, headers := SanitizeRequestForAPILog(req, material)
	if endpoint != "https://example.test/v1" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	wantHeaders := map[string][]string{
		"Host":              {"example.test"},
		"x-visible":         {"first", "second"},
		"Trailer":           {"X-Visible-Trailer"},
		"X-Visible-Trailer": {"visible trailer"},
	}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("headers = %#v, want %#v", headers, wantHeaders)
	}
	for _, secret := range []string{"alice", "password", "secret+token", "secret token", "built-in-secret"} {
		if strings.Contains(endpoint, secret) {
			t.Fatalf("endpoint contains credential %q: %s", secret, endpoint)
		}
	}
}

func TestSanitizeRequestForAPILogRejectsInvalidAndOpaqueEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
	}{
		{name: "missing host", endpoint: "https:/v1/responses"},
		{name: "opaque URL", endpoint: "mailto:provider@example.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := url.Parse(tt.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, _ := SanitizeRequestForAPILog(&http.Request{URL: parsed}, APILogCredentialMaterial{})
			if endpoint != "" {
				t.Fatalf("endpoint = %q, want invalid provenance omitted", endpoint)
			}
		})
	}
}

func TestSanitizeErrorForAPILogRemovesRawAndEscapedCredentials(t *testing.T) {
	secret := "alpha/beta value"
	material := NewAPILogCredentialMaterial(nil, nil, secret)
	text := strings.Join([]string{secret, url.QueryEscape(secret), url.PathEscape(secret)}, " | ")
	got := SanitizeErrorForAPILog(text, material)
	if got != "" {
		t.Fatalf("SanitizeErrorForAPILog() = %q, want whole-field omission", got)
	}
	for _, leaked := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
		if strings.Contains(got, leaked) {
			t.Fatalf("sanitized error contains %q: %q", leaked, got)
		}
	}
}

func TestSanitizeErrorForAPILogRemovesCredentialValueBeforeContainedName(t *testing.T) {
	const secret = "prefix/token/suffix"
	material := NewAPILogCredentialMaterial(nil, []string{"token"}, secret)
	got := SanitizeErrorForAPILog("provider echoed "+secret, material)
	if got != "" {
		t.Fatalf("credential-bearing error was not omitted: %q", got)
	}
}

func TestAPILogCredentialMaterialForRequestIncludesExactStructuredCredentialVariants(t *testing.T) {
	const (
		rawQuerySecret = "query%2fcredential%2fsentinel"
		authSecret     = "authorization-payload-sentinel"
		cookieSecret   = "cookie-subvalue-sentinel"
	)
	req, err := http.NewRequest(http.MethodGet, "https://provider.test/final?access_token="+rawQuerySecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+authSecret)
	req.Header.Set("Cookie", "session="+cookieSecret+"; visible=ordinary")

	material := APILogCredentialMaterialForRequest(req, APILogCredentialMaterial{})
	got := SanitizeErrorForAPILog(strings.Join([]string{rawQuerySecret, authSecret, cookieSecret}, " | "), material)
	for _, secret := range []string{rawQuerySecret, authSecret, cookieSecret} {
		if strings.Contains(got, secret) {
			t.Fatalf("structured request credential %q remained in sanitized error: %q", secret, got)
		}
	}
}

func TestBuildAPIAttemptRecordSanitizesPersistedError(t *testing.T) {
	secret := "credential-sentinel"
	record := buildAPIAttemptRecord("ag_test", "aa_test", 1, APIAttemptMeta{
		StartedAt:          time.Unix(1, 0),
		CredentialMaterial: NewAPILogCredentialMaterial(nil, nil, secret),
	}, APIAttemptResult{
		Err:        errors.New("provider echoed " + secret),
		Outcome:    apilog.AttemptTransportFail,
		FinishedAt: time.Unix(2, 0),
	})
	if record.ErrorMessage != "" {
		t.Fatalf("persisted credential-bearing error = %q, want omitted", record.ErrorMessage)
	}
}

func TestBuildAPIAttemptRecordSanitizesCredentialNamesButPreservesOrdinaryNames(t *testing.T) {
	const (
		credentialHeader = "X-Private-Gateway-Sentinel"
		credentialQuery  = "signed_secret_parameter"
		ordinaryHeader   = "X-Visible-Debug-Sentinel"
	)
	record := buildAPIAttemptRecord("ag_test", "aa_test", 1, APIAttemptMeta{
		StartedAt: time.Unix(1, 0),
		CredentialMaterial: NewAPILogCredentialMaterial(
			[]string{credentialHeader},
			[]string{credentialQuery},
		),
	}, APIAttemptResult{
		Err:        errors.New(strings.Join([]string{credentialHeader, credentialQuery, ordinaryHeader}, " | ")),
		Outcome:    apilog.AttemptTransportFail,
		FinishedAt: time.Unix(2, 0),
	})
	for _, credentialName := range []string{credentialHeader, credentialQuery} {
		if strings.Contains(record.ErrorMessage, credentialName) {
			t.Fatalf("persisted error contains credential name %q: %q", credentialName, record.ErrorMessage)
		}
	}
	if record.ErrorMessage != "" {
		t.Fatalf("credential-bearing error = %q, want whole-field omission", record.ErrorMessage)
	}
}

type credentialFailureSink struct {
	err      error
	observed []APILogFailure
}

func (s *credentialFailureSink) AppendAttempt(context.Context, apilog.APIAttemptRecord) error {
	return s.err
}

func (*credentialFailureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *credentialFailureSink) apiLogFailureObserver() func(APILogFailure) {
	return func(failure APILogFailure) {
		s.observed = append(s.observed, failure)
	}
}

func TestAPIAttemptAppendWarningSanitizesOwnedCredentialMaterial(t *testing.T) {
	const secret = "credential-warning-sentinel"
	sink := &credentialFailureSink{err: errors.New("sync failed for " + secret)}
	ctx := WithAPIAttemptSink(
		WithAPIAttemptGroup(context.Background(), NewAPIAttemptGroup("ag_warning_sanitize")),
		sink,
	)
	attempt := BeginAPIAttempt(ctx, APIAttemptMeta{
		StartedAt:          time.Now(),
		CredentialMaterial: NewAPILogCredentialMaterial(nil, nil, secret),
	})
	attempt.Complete(APIAttemptResult{
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: time.Now(),
	})

	if len(sink.observed) != 1 {
		t.Fatalf("failure observations = %d, want 1", len(sink.observed))
	}
	if got := sink.observed[0].Err.Error(); strings.Contains(got, secret) {
		t.Fatalf("failure warning leaked credential: %q", got)
	}
}

func TestAPILoggerSyncFailureWarningUsesAttemptCredentialSanitizationExactlyOnce(t *testing.T) {
	const secret = "sync-warning-credential-sentinel"
	syncErr := errors.New("sync failed for " + secret)
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldSync := apiLogFileSync
	apiLogFileSync = func(*os.File) error { return syncErr }
	t.Cleanup(func() {
		apiLogFileSync = oldSync
		_ = logger.Close()
	})
	var observed []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) {
		observed = append(observed, failure)
	})
	ctx := WithAPIAttemptSink(
		WithAPIAttemptGroup(context.Background(), NewAPIAttemptGroup("ag_sync_warning_sanitize")),
		logger,
	)
	startedAt := time.Now()
	meta := testAPIAttemptMeta(startedAt)
	meta.CredentialMaterial = NewAPILogCredentialMaterial(nil, nil, secret)
	BeginAPIAttempt(ctx, meta).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))

	if len(observed) != 1 {
		t.Fatalf("sync failure observer calls = %d, want exactly 1: %+v", len(observed), observed)
	}
	if got := observed[0].Err.Error(); strings.Contains(got, secret) {
		t.Fatalf("sync failure warning leaked credential: %q", got)
	}
	if errors.Is(observed[0].Err, syncErr) || errors.Unwrap(observed[0].Err) != nil {
		t.Fatalf("sanitized sync warning retained storage error behavior: %v", observed[0].Err)
	}

	apiLogFileSync = oldSync
	assertDetachedAPILogError(t, logger.Close(), syncErr)
}

func TestAPILoggerSettlementSyncFailureUsesGroupCredentialSanitizationExactlyOnce(t *testing.T) {
	const secret = "settlement-sync-credential-sentinel"
	syncErr := errors.New("settlement sync failed for " + secret)
	logger, err := NewAPILogger(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	oldSync := apiLogFileSync
	t.Cleanup(func() {
		apiLogFileSync = oldSync
		_ = logger.Close()
	})
	var observed []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) {
		observed = append(observed, failure)
	})
	group := NewAPIAttemptGroup("ag_settlement_sync_warning_sanitize")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), logger)
	startedAt := time.Now()
	meta := testAPIAttemptMeta(startedAt)
	meta.CredentialMaterial = NewAPILogCredentialMaterial(nil, nil, secret)
	BeginAPIAttempt(ctx, meta).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	apiLogFileSync = func(*os.File) error { return syncErr }
	group.Settle(ctx, apilog.AttemptSuccess)

	if len(observed) != 1 {
		t.Fatalf("settlement sync failure observer calls = %d, want exactly 1: %+v", len(observed), observed)
	}
	if observed[0].Operation != "append_settlement" {
		t.Fatalf("settlement sync failure operation = %q, want append_settlement", observed[0].Operation)
	}
	if got := observed[0].Err.Error(); strings.Contains(got, secret) {
		t.Fatalf("settlement sync failure warning leaked credential: %q", got)
	}
	if errors.Is(observed[0].Err, syncErr) || errors.Unwrap(observed[0].Err) != nil {
		t.Fatalf("sanitized settlement warning retained storage error behavior: %v", observed[0].Err)
	}

	apiLogFileSync = oldSync
	assertDetachedAPILogError(t, logger.Close(), syncErr)
}

func TestAPIAttemptAppendWarningSanitizesCredentialNamesButPreservesOrdinaryNames(t *testing.T) {
	const (
		credentialHeader = "X-Private-Warning-Sentinel"
		credentialQuery  = "warning_secret_parameter"
		ordinaryHeader   = "X-Visible-Warning-Sentinel"
	)
	storageErr := errors.New(strings.Join([]string{credentialHeader, credentialQuery, ordinaryHeader}, " | "))
	sink := &credentialFailureSink{err: storageErr}
	ctx := WithAPIAttemptSink(
		WithAPIAttemptGroup(context.Background(), NewAPIAttemptGroup("ag_warning_name_sanitize")),
		sink,
	)
	attempt := BeginAPIAttempt(ctx, APIAttemptMeta{
		StartedAt: time.Now(),
		CredentialMaterial: NewAPILogCredentialMaterial(
			[]string{credentialHeader},
			[]string{credentialQuery},
		),
	})
	attempt.Complete(APIAttemptResult{
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: time.Now(),
	})

	if len(sink.observed) != 1 {
		t.Fatalf("failure observations = %d, want 1", len(sink.observed))
	}
	got := sink.observed[0].Err.Error()
	for _, credentialName := range []string{credentialHeader, credentialQuery} {
		if strings.Contains(got, credentialName) {
			t.Fatalf("failure warning contains credential name %q: %q", credentialName, got)
		}
	}
	if strings.Contains(got, ordinaryHeader) {
		t.Fatalf("credential-bearing warning was partially preserved: %q", got)
	}
	if errors.Is(sink.observed[0].Err, storageErr) || errors.Unwrap(sink.observed[0].Err) != nil {
		t.Fatalf("sanitized warning retained storage error behavior: %v", sink.observed[0].Err)
	}
}

func TestAPILogErrorSanitizationDetachesOriginalErrorBehavior(t *testing.T) {
	const secret = "raw-error-secret-sentinel"
	cause := errors.New(secret)
	raw := &recoverableAPILogError{text: "storage failed for " + secret, cause: cause}
	flat := sanitizeAPILogError(raw, NewAPILogCredentialMaterial(nil, nil, secret))

	if raw.calls != 1 {
		t.Fatalf("raw Error() calls = %d, want 1", raw.calls)
	}
	if flat == nil || strings.Contains(flat.Error(), secret) {
		t.Fatalf("sanitized error leaked secret text: %v", flat)
	}
	if errors.Is(flat, raw) || errors.Is(flat, cause) || errors.Unwrap(flat) != nil {
		t.Fatalf("sanitized error retained original graph: %v", flat)
	}
	if recovered, ok := errors.AsType[*recoverableAPILogError](flat); ok {
		t.Fatalf("sanitized error recovered raw object: %#v", recovered)
	}

	observed := sanitizeAPILogError(observedAPILogError{text: raw.Error()}, NewAPILogCredentialMaterial(nil, nil, secret))
	if _, ok := observed.(apiLogObservedFailure); !ok {
		t.Fatalf("sanitized observed error lost marker: %T", observed)
	}
	if errors.Unwrap(observed) != nil || errors.Is(observed, raw) || errors.Is(observed, cause) {
		t.Fatalf("sanitized observed error retained original graph: %v", observed)
	}
}

type recoverableAPILogError struct {
	text  string
	cause error
	calls int
}

func (e *recoverableAPILogError) Error() string {
	e.calls++
	return e.text
}

func (e *recoverableAPILogError) Unwrap() error { return e.cause }

func TestAPIAttemptAppendWarningSanitizationPreservesObservedMarker(t *testing.T) {
	const credentialHeader = "X-Private-Observed-Sentinel"
	sink := &credentialFailureSink{
		err: observedAPILogError{text: "sync failed for " + credentialHeader},
	}
	ctx := WithAPIAttemptSink(
		WithAPIAttemptGroup(context.Background(), NewAPIAttemptGroup("ag_warning_observed_marker")),
		sink,
	)
	BeginAPIAttempt(ctx, APIAttemptMeta{
		StartedAt:          time.Now(),
		CredentialMaterial: NewAPILogCredentialMaterial([]string{credentialHeader}, nil),
	}).Complete(APIAttemptResult{
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: time.Now(),
	})

	if len(sink.observed) != 0 {
		t.Fatalf("already-observed sanitized failure was reported again: %+v", sink.observed)
	}
}

func TestClassifyAPIAttemptOutcomeTimeoutOwnership(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	callerDeadline, callerDeadlineCancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer callerDeadlineCancel()
	adapterDeadline, adapterDeadlineCancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer adapterDeadlineCancel()

	for _, tc := range []struct {
		name         string
		owner        APIAttemptContextOwnership
		decodeErr    error
		transportErr error
	}{
		{
			name:         "explicit caller cancellation",
			owner:        APIAttemptContextOwnership{Parent: canceled, Attempt: canceled},
			transportErr: context.Canceled,
		},
		{
			name:         "caller deadline",
			owner:        APIAttemptContextOwnership{Parent: callerDeadline, Attempt: callerDeadline},
			transportErr: context.DeadlineExceeded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAPIAttemptOutcome(tc.owner, 0, nil, tc.decodeErr, tc.transportErr); got != apilog.AttemptCallerCancel {
				t.Fatalf("outcome = %q, want %q", got, apilog.AttemptCallerCancel)
			}
		})
	}

	for _, tc := range []struct {
		name         string
		owner        APIAttemptContextOwnership
		decodeErr    error
		transportErr error
	}{
		{
			name:         "derived adapter deadline",
			owner:        APIAttemptContextOwnership{Parent: context.Background(), Attempt: adapterDeadline, TimeoutSource: APITimeoutAdapter},
			transportErr: context.DeadlineExceeded,
		},
		{
			name:         "response header timeout",
			owner:        APIAttemptContextOwnership{Parent: context.Background(), Attempt: context.Background(), TimeoutSource: APITimeoutResponseHeader},
			transportErr: errors.New("response header timeout"),
		},
		{
			name:      "SSE read timeout",
			owner:     APIAttemptContextOwnership{Parent: context.Background(), Attempt: context.Background(), TimeoutSource: APITimeoutSSERead},
			decodeErr: errors.New("stream read timeout"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAPIAttemptOutcome(tc.owner, 0, nil, tc.decodeErr, tc.transportErr); got != apilog.AttemptProviderTimeout {
				t.Fatalf("outcome = %q, want %q", got, apilog.AttemptProviderTimeout)
			}
		})
	}
}

func TestClassifyAPIAttemptOutcomeNonTimeouts(t *testing.T) {
	owner := APIAttemptContextOwnership{Parent: context.Background(), Attempt: context.Background()}
	for _, tc := range []struct {
		name         string
		status       int
		decodeErr    error
		transportErr error
		want         apilog.AttemptOutcomeClass
	}{
		{name: "transport failure", transportErr: errors.New("round trip"), want: apilog.AttemptTransportFail},
		{name: "provider rejection", status: http.StatusUnauthorized, want: apilog.AttemptProviderReject},
		{name: "decode failure", status: http.StatusOK, decodeErr: errors.New("decode"), want: apilog.AttemptDecodeFail},
		{name: "success", status: http.StatusOK, want: apilog.AttemptSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAPIAttemptOutcome(owner, tc.status, nil, tc.decodeErr, tc.transportErr); got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyAPIAttemptOutcomeDoesNotInferCallerCancellationFromSyntheticEvidence(t *testing.T) {
	owner := APIAttemptContextOwnership{Parent: context.Background(), Attempt: context.Background()}
	for _, testCase := range []struct {
		name         string
		decodeErr    error
		transportErr error
		want         apilog.AttemptOutcomeClass
	}{
		{
			name:      "decode error wraps canceled while attempt is live",
			decodeErr: fmt.Errorf("provider decode aborted: %w", context.Canceled),
			want:      apilog.AttemptDecodeFail,
		},
		{
			name:         "transport error wraps canceled while attempt is live",
			transportErr: fmt.Errorf("provider transport aborted: %w", context.Canceled),
			want:         apilog.AttemptTransportFail,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ClassifyAPIAttemptOutcome(owner, http.StatusOK, nil, testCase.decodeErr, testCase.transportErr); got != testCase.want {
				t.Fatalf("live-attempt outcome = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A blank header or query name names nothing, so it is not recorded.
func TestNewAPILogCredentialMaterialSkipsBlankNames(t *testing.T) {
	blank := []string{"  ", ""}
	if m := NewAPILogCredentialMaterial(blank, nil, "secret"); len(m.HeaderNames) != 0 {
		t.Fatalf("HeaderNames = %v, want empty", m.HeaderNames)
	}
	if m := NewAPILogCredentialMaterial(nil, blank, "secret"); len(m.QueryNames) != 0 {
		t.Fatalf("QueryNames = %v, want empty", m.QueryNames)
	}
}

func TestNewAPILogCredentialMaterialDuplicateValue(t *testing.T) {
	m := NewAPILogCredentialMaterial(nil, nil, "secret", "secret", "other")
	if len(m.Values) != 2 {
		t.Fatalf("Values = %v, want 2 (secret, other)", m.Values)
	}
}

func TestNewAPILogCredentialMaterialSkipsAnEmptyValue(t *testing.T) {
	material := NewAPILogCredentialMaterial(nil, nil, "", "credential-secret", "")
	want := APILogCredentialMaterial{
		Values:      []string{"credential-secret"},
		secretNames: []string{},
		patterns:    []string{"credential-secret"},
	}
	if !reflect.DeepEqual(material, want) {
		t.Fatalf("empty-filtered credential material = %#v, want %#v", material, want)
	}
	assertValueCredentialSanitizesExactly(t, material)
}

// Material built without the constructor's prepared patterns still scrubs
// its values.
func TestCredentialEvidencePatternsFallBackToValues(t *testing.T) {
	material := APILogCredentialMaterial{Values: []string{"credential-secret"}}
	if patterns := credentialEvidencePatterns(material); !reflect.DeepEqual(patterns, []string{"credential-secret"}) {
		t.Fatalf("fallback credential patterns = %q, want the value", patterns)
	}
	assertValueCredentialSanitizesExactly(t, material)
}

func assertValueCredentialSanitizesExactly(t *testing.T, material APILogCredentialMaterial) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://provider.test/v1/safe-marker?token=credential-secret&safe_query=safe-marker", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "safe-host.provider.test"
	req.Header = http.Header{
		"X-Credential-Value": {"prefix credential-secret suffix"},
		"X-Safe":             {"safe-marker"},
	}
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	wantHeaders := map[string][]string{
		"Host":   {"safe-host.provider.test"},
		"X-Safe": {"safe-marker"},
	}
	if endpoint != "https://provider.test/v1/safe-marker" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized value-material request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test/v1/safe-marker", wantHeaders)
	}
}

func TestAPILogCredentialMaterialForRequestNil(t *testing.T) {
	configured := NewAPILogCredentialMaterial([]string{"X-Custom"}, []string{"token"}, "secret")
	material := APILogCredentialMaterialForRequest(nil, configured)
	if _, ok := material.HeaderNames["X-Custom"]; !ok {
		t.Fatal("missing configured header name")
	}
	if _, ok := material.QueryNames["token"]; !ok {
		t.Fatal("missing configured query name")
	}
}

// A percent-escaped credential query name is recognized by its unescaped
// form, and its escaped spelling is recorded too.
func TestAPILogCredentialMaterialForRequestEscapedQueryName(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://provider.test/api?access%5Ftoken=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	material := APILogCredentialMaterialForRequest(req, APILogCredentialMaterial{})
	for _, name := range []string{"access_token", "access%5ftoken"} {
		if _, ok := material.QueryNames[name]; !ok {
			t.Fatalf("QueryNames = %v, want %q", material.QueryNames, name)
		}
	}
}

// Each cookie value is recorded as sent, unquoted, and percent-decoded; a
// pair with no value contributes nothing.
func TestStructuredCredentialHeaderValuesOfACookie(t *testing.T) {
	for _, tc := range []struct {
		name, cookie string
		want         []string
	}{
		{"pairs without a value", `noequals; empty=; quoted="quoted-value"`, []string{`"quoted-value"`, "quoted-value"}},
		{"percent-encoded value", "session=%41%42", []string{"%41%42", "AB"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := structuredCredentialHeaderValues("Cookie", tc.cookie); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("structured cookie values = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeRequestForAPILogNil(t *testing.T) {
	endpoint, headers := SanitizeRequestForAPILog(nil, APILogCredentialMaterial{})
	if endpoint != "" || headers != nil {
		t.Fatalf("endpoint=%q headers=%v, want empty/nil", endpoint, headers)
	}
}

// A Trailer header that names only credential headers (blank entries aside)
// is dropped rather than logged empty.
func TestSanitizeRequestForAPILogDropsAnEmptiedTrailer(t *testing.T) {
	for _, tc := range []struct {
		name, trailer string
		material      APILogCredentialMaterial
	}{
		{"common credential names", "Authorization, X-Api-Key", NewAPILogCredentialMaterial(nil, nil)},
		{"blank entries around a configured name", ", X-Custom, ", NewAPILogCredentialMaterial([]string{"X-Custom"}, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "https://provider.test", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Trailer", tc.trailer)
			endpoint, headers := SanitizeRequestForAPILog(req, tc.material)
			if _, ok := headers["Trailer"]; ok || endpoint == "" {
				t.Fatalf("sanitized = (%q, %v), want an endpoint and no Trailer", endpoint, headers)
			}
		})
	}
}

// A Trailer value that names no credential is kept byte for byte.
func TestSanitizeTrailerHeaderValuesNoRemoval(t *testing.T) {
	values := sanitizeTrailerHeaderValues([]string{"X-Debug,X-Info"}, APILogCredentialMaterial{})
	if len(values) != 1 || values[0] != "X-Debug,X-Info" {
		t.Fatalf("values = %q, want the original preserved exactly", values)
	}
}

// The logged Host is the one the client sends: never a Header["Host"] entry,
// and never a Host that is itself a credential.
func TestSanitizeRequestForAPILogHost(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     string
		header   string
		material APILogCredentialMaterial
		want     map[string][]string
	}{
		{"Host value carries a credential", "contains-secret-value", "", NewAPILogCredentialMaterial(nil, nil, "secret-value"),
			map[string][]string{"X-Safe": {"kept"}}},
		{"Host named as a credential header", "plain.provider.test", "", NewAPILogCredentialMaterial([]string{"Host"}, nil, "unrelated-secret"),
			map[string][]string{"X-Safe": {"kept"}}},
		{"Header[Host] beside the wire Host", "wire.provider.test", "should-be-skipped", APILogCredentialMaterial{},
			map[string][]string{"Host": {"wire.provider.test"}, "X-Safe": {"kept"}}},
		{"Header[Host] without a wire Host", "", "should-be-skipped", APILogCredentialMaterial{},
			map[string][]string{"X-Safe": {"kept"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "https://provider.test", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = tc.host
			req.Header.Set("X-Safe", "kept")
			if tc.header != "" {
				req.Header["Host"] = []string{tc.header}
			}
			endpoint, headers := SanitizeRequestForAPILog(req, tc.material)
			if endpoint != "https://provider.test" || !reflect.DeepEqual(headers, tc.want) {
				t.Fatalf("sanitized request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test", tc.want)
			}
		})
	}
}

// TestUnescapeQueryComponentError: an invalid escape returns the input as is.
func TestUnescapeQueryComponentError(t *testing.T) {
	if got := unescapeQueryComponent("%zz"); got != "%zz" {
		t.Fatalf("unescapeQueryComponent(%q) = %q, want %q", "%zz", got, "%zz")
	}
}

// Names that differ only by case fold to one variant.
func TestCaseFoldedCredentialNameVariantsDuplicate(t *testing.T) {
	if got := caseFoldedCredentialNameVariants([]string{"Key", "key", "KEY"}); !reflect.DeepEqual(got, []string{"key"}) {
		t.Fatalf("variants = %q, want exactly [key]", got)
	}
}

func TestCredentialValueVariantsEmpty(t *testing.T) {
	variants := credentialValueVariants([]string{"", "valid"})
	if len(variants) == 0 {
		t.Fatal("expected at least one variant for 'valid'")
	}
	for _, v := range variants {
		if v == "" {
			t.Fatal("empty variant should not be included")
		}
	}
}
