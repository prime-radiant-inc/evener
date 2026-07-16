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

	apilog "primeradiant.com/serf/llm/apilog"
)

func TestSanitizeRequestForAPILogExcludesCredentialMaterial(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://alice:password@example.test/v1?keep=visible&api_key=secret+token&opaque=secret+token&ordered=first&ordered=second", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{
		"x-visible":        {"first", "second"},
		"X-Gateway-Key":    {"secret token"},
		"X-Contains-Token": {"prefix secret token suffix"},
		"Authorization":    {"Bearer built-in-secret"},
	}
	material := NewAPILogCredentialMaterial(
		[]string{"x-gateway-key"},
		[]string{"API_KEY"},
		"alice", "password", "secret token", "built-in-secret",
	)

	endpoint, headers := SanitizeRequestForAPILog(req, material)
	if endpoint != "https://example.test/v1?keep=visible&ordered=first&ordered=second" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	wantHeaders := map[string][]string{"x-visible": {"first", "second"}}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("headers = %#v, want %#v", headers, wantHeaders)
	}
	for _, secret := range []string{"alice", "password", "secret+token", "secret token", "built-in-secret"} {
		if strings.Contains(endpoint, secret) {
			t.Fatalf("endpoint contains credential %q: %s", secret, endpoint)
		}
	}
}

func TestSanitizeErrorForAPILogRemovesRawAndEscapedCredentials(t *testing.T) {
	secret := "alpha/beta value"
	material := NewAPILogCredentialMaterial(nil, nil, secret)
	text := strings.Join([]string{secret, url.QueryEscape(secret), url.PathEscape(secret)}, " | ")
	got := SanitizeErrorForAPILog(text, material)
	for _, leaked := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
		if strings.Contains(got, leaked) {
			t.Fatalf("sanitized error contains %q: %q", leaked, got)
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
	if strings.Contains(record.ErrorMessage, secret) {
		t.Fatalf("persisted error contains credential: %q", record.ErrorMessage)
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
	if !strings.Contains(record.ErrorMessage, ordinaryHeader) {
		t.Fatalf("persisted error hid ordinary header name %q: %q", ordinaryHeader, record.ErrorMessage)
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
	BeginAPIAttempt(ctx, APIAttemptMeta{
		StartedAt:          time.Now(),
		CredentialMaterial: NewAPILogCredentialMaterial(nil, nil, secret),
	}).Complete(APIAttemptResult{
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: time.Now(),
	})

	if len(observed) != 1 {
		t.Fatalf("sync failure observer calls = %d, want exactly 1: %+v", len(observed), observed)
	}
	if got := observed[0].Err.Error(); strings.Contains(got, secret) {
		t.Fatalf("sync failure warning leaked credential: %q", got)
	}
	if !errors.Is(observed[0].Err, syncErr) {
		t.Fatalf("sanitized sync warning lost storage error identity: %v", observed[0].Err)
	}

	apiLogFileSync = oldSync
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
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
	if !strings.Contains(got, ordinaryHeader) {
		t.Fatalf("failure warning hid ordinary header name %q: %q", ordinaryHeader, got)
	}
	if !errors.Is(sink.observed[0].Err, storageErr) {
		t.Fatalf("sanitized warning lost storage error identity: %v", sink.observed[0].Err)
	}
}

func TestAPIAttemptAppendWarningSanitizationPreservesObservedMarker(t *testing.T) {
	const credentialHeader = "X-Private-Observed-Sentinel"
	sink := &credentialFailureSink{
		err: observedAPILogError{err: errors.New("sync failed for " + credentialHeader)},
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
			if got := ClassifyAPIAttemptOutcome(tc.owner, 0, tc.decodeErr, tc.transportErr); got != apilog.AttemptCallerCancel {
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
			if got := ClassifyAPIAttemptOutcome(tc.owner, 0, tc.decodeErr, tc.transportErr); got != apilog.AttemptProviderTimeout {
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
			if got := ClassifyAPIAttemptOutcome(owner, tc.status, tc.decodeErr, tc.transportErr); got != tc.want {
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
			if got := ClassifyAPIAttemptOutcome(owner, http.StatusOK, testCase.decodeErr, testCase.transportErr); got != testCase.want {
				t.Fatalf("live-attempt outcome = %q, want %q", got, testCase.want)
			}
		})
	}
}
