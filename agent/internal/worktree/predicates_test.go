package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeGitRunner builds a GitRunner rooted at repoRoot, wired directly to a
// real `git` binary via os/exec so a non-zero exit surfaces as an
// *exec.ExitError (satisfying the exitCoder contract predicates.go relies
// on to distinguish e.g. `merge-base --is-ancestor`'s exit 1 from a genuine
// failure).
func makeGitRunner(t *testing.T, repoRoot string) GitRunner {
	t.Helper()
	return func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			"HOME="+repoRoot,
		)
		var stdout strings.Builder
		var stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			return stdout.String(), &gitRunError{args: args, stderr: stderr.String(), err: err}
		}
		return stdout.String(), nil
	}
}

// gitRunError wraps a git invocation's failure, forwarding ExitCode() so
// errors.As(err, &exitCoder) sees through to the underlying
// *exec.ExitError exactly as a straight-through wrapper would.
type gitRunError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitRunError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + e.err.Error() + ": " + e.stderr
}

func (e *gitRunError) Unwrap() error { return e.err }

func (e *gitRunError) ExitCode() int {
	var ee *exec.ExitError
	if errors.As(e.err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// initRepo creates a fresh git repo at <tmp>/repo with an initial commit on
// "main" and returns its root path plus the initial commit's SHA.
func initRepo(t *testing.T) (root, initialSHA string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	runGit(t, root, "add", "f.txt")
	runGit(t, root, "commit", "-q", "-m", "init")
	sha := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	return root, sha
}

// addWorktree adds a worktree at <root>/<name> on a new branch <name>
// forked from base, returning the worktree's path.
func addWorktree(t *testing.T, root, name, base string) string {
	t.Helper()
	wtPath := filepath.Join(root, name)
	runGit(t, root, "worktree", "add", "-q", wtPath, "-b", name, base)
	return wtPath
}

// commitFile writes content to name inside dir and commits it.
func commitFile(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", msg)
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

// --- CleanTree ---

func TestCleanTree_Clean(t *testing.T) {
	root, _ := initRepo(t)
	run := makeGitRunner(t, root)

	clean, offending, err := CleanTree(run, root)
	if err != nil {
		t.Fatalf("CleanTree: %v", err)
	}
	if !clean {
		t.Fatalf("expected clean, got dirty with offending=%v", offending)
	}
	if len(offending) != 0 {
		t.Fatalf("expected no offending lines, got %v", offending)
	}
}

func TestCleanTree_DirtyModified(t *testing.T) {
	root, _ := initRepo(t)
	run := makeGitRunner(t, root)

	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}

	clean, offending, err := CleanTree(run, root)
	if err != nil {
		t.Fatalf("CleanTree: %v", err)
	}
	if clean {
		t.Fatalf("expected dirty, got clean")
	}
	if len(offending) != 1 || !strings.Contains(offending[0], "f.txt") {
		t.Fatalf("expected one offending line mentioning f.txt, got %v", offending)
	}
}

func TestCleanTree_DirtyUntracked(t *testing.T) {
	root, _ := initRepo(t)
	run := makeGitRunner(t, root)

	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}

	clean, offending, err := CleanTree(run, root)
	if err != nil {
		t.Fatalf("CleanTree: %v", err)
	}
	if clean {
		t.Fatalf("expected dirty, got clean")
	}
	if len(offending) != 1 || !strings.Contains(offending[0], "new.txt") {
		t.Fatalf("expected one offending line mentioning new.txt, got %v", offending)
	}
}

// --- Unchanged ---

func TestUnchanged_CleanAndTipEqualsBase(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	run := makeGitRunner(t, root)

	got, err := Unchanged(run, wt, base)
	if err != nil {
		t.Fatalf("Unchanged: %v", err)
	}
	if !got {
		t.Fatalf("expected unchanged=true")
	}
}

func TestUnchanged_CleanButTipMoved(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")
	run := makeGitRunner(t, root)

	got, err := Unchanged(run, wt, base)
	if err != nil {
		t.Fatalf("Unchanged: %v", err)
	}
	if got {
		t.Fatalf("expected unchanged=false (tip moved)")
	}
}

func TestUnchanged_DirtyButTipEqualsBase(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write scratch.txt: %v", err)
	}
	run := makeGitRunner(t, root)

	got, err := Unchanged(run, wt, base)
	if err != nil {
		t.Fatalf("Unchanged: %v", err)
	}
	if got {
		t.Fatalf("expected unchanged=false (dirty)")
	}
}

