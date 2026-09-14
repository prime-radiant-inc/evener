//go:build linux || darwin

package rendezvous

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// TestRemoveUnlessRegularRefusesAReplacementPublishedInTheWindow is the
// atomicity claim: the non-regular check and the unlink both happen under the
// per-PID ownership lock, so a replacement daemon reusing this PID cannot have
// its live entry deleted by the stale cleanup's fallback.
//
// The interposition publishes the replacement's regular entry at the artifact
// path immediately BEFORE the ownership lock is acquired -- the exact instant a
// bare os.Stat-then-Remove leaves open. A check made outside the lock would
// have seen the non-regular artifact and gone on to unlink the replacement;
// under the lock the check sees the regular entry and refuses.
func TestRemoveUnlessRegularRefusesAReplacementPublishedInTheWindow(t *testing.T) {
	dir := t.TempDir()
	const pid = 7404
	target := filepath.Join(dir, "7404.json")
	// A non-regular artifact is what sends the caller to the fallback in the
	// first place (RemoveIfOwned cannot read a directory as an entry).
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("seed non-regular artifact: %v", err)
	}
	replacement := ownershipTestEntry(pid)
	payload, err := json.Marshal(replacement)
	if err != nil {
		t.Fatalf("marshal replacement: %v", err)
	}

	prevFlock := ownershipFlock
	var once sync.Once
	ownershipFlock = func(fd int, how int) error {
		if how == syscall.LOCK_EX {
			once.Do(func() {
				if err := os.Remove(target); err != nil {
					t.Errorf("clear non-regular artifact: %v", err)
					return
				}
				if err := os.WriteFile(target, payload, 0o600); err != nil {
					t.Errorf("publish replacement entry: %v", err)
				}
			})
		}
		return prevFlock(fd, how)
	}
	t.Cleanup(func() { ownershipFlock = prevFlock })

	if err := RemoveUnlessRegular(dir, pid); err == nil {
		t.Fatal("RemoveUnlessRegular unlinked a replacement that published a regular entry under the lock")
	}
	kept, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("replacement's live entry was deleted: %v", err)
	}
	if !bytes.Equal(kept, payload) {
		t.Fatalf("replacement's live entry mutated: %s", kept)
	}
}
