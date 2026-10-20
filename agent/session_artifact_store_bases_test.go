package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// A temp dir that is gone must not cost the session its retention when another
// base can serve: the store walks the same two bases session scratch does.
func TestSessionArtifactStoreFallsBackPastADeadTempDir(t *testing.T) {
	previous := sessionArtifactStoreBasesFn
	sessionArtifactStoreBasesFn = func() []string {
		return []string{filepath.Join(t.TempDir(), "gone"), t.TempDir()}
	}
	t.Cleanup(func() { sessionArtifactStoreBasesFn = previous })

	store, err := sessionArtifactStoreFactory()
	if err != nil {
		t.Fatalf("store with a usable fallback base: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if ref, err := store.Put([]byte("full output")); err != nil || ref == "" {
		t.Fatalf("Put with fallback store = %q, %v", ref, err)
	}
}

// The fallback list must not offer an unusable base of its own: a cache dir
// that does not exist is skipped, leaving the process temp dir.
func TestSessionArtifactStoreBasesSkipMissingCacheDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	t.Setenv("XDG_CACHE_HOME", missing)
	bases := sessionArtifactStoreBasesFn()
	for _, base := range bases[1:] {
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			t.Fatalf("base %q is not an existing directory", base)
		}
	}
	if got := bases[0]; got != "" {
		t.Fatalf("first base = %q, want the process temp dir", got)
	}
}

// Every base failing is still a returned error rather than an empty store.
func TestSessionArtifactStoreReportsEveryBaseFailure(t *testing.T) {
	previous := sessionArtifactStoreBasesFn
	sessionArtifactStoreBasesFn = func() []string {
		return []string{filepath.Join(t.TempDir(), "gone"), filepath.Join(t.TempDir(), "also-gone")}
	}
	t.Cleanup(func() { sessionArtifactStoreBasesFn = previous })

	if store, err := sessionArtifactStoreFactory(); err == nil || store != nil {
		t.Fatalf("factory = %v, %v; want no store and an error", store, err)
	}
}
