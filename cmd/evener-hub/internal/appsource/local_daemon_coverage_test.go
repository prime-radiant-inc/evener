package appsource

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestRelaySessionKeyWithRef(t *testing.T) {
	got := relaySessionKey("local", "local:session123", "")
	if got != "local:session123" {
		t.Fatalf("expected 'local:session123', got %q", got)
	}
}

func TestRelaySessionKeyWithThreadID(t *testing.T) {
	got := relaySessionKey("local", "not-a-ref", "session456")
	// Should fall back to constructing a ref from sourceID + threadID
	if got == "" {
		t.Fatal("should not be empty")
	}
	if got != "local:session456" {
		t.Fatalf("expected 'local:session456', got %q", got)
	}
}

func TestDaemonAuthHeaderEmptyToken(t *testing.T) {
	if daemonAuthHeader("") != nil {
		t.Fatal("empty token should return nil")
	}
	if daemonAuthHeader("  ") != nil {
		t.Fatal("whitespace token should return nil")
	}
}

func TestDaemonAuthHeaderWithToken(t *testing.T) {
	header := daemonAuthHeader("my-token")
	if header == nil {
		t.Fatal("non-empty token should return header")
	}
	if got := header.Get("Authorization"); got != "Bearer my-token" {
		t.Fatalf("expected 'Bearer my-token', got %q", got)
	}
}

func TestLocalDaemonRendezvousEntryWithSessionID(t *testing.T) {
	item := LocalDaemonEntry{
		Entry:     rendezvous.Entry{ThreadID: "thread1"},
		SessionID: "session1",
	}
	entry := localDaemonRendezvousEntry(item)
	if entry.SessionID != "session1" {
		t.Fatalf("expected SessionID 'session1', got %q", entry.SessionID)
	}
	if entry.ThreadID != "thread1" {
		t.Fatalf("ThreadID should be preserved from Entry, got %q", entry.ThreadID)
	}
}

func TestLocalDaemonRendezvousEntryWithoutSessionID(t *testing.T) {
	item := LocalDaemonEntry{
		Entry: rendezvous.Entry{ThreadID: "thread1", SessionID: "original"},
	}
	entry := localDaemonRendezvousEntry(item)
	if entry.SessionID != "original" {
		t.Fatalf("SessionID should be preserved from Entry, got %q", entry.SessionID)
	}
}

func TestLocalDaemonThreadIDWithSessionID(t *testing.T) {
	item := LocalDaemonEntry{
		Entry:     rendezvous.Entry{ThreadID: "thread1"},
		SessionID: "session1",
	}
	if got := localDaemonThreadID(item); got != "session1" {
		t.Fatalf("expected 'session1', got %q", got)
	}
}

func TestLocalDaemonThreadIDWithoutSessionID(t *testing.T) {
	item := LocalDaemonEntry{
		Entry: rendezvous.Entry{ThreadID: "thread1"},
	}
	if got := localDaemonThreadID(item); got != "thread1" {
		t.Fatalf("expected 'thread1', got %q", got)
	}
}

func TestLocalDaemonMutationCallErrorNonWireError(t *testing.T) {
	err := errors.New("plain error")
	mapped := localDaemonMutationCallError("mutation-1", err)
	if mapped == nil || mapped.Error() != "plain error" {
		t.Fatalf("non-wire error should pass through, got %v", mapped)
	}
}

func TestLocalDaemonMutationCallErrorSessionUnavailable(t *testing.T) {
	// A SessionUnavailable error should be mapped to InternalError with
	// MutationOutcomeUnknown
	err := appwire.SessionUnavailable("session gone")
	mapped := localDaemonMutationCallError("mutation-1", err)
	if mapped == nil {
		t.Fatal("should not return nil")
	}
	var wire appwire.WireError
	if !errors.As(mapped, &wire) {
		t.Fatalf("mapped error should be a WireError, got %T: %v", mapped, mapped)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("expected CodeInternalError, got %q", wire.Code)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatal("Data should be ErrorData")
	}
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("expected ErrorMutationOutcomeUnknown, got %q", data.EvenerErrorInfo)
	}
	if data.ClientMutationID != "mutation-1" {
		t.Fatalf("expected ClientMutationID 'mutation-1', got %q", data.ClientMutationID)
	}
}

func TestLocalDaemonMutationCallErrorOtherUnavailable(t *testing.T) {
	// An Unavailable error that is NOT SessionUnavailable should pass through
	err := appwire.Unavailable(string(appwire.ErrorActionUnavailable))
	mapped := localDaemonMutationCallError("mutation-1", err)
	if mapped == nil {
		t.Fatal("should not return nil")
	}
	// Should pass through as-is (not converted to InternalError)
	var wire appwire.WireError
	if errors.As(mapped, &wire) {
		if wire.Code == appwire.CodeInternalError {
			t.Fatal("non-SessionUnavailable error should not be converted to InternalError")
		}
	}
}

func TestForwardLocalDaemonNotificationDelivers(t *testing.T) {
	ctx := context.Background()
	ch := make(chan appwire.Notification, 1)
	notif := appwire.Notification{Method: "test"}
	forwardLocalDaemonNotification(ctx, ch, notif)
	select {
	case got := <-ch:
		if got.Method != "test" {
			t.Fatalf("expected method 'test', got %q", got.Method)
		}
	default:
		t.Fatal("notification should have been delivered")
	}
}

func TestForwardLocalDaemonNotificationCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Use an unbuffered channel so the send would block if attempted
	ch := make(chan appwire.Notification)
	notif := appwire.Notification{Method: "test"}
	forwardLocalDaemonNotification(ctx, ch, notif)
	// Should not block — the cancelled context should be selected
	// (the notification goes nowhere because the channel is unbuffered and
	// nobody is receiving)
}
