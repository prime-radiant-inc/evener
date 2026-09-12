//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
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
// still has to reclaim there — and a live owner in that base keeps its scratch
// just as it does in the temp base.
func TestReclaimCrashedSessionScratchSweepsEveryAllocationBase(t *testing.T) {
	workspace := t.TempDir()
	temp := t.TempDir()
	cache := redirectUserCacheDir(t)
	t.Setenv("TMPDIR", temp)

	fromTemp := newRetainedSessionScratch(t, temp, workspace)
	fromCache := newRetainedSessionScratch(t, cache, workspace)
	liveInCache, err := sandbox.NewSessionScratch(cache, workspace)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = liveInCache.Cleanup() })
	age(t, fromTemp, fromCache, liveInCache.Dir)

	var warnings bytes.Buffer
	reclaimCrashedSessionScratch(workspace, &warnings)

	for _, dir := range []string{fromTemp, fromCache} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("abandoned scratch %q survived startup reclaim: %v", dir, err)
		}
	}
	if _, err := os.Stat(liveInCache.Dir); err != nil {
		t.Errorf("startup reclaim removed the leased scratch %q from the cache base: %v", liveInCache.Dir, err)
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

// TestReclaimCrashedSessionScratchAnchorsAtTheWorktreeRoot pins the reclaim to
// the workspace allocation anchors to. A command run from a subdirectory of a
// git worktree allocates against the worktree root, so a scratch base anywhere
// under that root — not merely under the directory the command started in — is
// one Evener never allocates into.
func TestReclaimCrashedSessionScratchAnchorsAtTheWorktreeRoot(t *testing.T) {
	workspace := newGitWorkspace(t)
	workspaceTemp := filepath.Join(workspace, "tmp")
	sub := filepath.Join(workspace, "sub")
	for _, dir := range []string{workspaceTemp, sub} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %q: %v", dir, err)
		}
	}
	cache := redirectUserCacheDir(t)
	t.Setenv("TMPDIR", workspaceTemp)

	insideWorkspace := newRetainedSessionScratch(t, workspaceTemp, "")
	abandoned := newRetainedSessionScratch(t, cache, workspace)
	age(t, insideWorkspace, abandoned)

	var warnings bytes.Buffer
	reclaimCrashedSessionScratch(sub, &warnings)

	if _, err := os.Stat(insideWorkspace); err != nil {
		t.Errorf("reclaim from %q removed %q, a base inside the worktree: %v", sub, insideWorkspace, err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("abandoned scratch %q in the cache base survived the reclaim: %v", abandoned, err)
	}
	if warnings.Len() != 0 {
		t.Errorf("startup reclaim reported a failure: %s", warnings.String())
	}
}

// newGitWorkspace returns a throwaway directory that reads as a git worktree
// root, which is what a session anchors its scratch to.
func newGitWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	return root
}

// TestServeReclaimsScratchAnchoredAtItsDirFlag drives serve's own flag parsing
// from an unrelated working directory: the daemon allocates its scratch against
// --dir, so the reclaim it starts has to read that same workspace and leave a
// scratch base inside it alone.
func TestServeReclaimsScratchAnchoredAtItsDirFlag(t *testing.T) {
	stateDir := t.TempDir()
	workspace := newGitWorkspace(t)
	workspaceTemp := filepath.Join(workspace, "tmp")
	if err := os.MkdirAll(workspaceTemp, 0o700); err != nil {
		t.Fatalf("create workspace temp dir: %v", err)
	}
	cache := redirectUserCacheDir(t)
	t.Setenv("TMPDIR", workspaceTemp)

	insideWorkspace := newRetainedSessionScratch(t, workspaceTemp, "")
	abandoned := newRetainedSessionScratch(t, cache, workspace)
	age(t, insideWorkspace, abandoned)

	var warnings bytes.Buffer
	stop := errors.New("stop after startup")
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.reclaimScratch = func(workingDir string) { reclaimCrashedSessionScratch(workingDir, &warnings) }
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) { return nil, nil, stop }

	args := []string{"--dir", workspace, "--state-dir", stateDir, "--model", "openai/gpt-test"}
	if err := runServeWithDeps(args, deps); !errors.Is(err, stop) {
		t.Fatalf("serve error = %v, want %v", err, stop)
	}
	if _, err := os.Stat(insideWorkspace); err != nil {
		t.Errorf("serve's reclaim removed %q, a base inside its --dir workspace: %v", insideWorkspace, err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("abandoned scratch %q in the cache base survived serve's reclaim: %v", abandoned, err)
	}
	if warnings.Len() != 0 {
		t.Errorf("serve's reclaim reported a failure: %s", warnings.String())
	}
}
