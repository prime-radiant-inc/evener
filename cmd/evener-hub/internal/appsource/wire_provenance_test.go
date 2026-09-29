package appsource

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// WIRE-01: a delivered InternalError is the daemon's (or remote hub's) own
// application verdict on the request. It must not be reclassified as a lost
// response from its message text, because that turns a deterministic
// application failure into an automatic mutation retry. The client marks a
// genuinely synthesized transport failure with the typed
// appwire.TransportFailureError, and only that provenance is treated as
// response loss.
func TestLocalDaemonCallErrorPreservesDeliveredInternalError(t *testing.T) {
	delivered := appwire.InternalError("cannot parse transcript: unexpected EOF")
	got := localDaemonCallError(delivered)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("got %T=%v, want the delivered WireError", got, got)
	}
	if wire.Code != appwire.CodeInternalError || wire.Message != delivered.Message {
		t.Fatalf("delivered InternalError rewritten: %+v", wire)
	}
	if _, ok := errors.AsType[appwire.TransportFailureError](got); ok {
		t.Fatalf("delivered InternalError misclassified as a transport failure: %v", got)
	}
}

func TestLocalDaemonMutationCallErrorPreservesDeliveredInternalError(t *testing.T) {
	delivered := appwire.InternalError("cannot parse transcript: unexpected EOF")
	mapped := localDaemonMutationCallError("m1", delivered)
	var wire appwire.WireError
	if !errors.As(mapped, &wire) {
		t.Fatalf("got %T=%v", mapped, mapped)
	}
	if wire.Code != appwire.CodeInternalError || wire.Message != delivered.Message {
		t.Fatalf("delivered InternalError became %+v, want it unchanged", wire)
	}
}

func TestRemoteHubCallErrorPreservesDeliveredInternalError(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	delivered := appwire.InternalError("cannot parse transcript: unexpected EOF")
	got := source.mapCallError(delivered)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("got %T=%v", got, got)
	}
	if wire.Code != appwire.CodeInternalError || wire.Message != delivered.Message {
		t.Fatalf("delivered InternalError rewritten: %+v", wire)
	}
}

func TestRemoteHubMutationCallErrorPreservesDeliveredInternalError(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	delivered := appwire.InternalError("cannot parse transcript: unexpected EOF")
	mapped := source.remoteHubMutationCallError("m1", delivered)
	wire, ok := errors.AsType[appwire.WireError](mapped)
	if !ok || wire.Code != appwire.CodeInternalError || wire.Message != delivered.Message {
		t.Fatalf("mapped = %T=%+v, want the delivered InternalError unchanged", mapped, mapped)
	}
	if data, ok := wire.Data.(appwire.ErrorData); ok && data.EvenerErrorInfo == appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("delivered InternalError became mutationOutcomeUnknown: %+v", data)
	}
}

// A genuine post-send disconnect is the client's own synthesized transport
// failure, marked with TransportFailureError. With the same message text as a
// delivered InternalError, it must still become an in-doubt mutation: local and
// remote map it identically to mutationOutcomeUnknown with an automatic retry.
func TestLocalDaemonMutationCallErrorMapsSynthesizedTransportFailure(t *testing.T) {
	lost := appwire.TransportFailureError{WireError: appwire.InternalError("unexpected EOF")}
	mapped := localDaemonMutationCallError("m1", lost)
	assertMutationOutcomeUnknownAutomatic(t, mapped, "m1")
}

func TestRemoteHubMutationCallErrorMapsSynthesizedTransportFailure(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	lost := appwire.TransportFailureError{WireError: appwire.InternalError("unexpected EOF")}
	mapped := source.remoteHubMutationCallError("m1", lost)
	assertMutationOutcomeUnknownAutomatic(t, mapped, "m1")
}

func assertMutationOutcomeUnknownAutomatic(t *testing.T, err error, clientMutationID string) {
	t.Helper()
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInternalError {
		t.Fatalf("mapped = %T=%+v, want an InternalError outcome-unknown", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("data = %T, want appwire.ErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("data = %+v, want mutationOutcomeUnknown/unknown/automatic", data)
	}
	if data.ClientMutationID != clientMutationID {
		t.Fatalf("client mutation id = %q, want %q", data.ClientMutationID, clientMutationID)
	}
}
