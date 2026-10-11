package hub

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The staleness tolerance absorbs same-second checkouts, restores and pulls
// that touch file mtimes without changing content; anything newer than that
// means the browser guards would exercise yesterday's bundle — the trap that
// produced a false red/green verdict in #4336's safe-area round.
const distFreshnessTolerance = 2 * time.Second

// distStalenessError reports whether a dist build older than the newest
// source input is stale beyond the checkout tolerance. It is pure so the
// tolerance semantics stay covered in the ordinary (untagged) test lane.
func distStalenessError(distNewest, srcNewest time.Time) error {
	if age := srcNewest.Sub(distNewest); age > distFreshnessTolerance {
		return fmt.Errorf("production frontend is stale: newest source change is %s newer than the build. Run make build-web before the browser guards", age)
	}
	return nil
}

// The frontend's top-level build inputs live outside src/ and feed the
// bundle directly: the SPA entry, the build config, the build manifest's
// script, and the dependency lock. A change confined to any of them must
// read as stale too.
var distBuildInputs = []string{
	"frontend/index.html",
	"frontend/package.json",
	"frontend/vite.config.ts",
	"frontend/package-lock.json",
}

// buildInputNewest returns the newest mtime across the frontend's source
// tree, the shared AppWire TypeScript package, and the top-level build
// inputs.
func buildInputNewest(t *testing.T) time.Time {
	t.Helper()
	newest := newestMtime(t, "frontend/src")
	if tsNewest := newestMtime(t, "../../appwire-client/typescript"); tsNewest.After(newest) {
		newest = tsNewest
	}
	for _, input := range distBuildInputs {
		info, err := os.Stat(input)
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// newestMtime walks dir and returns the newest regular-file mtime, skipping
// node_modules and generated dist output (the AppWire package's `npm pack`
// prepack build regenerates its dist on every `make test-api-package`, and a
// fresh generated file must not read as a source change), but never the walk
// root itself: the staleness probe walks frontend/dist, and a root named dist
// must still be walked or the probe reports the zero time and every guard
// reads as stale. A missing directory reports the zero time: the preceding
// dist read already fails a run without a built frontend, and an absent
// source directory cannot make the dist stale.
func newestMtime(t *testing.T, dir string) time.Time {
	t.Helper()
	var newest time.Time
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != dir && (entry.Name() == "node_modules" || entry.Name() == "dist") {
				return fs.SkipDir
			}
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
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cannot inspect %s for staleness: %v", dir, err)
	}
	return newest
}

func TestDistStalenessTolerance(t *testing.T) {
	dist := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	if err := distStalenessError(dist, dist.Add(distFreshnessTolerance)); err != nil {
		t.Fatalf("a source change within the tolerance must not read as stale: %v", err)
	}
	err := distStalenessError(dist, dist.Add(distFreshnessTolerance+time.Second))
	if err == nil {
		t.Fatal("a source change beyond the tolerance must read as stale")
	}
}

// The staleness probe walks the built dist as its own root, so the
// generated-output skip must not swallow the walk when the root itself is
// named dist.
func TestNewestMtimeWalksRootNamedDist(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "index.html")
	if err := os.WriteFile(file, []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := newestMtime(t, root); !got.Equal(want.ModTime()) {
		t.Fatalf("the staleness probe must walk a root named dist: got %v, want %v", got, want.ModTime())
	}
}

// Generated output nested inside a walked tree must not read as a source
// change: the AppWire package's prepack regenerates its dist on every
// `make test-api-package`.
func TestNewestMtimeSkipsNestedDist(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "client.ts")
	if err := os.WriteFile(src, []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(root, "dist", "pack-fake.tgz")
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("packed"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := want.ModTime().Add(time.Hour)
	if err := os.Chtimes(generated, future, future); err != nil {
		t.Fatal(err)
	}
	if got := newestMtime(t, root); !got.Equal(want.ModTime()) {
		t.Fatalf("nested generated dist output must not read as a source change: got %v, want %v", got, want.ModTime())
	}
}

// A change confined to a top-level build input is the stale-bundle scenario
// itself, so each input must count toward the newest source change.
func TestBuildInputNewestIncludesTopLevelInputs(t *testing.T) {
	newest := buildInputNewest(t)
	for _, input := range distBuildInputs {
		info, err := os.Stat(input)
		if err != nil {
			t.Fatalf("top-level build input %s is missing: %v", input, err)
		}
		if newest.Before(info.ModTime()) {
			t.Fatalf("top-level build input %s must count toward the newest source change", input)
		}
	}
}
