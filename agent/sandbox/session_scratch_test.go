package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionScratchLifecycle(t *testing.T) {
	base := t.TempDir()
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(scratch.Dir), sessionScratchPrefix) {
		t.Errorf("session scratch %q must carry the %q prefix", scratch.Dir, sessionScratchPrefix)
	}
	if fi, err := os.Stat(scratch.Dir); err != nil || !fi.IsDir() {
		t.Fatalf("scratch must exist as a directory: %v", err)
	} else if got := fi.Mode().Perm(); got != 0o700 {
		t.Fatalf("scratch mode = %04o, want 0700", got)
	}
	if err := os.WriteFile(filepath.Join(scratch.Dir, "scratch"), []byte("x"), 0o600); err != nil {
		t.Fatalf("scratch must be writable: %v", err)
	}
	if err := scratch.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(scratch.Dir); !os.IsNotExist(err) {
		t.Fatalf("scratch remains after cleanup: %v", err)
	}
}

func TestSessionScratchCleanupRefusesUnownedPath(t *testing.T) {
	base := t.TempDir()
	unrelated := filepath.Join(base, "ordinary-temp")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	scratch := &SessionScratch{Dir: unrelated, base: base}
	if err := scratch.Cleanup(); err == nil {
		t.Fatal("Cleanup accepted a directory outside its allocated namespace")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("Cleanup touched unrelated directory: %v", err)
	}
}

func TestSessionScratchAgeSweepsOnlyStaleSerfDirs(t *testing.T) {
	base := t.TempDir()
	stale := filepath.Join(base, sessionScratchPrefix+"crashed")
	fresh := filepath.Join(base, sessionScratchPrefix+"fresh")
	foreign := filepath.Join(base, "not-serf-keepme")
	for _, dir := range []string{stale, fresh, foreign} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{stale, foreign} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale crashed-session directory remains: %v", err)
	}
	for _, dir := range []string{fresh, foreign} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("sweep removed %q: %v", dir, err)
		}
	}
}

func TestSessionScratchSweepSkipsOldLiveLease(t *testing.T) {
	base := t.TempDir()
	workspace := t.TempDir()
	live, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatalf("create live scratch: %v", err)
	}
	t.Cleanup(func() { _ = live.Cleanup() })
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(live.Dir, old, old); err != nil {
		t.Fatal(err)
	}

	next, err := NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatalf("create next scratch: %v", err)
	}
	t.Cleanup(func() { _ = next.Cleanup() })
	if _, err := os.Stat(live.Dir); err != nil {
		t.Fatalf("sweep removed old scratch with a live lease: %v", err)
	}
}

func TestSessionScratchSweepRemovesOldReleasedLease(t *testing.T) {
	base := t.TempDir()
	crashed := filepath.Join(base, sessionScratchPrefix+"released")
	if err := os.Mkdir(crashed, 0o700); err != nil {
		t.Fatal(err)
	}
	lease, contended, err := acquireScratchLease(filepath.Join(crashed, sessionScratchLeaseName))
	if err != nil || contended {
		t.Fatalf("acquire fixture lease: contended=%v err=%v", contended, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release fixture lease: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(crashed, old, old); err != nil {
		t.Fatal(err)
	}

	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if _, err := os.Stat(crashed); !os.IsNotExist(err) {
		t.Fatalf("old scratch with released lease remains: %v", err)
	}
}

func TestSessionScratchFallsBackOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	cacheBase := t.TempDir()
	canonicalCacheBase, err := filepath.EvalSymlinks(cacheBase)
	if err != nil {
		t.Fatal(err)
	}
	oldTemp, oldCache := sessionScratchTempDir, sessionScratchUserCacheDir
	sessionScratchTempDir = func() string { return filepath.Join(workspace, "tmp") }
	sessionScratchUserCacheDir = func() (string, error) { return cacheBase, nil }
	t.Cleanup(func() {
		sessionScratchTempDir = oldTemp
		sessionScratchUserCacheDir = oldCache
	})
	if err := os.MkdirAll(sessionScratchTempDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	scratch, err := NewSessionScratch("", workspace)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	t.Cleanup(func() { _ = scratch.Cleanup() })
	if !pathWithin(scratch.Dir, canonicalCacheBase) {
		t.Fatalf("scratch %q is not under cache fallback %q", scratch.Dir, cacheBase)
	}
	if pathWithin(scratch.Dir, workspace) {
		t.Fatalf("scratch %q is inside workspace %q", scratch.Dir, workspace)
	}
}

func TestSessionScratchDoesNotChmodCandidateBase(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o750); err != nil {
		t.Fatal(err)
	}
	scratch, err := NewSessionScratch(base, t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if got := fileMode(t, base); got != 0o750 {
		t.Fatalf("base mode after allocation = %04o, want 0750", got)
	}
	if got := fileMode(t, scratch.Dir); got != 0o700 {
		t.Fatalf("scratch mode = %04o, want 0700", got)
	}
	if err := scratch.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if got := fileMode(t, base); got != 0o750 {
		t.Fatalf("base mode after cleanup = %04o, want 0750", got)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
