package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	apilog "primeradiant.com/evener/llm/apilog"
)

// TestSetWireRequestMetadataOnActiveAttempt exercises the active path of
// SetWireRequestMetadata, which replaces the preliminary request snapshot with
// the post-transport method, endpoint, headers, and credential material.
func TestSetWireRequestMetadataOnActiveAttempt(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_wire_meta")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	attempt.SetWireRequestMetadata(http.MethodPut, "https://provider.test/v1/updated",
		http.Header{"X-After": []string{"value"}}, NewAPILogCredentialMaterial(nil, nil, "secret"))
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
	if v := attempts[0].Request.Headers["X-After"]; len(v) != 1 || v[0] != "value" {
		t.Fatalf("headers = %v, want X-After=value", attempts[0].Request.Headers)
	}
}

// TestSetWireRequestMetadataOnInertAttemptIsNoop verifies the early return when
// the attempt is not active.
func TestSetWireRequestMetadataOnInertAttemptIsNoop(t *testing.T) {
	a := &APIAttempt{}
	a.SetWireRequestMetadata(http.MethodGet, "https://noop.test", http.Header{}, APILogCredentialMaterial{})
	// No panic, no state change — the inert attempt stays inert.
	if a.Active() {
		t.Fatal("inert attempt became active after SetWireRequestMetadata")
	}
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

// TestCompleteWithNilSinkCoversNoAppendPath exercises the branch in Complete
// where the attempt has a group but no sink.
func TestCompleteWithNilSinkCoversNoAppendPath(t *testing.T) {
	group := NewAPIAttemptGroup("ag_nil_sink")
	ctx := WithAPIAttemptGroup(context.Background(), group)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	// Without a sink in context, the attempt is inert (Active() == false).
	if attempt.Active() {
		t.Fatal("attempt without sink should not be active")
	}
	attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	// No panic, nothing appended.
}

// TestSettleResultNilGroup covers the nil-receiver guard.
func TestSettleResultNilGroup(t *testing.T) {
	var group *APIAttemptGroup
	group.SettleResult(context.Background(), errors.New("err"))
	// No panic.
}

// TestSettleNilGroup covers the nil-receiver guard in settle.
func TestSettleNilGroup(t *testing.T) {
	var group *APIAttemptGroup
	group.Settle(context.Background(), apilog.AttemptSuccess)
	// No panic.
}

// TestWaitForPriorAPIAttemptsNilContext covers the nil-ctx path in
// apiAttemptGroupFromContext.
func TestWaitForPriorAPIAttemptsNilContext(t *testing.T) {
	WaitForPriorAPIAttempts(nil)
	// No panic.
}

// TestSanitizeAPILogErrorNil covers the nil-error fast return.
func TestSanitizeAPILogErrorNil(t *testing.T) {
	if err := sanitizeAPILogError(nil, APILogCredentialMaterial{}); err != nil {
		t.Fatalf("sanitizeAPILogError(nil) = %v, want nil", err)
	}
}

// TestAPILogCredentialMaterialFromContextNil covers the nil-ctx path.
func TestAPILogCredentialMaterialFromContextNil(t *testing.T) {
	_, ok := apiLogCredentialMaterialFromContext(nil)
	if ok {
		t.Fatal("nil context should report no credential material")
	}
}

// TestCanonicalAPIAttemptOutcomeDefaultSuccess covers the default branch where
// the outcome is empty, there is no error, and the function returns AttemptSuccess.
func TestCanonicalAPIAttemptOutcomeDefaultSuccess(t *testing.T) {
	got := canonicalAPIAttemptOutcome(APIAttemptResult{})
	if got != apilog.AttemptSuccess {
		t.Fatalf("canonicalAPIAttemptOutcome(empty) = %q, want %q", got, apilog.AttemptSuccess)
	}
}

// TestOptionalAPILogIntNotPresent covers the !present path.
func TestOptionalAPILogIntNotPresent(t *testing.T) {
	if got := optionalAPILogInt(42, false); got != nil {
		t.Fatalf("optionalAPILogInt(42, false) = %v, want nil", got)
	}
}

// TestCloneAPILogIntNil covers the nil-input path of cloneAPILogInt.
func TestCloneAPILogIntNil(t *testing.T) {
	if got := cloneAPILogInt(nil); got != nil {
		t.Fatalf("cloneAPILogInt(nil) = %v, want nil", got)
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

	// Manually invoke recordFailure with a nil error to cover the early return.
	group.recordFailure(APILogFailure{Operation: "test", AttemptGroupID: group.ID})
	if observerCalled {
		t.Fatal("observer called for nil-error failure")
	}
}

// TestMergeCredentialMaterialCoversBothSides exercises mergeAPILogCredentialMaterial
// with non-empty left and right to cover the merge loops.
func TestMergeCredentialMaterialCoversBothSides(t *testing.T) {
	left := NewAPILogCredentialMaterial([]string{"X-Left"}, []string{"left_q"}, "left-secret")
	right := NewAPILogCredentialMaterial([]string{"X-Right"}, []string{"right_q"}, "right-secret")
	merged := mergeAPILogCredentialMaterial(left, right)
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
}
