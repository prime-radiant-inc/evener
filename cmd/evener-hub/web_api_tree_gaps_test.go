package main

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestHubRefFromAppThreadWithRef covers the path where the thread has a Ref.
func TestHubRefFromAppThreadWithRef(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-1",
		Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:thread-1"},
	}
	ref := hubRefFromAppThread(thread)
	if ref.HostID != "local" || ref.SessionID != "thread-1" {
		t.Fatalf("ref = %+v, want HostID=local SessionID=thread-1", ref)
	}
}

// TestHubRefFromAppThreadWithoutRef covers the path where Ref is empty and
// is constructed from Source + ID.
func TestHubRefFromAppThreadWithoutRef(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-2",
		Source: "remote",
	}
	ref := hubRefFromAppThread(thread)
	if ref.HostID != "remote" || ref.SessionID != "thread-2" {
		t.Fatalf("ref = %+v, want HostID=remote SessionID=thread-2", ref)
	}
}

// TestHubRefFromAppThreadInvalidRef covers the path where the Ref is not
// parseable and the function falls back to LocalRef.
func TestHubRefFromAppThreadInvalidRef(t *testing.T) {
	thread := appwire.Thread{
		ID:     "thread-3",
		Source: "local",
		// Use a ref with invalid characters (spaces) that won't parse.
		Evener: appwire.EvenerThread{Ref: "bad ref with spaces"},
	}
	ref := hubRefFromAppThread(thread)
	// Should fall back to LocalRef(thread.ID).
	if ref.HostID != "local" || ref.SessionID != "thread-3" {
		t.Fatalf("ref = %+v, want LocalRef fallback HostID=local SessionID=thread-3", ref)
	}
}

// TestHubUsageFromAppwireNil covers the nil path.
func TestHubUsageFromAppwireNil(t *testing.T) {
	if got := hubUsageFromAppwire(nil); got != nil {
		t.Fatalf("hubUsageFromAppwire(nil) = %v, want nil", got)
	}
}

// TestHubUsageFromAppwireNonNil covers the non-nil path.
func TestHubUsageFromAppwireNonNil(t *testing.T) {
	u := &appwire.EvenerUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 25, TotalTokens: 175}
	got := hubUsageFromAppwire(u)
	if got == nil {
		t.Fatal("hubUsageFromAppwire should not return nil for non-nil input")
	}
	if got.InputTokens != 100 || got.OutputTokens != 50 || got.CacheReadTokens != 25 || got.TotalTokens != 175 {
		t.Fatalf("usage = %+v, want InputTokens=100 OutputTokens=50 CacheReadTokens=25 TotalTokens=175", got)
	}
}

// TestHubCapabilitiesFromAppwire covers the full mapping.
func TestHubCapabilitiesFromAppwire(t *testing.T) {
	caps := appwire.ThreadCapabilities{
		Send: true, Steer: true, Interrupt: true, Compact: true,
		Clear: true, ForkFromTurn: true, Shutdown: true, ChangeModel: true, Queue: true,
	}
	got := hubCapabilitiesFromAppwire(caps)
	if !got.Send || !got.Steer || !got.Interrupt || !got.Compact {
		t.Fatalf("capabilities = %+v, want all true", got)
	}
	if !got.Clear || !got.Fork || !got.Shutdown || !got.ChangeModel || !got.Queue {
		t.Fatalf("capabilities = %+v, want all true", got)
	}
}

// TestHubCapabilitiesFromAppwireAllFalse covers the all-false path.
func TestHubCapabilitiesFromAppwireAllFalse(t *testing.T) {
	caps := appwire.ThreadCapabilities{}
	got := hubCapabilitiesFromAppwire(caps)
	if got.Send || got.Steer || got.Interrupt || got.Compact || got.Clear || got.Fork || got.Shutdown || got.ChangeModel || got.Queue {
		t.Fatalf("capabilities = %+v, want all false", got)
	}
}
