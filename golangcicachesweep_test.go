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

// sweepRepoHash reproduces the repository bucket both the helper and the
// sweeper derive: the first 16 hex chars of sha256 over the repository's
// absolute Git common directory. Every worktree of one clone resolves to the
// same common directory and no other clone does, which is what keeps one
// clone's sweeper from judging another clone's caches.
func sweepRepoHash(commonDir string) string {
	sum := sha256.Sum256([]byte(commonDir))
	return hex.EncodeToString(sum[:])[:16]
}

// sweepEnv returns the environment every sweeper and helper subprocess in
// this file runs with: gittest's hermetic environment — no ambient GIT_DIR
// or git configuration can redirect the script's git calls — plus the
// fixture cache root and the sweep age the fixture means.
// GOLANGCI_SWEEP_MIN_AGE_HOURS is the documented operator knob (168 keeps a
// week), so inheriting an exported value would change what the fixtures'
// 72-hour-old mtimes mean and fail the suite on a machine that simply
// followed the script's usage comment. Both keys are dropped from the base
// rather than appended to it: a duplicated environ entry resolves by
// whichever occurrence the platform reads first, not reliably by the last.
func sweepEnv(cacheHome string) []string {
	var env []string
	for _, entry := range gittest.Env() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "XDG_CACHE_HOME" || key == "GOLANGCI_SWEEP_MIN_AGE_HOURS" {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"XDG_CACHE_HOME="+cacheHome,
		"GOLANGCI_SWEEP_MIN_AGE_HOURS=48",
	)
}

// runSweep runs the sweeper against the fixture repo and returns its combined
// output and exit status. cwd stays in the fixture repo (that is where the
// live worktree set must be read from) while the script itself is the one
// from this repository's checkout. XDG_CACHE_HOME points the script at the
// fixture cache root, so nothing on the real machine is at risk.
func runSweep(t *testing.T, repo string, cacheHome string, args ...string) (string, int) {
	t.Helper()
	return runSweepWithEnv(t, repo, sweepEnv(cacheHome), args...)
}

func runSweepWithEnv(t *testing.T, repo string, env []string, args ...string) (string, int) {
	t.Helper()
	return runSweepWithShellAndEnv(t, "sh", repo, env, args...)
}

