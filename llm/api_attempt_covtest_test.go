package llm

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	apilog "primeradiant.com/evener/llm/apilog"
)

// TestCovAPIAttemptContextActiveNilContext covers the nil-ctx early return
// in APIAttemptContextActive (line 104-105).
func TestCovAPIAttemptContextActiveNilContext(t *testing.T) {
	if APIAttemptContextActive(nil) {
		t.Fatal("APIAttemptContextActive(nil) = true, want false")
	}
}

// TestCovAPIAttemptContextActivePartialContext covers the path where the
// context has a group but no sink, and vice versa, so the function returns
// false.
func TestCovAPIAttemptContextActivePartialContext(t *testing.T) {
	group := NewAPIAttemptGroup("ag_partial")
	ctxGroupOnly := WithAPIAttemptGroup(context.Background(), group)
	if APIAttemptContextActive(ctxGroupOnly) {
		t.Fatal("group without sink should be inactive")
	}
	sink := &recordingAPIAttemptSink{}
	ctxSinkOnly := WithAPIAttemptSink(context.Background(), sink)
	if APIAttemptContextActive(ctxSinkOnly) {
		t.Fatal("sink without group should be inactive")
	}
}

// TestCovCredentialMaterialInactiveAttempt covers the !Active() early return
// in CredentialMaterial (line 158-159).
func TestCovCredentialMaterialInactiveAttempt(t *testing.T) {
	a := &APIAttempt{meta: APIAttemptMeta{CredentialMaterial: NewAPILogCredentialMaterial([]string{"X-Key"}, nil, "secret")}}
	got := a.CredentialMaterial()
	if !reflect.DeepEqual(got, APILogCredentialMaterial{}) {
		t.Fatalf("inactive CredentialMaterial = %+v, want empty", got)
	}
}

// TestCovSetRequestBodyInactiveAttempt covers the !Active() early return in
// SetRequestBody (line 169-170).
func TestCovSetRequestBodyInactiveAttempt(t *testing.T) {
	wantBody := []byte("original")
	a := &APIAttempt{meta: APIAttemptMeta{RequestBody: append([]byte(nil), wantBody...), RequestBodyInexact: true}}
	a.SetRequestBody([]byte("body"), true)
	if !reflect.DeepEqual(a.meta.RequestBody, wantBody) || !a.meta.RequestBodyInexact {
		t.Fatalf("inactive SetRequestBody mutated metadata to body %q, inexact %v", a.meta.RequestBody, a.meta.RequestBodyInexact)
	}
}

// TestCovMergeCredentialMaterialInactiveAttempt covers the !Active() early
// return in MergeCredentialMaterial (line 181-182).
func TestCovMergeCredentialMaterialInactiveAttempt(t *testing.T) {
	want := NewAPILogCredentialMaterial([]string{"X-Original"}, nil, "original-secret")
	a := &APIAttempt{meta: APIAttemptMeta{CredentialMaterial: want}}
	a.MergeCredentialMaterial(NewAPILogCredentialMaterial(nil, nil, "secret"))
	if !reflect.DeepEqual(a.meta.CredentialMaterial, want) {
		t.Fatalf("inactive MergeCredentialMaterial mutated metadata to %+v, want %+v", a.meta.CredentialMaterial, want)
	}
}

// TestCovBeginAPIAttemptNilGroup covers the group == nil path in
// BeginAPIAttempt (line 217-218).
func TestCovBeginAPIAttemptNilGroup(t *testing.T) {
	attempt := BeginAPIAttempt(context.Background(), testAPIAttemptMeta(time.Unix(1_700_000_000, 0).UTC()))
	if attempt.Active() {
		t.Fatal("attempt with no group should be inert")
	}
}

// TestCovCompleteNilReceiver covers the nil-receiver guard in Complete
// (line 254-255).
func TestCovCompleteNilReceiver(t *testing.T) {
	var a *APIAttempt
	a.Complete(testAPIAttemptResult(time.Now(), apilog.AttemptSuccess, nil))
	// No panic.
}

// TestCovCompleteNilSink covers the a.sink == nil early return in Complete
// (line 262-263). This needs a group but no sink bound.
func TestCovCompleteNilSink(t *testing.T) {
	group := NewAPIAttemptGroup("ag_nil_sink_complete")
	sink := &recordingAPIAttemptSink{}
	ctx := WithAPIAttemptSink(context.Background(), sink)
	// Manually construct an attempt with a group but nil sink.
	a := &APIAttempt{
		group: group,
		ctx:   ctx,
		meta:  testAPIAttemptMeta(time.Unix(1_700_000_000, 0).UTC()),
		id:    "test-id",
		index: 1,
	}
	group.pendingAttempts.Add(1)
	a.Complete(testAPIAttemptResult(time.Unix(1_700_000_000, 0).UTC().Add(time.Millisecond), apilog.AttemptSuccess, nil))
	group.Settle(ctx, apilog.AttemptSuccess)
	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 0 {
		t.Fatalf("attempt count = %d, want 0 for nil attempt sink", len(attempts))
	}
	if len(settlements) != 1 || settlements[0].AttemptGroupID != group.ID || settlements[0].FinalAttemptCount != 0 || settlements[0].Outcome != apilog.AttemptSuccess {
		t.Fatalf("settlements = %+v, want one successful zero-attempt settlement for %q", settlements, group.ID)
	}
}

