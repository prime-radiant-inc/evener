//go:build linux

package sshconn

// The Linux arm's end-to-end integration: a fenced spawn through the real
// platform boundary agent/execenv creates (a cgroup v2 child under this
// process's delegated subtree), with the real operation store. It skips where
// the host delegates no writable cgroup2 subtree, exactly the "where available"
// the spec names, and it is the one place the production factory is exercised
// rather than a scripted boundary.

import (
	"context"
	"errors"
	"os"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// TestFencedSpawnRealCgroupArmRunsOwnedAndTearsDown drives one spawn through
// execenv's real Linux boundary: the child runs inside the pre-created cgroup,
// the intent is matched with the kernel's own start token, the clean exit drops
// the intent and removes the boundary, and the record can then complete — the
// state a boot would otherwise have to reap.
func TestFencedSpawnRealCgroupArmRunsOwnedAndTearsDown(t *testing.T) {
	store := newFenceStore(t)
	record := newRunningFenceRecord(t, store)
	scope := NewSpawnScope(record.ID, store)

	// Record the identity the real factory created so the teardown is
	// inspectable; the boundary itself is the platform default.
	var created BoundaryID
	scope.create = func(root string) (SpawnBoundary, error) {
		boundary, err := defaultSpawnBoundary(root)
		if err != nil {
			return nil, err
		}
		created = boundary.Identity()
		return boundary, nil
	}

	out, err := (execRunner{}).Run(WithSpawnScope(context.Background(), scope), []string{"/bin/sh", "-c", "printf real"}, nil)
	if errors.Is(err, ErrSpawnBoundaryUnavailable) {
		t.Skipf("no writable cgroup2 subtree on this host: %v", err)
	}
	if err != nil {
		t.Fatalf("fenced Run on the real cgroup arm: %v", err)
	}
	if string(out) != "real" {
		t.Fatalf("output = %q, want real", out)
	}
	if created.CgroupID == "" {
		t.Fatal("the real boundary reported no cgroup id")
	}
	if _, statErr := os.Stat(created.CgroupID); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the boundary %s survived a clean exit (stat = %v)", created.CgroupID, statErr)
	}
	if got := store.SpawnIntentRecords(); len(got) != 0 {
		t.Fatalf("intents after a clean exit = %d, want none", len(got))
	}
	if _, err := store.TransitionToState(record.ID, hostops.StateComplete, &hostops.Result{OK: true, Message: "done"}, "done"); err != nil {
		t.Fatalf("the record could not complete after a clean real-boundary spawn: %v", err)
	}
}
