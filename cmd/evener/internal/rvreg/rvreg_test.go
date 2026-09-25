package rvreg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/rendezvous"
)

// TestRegistrationFailedUpdateLeavesMemoryAndDiskAgreeing is the regression
// for UpdateSessionID mutating the in-registration copy BEFORE it persists the
// new identity. A rendezvous write that fails (here: a directory staged where
// the atomic write puts <pid>.json.tmp, so the write fails while the previous
// record stays on disk) must leave memory holding the identity that is
// actually on disk. Without that, Remove compares a memory identity newer than
// the file, sees the mismatch, and diskHoldsReplacement misreads the stale
// file as a live replacement's entry — declaring a completed no-op while
// leaving our own rendezvous artifact behind.
func TestRegistrationFailedUpdateLeavesMemoryAndDiskAgreeing(t *testing.T) {
	runDir := t.TempDir()
	const pid = 7373
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:        pid,
		Protocol:   "evener-appwire-v1",
		Endpoint:   "ws://127.0.0.1:9/rpc",
		SourceID:   "local",
		ThreadID:   "01OLD",
		SessionID:  "01OLD",
		InstanceID: "01OLD",
		StartedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	artifact := filepath.Join(runDir, fmt.Sprintf("%d.json", pid))
	// A directory at the temp path the atomic write renames from makes the
	// update's write fail and leaves the registered record untouched.
	sabotage := artifact + ".tmp"
	if err := os.Mkdir(sabotage, 0o700); err != nil {
		t.Fatalf("sabotage rendezvous write: %v", err)
	}

	if err := reg.UpdateSessionID("01NEW"); err == nil {
		t.Fatal("UpdateSessionID reported success despite a failing rendezvous write")
	}

	// Observable consequence 1: the in-memory registration still reports the
	// identity that is on disk, so Entry never overstates what this process
	// published.
	got, ok := reg.Entry()
	if !ok {
		t.Fatal("Entry after a failed UpdateSessionID must still report the registered record")
	}
	if got.ThreadID != "01OLD" || got.SessionID != "01OLD" || got.InstanceID != "01OLD" {
		t.Fatalf("Entry after a failed update = %+v, want the OLD identity still on disk", got)
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].SessionID != "01OLD" {
		t.Fatalf("on-disk entries after a failed update = %+v, want exactly the OLD record", entries)
	}

	// Observable consequence 2: with memory and disk agreeing, the
	// ownership-checked removal deletes this process's own artifact instead of
	// misreading the disagreement as a live replacement's entry and silently
	// leaving the stale file behind.
	if err := os.Remove(sabotage); err != nil {
		t.Fatalf("clear sabotage: %v", err)
	}
	if err := reg.Remove(); err != nil {
		t.Fatalf("Remove after a failed UpdateSessionID: %v", err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("a failed UpdateSessionID left our own rendezvous artifact behind: stat err = %v", err)
	}
}

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
	if err := reg.Remove(); err != nil {
		t.Fatalf("retry Remove: %v", err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("retry did not remove the rendezvous artifact: %v", err)
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
		Protocol:  "evener-appwire-v6",
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

// TestRegistrationRemoveLeavesExternallyOverwrittenFile is the daemon-exit
// half of the replacement race: if the rendezvous file on disk no longer
// matches what this process registered (a replacement daemon rewrote it for
// the same PID), Remove must leave the replacement's file intact instead of
// deleting it. The replacement owns the record and this process's own entry is
// already gone, so the guard's refusal is a completed no-op — Remove returns
// nil and the shutdown loop does not spend its retry budget or log "entry may
// be stale" over a live replacement.
func TestRegistrationRemoveLeavesExternallyOverwrittenFile(t *testing.T) {
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
	if err := reg.Remove(); err != nil {
		t.Fatalf("Remove = %v, want nil: a live replacement owns the record, so the refusal is a completed no-op", err)
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].ThreadID, "01REPLACEMENT") {
		t.Fatalf("replacement entry destroyed by the stale daemon's Remove: %+v", entries)
	}
	// The removal path stays idempotent: a repeated call is still a no-op.
	if err := reg.Remove(); err != nil {
		t.Fatalf("second Remove = %v, want nil", err)
	}
}

// TestRegistrationRemovePreservesOriginalErrorWhenFallbackRefuses pins that a
// genuine failure to remove this process's own artifact is reported with its
// original cause. RemoveIfOwned cannot parse a corrupt regular file, and
// RemoveUnlessRegular refuses to unlink any regular file, so the fallback's
// secondary "regular file" refusal must not displace the real failure the
// shutdown loop logs and retries.
func TestRegistrationRemovePreservesOriginalErrorWhenFallbackRefuses(t *testing.T) {
	runDir := t.TempDir()
	const pid = 6161
	reg := &Registration{}
	if err := reg.Register(runDir, rendezvous.Entry{PID: pid, ThreadID: "01OLD", SessionID: "01OLD"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	artifact := filepath.Join(runDir, "6161.json")
	if err := os.WriteFile(artifact, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt rendezvous artifact: %v", err)
	}
	err := reg.Remove()
	if err == nil {
		t.Fatal("expected Remove to fail on an unparseable artifact")
	}
	if strings.Contains(err.Error(), "regular file") {
		t.Fatalf("Remove surfaced the fallback's secondary refusal instead of the original cause: %v", err)
	}
	if !strings.Contains(err.Error(), "parse rendezvous file") {
		t.Fatalf("Remove error = %v, want the original RemoveIfOwned parse failure", err)
	}
	// The corrupt regular file is never unlinked unguarded.
	if _, statErr := os.Stat(artifact); statErr != nil {
		t.Fatalf("Remove touched the regular artifact it refused to unlink: %v", statErr)
	}
}

// TestRegistrationRemoveTreatsExcludedFieldDriftAsReplacement is the M3
// regression. A same-PID artifact that differs from this registration only in a
// field OwnershipFingerprint deliberately excludes (HubToken, SpawnedBy, Agent,
// Model, Provider) is refused by RemoveIfOwned's exact-ownership guard, yet it
// fingerprints identically to this process's record. Before the fix
// diskHoldsReplacement compared fingerprints, reported "not a replacement", and
// Remove returned the original retryable error -- so the shutdown loop logged
// "entry may be stale" and spent its retry budget on a record this process no
// longer owns. The re-check now asks the same question the guard refuses on, so
// the refusal is a completed no-op and the replacement's record is left intact.
func TestRegistrationRemoveTreatsExcludedFieldDriftAsReplacement(t *testing.T) {
	mutations := map[string]func(*rendezvous.Entry){
		"hub token":  func(e *rendezvous.Entry) { e.HubToken = "different-secret" },
		"spawned by": func(e *rendezvous.Entry) { e.SpawnedBy = "evener-tui" },
		"agent":      func(e *rendezvous.Entry) { e.Agent = "other-agent" },
		"model":      func(e *rendezvous.Entry) { e.Model = "other-model" },
		"provider":   func(e *rendezvous.Entry) { e.Provider = "other-provider" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			runDir := t.TempDir()
			const pid = 5371
			mine := rendezvous.Entry{
				PID:        pid,
				Address:    "127.0.0.1:4100",
				Protocol:   "evener-appwire-v6",
				Endpoint:   "ws://127.0.0.1:4100/rpc",
				SourceID:   "local",
				ThreadID:   "01MINE",
				SessionID:  "01MINE",
				InstanceID: "01MINE",
				Agent:      "evener",
				Model:      "model-a",
				Provider:   "provider-a",
				HubToken:   "hub-token-a",
				SpawnedBy:  "evener-hub",
				StartedAt:  time.Now(),
			}
			reg := &Registration{}
			if err := reg.Register(runDir, mine); err != nil {
				t.Fatalf("Register: %v", err)
			}
			replacement := mine
			mutate(&replacement)
			// Premise of the regression: the excluded field is invisible to the
			// canonical fingerprint, which is exactly why the fingerprint could
			// not answer this question.
			if rendezvous.OwnershipFingerprint(replacement) != rendezvous.OwnershipFingerprint(mine) {
				t.Fatalf("mutating %s moved the fingerprint; choose a field OwnershipFingerprint excludes", name)
			}
			if _, err := rendezvous.Write(runDir, replacement); err != nil {
				t.Fatalf("replacement write: %v", err)
			}
			if err := reg.Remove(); err != nil {
				t.Fatalf("Remove = %v, want nil: the guard refused a same-PID record that is not ours, so the refusal is a completed no-op, not a retryable failure", err)
			}
			// The replacement's record survives byte-for-byte; the stale daemon
			// neither deleted nor rewrote it.
			artifact := filepath.Join(runDir, fmt.Sprintf("%d.json", pid))
			got, err := os.ReadFile(artifact)
			if err != nil {
				t.Fatalf("replacement record after Remove: %v", err)
			}
			want, err := json.Marshal(replacement)
			if err != nil {
				t.Fatalf("marshal replacement: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("on-disk record after Remove = %s, want the replacement's %s", got, want)
			}
		})
	}
}

// TestRegistrationRemoveStaysRetryableWhenReplacementCheckCannotAnswer pins the
// conservative half of diskHoldsReplacement. When the artifact cannot be parsed
// or the ownership re-check itself cannot run, the caller must keep its original
// retryable error rather than be handed a false completed no-op -- a transient
// filesystem failure must not look like a replacement.
func TestRegistrationRemoveStaysRetryableWhenReplacementCheckCannotAnswer(t *testing.T) {
	t.Run("unparseable same-pid artifact", func(t *testing.T) {
		runDir := t.TempDir()
		const pid = 8383
		reg := &Registration{}
		if err := reg.Register(runDir, rendezvous.Entry{PID: pid, ThreadID: "01OLD", SessionID: "01OLD"}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		artifact := filepath.Join(runDir, fmt.Sprintf("%d.json", pid))
		if err := os.WriteFile(artifact, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("corrupt artifact: %v", err)
		}
		err := reg.Remove()
		if err == nil {
			t.Fatal("Remove declared a completed no-op for an unparseable artifact; a transient read/parse failure must stay retryable")
		}
		if !strings.Contains(err.Error(), "parse rendezvous file") {
			t.Fatalf("Remove error = %v, want the original retryable parse failure", err)
		}
		if _, statErr := os.Stat(artifact); statErr != nil {
			t.Fatalf("refused artifact was unlinked: %v", statErr)
		}
	})

	t.Run("ownership re-check cannot take the lock", func(t *testing.T) {
		if !rendezvous.StrongOwnershipAvailable() {
			t.Skip("platform has no ownership lock to fail")
		}
		runDir := t.TempDir()
		const pid = 8484
		mine := rendezvous.Entry{
			PID:       pid,
			ThreadID:  "01MINE",
			SessionID: "01MINE",
			Model:     "model-a",
			StartedAt: time.Now(),
		}
		reg := &Registration{}
		if err := reg.Register(runDir, mine); err != nil {
			t.Fatalf("Register: %v", err)
		}
		// The on-disk record differs only in a fingerprint-excluded field, i.e.
		// it would be reported as a replacement if the re-check could read it. A
		// re-check that cannot run must not guess.
		replacement := mine
		replacement.Model = "other-model"
		if _, err := rendezvous.Write(runDir, replacement); err != nil {
			t.Fatalf("replacement write: %v", err)
		}
		lock := filepath.Join(runDir, fmt.Sprintf("%d.lock", pid))
		if err := os.Remove(lock); err != nil {
			t.Fatalf("remove ownership lock inode: %v", err)
		}
		if err := os.Mkdir(lock, 0o700); err != nil {
			t.Fatalf("sabotage ownership lock path: %v", err)
		}
		if err := reg.Remove(); err == nil {
			t.Fatal("Remove declared a completed no-op while the ownership re-check could not run")
		}
	})
}
