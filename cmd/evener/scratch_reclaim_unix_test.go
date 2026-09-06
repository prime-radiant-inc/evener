//go:build unix

package main

import (
	"bytes"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
)

// TestReclaimCrashedSessionScratchRemovesOnlyAbandonedScratch drives the startup
// reclaim over the base a real session allocates from (os.TempDir, TMPDIR here)
// with three real scratch directories: one retained past the reclaim window with
// no owner left, one whose owner still holds its lease, and one retained just
// now. Only the first belongs to nobody.
func TestReclaimCrashedSessionScratchRemovesOnlyAbandonedScratch(t *testing.T) {
	base := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("TMPDIR", base)

	abandoned := newRetainedSessionScratch(t, base, workspace)
	fresh := newRetainedSessionScratch(t, base, workspace)
	live, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = live.Cleanup() })

	stale := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{abandoned, live.Dir} {
		if err := os.Chtimes(dir, stale, stale); err != nil {
			t.Fatalf("age %q: %v", dir, err)
		}
	}

	var warnings bytes.Buffer
	reclaimCrashedSessionScratch(&warnings)

	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("abandoned session scratch %q survived startup reclaim: %v", abandoned, err)
	}
	for _, dir := range []string{live.Dir, fresh} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("startup reclaim removed %q: %v", dir, err)
		}
	}
	if warnings.Len() != 0 {
		t.Errorf("startup reclaim reported a failure: %s", warnings.String())
	}
}

// newRetainedSessionScratch allocates a session scratch directory and retains it
// exactly as session teardown does: the lease is released and the directory is
// left on disk for the reclaim to find.
func newRetainedSessionScratch(t *testing.T, base, workspace string) string {
	t.Helper()
	scratch, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if err := scratch.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch.Dir) })
	return scratch.Dir
}
