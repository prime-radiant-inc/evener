package llm

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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