// TestCovAPIAttemptGroupFromContextNilContext covers the nil-ctx path
// (line 309-310).
func TestCovAPIAttemptGroupFromContextNilContext(t *testing.T) {
	if got := apiAttemptGroupFromContext(nil); got != nil {
		t.Fatalf("apiAttemptGroupFromContext(nil) = %v, want nil", got)
	}
}

// TestCovAPILogCredentialMaterialFromContextNilContext covers the nil-ctx
// path in apiLogCredentialMaterialFromContext (line 378-379).
func TestCovAPILogCredentialMaterialFromContextNilContext(t *testing.T) {
	_, ok := apiLogCredentialMaterialFromContext(nil)
	if ok {
		t.Fatal("apiLogCredentialMaterialFromContext(nil) should report false")
	}
}

// TestCovSanitizedObservedAPILogErrorAPILogFailureWasObserved exercises the
// apiLogFailureWasObserved method on sanitizedObservedAPILogError (line 333),
// which is otherwise never called directly. It also covers the observed=true
// path in sanitizeAPILogError (line 352-353).
func TestCovSanitizedObservedAPILogErrorAPILogFailureWasObserved(t *testing.T) {
	// A plain error does not implement apiLogObservedFailure, so
	// sanitizeAPILogError returns sanitizedAPILogError (not observed).
	plainErr := errors.New("boom")
	result := sanitizeAPILogError(plainErr, APILogCredentialMaterial{})
	if result.Error() != plainErr.Error() {
		t.Fatalf("sanitized plain error = %q, want %q", result, plainErr)
	}
	var observed apiLogObservedFailure
	if errors.As(result, &observed) {
		t.Fatalf("plain error should not be observed: %T", result)
	}

	// An error that implements apiLogObservedFailure should produce
	// sanitizedObservedAPILogError, which also implements the interface.
	observedErr := sanitizedObservedAPILogError{sanitizedAPILogError{text: "observed"}}
	result2 := sanitizeAPILogError(observedErr, APILogCredentialMaterial{})
	if !errors.As(result2, &observed) {
		t.Fatalf("observed error should stay observed after sanitize: %T", result2)
	}
	if result2.Error() != "observed" {
		t.Fatalf("sanitized observed error = %q, want observed", result2)
	}
	// Call the method directly to cover line 333.
	observed.apiLogFailureWasObserved()
}

// TestCovCloneCredentialFreeHTTPHeaderCredentialBearing covers the
// credentialBearing = true; break path in cloneCredentialFreeHTTPHeader
// (line 598-599). When a header value contains credential evidence, the entire
// header is skipped.
func TestCovCloneCredentialFreeHTTPHeaderCredentialBearing(t *testing.T) {
	material := NewAPILogCredentialMaterial(nil, nil, "secret-value")
	header := http.Header{
		"X-Safe":   []string{"ok"},
		"X-Secret": []string{"bearer secret-value"},
	}
	cloned := cloneCredentialFreeHTTPHeader(header, material.patterns, material.secretNames)
	want := apilog.EncodedHeader{"X-Safe": []string{"ok"}}
	if !reflect.DeepEqual(cloned, want) {
		t.Fatalf("credential-free headers = %#v, want %#v", cloned, want)
	}
}

// TestCovCloneCredentialFreeHTTPHeaderCredentialNameEvidence covers the
// containsCredentialDurableStringEvidenceParts(name) path in
// cloneCredentialFreeHTTPHeader (line 593). When a header name itself
// contains a credential string, the header is skipped.
func TestCovCloneCredentialFreeHTTPHeaderCredentialNameEvidence(t *testing.T) {
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

// TestCovAPILogCredentialMaterialFromContextWithMaterial covers the ok=true
// path where a material is attached to the context.
func TestCovAPILogCredentialMaterialFromContextWithMaterial(t *testing.T) {
	material := NewAPILogCredentialMaterial(nil, nil, "secret")
	ctx := withAPILogCredentialMaterial(context.Background(), material)
	got, ok := apiLogCredentialMaterialFromContext(ctx)
	if !ok {
		t.Fatal("expected material from context")
	}
	if len(got.Values) != 1 || got.Values[0] != "secret" {
		t.Fatalf("material values = %v, want [secret]", got.Values)
	}
}
