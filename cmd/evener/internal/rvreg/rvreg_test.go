package rvreg

import (
	"strings"
	"testing"
	"time"

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

// TestRegistrationEntryReturnsDetachedCopy covers the accessor the retire
// path uses to revalidate a caller's ownership generation: Entry must report
// the registered record, and mutating the returned value must not corrupt
// the registration's own state.
func TestRegistrationEntryReturnsDetachedCopy(t *testing.T) {
	reg := &Registration{}
	if _, ok := reg.Entry(); ok {
		t.Fatal("Entry on an unregistered Registration must report not-ok")
	}
	want := rendezvous.Entry{
		PID:       2468,
		Address:   "127.0.0.1:4600",
		Protocol:  "evener-appwire-v5",
		ThreadID:  "01JENT",
		SessionID: "01JENT",
		StateDir:  "/state/entry",
		StartedAt: time.Now(),
	}
	runDir := t.TempDir()
	if err := reg.Register(runDir, want); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := reg.Entry()
	if !ok {
		t.Fatal("Entry after Register must report ok")
	}
	if got.PID != want.PID || got.StateDir != want.StateDir || !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("Entry = %+v, want %+v", got, want)
	}
	got.StateDir = "/corrupted"
	again, _ := reg.Entry()
	if again.StateDir != want.StateDir {
		t.Fatalf("Entry shares state with the caller: StateDir = %q after mutating the first copy", again.StateDir)
	}
}

// TestRegistrationRemoveAfterUpdateSessionID proves the ownership-checked
// removal still removes after the entry's session identity advanced — the
// in-registration copy tracks UpdateSessionID, so the remove matches.
func TestRegistrationRemoveAfterUpdateSessionID(t *testing.T) {
	runDir := t.TempDir()
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:       3579,
		ThreadID:  "01OLD",
		SessionID: "01OLD",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.UpdateSessionID("01NEW"); err != nil {
		t.Fatalf("UpdateSessionID: %v", err)
	}
	if err := reg.Remove(); err != nil {
		t.Fatalf("Remove after UpdateSessionID: %v", err)
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries after Remove = %+v, want none", entries)
	}
}

// TestRegistrationRemoveRefusesExternallyOverwrittenFile is the daemon-exit
// half of the replacement race: if the rendezvous file on disk no longer
// matches what this process registered (a replacement daemon rewrote it for
// the same PID), Remove must refuse and leave the replacement's file intact
// instead of deleting it.
func TestRegistrationRemoveRefusesExternallyOverwrittenFile(t *testing.T) {
	runDir := t.TempDir()
	mine := rendezvous.Entry{
		PID:       4680,
		ThreadID:  "01MINE",
		SessionID: "01MINE",
		StartedAt: time.Now(),
	}
	reg := &Registration{}
	if err := reg.Register(runDir, mine); err != nil {
		t.Fatalf("Register: %v", err)
	}
	replacement := mine
	replacement.ThreadID = "01REPLACEMENT"
	replacement.SessionID = "01REPLACEMENT"
	replacement.StartedAt = mine.StartedAt.Add(time.Hour)
	if _, err := rendezvous.Write(runDir, replacement); err != nil {
		t.Fatalf("external overwrite: %v", err)
	}
	if err := reg.Remove(); err == nil {
		t.Fatal("Remove deleted a rendezvous file this process no longer owns")
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].ThreadID, "01REPLACEMENT") {
		t.Fatalf("replacement entry destroyed by the stale daemon's Remove: %+v", entries)
	}
}
