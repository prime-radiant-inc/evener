//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

	age(t, abandoned, live.Dir)

	var warnings bytes.Buffer
	reclaimCrashedSessionScratch(workspace, &warnings)

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

// TestReclaimCrashedSessionScratchSkipsWorkspaceContainedTempBase pins the
// reclaim to the bases allocation actually used. A workspace that contains the
// temp dir pushes allocation onto the user cache dir, so the abandoned scratch
// to reclaim is there — and the prefixed directory sitting in the workspace's
// own temp dir was never Evener's to remove.
func TestReclaimCrashedSessionScratchSkipsWorkspaceContainedTempBase(t *testing.T) {
	workspace := t.TempDir()
	workspaceTemp := filepath.Join(workspace, "tmp")
	if err := os.MkdirAll(workspaceTemp, 0o700); err != nil {
		t.Fatalf("create workspace temp dir: %v", err)
	}
	cache := redirectUserCacheDir(t)
	t.Setenv("TMPDIR", workspaceTemp)

	abandoned := newRetainedSessionScratch(t, "", workspace)
	if !strings.HasPrefix(abandoned, cache+string(filepath.Separator)) {
		t.Fatalf("allocation for workspace %q chose %q, want a child of the cache base %q", workspace, abandoned, cache)
	}
	insideWorkspace := newRetainedSessionScratch(t, workspaceTemp, "")

	age(t, abandoned, insideWorkspace)

	var warnings bytes.Buffer
	reclaimCrashedSessionScratch(workspace, &warnings)

	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("abandoned scratch %q in the cache base survived startup reclaim: %v", abandoned, err)
	}
	if _, err := os.Stat(insideWorkspace); err != nil {
		t.Errorf("startup reclaim removed %q from a base inside the workspace: %v", insideWorkspace, err)
	}
	if warnings.Len() != 0 {
		t.Errorf("startup reclaim reported a failure: %s", warnings.String())
	}
}

// TestReclaimCrashedSessionScratchSweepsEveryAllocationBase covers the scratch
// this host allocated for OTHER workspaces: a workspace containing the temp dir
// allocates from the user cache dir, so a startup whose own temp dir is usable
// still has to reclaim there.
func TestReclaimCrashedSessionScratchSweepsEveryAllocationBase(t *testing.T) {
	workspace := t.TempDir()
	temp := t.TempDir()
	cache := redirectUserCacheDir(t)
	t.Setenv("TMPDIR", temp)

	fromTemp := newRetainedSessionScratch(t, temp, workspace)
	fromCache := newRetainedSessionScratch(t, cache, workspace)
	age(t, fromTemp, fromCache)

	var warnings bytes.Buffer
	reclaimCrashedSessionScratch(workspace, &warnings)

	for _, dir := range []string{fromTemp, fromCache} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("abandoned scratch %q survived startup reclaim: %v", dir, err)
		}
	}
	if warnings.Len() != 0 {
		t.Errorf("startup reclaim reported a failure: %s", warnings.String())
	}
}

// redirectUserCacheDir points os.UserCacheDir at a throwaway directory and
// returns its canonical path, so a test drives the real cache-base selection
// without touching the developer's cache.
func redirectUserCacheDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatalf("create cache base: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(cache)
	if err != nil {
		t.Fatalf("resolve cache base: %v", err)
	}
	return canonical
}

// age moves directories past the reclaim window.
func age(t *testing.T, dirs ...string) {
	t.Helper()
	stale := time.Now().Add(-48 * time.Hour)
	for _, dir := range dirs {
		if err := os.Chtimes(dir, stale, stale); err != nil {
			t.Fatalf("age %q: %v", dir, err)
		}
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