// --- Merged: fixture 1, true merge (merge commit) → ancestry arm ---

func TestMerged_TrueMerge_AncestryArm(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	// Merge lane into main with a real merge commit (--no-ff).
	runGit(t, root, "merge", "-q", "--no-ff", "-m", "merge lane", "lane")

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "main", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !result.Merged {
		t.Fatalf("expected merged=true, got %+v", result)
	}
	if result.Arm != "ancestry" {
		t.Fatalf("expected arm=ancestry, got %+v", result)
	}
	if result.TargetRef != "refs/heads/main" {
		t.Fatalf("expected TargetRef=refs/heads/main, got %+v", result)
	}
}

// --- Merged: fixture 2, fast-forward merge → ancestry arm ---

func TestMerged_FastForward_AncestryArm(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	// Fast-forward main to lane's tip (main never diverged).
	runGit(t, root, "merge", "-q", "--ff-only", "lane")

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "main", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !result.Merged || result.Arm != "ancestry" {
		t.Fatalf("expected merged=true arm=ancestry, got %+v", result)
	}
}

// --- Merged: fixture 3, rebase-merge → cherry arm ---

func TestMerged_RebaseMerge_CherryArm(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	// Advance main independently so the lane must be rebased, not ff'd.
	commitFile(t, root, "main.txt", "main work\n", "main commit")

	// Rebase the lane onto main's new tip, then fast-forward main onto the
	// rebased lane. The lane branch itself still points at the ORIGINAL
	// (pre-rebase) commit — ancestry must fail; only cherry can recognize
	// this.
	runGit(t, wt, "rebase", "-q", "main")
	runGit(t, root, "merge", "-q", "--ff-only", "lane")

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "main", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !result.Merged {
		t.Fatalf("expected merged=true, got %+v", result)
	}
	if result.Arm != "cherry" {
		t.Fatalf("expected arm=cherry, got %+v", result)
	}
}

// --- Merged: fixture 4, single-commit squash merge → cherry arm ---

func TestMerged_SingleCommitSquash_CherryArm(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	// Squash-merge lane into main: content lands, but as a NEW commit with
	// a different tree/parent history than the lane's own commit.
	runGit(t, root, "merge", "-q", "--squash", "lane")
	runGit(t, root, "commit", "-q", "-m", "squash lane")

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "main", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !result.Merged {
		t.Fatalf("expected merged=true, got %+v", result)
	}
	if result.Arm != "cherry" {
		t.Fatalf("expected arm=cherry, got %+v", result)
	}
}

// --- Merged: fixture 5, multi-commit squash merge → NOT merged (documented
// undetectable case per spec §5/§11) ---

func TestMerged_MultiCommitSquash_NotDetected(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	commitFile(t, wt, "a.txt", "a\n", "lane commit A")
	tip := commitFile(t, wt, "b.txt", "b\n", "lane commit B")

	// Squash-merge lane (two commits) into main as a single new commit. The
	// squash commit is the SUM of the lane; no per-commit equivalence
	// exists, so cherry cannot recognize either lane commit individually.
	runGit(t, root, "merge", "-q", "--squash", "lane")
	runGit(t, root, "commit", "-q", "-m", "squash lane (2 commits)")

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "main", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if result.Merged {
		t.Fatalf("expected merged=false (multi-commit squash is documented undetectable), got %+v", result)
	}
}

// --- Merged: fixture 6, detached-HEAD main root reviewing the tip → NOT
// merged (rev-6 destruction case: HEAD must never be consulted) ---

func TestMerged_DetachedHEADReviewingTip_NotMerged(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	// main never merged lane. Now the user checks out the lane's tip
	// SHA detached in the MAIN repo root, "reviewing" it — this must not
	// make Merged consult HEAD and see the tip as its own ancestor.
	runGit(t, root, "checkout", "-q", "--detach", tip)

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "main", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if result.Merged {
		t.Fatalf("expected merged=false (HEAD must not be consulted), got %+v", result)
	}
}

// --- Merged: fixture 7, local target branch parked behind but
// refs/remotes/origin/<target> contains the merge → merged via
// remote-tracking tip ---

