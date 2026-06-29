package promoter

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPersistPaths_DefaultOff is the load-bearing gate contract: with the env
// unset, PersistPaths returns the caller's fallback temp paths verbatim and
// persist=false, so a normal fuzz run writes nothing into the tree.
func TestPersistPaths_DefaultOff(t *testing.T) {
	t.Setenv(persistEnv, "")

	pkgDir := t.TempDir()
	fbEmit := filepath.Join(t.TempDir(), "emit")
	fbBuckets := filepath.Join(t.TempDir(), "buckets.json")

	emit, buckets, persist := PersistPaths(pkgDir, fbEmit, fbBuckets)
	if persist {
		t.Fatal("persist = true with env unset, want false")
	}
	if emit != fbEmit {
		t.Fatalf("emitDir = %q, want fallback %q", emit, fbEmit)
	}
	if buckets != fbBuckets {
		t.Fatalf("bucketsPath = %q, want fallback %q", buckets, fbBuckets)
	}
}

// TestPersistPaths_NonTruthyOff proves only the canonical truthy spellings flip
// persistence on; anything else keeps the default-off behavior.
func TestPersistPaths_NonTruthyOff(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off", "maybe", " "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(persistEnv, v)
			_, _, persist := PersistPaths(t.TempDir(), "e", "b")
			if persist {
				t.Fatalf("persist = true for %q, want false", v)
			}
		})
	}
}

// TestPersistPaths_OnWritesIntoTree proves that when the env is truthy and a
// repo root is found, the emit dir is the package dir and the bucket store is
// the committed repo-root fuzz/state/buckets.json.
func TestPersistPaths_OnWritesIntoTree(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on", "TRUE"} {
		t.Run(v, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.25.0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			pkgDir := filepath.Join(root, "agent")
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				t.Fatal(err)
			}

			t.Setenv(persistEnv, v)
			emit, buckets, persist := PersistPaths(pkgDir, "fb-emit", "fb-buckets")
			if !persist {
				t.Fatalf("persist = false for %q, want true", v)
			}
			if emit != pkgDir {
				t.Fatalf("emitDir = %q, want pkgDir %q", emit, pkgDir)
			}
			want := filepath.Join(root, "fuzz", "state", "buckets.json")
			if buckets != want {
				t.Fatalf("bucketsPath = %q, want %q", buckets, want)
			}
		})
	}
}

// TestPersistPaths_OnButNoRootDeclines proves that an enabled env with no
// discoverable go.work declines persistence rather than writing outside a repo.
func TestPersistPaths_OnButNoRootDeclines(t *testing.T) {
	// A directory tree with no go.work anywhere above it.
	pkgDir := t.TempDir()
	t.Setenv(persistEnv, "1")

	emit, buckets, persist := PersistPaths(pkgDir, "fb-emit", "fb-buckets")
	if persist {
		t.Fatal("persist = true with no repo root, want false")
	}
	if emit != "fb-emit" || buckets != "fb-buckets" {
		t.Fatalf("declined persistence must return fallbacks, got emit=%q buckets=%q", emit, buckets)
	}
}
