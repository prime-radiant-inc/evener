package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/gittest"
)

// The go-dependency-review job checks out refs/pull/N/merge: the pull request
// merged into its base branch as of that run. Its comparison base must be the
// tree that checkout was built from — the merge base of HEAD and the base
// branch's head as fetched now, which is the merge commit's first parent. The
// event's base.sha names the commit the branch was cut from; comparing with it
// counted every dependency the base branch changed in between as this pull
// request's own change, so a long-lived pull request failed the required static
// lane on modules it never touched (#6396).
func TestCIDependencyReviewComparesWithTheBaseBranchMergeBase(t *testing.T) {
	script, env := dependencyReviewCompareStep(t)
	// The script resolves origin/$PR_BASE_REF, so the step must fill it from the
	// pull request's base branch. A wrong expression here (the head branch, or
	// an empty value) would pass the rest of this test, which supplies the
	// variable itself.
	if got := env["PR_BASE_REF"]; got != "${{ github.event.pull_request.base.ref }}" {
		t.Fatalf("the compare step's PR_BASE_REF = %q, want the pull request's base branch ref", got)
	}
	repo, branchPoint, baseNow := dependencyReviewCheckout(t)

	args := runDependencyReviewCompare(t, repo, script, map[string]string{
		"EVENT_NAME":  "pull_request",
		"PR_BASE_REF": "main",
		// The step's dispatch input, empty on a pull request.
		"DISPATCH_BASE": "",
		// The event's base.sha, still passed as the workflow passed it before
		// the fix: the step must not use it.
		"BASE_SHA":     branchPoint,
		"ACKNOWLEDGED": "false",
	})

	base, ok := flagValue(args, "--base")
	if !ok {
		t.Fatalf("the compare step did not pass --base: %v", args)
	}
	if base == branchPoint {
		t.Fatalf("--base = %s, the event's base.sha (the branch point); every module the base branch changed since then is reported as this pull request's change", base)
	}
	if base != baseNow {
		t.Fatalf("--base = %s, want the merge base %s of the checked-out merge commit and the base branch's current head", base, baseNow)
	}
}

// A dispatch checks out the branch head rather than a merge commit, so it has
// no merge base to resolve against and keeps using the base the dispatch
// resolved. The PR CI watchdog depends on this path when GitHub drops an event.
func TestCIDependencyReviewDispatchKeepsItsResolvedBase(t *testing.T) {
	script := dependencyReviewCompareScript(t)

	args := runDependencyReviewCompare(t, t.TempDir(), script, map[string]string{
		"EVENT_NAME":    "workflow_dispatch",
		"PR_BASE_REF":   "",
		"DISPATCH_BASE": "1111111111111111111111111111111111111111",
		"ACKNOWLEDGED":  "true",
	})

	if base, _ := flagValue(args, "--base"); base != "1111111111111111111111111111111111111111" {
		t.Fatalf("--base = %q, want the base the dispatch resolved", base)
	}
}

// The job runs on every event and the command exits before comparing on push
// and merge_group, where neither a base ref nor a dispatch base exists. The
// step must reach that exit rather than failing on an unset variable, because
// `static` needs this job and the required check would fail main and the merge
// queue.
func TestCIDependencyReviewComparesNothingOutsideAPullRequest(t *testing.T) {
	script := dependencyReviewCompareScript(t)

	for _, event := range []string{"push", "merge_group"} {
		t.Run(event, func(t *testing.T) {
			args := runDependencyReviewCompare(t, t.TempDir(), script, map[string]string{
				"EVENT_NAME":    event,
				"PR_BASE_REF":   "",
				"DISPATCH_BASE": "",
				"ACKNOWLEDGED":  "false",
			})
			if base, _ := flagValue(args, "--base"); base != "" {
				t.Errorf("--base = %q, want empty so the command exits before comparing", base)
			}
		})
	}
}

// dependencyReviewCompareScript returns the run script of the step that
// invokes evener-go-deps.
func dependencyReviewCompareScript(t *testing.T) string {
	t.Helper()
	script, _ := dependencyReviewCompareStep(t)
	return script
}

// dependencyReviewCompareStep returns the run script and the env of the step
// that invokes evener-go-deps, so a case can check how the workflow fills the
// variables the script reads.
func dependencyReviewCompareStep(t *testing.T) (string, map[string]string) {
	t.Helper()
	workflow := readWorkflow(t, ".github/workflows/ci.yml")
	job, ok := workflow.Jobs["go-dependency-review"]
	if !ok {
		t.Fatal("ci.yml has no go-dependency-review job")
	}
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "cmd/evener-go-deps") {
			return step.Run, step.Env
		}
	}
	t.Fatal("go-dependency-review has no step that runs cmd/evener-go-deps")
	return "", nil
}

// dependencyReviewCheckout builds the shape the job checks out: a base branch
// that moved past the branch point, and refs/pull/N/merge, the branch merged
// into the newer base branch, at HEAD. It returns the repository, the branch
// point the event's base.sha names, and the base branch's head.
func dependencyReviewCheckout(t *testing.T) (repo, branchPoint, baseNow string) {
	t.Helper()
	repo = t.TempDir()
	gittest.Run(t, repo, "init", "-q", "-b", "main")
	commit := func(message string) string {
		t.Helper()
		gittest.Run(t, repo, "add", "-A")
		gittest.Run(t, repo, "commit", "-q", "-m", message)
		return gittest.Run(t, repo, "rev-parse", "HEAD")
	}
	writeTestFile(t, filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644)
	branchPoint = commit("branch point")
	gittest.Run(t, repo, "checkout", "-q", "-b", "pr")
	writeTestFile(t, filepath.Join(repo, "pr.txt"), []byte("pr\n"), 0o644)
	commit("pull request change")
	gittest.Run(t, repo, "checkout", "-q", "main")
	writeTestFile(t, filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644)
	baseNow = commit("base branch moves")
	gittest.Run(t, repo, "checkout", "-q", "-b", "merge", "main")
	gittest.Run(t, repo, "merge", "-q", "--no-ff", "-m", "merge the pull request", "pr")
	// actions/checkout fetches the base branch's head now; the merge commit's
	// own ancestry is what it was built from.
	gittest.Run(t, repo, "update-ref", "refs/remotes/origin/main", baseNow)
	if got := gittest.Run(t, repo, "merge-base", "HEAD", "origin/main"); got != baseNow {
		t.Fatalf("merge base of the merge commit and origin/main = %s, want its first parent %s", got, baseNow)
	}
	return repo, branchPoint, baseNow
}

// runDependencyReviewCompare runs the step's script in repo with a `go` shim
// that records its arguments, so the test reads the base the step hands
// evener-go-deps without building it. The script runs with gittest's
// environment so the host's git configuration cannot change what it resolves.
func runDependencyReviewCompare(t *testing.T, repo, script string, env map[string]string) []string {
	t.Helper()
	shim := t.TempDir()
	log := filepath.Join(shim, "go-args")
	writeTestFile(t, filepath.Join(shim, "go"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$GO_ARGS_LOG\"\n"), 0o755)
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = repo
	cmd.Env = append(gittest.Env(),
		"PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GO_ARGS_LOG="+log,
	)
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the compare step failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read the go shim's arguments: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// flagValue returns the argument after flag. A flag with no value at the end of
// args has no value.
func flagValue(args []string, flag string) (string, bool) {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
