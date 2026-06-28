package rvreg

import (
	"testing"

	"primeradiant.com/serf/rendezvous"
)

func TestRegistrationUpdatesSessionIdentity(t *testing.T) {
	runDir := t.TempDir()
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:       4242,
		Protocol:  "serf-appwire-v1",
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "01OLD",
		SessionID: "01OLD",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := reg.UpdateSessionID("01NEW"); err != nil {
		t.Fatalf("UpdateSessionID: %v", err)
	}

	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	if entries[0].ThreadID != "01NEW" || entries[0].SessionID != "01NEW" {
		t.Fatalf("entry identity=%+v", entries[0])
	}
	// RV-02: non-identity fields must survive an UpdateSessionID call.
	if entries[0].Protocol != "serf-appwire-v1" {
		t.Errorf("Protocol=%q, want serf-appwire-v1", entries[0].Protocol)
	}
	if entries[0].Endpoint != "ws://127.0.0.1:1/rpc" {
		t.Errorf("Endpoint=%q, want ws://127.0.0.1:1/rpc", entries[0].Endpoint)
	}
	if entries[0].SourceID != "local" {
		t.Errorf("SourceID=%q, want local", entries[0].SourceID)
	}
}

// TestRegistrationRemoveClearsEntry covers Remove() — the only cleanup path.
// RV-01: after Register + Remove the rendezvous directory must be empty.
func TestRegistrationRemoveClearsEntry(t *testing.T) {
	runDir := t.TempDir()
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:       9999,
		Protocol:  "serf-appwire-v1",
		Endpoint:  "ws://127.0.0.1:2/rpc",
		SourceID:  "local",
		ThreadID:  "01ABC",
		SessionID: "01ABC",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := reg.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List after Remove: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty list after Remove, got %+v", entries)
	}
}

// TestUpdateSessionIDDefensiveBranches covers the two early-return guards in
// UpdateSessionID that are not reachable from the happy-path test.
// RV-03
func TestUpdateSessionIDDefensiveBranches(t *testing.T) {
	t.Run("empty session id returns error", func(t *testing.T) {
		runDir := t.TempDir()
		reg := &Registration{}
		if err := reg.Register(runDir, rendezvous.Entry{
			PID:       1111,
			ThreadID:  "01OLD",
			SessionID: "01OLD",
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := reg.UpdateSessionID(""); err == nil {
			t.Fatal("expected error for empty session id, got nil")
		}
	})

	t.Run("unregistered returns nil without panicking", func(t *testing.T) {
		reg := &Registration{} // never registered
		if err := reg.UpdateSessionID("01ANY"); err != nil {
			t.Fatalf("expected nil for unregistered Registration, got %v", err)
		}
	})
}
