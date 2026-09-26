package sshconn

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestManagerHostLocksReleasedAfterRemove pins the round-7 M3 finding: the
// per-host gates lived in a map that only ever grew — AddHost inserted one
// mutex per name and nothing ever removed it — so a hub churning unique host
// names (add, remove, never the same name twice) leaked every gate for the
// manager's lifetime. The gates are refcounted now: every acquisition pairs
// with a release, the last release drops the entry, and the map holds gates
// only for names with live users.
func TestManagerHostLocksReleasedAfterRemove(t *testing.T) {
	m := newTestManager(t, testRegistry(t), &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}, Options{})
	const adds = 32
	for i := range adds {
		entry := hostreg.Host{Name: fmt.Sprintf("churn-%02d", i), SSH: "churn.example"}
		if err := m.AddHost(entry); err != nil {
			t.Fatalf("AddHost(%d): %v", i, err)
		}
		if err := m.RemoveHost(entry.Name); err != nil {
			t.Fatalf("RemoveHost(%d): %v", i, err)
		}
	}
	if got := len(m.locks); got != 0 {
		t.Fatalf("manager retains %d host-lock entries after %d completed add/remove cycles; a finished cycle must leave no gate behind", got, adds)
	}
	// A cleaned name gates correctly again: the next acquisition builds a fresh
	// entry rather than colliding with a stale one. The gate exists only while
	// a user holds it, so a completed add/remove cycle still leaves none — a
	// name with no live user owns no entry, exactly the bounded state the
	// refcount exists for.
	if err := m.AddHost(hostreg.Host{Name: "churn-00", SSH: "churn.example"}); err != nil {
		t.Fatalf("AddHost(churn-00) after cleanup: %v", err)
	}
	if err := m.RemoveHost("churn-00"); err != nil {
		t.Fatalf("RemoveHost(churn-00) after re-add: %v", err)
	}
	if got := len(m.locks); got != 0 {
		t.Fatalf("host-lock entries after the completed re-add cycle = %d, want 0", got)
	}
}

// TestManagerHostLockNotDroppedWhileHeld pins the hazard the refcounted
// cleanup must not introduce: a gate may never be dropped while a holder still
// uses it, or the next acquisition for the name would build a second mutex and
// two goroutines would hold "the" gate at once. The test holds alpha's gate,
// runs an unrelated name through a full add/remove cycle — the cleanup path —
// and then proves a fresh acquisition of alpha still excludes against the held
// gate instead of sailing through a replacement.
func TestManagerHostLockNotDroppedWhileHeld(t *testing.T) {
	m := newTestManager(t, testRegistry(t, hostreg.Host{Name: "alpha", SSH: "alpha.example"}), &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}, Options{})
	lock := m.hostLock("alpha")
	// Pair the acquisition with its release the way every other holder does —
	// an unpaired hostLock leaves the refs entry behind, exactly the leak the
	// refcount exists to bound — and prove the release is the gate's last
	// reference: the map must drop the name once the holder lets go.
	defer func() {
		m.releaseHostLock("alpha")
		if got := len(m.locks); got != 0 {
			t.Errorf("host-lock entries after the held gate's release = %d, want 0", got)
		}
	}()
	lock.Lock()

	// Unrelated churn runs the cleanup path while alpha's gate is held.
	if err := m.AddHost(hostreg.Host{Name: "beta", SSH: "beta.example"}); err != nil {
		lock.Unlock()
		t.Fatalf("AddHost(beta): %v", err)
	}
	if err := m.RemoveHost("beta"); err != nil {
		lock.Unlock()
		t.Fatalf("RemoveHost(beta): %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- m.RemoveHost("alpha") }()
	select {
	case <-done:
		lock.Unlock()
		t.Fatal("RemoveHost(alpha) completed while its gate was held: cleanup dropped a live holder's gate")
	case <-time.After(150 * time.Millisecond):
	}
	lock.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RemoveHost(alpha) after the release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveHost(alpha) never completed after its gate was released")
	}
}
