package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestInstallLockSerializesConcurrentUpgrades proves two Upgrade calls
// against the same prefix do not interleave: the second waits for (or
// cleanly contends with) the first instead of racing Remove+Symlink pairs
// into EEXIST holes or mixed-release pairs. Fails today: hubUpdateMu is
// process-local and installExtractedBinaries takes no lock.
func TestInstallLockSerializesConcurrentUpgrades(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	server := checksumTestServer(t, archive, "")
	t.Cleanup(server.Close)

	prefix := t.TempDir()
	opts := func() Options {
		return Options{
			Requested: "snapshot", CurrentChannel: "snapshot",
			Prefix: prefix, GOOS: "linux", GOARCH: "amd64",
			RepoURL: server.URL,
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = Upgrade(t.Context(), opts())
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Upgrade: %v", err)
		}
	}
	for _, bin := range installBinaries {
		link := filepath.Join(prefix, "bin", bin)
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("readlink %s: %v", link, err)
		}
		if !strings.HasPrefix(target, prefix) {
			t.Fatalf("symlink %s -> %q, want inside %s", link, target, prefix)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("symlink target missing: %v", err)
		}
	}
}

// TestSymlinkSwapIsAtomic proves entrypoint replacement never leaves a
// hole: swapSymlink writes a temp link and renames over the destination,
// so concurrent readers see the old or the new target, never EEXIST or a
// missing path. Fails today: Remove+Symlink is two steps.
func TestSymlinkSwapIsAtomic(t *testing.T) {
	dir := t.TempDir()
	oldTarget := filepath.Join(dir, "old")
	newTarget := filepath.Join(dir, "new")
	for _, p := range []string{oldTarget, newTarget} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(oldTarget, link); err != nil {
		t.Fatal(err)
	}
	if err := swapSymlink(newTarget, link); err != nil {
		t.Fatalf("swapSymlink: %v", err)
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != newTarget {
		t.Fatalf("link -> %q, want %q", got, newTarget)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, "link.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp links left behind: %v", leftovers)
	}
}
