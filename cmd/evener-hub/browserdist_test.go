//go:build browserguard

package hub

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// requireFreshDist fails a browser guard when the embedded production
// frontend is missing or plausibly stale. The guard serves the built dist,
// not the source tree, so running `go test` after a source edit silently
// exercises yesterday's bundle — the trap that produced a false red/green
// verdict in #4336's safe-area round. Rebuilding is cheap; a stale verdict is
// worse than none. The two-second tolerance absorbs same-second checkouts,
// restores and pulls that touch file mtimes without changing content.
func requireFreshDist(t *testing.T) {
	t.Helper()
	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Fatalf("production frontend is required, run make build-web: %v", err)
	}
	distNewest := newestMtime(t, "frontend/dist")
	srcNewest := newestMtime(t, "frontend/src")
	if tsNewest := newestMtime(t, "../../appwire-client/typescript"); tsNewest.After(srcNewest) {
		srcNewest = tsNewest
	}
	if age := srcNewest.Sub(distNewest); age > 2*time.Second {
		t.Fatalf("production frontend is stale: newest source change is %s newer than the build. Run make build-web before the browser guards", age)
	}
}

// newestMtime walks dir and returns the newest regular-file mtime, skipping
// node_modules. A missing directory reports the zero time: the preceding
// dist read already fails a run without a built frontend, and an absent
// source directory cannot make the dist stale.
func newestMtime(t *testing.T, dir string) time.Time {
	t.Helper()
	var newest time.Time
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "node_modules" {
			return fs.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("cannot inspect %s for staleness: %v", dir, err)
	}
	return newest
}
