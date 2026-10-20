package agent

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A temp dir that is gone must not cost the session its retention when the
// user cache dir can serve: the store tries the cache dir only after the temp
// dir fails, which is what a dead inherited TMPDIR produces.
func TestSessionArtifactStoreFallsBackPastADeadTempDir(t *testing.T) {
	cache := t.TempDir()
	previous := sessionArtifactStoreCacheDir
	sessionArtifactStoreCacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { sessionArtifactStoreCacheDir = previous })
	// The first base is the process temp dir; a dead TMPDIR makes it fail.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))

	store, err := newSessionArtifactStore()
	if err != nil {
		t.Fatalf("store with a usable cache dir: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte("full output"))
	if err != nil || ref == "" {
		t.Fatalf("Put with fallback store = %q, %v", ref, err)
	}
}

// A working temp dir must not consult the cache dir at all: session scratch
// refuses that probe on its fast path for the same reason.
func TestSessionArtifactStoreDoesNotTouchTheCacheDirWhenTheTempDirWorks(t *testing.T) {
	previous := sessionArtifactStoreCacheDir
	consulted := false
	sessionArtifactStoreCacheDir = func() (string, error) {
		consulted = true
		return "", errors.New("must not be consulted")
	}
	t.Cleanup(func() { sessionArtifactStoreCacheDir = previous })

	store, err := newSessionArtifactStore()
	if err != nil {
		t.Fatalf("store from a working temp dir: %v", err)
	}
	store.Close()
	if consulted {
		t.Fatal("the cache dir was resolved although the process temp dir served")
	}
}

// Every base failing is still an error, and it names both failures.
func TestSessionArtifactStoreReportsEveryBaseFailure(t *testing.T) {
	deadTemp := filepath.Join(t.TempDir(), "gone")
	deadCache := filepath.Join(t.TempDir(), "also-gone")
	t.Setenv("TMPDIR", deadTemp)
	previous := sessionArtifactStoreCacheDir
	sessionArtifactStoreCacheDir = func() (string, error) { return deadCache, nil }
	t.Cleanup(func() { sessionArtifactStoreCacheDir = previous })

	store, err := newSessionArtifactStore()
	if err == nil || store != nil {
		t.Fatalf("store = %v, %v; want no store and an error", store, err)
	}
	for _, want := range []string{deadTemp, deadCache} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the failed base %q", err, want)
		}
	}
}
