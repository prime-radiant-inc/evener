package rendezvous

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ownershipTestEntry builds a fully-populated entry so field-level mutation
// tests cannot pass vacuously on zero values.
func ownershipTestEntry(pid int) Entry {
	return Entry{
		PID:          pid,
		Address:      "127.0.0.1:4100",
		Protocol:     "evener-appwire-v6",
		Endpoint:     "ws://127.0.0.1:4100/rpc",
		SourceID:     "local",
		ThreadID:     "01JTHREAD",
		SessionID:    "01JSESSION",
		WorkspaceRef: "local:01JWORKSPACE",
		InstanceID:   "01JSESSION",
		WorkingDir:   "/work",
		StateDir:     "/state",
		StartedAt:    time.Now(), // monotonic reading present on purpose
	}
}

func ownershipEntries(t *testing.T, dir string) []Entry {
	t.Helper()
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return entries
}

// TestRemoveIfOwnedRemovesExactEntry is the happy path, and it pins the
// StartedAt comparison rule: the in-memory entry carries a monotonic clock
// reading that JSON persistence strips, so the ownership check must use
// time.Time.Equal (instant equality), never ==.
func TestRemoveIfOwnedRemovesExactEntry(t *testing.T) {
	dir := t.TempDir()
	entry := ownershipTestEntry(4321)
	if _, err := Write(dir, entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := RemoveIfOwned(dir, entry); err != nil {
		t.Fatalf("RemoveIfOwned(exact in-memory entry): %v", err)
	}
	if got := ownershipEntries(t, dir); len(got) != 0 {
		t.Fatalf("entries after removal = %+v, want none", got)
	}
}

// TestRemoveIfOwnedRefusesStaleIdentity proves a remove carrying a stale
// snapshot of the entry — same PID, drifted content — refuses and leaves the
// live file untouched. This is what keeps a dying daemon's deferred cleanup
// from deleting its live replacement's rendezvous.
func TestRemoveIfOwnedRefusesStaleIdentity(t *testing.T) {
	live := ownershipTestEntry(5321)
	mutations := map[string]func(Entry) Entry{
		"started-at drift": func(e Entry) Entry { e.StartedAt = e.StartedAt.Add(time.Second); return e },
		"address drift":    func(e Entry) Entry { e.Address = "127.0.0.1:9999"; return e },
		"state-dir drift":  func(e Entry) Entry { e.StateDir = "/state/other"; return e },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Write(dir, live); err != nil {
				t.Fatalf("Write: %v", err)
			}
			stale := mutate(live)
			if err := RemoveIfOwned(dir, stale); err == nil {
				t.Fatal("RemoveIfOwned accepted a stale identity")
			}
			entries := ownershipEntries(t, dir)
			if len(entries) != 1 {
				t.Fatalf("entries after refused removal = %+v, want the live entry kept", entries)
			}
			if !entries[0].StartedAt.Equal(live.StartedAt) || entries[0].Address != live.Address || entries[0].StateDir != live.StateDir {
				t.Fatalf("live entry mutated by refused removal: %+v", entries[0])
			}
		})
	}
}

// TestRemoveIfOwnedLeavesReplacement proves one daemon's cleanup never
// touches another live daemon's file, and that a missing file is a clean
// no-op (the loser of an exit race must not error).
func TestRemoveIfOwnedLeavesReplacement(t *testing.T) {
	dir := t.TempDir()
	first := ownershipTestEntry(6100)
	second := ownershipTestEntry(6200)
	if _, err := Write(dir, first); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	if _, err := Write(dir, second); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	if err := RemoveIfOwned(dir, first); err != nil {
		t.Fatalf("RemoveIfOwned(first): %v", err)
	}
	entries := ownershipEntries(t, dir)
	if len(entries) != 1 || entries[0].PID != second.PID {
		t.Fatalf("entries after first daemon's cleanup = %+v, want only pid %d", entries, second.PID)
	}
	// Removing an already-absent entry is nil: exit raced with another
	// cleanup, nothing left to do.
	if err := RemoveIfOwned(dir, first); err != nil {
		t.Fatalf("RemoveIfOwned(missing): %v, want nil", err)
	}
}