func runSweepWithShellAndEnv(t *testing.T, shell, repo string, env []string, args ...string) (string, int) {
	t.Helper()
	self, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(self, golangciCacheSweepScript)
	cmdArgs := append([]string{script}, args...)
	cmd := exec.Command(shell, cmdArgs...)
	cmd.Dir = repo
	cmd.Env = env
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

// makeSweepCacheDir creates one cache entry named name under parent — a
// repository's bucket, or the bare cache root for a pre-bucketing layout
// entry — and returns its path.
func makeSweepCacheDir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
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
// bucket is never touched. The sweep is scoped to this repository's bucket,
// so another clone's caches — live or dead — and pre-bucketing entries with
// no owner marker are never candidates. --dry-run reports the dead-and-old
// dir without deleting anything, and outside a git repo the script deletes
// nothing and fails loudly.
func TestGolangciCacheSweepDeletesDeadCaches(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"sh", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH (%v), so nothing can run the sweeper", tool, err)
		}
	}
	if testing.Short() {
		t.Skip("the fixture clones git repos and registers worktrees")
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

	// An independent clone sharing the same cache home: the two repositories
	// never share a Git common directory, so a sweep from `repo` must not
	// judge `repo-b`'s worktrees dead — the cross-clone deletion the bucket
	// scoping exists to stop.
	repoB := filepath.Join(fixture, "repo-b")
	gittest.Run(t, fixture, "init", "-q", "repo-b")
	if err := os.WriteFile(filepath.Join(repoB, "README"), []byte("fixture-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repoB, "add", "README")
	gittest.Run(t, repoB, "commit", "-qm", "baseline")
	extraB := filepath.Join(fixture, "extra-worktree-b")
	gittest.Run(t, repoB, "worktree", "add", "-q", "--", extraB)

	commonA := gittest.Run(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	commonB := gittest.Run(t, repoB, "rev-parse", "--path-format=absolute", "--git-common-dir")

	// The production helper and the sweeper both hash roots and common
	// directories exactly the way this test does. Pin the shared format on
	// the helper side too: if scripts/lib/golangci-lint-cache.sh ever drifts
	// (say to 12 chars, or off the common directory), the sweeper would
	// classify every live cache as dead or sweep outside its bucket, and
	// only this assertion would notice.
	selfDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	helperCmd := exec.Command("bash", filepath.Join(selfDir, "scripts", "lib", "golangci-lint-cache.sh"))
	helperCmd.Env = sweepEnv(filepath.Join(fixture, "cache"))
	helperOut, err := helperCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run golangci-lint-cache.sh: %v\n%s", err, helperOut)
	}
	helperKey := strings.TrimSpace(string(helperOut))
	if got := filepath.Base(helperKey); got != sweepCacheHash(selfDir) {
		t.Errorf("scripts/lib/golangci-lint-cache.sh prints %q, whose basename %q is not the sha256-16 worktree key %q this test and the sweeper both compute; the worktree hash format has drifted", helperKey, got, sweepCacheHash(selfDir))
	}
	selfCommon := gittest.Run(t, selfDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	helperBucket := filepath.Base(filepath.Dir(helperKey))
	if helperBucket != sweepRepoHash(selfCommon) {
		t.Errorf("scripts/lib/golangci-lint-cache.sh prints %q, whose bucket %q is not the sha256-16 key %q over this checkout's common directory %q; the bucket hash format has drifted", helperKey, helperBucket, sweepRepoHash(selfCommon), selfCommon)
	}

	// The cache root lives under a fixture XDG_CACHE_HOME, never the user's.
	cacheHome := filepath.Join(fixture, "cache")
	bareRoot := filepath.Join(cacheHome, "evener", "golangci-lint")
	bucketA := filepath.Join(bareRoot, sweepRepoHash(commonA))
	bucketB := filepath.Join(bareRoot, sweepRepoHash(commonB))

	liveMain := makeSweepCacheDir(t, bucketA, sweepCacheHash(repo))
	// A LIVE worktree's cache with an old mtime: the live-key check must
	// protect it even when the age guard would not, because warm cache dirs
	// carry mtimes much older than their last write (writes into existing
	// shard dirs do not refresh the root mtime). This is the oracle's teeth:
	// a drifted hash (script or helper) classifies live caches as dead, and
	// only this case catches it.
	// Any dir expected to be treated as old gets an mtime three days back:
	// past any sane age guard.
	stale := time.Now().Add(-72 * time.Hour)
	liveOld := makeSweepCacheDir(t, bucketA, sweepCacheHash(extra))
	if err := os.Chtimes(liveOld, stale, stale); err != nil {
		t.Fatal(err)
	}
	deadOld := makeSweepCacheDir(t, bucketA, sweepCacheHash(filepath.Join(fixture, "long-gone")))
	deadFresh := makeSweepCacheDir(t, bucketA, sweepCacheHash(filepath.Join(fixture, "just-disposed")))
	if err := os.Chtimes(deadOld, stale, stale); err != nil {
		t.Fatal(err)
	}
	// Entries the 16-hex name check alone must protect. Both are backdated
	// past the age guard, so the age filter cannot be what saves them: the
	// name check is the only thing standing between them and the delete,
	// which is what these fixtures prove (breaking the name filter sweeps
	// both; verified by mutation while writing this).
	foreign := makeSweepCacheDir(t, bucketA, "not-a-cache-key")
	shortKey := makeSweepCacheDir(t, bucketA, "abc123")
	if err := os.Chtimes(foreign, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(shortKey, stale, stale); err != nil {
		t.Fatal(err)
	}
	// Clone B's bucket: a LIVE worktree's cache old enough that an unscoped
	// sweep would take it, and one of clone B's own dead caches. Clone A can
	// attribute neither — an unscoped `git worktree list` never names clone
	// B's trees — so neither is clone A's to delete; clone B's own sweep
	// reclaims its dead.
	cloneBOld := makeSweepCacheDir(t, bucketB, sweepCacheHash(extraB))
	cloneBDead := makeSweepCacheDir(t, bucketB, sweepCacheHash(filepath.Join(fixture, "b-gone")))
	if err := os.Chtimes(cloneBOld, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cloneBDead, stale, stale); err != nil {
		t.Fatal(err)
	}
	// A pre-bucketing cache dir directly under the bare root: it carries no
	// owning-clone marker, so which clone's sweeper may judge it is unknown,
	// and the sweeper leaves it to the operator.
	legacy := makeSweepCacheDir(t, bareRoot, sweepCacheHash(filepath.Join(fixture, "legacy-gone")))
	if err := os.Chtimes(legacy, stale, stale); err != nil {
		t.Fatal(err)
	}

	out, status := runSweep(t, repo, cacheHome)
	if status != 0 {
		t.Fatalf("sweeper failed inside a git repo: exit %d\n%s", status, out)
	}
	if _, err := os.Stat(liveMain); err != nil {
		t.Errorf("the main checkout's cache was swept; a live worktree's cache must never be deleted\n%s", out)
	}
	if _, err := os.Stat(liveOld); err != nil {
		t.Errorf("a registered worktree's cache with an old mtime was swept; the live-key check must protect it independently of the age guard\n%s", out)
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
	if _, err := os.Stat(cloneBOld); err != nil {
		t.Errorf("another clone's live cache was swept; a sweeper scoped to this repository's bucket must never judge another clone's worktrees dead\n%s", out)
	}
	if _, err := os.Stat(cloneBDead); err != nil {
		t.Errorf("another clone's dead cache was swept; clone A cannot attribute it, so it is not clone A's to delete (clone B's own sweep reclaims it)\n%s", out)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("a pre-bucketing entry under the bare cache root was swept; its owning clone is unknown, and what cannot be attributed must be preserved\n%s", out)
	}
	if strings.Contains(out, filepath.Base(cloneBOld)) {
		t.Errorf("the sweep report names another clone's cache; the report must stay inside this repository's bucket:\n%s", out)
	}

	// --dry-run names the dead-and-old dir without deleting it.
	cacheHome2 := filepath.Join(fixture, "cache2")
	bucketA2 := filepath.Join(cacheHome2, "evener", "golangci-lint", sweepRepoHash(commonA))
	deadOld2 := makeSweepCacheDir(t, bucketA2, sweepCacheHash(filepath.Join(fixture, "another-gone")))
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

	// The bucket belongs to the clone, not the calling worktree: a run from
	// the registered linked worktree must resolve the same bucket the main
	// checkout does (every worktree hashes the shared common directory), so
	// the dead-and-old cache the main checkout's run would sweep is swept
	// from the worktree too — not silently skipped in a bucket of its own.
	cacheHome3 := filepath.Join(fixture, "cache3")
	bucketA3 := filepath.Join(cacheHome3, "evener", "golangci-lint", sweepRepoHash(commonA))
	deadOld3 := makeSweepCacheDir(t, bucketA3, sweepCacheHash(filepath.Join(fixture, "third-gone")))
	if err := os.Chtimes(deadOld3, stale, stale); err != nil {
		t.Fatal(err)
	}
	out3, status3 := runSweep(t, extra, cacheHome3)
	if status3 != 0 {
		t.Fatalf("the sweeper failed from a registered worktree: exit %d\n%s", status3, out3)
	}
	if _, err := os.Stat(deadOld3); err == nil {
		t.Errorf("the sweeper run from a registered worktree kept the dead dir; every worktree of a clone must resolve the same bucket\n%s", out3)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat deadOld3: %v", err)
	}

	// A newline in the cache home must not turn a candidate into an
	// unrelated directory outside the repository's cache bucket.
	protected := filepath.Join(fixture, "protected")
	if err := os.Mkdir(protected, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(protected, "keep-me")
	if err := os.WriteFile(sentinel, []byte("unrelated data"), 0o644); err != nil {
		t.Fatal(err)
	}
	newlineHome := protected + "\ncache"
	newlineBucket := filepath.Join(newlineHome, "evener", "golangci-lint", sweepRepoHash(commonA))
	newlineDead := makeSweepCacheDir(t, newlineBucket, sweepCacheHash(filepath.Join(fixture, "newline-gone")))
	if err := os.Chtimes(newlineDead, stale, stale); err != nil {
		t.Fatal(err)
	}
	newlineOut, newlineStatus := runSweep(t, repo, newlineHome)
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("a newline in the cache home deleted unrelated data outside its bucket: %v\nexit %d\n%s", err, newlineStatus, newlineOut)
	}
	if newlineStatus != 0 {
		t.Errorf("sweeper failed with a newline in the cache home: exit %d\n%s", newlineStatus, newlineOut)
	}
	if _, err := os.Stat(newlineDead); !os.IsNotExist(err) {
		t.Errorf("the dead cache inside the newline-containing bucket was not swept: %v\n%s", err, newlineOut)
	}

	// A failed worktree hash must stop before an incomplete live set can
	// sweep a registered worktree's old cache.
	realShasum, err := exec.LookPath("shasum")
	if err != nil {
		t.Skipf("shasum is not on PATH: %v", err)
	}
	bin := filepath.Join(fixture, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := `#!/bin/sh
root=$(cat)
if [ "$root" = "$SWEEP_FAIL_HASH_ROOT" ]; then
	: > "$SWEEP_HASH_FAILURE_MARKER"
	exit 1
fi
if [ -f "$SWEEP_HASH_FAILURE_MARKER" ]; then
	: > "$SWEEP_HASH_AFTER_MARKER"
fi
printf '%s' "$root" | "$SWEEP_REAL_SHASUM" -a 256
`
	if err := os.WriteFile(filepath.Join(bin, "shasum"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	hashHome := filepath.Join(fixture, "hash-failure-cache")
	hashBucket := filepath.Join(hashHome, "evener", "golangci-lint", sweepRepoHash(commonA))
	oldMain := makeSweepCacheDir(t, hashBucket, sweepCacheHash(repo))
	oldDead := makeSweepCacheDir(t, hashBucket, sweepCacheHash(filepath.Join(fixture, "hash-failure-gone")))
	for _, dir := range []string{oldMain, oldDead} {
		if err := os.Chtimes(dir, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(fixture, "failed-hash")
	afterMarker := filepath.Join(fixture, "hash-after-failure")
	var env []string
	for _, entry := range sweepEnv(hashHome) {
		if !strings.HasPrefix(entry, "PATH=") {
			env = append(env, entry)
		}
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SWEEP_FAIL_HASH_ROOT="+repo, "SWEEP_REAL_SHASUM="+realShasum, "SWEEP_HASH_FAILURE_MARKER="+marker, "SWEEP_HASH_AFTER_MARKER="+afterMarker)
	hashOut, hashStatus := runSweepWithEnv(t, repo, env)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the fixture did not run its failing intermediate hash: %v\n%s", err, hashOut)
	}
	if _, err := os.Stat(afterMarker); !os.IsNotExist(err) {
		t.Errorf("the hash loop continued after a worktree hash failed: %v\nexit %d\n%s", err, hashStatus, hashOut)
	}
	if hashStatus == 0 {
		t.Errorf("the sweeper accepted an incomplete live set after a failed intermediate hash:\n%s", hashOut)
	}
	if _, err := os.Stat(oldMain); err != nil {
		t.Errorf("the sweeper deleted a registered worktree's old cache after its hash failed: %v\n%s", err, hashOut)
	}
	if _, err := os.Stat(oldDead); err != nil {
		t.Errorf("the sweeper deleted a cache after an incomplete live set: %v\n%s", err, hashOut)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	bashOut, bashStatus := runSweepWithShellAndEnv(t, "bash", repo, env)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("bash did not run the failing worktree hash: %v\n%s", err, bashOut)
	}
	if bashStatus == 0 {
		t.Errorf("bash accepted an incomplete live set after a failed intermediate hash:\n%s", bashOut)
	}
	if _, err := os.Stat(oldMain); err != nil {
		t.Errorf("bash deleted a registered worktree's old cache after its hash failed: %v\n%s", err, bashOut)
	}
	if _, err := os.Stat(oldDead); err != nil {
		t.Errorf("bash deleted a cache after an incomplete live set: %v\n%s", err, bashOut)
	}

	// A bare common repository has a bare metadata entry alongside its
	// registered linked worktrees; those worktrees still form a live set.
	bareRepo := filepath.Join(fixture, "bare.git")
	gittest.Run(t, fixture, "clone", "-q", "--bare", repo, bareRepo)
	bareHome := filepath.Join(fixture, "bare-cache")
	bareBucket := filepath.Join(bareHome, "evener", "golangci-lint", sweepRepoHash(bareRepo))
	bareDead := makeSweepCacheDir(t, bareBucket, sweepCacheHash(filepath.Join(fixture, "bare-gone")))
	if err := os.Chtimes(bareDead, stale, stale); err != nil {
		t.Fatal(err)
	}
	bareOnlyOut, bareOnlyStatus := runSweep(t, bareRepo, bareHome)
	if bareOnlyStatus == 0 {
		t.Errorf("an unlinked bare clone accepted an empty registered worktree set:\n%s", bareOnlyOut)
	}
	if _, err := os.Stat(bareDead); err != nil {
		t.Errorf("an unlinked bare clone deleted a cache without a usable live set: %v\n%s", err, bareOnlyOut)
	}
	bareLinked := filepath.Join(fixture, "bare-linked")
	gittest.Run(t, bareRepo, "worktree", "add", "-q", "--detach", bareLinked)
	bareCommon := gittest.Run(t, bareLinked, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if bareCommon != bareRepo {
		t.Fatalf("bare clone fixture's common directory %q differs from its bucket root %q", bareCommon, bareRepo)
	}
	bareLive := makeSweepCacheDir(t, bareBucket, sweepCacheHash(bareLinked))
	if err := os.Chtimes(bareLive, stale, stale); err != nil {
		t.Fatal(err)
	}
	bareOut, bareStatus := runSweep(t, bareLinked, bareHome)
	if bareStatus != 0 {
		t.Errorf("sweep from a bare-backed linked worktree failed: exit %d\n%s", bareStatus, bareOut)
	}
	if _, err := os.Stat(bareLive); err != nil {
		t.Errorf("a bare-backed registered worktree's old cache was deleted: %v\n%s", err, bareOut)
	}
	if _, err := os.Stat(bareDead); !os.IsNotExist(err) {
		t.Errorf("a bare-backed orphan cache was not swept: %v\n%s", err, bareOut)
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
	cmd.Env = sweepEnv(cacheHome)
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
