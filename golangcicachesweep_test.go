package evener_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/gittest"
	"primeradiant.com/evener/internal/testfs"
)

// golangciCacheSweepScript is the local cleanup target under test. It deletes
// per-worktree golangci-lint analysis caches whose owning worktree no longer
// exists, so a fixture git repo with a registered extra worktree and a fake
// XDG_CACHE_HOME exercises the whole decision: hash the live roots, delete the
// dead-and-old, keep everything else.
const golangciCacheSweepScript = "scripts/ops/golangci-cache-sweep.sh"

// sweepCacheHash reproduces scripts/lib/golangci-lint-cache.sh's worktree key:
// the first 16 hex chars of sha256 over the worktree root's absolute path.
func sweepCacheHash(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:16]
}

// runSweep runs the sweeper against the fixture repo and returns its combined
// output and exit status. cwd stays in the fixture repo (that is where the
// live worktree set must be read from) while the script itself is the one
// from this repository's checkout. XDG_CACHE_HOME points the script at the
// fixture cache root, so nothing on the real machine is at risk.
func runSweep(t *testing.T, repo string, cacheHome string, args ...string) (string, int) {
	t.Helper()
	self, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(self, golangciCacheSweepScript)
	cmdArgs := append([]string{script}, args...)
	cmd := exec.Command("sh", cmdArgs...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+cacheHome)
	out, err := cmd.CombinedOutput()
	status := 0
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			status = exitErr.ExitCode()
		} else {
			t.Fatalf("run sweeper: %v\n%s", err, out)
		}
	}
	return string(out), status
}

