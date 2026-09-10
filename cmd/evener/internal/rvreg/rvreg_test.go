package rvreg

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/rendezvous"
)

func TestRegistrationUpdatesSessionIdentity(t *testing.T) {
	runDir := t.TempDir()
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:          4242,
		Protocol:     "evener-appwire-v1",
		Endpoint:     "ws://127.0.0.1:1/rpc",
		SourceID:     "local",
		ThreadID:     "01OLD",
		SessionID:    "01OLD",
		WorkspaceRef: "local:01WORKSPACE",
		InstanceID:   "01OLD",
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
	if entries[0].WorkspaceRef != "local:01WORKSPACE" || entries[0].InstanceID != "01NEW" {
		t.Fatalf("entry stable/live identity=%+v", entries[0])
	}
	// RV-02: non-identity fields must survive an UpdateSessionID call.
	if entries[0].Protocol != "evener-appwire-v1" {
		t.Errorf("Protocol=%q, want evener-appwire-v1", entries[0].Protocol)
	}
	if entries[0].Endpoint != "ws://127.0.0.1:1/rpc" {
		t.Errorf("Endpoint=%q, want ws://127.0.0.1:1/rpc", entries[0].Endpoint)
	}
	if entries[0].SourceID != "local" {
		t.Errorf("SourceID=%q, want local", entries[0].SourceID)
	}
}

func TestRegistrationRemoveRetriesAfterFailure(t *testing.T) {
	runDir := t.TempDir()
	reg := &Registration{}
	const pid = 5151
	if err := reg.Register(runDir, rendezvous.Entry{PID: pid, ThreadID: "01OLD", SessionID: "01OLD"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	artifact := filepath.Join(runDir, "5151.json")
	if err := os.Remove(artifact); err != nil {
		t.Fatalf("remove rendezvous artifact: %v", err)
	}
	if err := os.Mkdir(artifact, 0o700); err != nil {
		t.Fatalf("replace artifact with directory: %v", err)
	}
	child := filepath.Join(artifact, "blocker")
	if err := os.WriteFile(child, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if err := reg.Remove(); err == nil {
		t.Fatal("expected first Remove to fail")
	}
	if err := reg.UpdateSessionID("01LATE"); err == nil {
		t.Fatal("expected late update to remain rejected after failed Remove")
	}
	if err := reg.Register(runDir, rendezvous.Entry{PID: pid}); err == nil {
		t.Fatal("expected Register to remain rejected after failed Remove")
	}
	if err := os.Remove(child); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	if err := os.Remove(artifact); err != nil {
		t.Fatalf("remove artifact directory: %v", err)
	}
	if err := reg.Remove(); err != nil {
		t.Fatalf("retry Remove: %v", err)
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List after retry: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries after retry=%+v", entries)
	}
}

// TestRegistrationRemoveClearsEntry covers Remove() — the only cleanup path.
// RV-01: after Register + Remove the rendezvous directory must be empty.
func TestRegistrationRemoveClearsEntry(t *testing.T) {
	runDir := t.TempDir()
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:       9999,
		Protocol:  "evener-appwire-v1",
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
	if err := reg.UpdateSessionID("01LATE"); err == nil {
		t.Fatal("expected UpdateSessionID after Remove to fail")
	}
	if err := reg.Register(runDir, rendezvous.Entry{PID: 9999}); err == nil {
		t.Fatal("expected Register after Remove to fail")
	}
	entries, err = rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List after rejected resurrection: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected rejected resurrection to leave directory empty, got %+v", entries)
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
