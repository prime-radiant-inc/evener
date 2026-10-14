package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// truncationGitEnv answers RunGit from a table keyed by the joined argv, so a
// test can hand a record parser a capture with the truncation flag set.
type truncationGitEnv struct {
	execenv.ExecutionEnvironment
	results map[string]execenv.ExecResult
}

func (e truncationGitEnv) ExecArgv(_ context.Context, _ string, args []string, _ int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	if res, ok := e.results[strings.Join(args, " ")]; ok {
		return res, nil
	}
	return execenv.ExecResult{}, errors.New("unscripted git: " + strings.Join(args, " "))
}

// TestGitRunnerRefusesATruncatedPorcelainCapture pins the worktree GitRunner
// boundary: a capped capture is head and tail fragments, not a record stream, so
// gitRunner must refuse it with ErrOutputTruncated and the porcelain readers
// must not decode a partial listing as the whole one (#4568 round 2). Backing
// the runner with git is immutable, scripted models refuse the verdicts.
func TestGitRunnerRefusesATruncatedPorcelainCapture(t *testing.T) {
	t.Parallel()
	env := truncationGitEnv{results: map[string]execenv.ExecResult{
		"worktree list --porcelain": {Stdout: "worktree /x\n", StdoutTruncated: true},
	}}
	_, err := gitRunner(context.Background(), env)("worktree", "list", "--porcelain")
	if !errors.Is(err, execenv.ErrOutputTruncated) {
		t.Fatalf("gitRunner on a truncated porcelain capture = %v, want ErrOutputTruncated", err)
	}
}

// TestSnapshotGitRefusesATruncatedStatus pins the status parser: a truncated
// status stream must not be decoded into partial counts (a capped stream
// reporting 100 modified against a true 300 is the #4568 round-2 defect). Like
// a failed exec, the truncated capture yields no counts rather than a wrong one.
func TestSnapshotGitRefusesATruncatedStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := truncationGitEnv{results: map[string]execenv.ExecResult{
		"rev-parse --is-inside-work-tree":   {Stdout: "true\n"},
		"rev-parse --abbrev-ref HEAD":       {Stdout: "main\n"},
		"status --porcelain":                {Stdout: " M a\n?? b\n", StdoutTruncated: true},
		"log -n 5 --pretty=format:%h%x20%s": {Stdout: "abc first\n"},
	}}
	inRepo, branch, mod, untracked, _, statusKnown := snapshotGit(context.Background(), env, root)
	if !inRepo || branch != "main" {
		t.Fatalf("snapshotGit on a truncated status = inRepo %v branch %q, want true/main", inRepo, branch)
	}
	if mod != 0 || untracked != 0 {
		t.Fatalf("a truncated status capture yielded %d modified / %d untracked, want 0/0 (refused, not decoded)", mod, untracked)
	}
	if statusKnown {
		t.Fatal("a truncated status capture reported the counts as known: the prompt would render a dirty repo as clean")
	}
}

// TestTruncatedStatusOmitsTheCountClaimFromThePrompt is M3 at the render
// boundary: with the status unknown the git block must not state a count, so a
// dirty repo is never rendered as clean. promptData.GitStatusUnknown is the
// structured flag the template gates on; the known-status render still carries
// its counts, and the unknown-status render still carries the known branch.
func TestTruncatedStatusOmitsTheCountClaimFromThePrompt(t *testing.T) {
	t.Parallel()
	known := promptData{IsGitRepo: true, GitBranch: "main", GitModifiedFiles: 3, GitUntrackedFiles: 2}
	outKnown, err := executeSystemPromptTemplate(known)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outKnown, "Modified files: 3") {
		t.Fatalf("known-status prompt lost its counts: %q", gitFragment(outKnown))
	}
	unknown := promptData{IsGitRepo: true, GitBranch: "main", GitModifiedFiles: 3, GitUntrackedFiles: 2, GitStatusUnknown: true}
	outUnknown, err := executeSystemPromptTemplate(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outUnknown, "Modified files:") || strings.Contains(outUnknown, "Untracked files:") {
		t.Fatalf("unknown-status prompt still claims counts: %q", gitFragment(outUnknown))
	}
	if !strings.Contains(outUnknown, "Branch: main") {
		t.Fatalf("unknown-status prompt dropped the known branch: %q", gitFragment(outUnknown))
	}
}

// gitFragment returns the rendered prompt's <git> block for a failure message.
func gitFragment(prompt string) string {
	if i := strings.Index(prompt, "<git>"); i >= 0 {
		if j := strings.Index(prompt[i:], "</git>"); j >= 0 {
			return prompt[i : i+j+len("</git>")]
		}
	}
	return prompt
}

// TestGitRunnerPreservesATruncatedMutation is M1: a successful `worktree add`
// whose stdout (a verbose post-checkout hook) tripped the capture cap must still
// succeed — its stdout is progress diagnostic nobody decodes, and failing it
// would strand the worktree and branch the command just created. A mutating
// lock is the same; a record-parsing read still refuses.
func TestGitRunnerPreservesATruncatedMutation(t *testing.T) {
	t.Parallel()
	env := truncationGitEnv{results: map[string]execenv.ExecResult{
		"worktree add --lock --reason r -b b -- /p base": {Stdout: "Preparing worktree\n[... 9 bytes omitted ...]\nhint: done\n", StdoutTruncated: true},
	}}
	if _, err := gitRunner(context.Background(), env)("worktree", "add", "--lock", "--reason", "r", "-b", "b", "--", "/p", "base"); err != nil {
		t.Fatalf("a successful worktree add with truncated diagnostic output = %v, want success", err)
	}
	lockEnv := truncationGitEnv{results: map[string]execenv.ExecResult{
		"worktree lock --reason m /p": {Stdout: "locked\n", StdoutTruncated: true},
	}}
	if _, err := gitRunner(context.Background(), lockEnv)("worktree", "lock", "--reason", "m", "/p"); err != nil {
		t.Fatalf("a successful worktree lock with truncated output = %v, want success", err)
	}
	listEnv := truncationGitEnv{results: map[string]execenv.ExecResult{
		"worktree list --porcelain": {Stdout: "worktree /x\n", StdoutTruncated: true},
	}}
	if _, err := gitRunner(context.Background(), listEnv)("worktree", "list", "--porcelain"); !errors.Is(err, execenv.ErrOutputTruncated) {
		t.Fatalf("a truncated porcelain listing = %v, want ErrOutputTruncated", err)
	}
}