// makeSweepCacheDir creates one cache entry under the fixture cache root and
// returns its path.
func makeSweepCacheDir(t *testing.T, cacheHome, name string) string {
	t.Helper()
	dir := filepath.Join(cacheHome, "evener", "golangci-lint", name)
	if err := os.MkdirAll(filepath.Join(dir, "00"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00", "entry-d"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestGolangciCacheSweepDeletesDeadCaches pins the sweeper's whole contract
// against a fixture: a cache dir whose worktree still exists is kept, a dead
// one past the age guard is deleted, a dead one younger than the guard is kept
// (the lane may have been disposed mid-lint), and a non-hex entry in the
// cache root is never touched. --dry-run reports the dead-and-old dir without
// deleting anything, and outside a git repo the script deletes nothing and
// fails loudly.
func TestGolangciCacheSweepDeletesDeadCaches(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"sh", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH (%v), so nothing can run the sweeper", tool, err)
		}
	}
	if testing.Short() {
		t.Skip("the fixture clones a git repo and registers a worktree")
	}

	fixture := testfs.CanonicalTempDir(t)
	repo := filepath.Join(fixture, "repo")
	// gittest.Run pins the author, committer, and config to nothing, so a
	// developer's global git configuration (commit.gpgsign, hooks, init
	// templates) cannot make the fixture nondeterministic.
	gittest.Run(t, fixture, "init", "-q", "repo")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "README")
	gittest.Run(t, repo, "commit", "-qm", "baseline")

	extra := filepath.Join(fixture, "extra-worktree")
	gittest.Run(t, repo, "worktree", "add", "-q", "--", extra)

	// The production helper and the sweeper both hash roots exactly the way
	// this test does. Pin the shared format on the helper side too: if
	// scripts/lib/golangci-lint-cache.sh ever drifts (say to 12 chars), the
	// sweeper would classify every live cache as dead, and only this
	// assertion would notice.
	selfDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	helperOut, err := exec.Command("bash", filepath.Join(selfDir, "scripts", "lib", "golangci-lint-cache.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("run golangci-lint-cache.sh: %v\n%s", err, helperOut)
	}
	helperKey := strings.TrimSpace(string(helperOut))
	if got := filepath.Base(helperKey); got != sweepCacheHash(selfDir) {
		t.Errorf("scripts/lib/golangci-lint-cache.sh prints %q, whose basename %q is not the sha256-16 key %q this test and the sweeper both compute; the hash format has drifted", helperKey, got, sweepCacheHash(selfDir))
	}

	// The cache root lives under a fixture XDG_CACHE_HOME, never the user's.
	cacheHome := filepath.Join(fixture, "cache")
	liveMain := makeSweepCacheDir(t, cacheHome, sweepCacheHash(repo))
	liveExtra := makeSweepCacheDir(t, cacheHome, sweepCacheHash(extra))
	// A LIVE worktree's cache with an old mtime: the live-key check must
	// protect it even when the age guard would not, because warm cache dirs
	// carry mtimes much older than their last write (writes into existing
	// shard dirs do not refresh the root mtime). This is the oracle's teeth:
	// a drifted hash (script or helper) classifies live caches as dead, and
	// only this case catches it.
	// Any dir expected to be treated as old gets an mtime three days back:
	// past any sane age guard.
	stale := time.Now().Add(-72 * time.Hour)
	liveOld := makeSweepCacheDir(t, cacheHome, sweepCacheHash(extra))
	if err := os.Chtimes(liveOld, stale, stale); err != nil {
		t.Fatal(err)
	}
	deadOld := makeSweepCacheDir(t, cacheHome, sweepCacheHash(filepath.Join(fixture, "long-gone")))
	deadFresh := makeSweepCacheDir(t, cacheHome, sweepCacheHash(filepath.Join(fixture, "just-disposed")))
	if err := os.Chtimes(deadOld, stale, stale); err != nil {
		t.Fatal(err)
	}
	// A non-hex-named entry must never be swept: the cache root belongs to
	// golangci-lint's layout, and anything unrecognized is someone else's.
	foreign := makeSweepCacheDir(t, cacheHome, "not-a-cache-key")
	// A hex-named but too-short entry is also not the sweeper's to judge.
	shortKey := makeSweepCacheDir(t, cacheHome, "abc123")

	out, status := runSweep(t, repo, cacheHome)
	if status != 0 {
		t.Fatalf("sweeper failed inside a git repo: exit %d\n%s", status, out)
	}
	if _, err := os.Stat(liveMain); err != nil {
		t.Errorf("the main checkout's cache was swept; a live worktree's cache must never be deleted\n%s", out)
	}
	if _, err := os.Stat(liveExtra); err != nil {
		t.Errorf("a registered worktree's cache was swept; a live worktree's cache must never be deleted\n%s", out)
	}
	if _, err := os.Stat(liveOld); err != nil {
		t.Errorf("a live worktree's cache with an old mtime was swept; the live-key check must protect it independently of the age guard\n%s", out)
	}
	if _, err := os.Stat(deadOld); err == nil {
		t.Errorf("the dead-and-3-days-old cache was kept; past the age guard it must be deleted\n%s", out)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat deadOld: %v", err)
	}
	if !strings.Contains(out, filepath.Base(deadOld)) {
		t.Errorf("the sweep report should name the key it swept:\n%s", out)
	}
	if _, err := os.Stat(deadFresh); err != nil {
		t.Errorf("a freshly-dead cache was swept; the age guard exists so a lane disposed mid-lint is never swept\n%s", out)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a non-hex entry was swept; the sweeper must judge only 16-hex cache keys\n%s", out)
	}
	if _, err := os.Stat(shortKey); err != nil {
		t.Errorf("a too-short hex entry was swept; the sweeper must judge only 16-hex cache keys\n%s", out)
	}
	if !strings.Contains(out, filepath.Base(deadOld)) {
		t.Errorf("the sweep report should name the key it swept:\n%s", out)
	}

	// --dry-run names the dead-and-old dir without deleting it.
	cacheHome2 := filepath.Join(fixture, "cache2")
	deadOld2 := makeSweepCacheDir(t, cacheHome2, sweepCacheHash(filepath.Join(fixture, "another-gone")))
	if err := os.Chtimes(deadOld2, stale, stale); err != nil {
		t.Fatal(err)
	}
	out, status = runSweep(t, repo, cacheHome2, "--dry-run")
	if status != 0 {
		t.Fatalf("dry run failed: exit %d\n%s", status, out)
	}
	if _, err := os.Stat(deadOld2); err != nil {
		t.Errorf("dry run deleted %s; --dry-run must not delete\n%s", deadOld2, out)
	} else if !strings.Contains(out, filepath.Base(deadOld2)) {
		t.Errorf("dry run did not name the dir it would delete:\n%s", out)
	}
	out2, status2 := runSweep(t, repo, cacheHome2)
	if status2 != 0 {
		t.Fatalf("the second real run failed: exit %d\n%s", status2, out2)
	}
	if _, err := os.Stat(deadOld2); err == nil {
		t.Errorf("the second real run kept the dead dir; it must delete it\n%s", out2)
	}

	// Outside a git repo the script must delete nothing and fail loudly:
	// `git worktree list` failing means the live-root set is unknown, and an
	// unknown set must never be treated as empty.
	farAway := testfs.CanonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(farAway, "README"), []byte("no repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(self, golangciCacheSweepScript))
	cmd.Dir = farAway
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+cacheHome)
	farOut, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the sweeper succeeded outside a git repo; an unknown live-root set must be a loud failure, not a sweep\n%s", farOut)
	}
	if _, statErr := os.Stat(deadFresh); statErr != nil {
		t.Errorf("something was deleted on the outside-a-repo failure path; the failure must delete nothing\n%s", farOut)
	}
}

// TestGolangciCacheSweepTargetIsWired pins the make target's existence and
// that it stays a local cleanup action, out of LINT_TARGETS: a required lint
// run must never depend on the very cache it is trying to warm.
func TestGolangciCacheSweepTargetIsWired(t *testing.T) {
	outBytes, err := exec.Command("make", "-n", "lint-cache-sweep").CombinedOutput()
	if err != nil {
		t.Fatalf("make -n lint-cache-sweep failed; the target must exist:\n%s", outBytes)
	}
	if !strings.Contains(string(outBytes), golangciCacheSweepScript) {
		t.Fatalf("lint-cache-sweep does not run the sweeper script:\n%s", outBytes)
	}
	targets, err := exec.Command("make", "-n", "lint").CombinedOutput()
	if err == nil && strings.Contains(string(targets), golangciCacheSweepScript) {
		t.Fatalf("lint-cache-sweep rode along in `make lint`; sweeping is a local cleanup action, not a gate")
	}
}
