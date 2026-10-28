package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// The fatal-stream wrapper forwards its cause everywhere a caller looks:
// rendering, unwrapping, and type detection through an outer wrap.
func TestFatalStreamErrorForwardsItsCause(t *testing.T) {
	cause := errors.New("in-band failure")
	fatal := &FatalStreamError{Err: cause}
	if fatal.Error() != cause.Error() {
		t.Fatalf("Error() = %q, want the cause's rendering", fatal.Error())
	}
	if !errors.Is(fatal, cause) {
		t.Fatal("errors.Is must reach the wrapped cause")
	}
	var detected *FatalStreamError
	if !errors.As(fmt.Errorf("stream: %w", fatal), &detected) || detected.Err != cause {
		t.Fatalf("errors.As through a wrap = %v, want the original wrapper", detected)
	}
}

// Claiming without an association answers nil: a nil response (no exchange
// happened) and a response the round tripper never tracked both decline
// rather than fabricate a capture.
func TestClaimResponseDeclinesWithoutAnAssociation(t *testing.T) {
	rt := &apiAttemptRoundTripper{}
	if got := rt.claimResponse(nil); got != nil {
		t.Fatalf("claimResponse(nil) = %v, want nil", got)
	}
	if got := rt.claimResponse(&http.Response{}); got != nil {
		t.Fatalf("claimResponse of an untracked response = %v, want nil", got)
	}
}

// A tracked body hands its capture to exactly one claimer: the second claim
// gets nothing, so a concurrent or repeated claim cannot complete the same
// attempt twice.
func TestResponseBodyClaimIsOnceOnly(t *testing.T) {
	capture := &APIAttemptCapture{}
	body := &apiAttemptResponseBody{attempt: capture}
	resp := &http.Response{}
	rt := &apiAttemptRoundTripper{responses: map[*http.Response]*apiAttemptResponseBody{resp: body}}
	if got := rt.claimResponse(resp); got != capture {
		t.Fatalf("first claim = %v, want the body's capture", got)
	}
	if got := rt.claimResponse(resp); got != nil {
		t.Fatalf("second claim = %v, want nil: the claim is once-only", got)
	}
}

// A redirect hook that reports no candidate merges nothing: the nil guard is
// the whole contract, and the accumulated material must stay untouched.
func TestMergeRedirectCandidateMaterialIgnoresANilCandidate(t *testing.T) {
	rt := &apiAttemptRoundTripper{}
	rt.mergeRedirectCandidateMaterial(nil)
	if !reflect.DeepEqual(rt.credentialMaterial, llm.APILogCredentialMaterial{}) {
		t.Fatalf("credential material = %+v, want untouched zero value", rt.credentialMaterial)
	}
}

// A pending completion with no attempt behind it is the zero value redirects
// can produce for an inert exchange; completing it is a no-op, never a panic.
func TestPendingCompletionIsSafeOnItsZeroValue(t *testing.T) {
	var pending *pendingAPIAttemptCompletion
	pending.complete()
}

// Closing a claimed response body does not complete the attempt: the owner
// of the returned capture does, so a caller that merely closes the body can
// neither fabricate a record nor block the owner's later one.
func TestDoWithAPIAttemptsCloseAloneDoesNotCompleteTheAttempt(t *testing.T) {
	const responseBytes = "response bytes the caller never read"
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Body != nil {
			_, _ = io.Copy(io.Discard, request.Body)
			_ = request.Body.Close()
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(responseBytes)),
		}, nil
	})}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_unclaimed_close", sink),
		http.MethodPost,
		"https://provider.test/v1",
		io.NopCloser(strings.NewReader("request bytes")),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("response close: %v", err)
	}
	if got := sink.count(); got != 0 {
		t.Fatalf("attempts after close = %d, want 0: Close must not complete the owner's attempt", got)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
	record := onlyAttempt(t, sink)
	if record.Response == nil || record.Response.StatusCode == nil || *record.Response.StatusCode != http.StatusOK {
		t.Fatalf("recorded response = %+v, want the 200 the exchange returned", record.Response)
	}
}
