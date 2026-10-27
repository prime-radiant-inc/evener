package hub

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/identifier"
)

// A cancelled pass must not remove an eligible archived tree; the next live
// pass must still discover and remove that same tree.
func TestReconcileArchivedScratchKeepsCancelledWorkForLaterPass(t *testing.T) {
	t.Setenv("TMPDIR", canonicalTempDir(t))
	id := hubtest.SessionID(t)
	projects := t.TempDir()
	project, err := identifier.ResolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSessionUpdatedAt(t, filepath.Join(projects, project.ID), id, project.CanonicalPath, time.Now())
	past := hubcore.NewPastIndex(filepath.Join(projects, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	if err := archive.Set("", "session", id, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	tree := mintEndedScratchTree(t, id)
	cfg := hubcore.WebConfig{Past: past, Archive: archive}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reconcileArchivedScratch(ctx, cfg, time.Now())
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("cancelled reconciliation removed an eligible tree: %v", err)
	}
	reconcileArchivedScratch(t.Context(), cfg, time.Now())
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatalf("later reconciliation left the eligible archived tree: %v", err)
	}
}

// Losing ctx either from Run to reconcile or from reconcile to the actual
// scanner must not enumerate and remove an eligible tree after shutdown.
func TestScratchReconcilerRunCancelsAtRealScannerEntry(t *testing.T) {
	testScratchReconcilerRunCancellation(t, true)
}

// A completed real listing does not authorize deletion after the owner cancels.
func TestScratchReconcilerRunKeepsCandidatesCancelledAfterRealListing(t *testing.T) {
	testScratchReconcilerRunCancellation(t, false)
}

func testScratchReconcilerRunCancellation(t *testing.T, cancelAtEntry bool) {
	t.Helper()
	// Keep both implicit scratch bases disposable, not the machine's caches.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMPDIR", canonicalTempDir(t))
	id := hubtest.SessionID(t)
	projects := t.TempDir()
	project, err := identifier.ResolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSessionUpdatedAt(t, filepath.Join(projects, project.ID), id, project.CanonicalPath, time.Now())
	past := hubcore.NewPastIndex(filepath.Join(projects, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	if err := archive.Set("", "session", id, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	tree := mintEndedScratchTree(t, id)
	cfg := hubcore.WebConfig{Past: past, Archive: archive}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	actualScanner := scanScratchTreeRootIDs
	scanned := make(chan []string, 1)
	scanScratchTreeRootIDs = func(scanCtx context.Context, bases ...string) []string {
		if cancelAtEntry {
			cancel()
		}
		ids := actualScanner(scanCtx, bases...)
		if !cancelAtEntry {
			cancel()
		}
		scanned <- ids
		return ids
	}
	t.Cleanup(func() { scanScratchTreeRootIDs = actualScanner })
	r := newScratchReconciler(cfg)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		waitFor(t, func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}, "scratch reconciler did not join its canceled Run")
	})
	r.Kick()
	var listed []string
	waitFor(t, func() bool {
		select {
		case listed = <-scanned:
			return true
		default:
			return false
		}
	}, "kicked Run did not reach the real scratch scanner")
	waitFor(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, "scratch reconciler did not join after cancellation")
	if cancelAtEntry && len(listed) != 0 {
		t.Errorf("scanner returned %v after cancellation at entry, want no candidates", listed)
	}
	if !cancelAtEntry && !slices.Contains(listed, id) {
		t.Errorf("real listing returned %v, want archived fixture %s before cancellation", listed, id)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("cancelled Run removed its eligible scratch tree: %v", err)
	}

	// A new owner, after the previous Run joined, can still reconcile the tree.
	scanScratchTreeRootIDs = actualScanner
	nextCtx, stopNext := context.WithCancel(t.Context())
	defer stopNext()
	next := newScratchReconciler(cfg)
	nextDone := make(chan struct{})
	go func() { defer close(nextDone); next.Run(nextCtx) }()
	t.Cleanup(func() {
		stopNext()
		waitFor(t, func() bool {
			select {
			case <-nextDone:
				return true
			default:
				return false
			}
		}, "fresh scratch reconciler did not join")
	})
	next.Kick()
	waitFor(t, func() bool {
		_, err := os.Lstat(tree)
		return os.IsNotExist(err)
	}, "a fresh Run did not remove the eligible released tree")
	stopNext()
	waitFor(t, func() bool {
		select {
		case <-nextDone:
			return true
		default:
			return false
		}
	}, "fresh scratch reconciler did not join after cancellation")
}
