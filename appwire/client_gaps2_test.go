package appwire

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestWithRequestIDObserverNil covers the nil-observe early return (line 149-150).
func TestWithRequestIDObserverNil(t *testing.T) {
	ctx := context.Background()
	got := WithRequestIDObserver(ctx, nil)
	if got != ctx {
		t.Fatalf("WithRequestIDObserver(ctx, nil) should return ctx unchanged")
	}
}

// TestProtocolVersionMismatchErrorError covers the Error() method (line 318).
func TestProtocolVersionMismatchErrorError(t *testing.T) {
	err := ProtocolVersionMismatchError{Got: "v0", Want: "v2"}
	s := err.Error()
	if s == "" {
		t.Fatalf("Error() should return non-empty string")
	}
	if !strings.Contains(s, "v0") || !strings.Contains(s, "v2") {
		t.Fatalf("Error() should contain both versions, got %q", s)
	}
}

// TestClientMarkClosedIdempotent covers the second markClosed call that sees
// the closed channel already closed (line 276).
func TestClientMarkClosedIdempotent(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	client.markClosed(errors.New("first error"))
	client.markClosed(errors.New("second error"))
	if client.closedError().Error() != "first error" {
		t.Fatalf("closedError after double markClosed should be first error, got %v", client.closedError())
	}
}

// TestClientClosedErrorDefault covers the default error when closeErr is nil
// (line 289).
func TestClientClosedErrorDefault(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	// closeErr is nil, so closedError returns the default message.
	err := client.closedError()
	if err == nil {
		t.Fatalf("closedError should return non-nil when closeErr is nil")
	}
	if err.Error() != "appwire client closed" {
		t.Fatalf("closedError = %q, want %q", err.Error(), "appwire client closed")
	}
}

// TestClientInitializeRejectsClientSideProtocolMismatch covers the client-side
// protocol version mismatch (line 327): when the caller passes a ProtocolVersion
// that doesn't match, Initialize returns an error without sending a request.
func TestClientInitializeRejectsClientSideProtocolMismatch(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx := t.Context()
	client.Start(ctx)
	defer client.Close()

	_, err := client.Initialize(ctx, InitializeParams{ProtocolVersion: "evener-appwire-v999"})
	if err == nil {
		t.Fatal("Initialize with wrong protocol version should error")
	}
	// No request should have been sent to the transport.
	select {
	case msg := <-transport.writes:
		t.Fatalf("unexpected write to transport: %+v", msg)
	default:
	}
}
