package server

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestThreadClearRequestHash pins the property the "client mutation ID reused
// with a different clear request" guard is built on: equal params hash equal
// and different params hash different. The error return is unreachable for
// the current all-strings params struct, so only the nil-error path runs here;
// it exists so an encoding failure surfaces instead of collapsing every
// request onto the hash of nil.
func TestThreadClearRequestHash(t *testing.T) {
	base := appwire.ThreadClearParams{Ref: "local:a", ClientMutationID: "clear-1", ExpectedInstanceID: "old"}
	first, err := threadClearRequestHash(base)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first == "" {
		t.Fatal("hash is empty")
	}
	same, err := threadClearRequestHash(base)
	if err != nil {
		t.Fatalf("hash of equal params: %v", err)
	}
	if same != first {
		t.Fatalf("equal params hashed differently: %q vs %q", first, same)
	}
	changed := base
	changed.ExpectedInstanceID = "new"
	different, err := threadClearRequestHash(changed)
	if err != nil {
		t.Fatalf("hash of different params: %v", err)
	}
	if different == first {
		t.Fatal("different params hashed identically, so the reuse guard cannot fire")
	}
}

// A symlink planted where a fixed "<journal>.tmp" would be must not redirect
// the journal write, and the journal must read back what was persisted.
func TestPersistThreadClearJournalDoesNotFollowSymlinkPlantedAtTempName(t *testing.T) {
	stateDir := t.TempDir()
	path := threadClearJournalPath(stateDir)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	records := map[string]threadClearRecord{
		"clear-1": {ClientMutationID: "clear-1", State: threadClearReserved},
	}

	if err := persistThreadClearJournal(path, records); err != nil {
		t.Fatalf("persist: %v", err)
	}

	if got, err := os.ReadFile(victim); err != nil || string(got) != "untouched" {
		t.Fatalf("symlink target = %q, %v; want untouched", got, err)
	}
	loaded, err := loadThreadClearJournal(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := loaded["clear-1"]; !ok || len(loaded) != 1 {
		t.Fatalf("loaded %v, want the persisted record", loaded)
	}
}