// TestRemoveIfOwnedKeepsLockInode pins the lock-file lifecycle: the
// <pid>.lock inode is never deleted, so an fd held across the critical
// section can never be orphaned onto a fresh inode. Only meaningful where
// strong ownership exists.
func TestRemoveIfOwnedKeepsLockInode(t *testing.T) {
	if !StrongOwnershipAvailable() {
		t.Skip("strong ownership unavailable on this platform")
	}
	dir := t.TempDir()
	entry := ownershipTestEntry(7121)
	if _, err := Write(dir, entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := RemoveIfOwned(dir, entry); err != nil {
		t.Fatalf("RemoveIfOwned: %v", err)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.PID == entry.PID {
			t.Fatalf("rendezvous entry %+v survived removal", e)
		}
	}
	if _, err := os.Stat(ownershipLockPath(dir, entry.PID)); err != nil {
		t.Fatalf("lock inode for pid %d was deleted; a held fd would be orphaned", entry.PID)
	}
}

// TestDiskHoldsReplacementUsesExactOwnership pins the single identity authority
// the removal path re-checks a refused removal with: DiskHoldsReplacement must
// answer with exactly the comparison RemoveIfOwned refuses on. In particular
// every field OwnershipFingerprint omits still counts as identity, so a same-PID
// record differing only in an excluded field is a replacement even though it
// fingerprints identically.
func TestDiskHoldsReplacementUsesExactOwnership(t *testing.T) {
	t.Run("exact identity is not a replacement", func(t *testing.T) {
		dir := t.TempDir()
		entry := ownershipTestEntry(9311)
		if _, err := Write(dir, entry); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := DiskHoldsReplacement(dir, entry)
		if err != nil {
			t.Fatalf("DiskHoldsReplacement(exact): %v", err)
		}
		if got {
			t.Fatal("this process's own exact entry was reported as a replacement")
		}
	})

	t.Run("fingerprint-excluded drift is a replacement", func(t *testing.T) {
		mutations := map[string]func(*Entry){
			"hub token":  func(e *Entry) { e.HubToken = "different-secret" },
			"spawned by": func(e *Entry) { e.SpawnedBy = "evener-tui" },
			"agent":      func(e *Entry) { e.Agent = "other-agent" },
			"model":      func(e *Entry) { e.Model = "other-model" },
			"provider":   func(e *Entry) { e.Provider = "other-provider" },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				mine := ownershipTestEntry(9312)
				mine.Agent, mine.Model, mine.Provider = "evener", "model-a", "provider-a"
				mine.HubToken, mine.SpawnedBy = "hub-token-a", "evener-hub"
				if _, err := Write(dir, mine); err != nil {
					t.Fatalf("Write: %v", err)
				}
				replacement := mine
				mutate(&replacement)
				if OwnershipFingerprint(replacement) != OwnershipFingerprint(mine) {
					t.Fatalf("mutating %s moved the fingerprint; the case must be invisible to it", name)
				}
				if _, err := Write(dir, replacement); err != nil {
					t.Fatalf("Write replacement: %v", err)
				}
				got, err := DiskHoldsReplacement(dir, mine)
				if err != nil {
					t.Fatalf("DiskHoldsReplacement: %v", err)
				}
				if !got {
					t.Fatalf("same-PID record differing only in %s was not reported as a replacement", name)
				}
				// The guard itself is unchanged: it still refuses this record.
				if err := RemoveIfOwned(dir, mine); err == nil {
					t.Fatal("RemoveIfOwned accepted a record differing in an excluded field")
				}
			})
		}
	})

	t.Run("fingerprinted drift is a replacement", func(t *testing.T) {
		dir := t.TempDir()
		mine := ownershipTestEntry(9313)
		if _, err := Write(dir, mine); err != nil {
			t.Fatalf("Write: %v", err)
		}
		replacement := mine
		replacement.StartedAt = mine.StartedAt.Add(time.Second)
		if _, err := Write(dir, replacement); err != nil {
			t.Fatalf("Write replacement: %v", err)
		}
		got, err := DiskHoldsReplacement(dir, mine)
		if err != nil {
			t.Fatalf("DiskHoldsReplacement: %v", err)
		}
		if !got {
			t.Fatal("a record with a drifted fingerprinted field was not reported as a replacement")
		}
	})

	t.Run("missing artifact is not a replacement", func(t *testing.T) {
		dir := t.TempDir()
		got, err := DiskHoldsReplacement(dir, ownershipTestEntry(9314))
		if err != nil {
			t.Fatalf("DiskHoldsReplacement(missing): %v", err)
		}
		if got {
			t.Fatal("a missing artifact was reported as a replacement")
		}
	})

	t.Run("unparseable artifact errors instead of answering", func(t *testing.T) {
		dir := t.TempDir()
		const pid = 9315
		target := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
		if err := os.WriteFile(target, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("corrupt artifact: %v", err)
		}
		got, err := DiskHoldsReplacement(dir, ownershipTestEntry(pid))
		if err == nil {
			t.Fatal("DiskHoldsReplacement answered for an unparseable artifact instead of erroring")
		}
		if got {
			t.Fatal("an unparseable artifact was reported as a replacement")
		}
	})

	t.Run("ownership lock failure errors instead of answering", func(t *testing.T) {
		if !StrongOwnershipAvailable() {
			t.Skip("strong ownership unavailable on this platform")
		}
		dir := t.TempDir()
		const pid = 9316
		if err := os.Mkdir(ownershipLockPath(dir, pid), 0o700); err != nil {
			t.Fatalf("sabotage ownership lock path: %v", err)
		}
		got, err := DiskHoldsReplacement(dir, ownershipTestEntry(pid))
		if err == nil {
			t.Fatal("DiskHoldsReplacement answered without taking the ownership lock")
		}
		if got {
			t.Fatal("a failed re-check was reported as a replacement")
		}
	})
}
