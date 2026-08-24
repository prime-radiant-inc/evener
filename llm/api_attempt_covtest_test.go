package llm

import (
	"context"
	"errors"
	"net/http"
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
	a := &APIAttempt{}
	got := a.CredentialMaterial()
	if len(got.HeaderNames) != 0 || len(got.Values) != 0 {
		t.Fatalf("inactive CredentialMaterial = %+v, want empty", got)
	}
}

// TestCovSetRequestBodyInactiveAttempt covers the !Active() early return in
// SetRequestBody (line 169-170).
func TestCovSetRequestBodyInactiveAttempt(t *testing.T) {
	a := &APIAttempt{}
	a.SetRequestBody([]byte("body"), true)
	// No panic — the inert attempt stays inert.
	if a.Active() {
		t.Fatal("inert attempt became active after SetRequestBody")
	}
}

// TestCovMergeCredentialMaterialInactiveAttempt covers the !Active() early
// return in MergeCredentialMaterial (line 181-182).
func TestCovMergeCredentialMaterialInactiveAttempt(t *testing.T) {
	a := &APIAttempt{}
	a.MergeCredentialMaterial(NewAPILogCredentialMaterial(nil, nil, "secret"))
	if a.Active() {
		t.Fatal("inert attempt became active after MergeCredentialMaterial")
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
	// Manually construct an attempt with a group but nil sink.
	a := &APIAttempt{
		group: group,
		ctx:   context.Background(),
		meta:  testAPIAttemptMeta(time.Unix(1_700_000_000, 0).UTC()),
		id:    "test-id",
		index: 1,
	}
	group.pendingAttempts.Add(1)
	a.Complete(testAPIAttemptResult(time.Unix(1_700_000_000, 0).UTC().Add(time.Millisecond), apilog.AttemptSuccess, nil))
	// No panic, nothing appended.
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
	if _, ok := cloned["X-Secret"]; ok {
		t.Fatal("credential-bearing header should be excluded")
	}
	if v := cloned["X-Safe"]; len(v) != 1 || v[0] != "ok" {
		t.Fatalf("safe header = %v, want [ok]", v)
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
	if _, ok := cloned["X-secret-header"]; ok {
		t.Fatal("header with credential evidence in name should be excluded")
	}
	if v := cloned["X-Safe"]; len(v) != 1 || v[0] != "ok" {
		t.Fatalf("safe header = %v, want [ok]", v)
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