func TestMerged_RemoteTrackingTipAheadOfLocal(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	// Set up a bare "remote", clone-equivalent via a local path remote, so
	// we get real refs/remotes/origin/* refs from an actual fetch.
	remoteDir := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, root, "init", "-q", "--bare", remoteDir)
	runGit(t, root, "remote", "add", "origin", remoteDir)
	// Push main (still at base) so origin/main exists...
	runGit(t, root, "push", "-q", "origin", "main")

	// ...then merge lane into main LOCALLY-BUT-SEPARATELY: simulate "origin
	// has the merge, local main is parked behind" by merging on a scratch
	// clone and pushing that, leaving local main untouched at base.
	scratch := filepath.Join(t.TempDir(), "scratch")
	runGit(t, root, "clone", "-q", remoteDir, scratch)
	runGit(t, scratch, "config", "user.email", "test@example.com")
	runGit(t, scratch, "config", "user.name", "Test")
	// The bare remote's HEAD symref may still point at a never-pushed
	// default branch (e.g. "master"), which would leave the clone's
	// checkout unborn — check out the pushed branch explicitly.
	runGit(t, scratch, "checkout", "-q", "-B", "main", "origin/main")
	runGit(t, scratch, "fetch", "-q", root, "lane:lane")
	runGit(t, scratch, "merge", "-q", "--no-ff", "-m", "merge lane", "lane")
	runGit(t, scratch, "push", "-q", "origin", "main")

	// Refresh origin/main's remote-tracking ref in root without moving
	// local main.
	runGit(t, root, "fetch", "-q", "origin")

	// Confirm the setup: local main must still be at base (behind), and
	// refs/remotes/origin/main must differ from it.
	localMainSHA := strings.TrimSpace(runGit(t, root, "rev-parse", "refs/heads/main"))
	if localMainSHA != base {
		t.Fatalf("fixture setup: expected local main to stay at base %s, got %s", base, localMainSHA)
	}
	remoteMainSHA := strings.TrimSpace(runGit(t, root, "rev-parse", "refs/remotes/origin/main"))
	if remoteMainSHA == base {
		t.Fatalf("fixture setup: expected refs/remotes/origin/main to be ahead of base")
	}

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "main", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !result.Merged {
		t.Fatalf("expected merged=true via remote-tracking tip, got %+v", result)
	}
	if result.TargetRef != "refs/remotes/origin/main" {
		t.Fatalf("expected TargetRef=refs/remotes/origin/main, got %+v", result)
	}
}

// --- Merged: fixture 8, target branch deleted entirely → TargetUnknown ---

func TestMerged_TargetBranchDeleted_TargetUnknown(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "does-not-exist", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if result.Merged {
		t.Fatalf("expected merged=false, got %+v", result)
	}
	if !result.TargetUnknown {
		t.Fatalf("expected TargetUnknown=true, got %+v", result)
	}
}

func TestMerged_EmptyMergeTarget_TargetUnknown(t *testing.T) {
	root, base := initRepo(t)
	wt := addWorktree(t, root, "lane", base)
	tip := commitFile(t, wt, "lane.txt", "lane work\n", "lane commit")

	run := makeGitRunner(t, root)
	result, err := Merged(run, tip, "", base)
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if result.Merged {
		t.Fatalf("expected merged=false, got %+v", result)
	}
	if !result.TargetUnknown {
		t.Fatalf("expected TargetUnknown=true, got %+v", result)
	}
}

// --- Adopted: truth table ---

func TestAdopted_TruthTable(t *testing.T) {
	const base = "base-sha"
	const removal = "removal-sha"
	const moved = "moved-sha"

	tests := []struct {
		name         string
		tip          string
		base         string
		tipAtRemoval string
		want         bool
	}{
		{"moved tip is adopted", moved, base, removal, true},
		{"tip equals base is not adopted", base, base, removal, false},
		{"tip equals tipAtRemoval is not adopted", removal, base, removal, false},
		{"reset back to base is not adopted", base, base, moved, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Adopted(tt.tip, tt.base, tt.tipAtRemoval)
			if got != tt.want {
				t.Fatalf("Adopted(%q, %q, %q) = %v, want %v", tt.tip, tt.base, tt.tipAtRemoval, got, tt.want)
			}
		})
	}
}
