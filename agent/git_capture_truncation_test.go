package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
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

// TestSnapshotGitKeepsCountsWhenOnlyStderrTrippedTheCap is MEDIUM 2 (round 15):
// the status and log guards read ExecResult.Truncated(), which fires for EITHER
// stream, so a successful porcelain status whose STDERR overflowed — a noisy hook
// or warning storm on a perfectly good status — was discarded along with its
// valid counts, and the prompt rendered a dirty repository as clean. These
// parsers read stdout only, so the stdout stream is what must be whole.
func TestSnapshotGitKeepsCountsWhenOnlyStderrTrippedTheCap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := truncationGitEnv{results: map[string]execenv.ExecResult{
		"rev-parse --is-inside-work-tree":   {Stdout: "true\n"},
		"rev-parse --abbrev-ref HEAD":       {Stdout: "main\n"},
		"status --porcelain":                {Stdout: " M a\n?? b\n", StderrTruncated: true},
		"log -n 5 --pretty=format:%h%x20%s": {Stdout: "abc first\n", StderrTruncated: true},
	}}
	inRepo, branch, mod, untracked, commits, statusKnown := snapshotGit(context.Background(), env, root)
	if !inRepo || branch != "main" {
		t.Fatalf("inRepo %v branch %q, want true/main", inRepo, branch)
	}
	if !statusKnown || mod != 1 || untracked != 1 {
		t.Fatalf("a capped STDERR discarded a valid status: statusKnown=%v counts=%d/%d, want true/1/1",
			statusKnown, mod, untracked)
	}
	if len(commits) != 1 || commits[0] != "abc first" {
		t.Fatalf("a capped STDERR discarded a valid log: %q", commits)
	}
}

// TestGitRunnerLetsAParsedCommandThroughWhenOnlyStderrTrippedTheCap is the same
// defect at the worktree GitRunner, where it is worse: a parsed-stdout command
// whose stderr overflowed failed outright, so a clean tree could not be reported
// clean on the strength of a warning nobody asked for. The stdout refusal is
// untouched — a capped stdout still refuses.
func TestGitRunnerLetsAParsedCommandThroughWhenOnlyStderrTrippedTheCap(t *testing.T) {
	t.Parallel()
	env := truncationGitEnv{results: map[string]execenv.ExecResult{
		"worktree list --porcelain": {Stdout: "worktree /x\n", StderrTruncated: true},
	}}
	out, err := gitRunner(context.Background(), env)("worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("a porcelain read with a capped STDERR = %v, want success: its stdout is whole", err)
	}
	if !strings.Contains(out, "worktree /x") {
		t.Fatalf("the whole stdout was not returned: %q", out)
	}

	capped := truncationGitEnv{results: map[string]execenv.ExecResult{
		"worktree list --porcelain": {Stdout: "worktree /x\n", StdoutTruncated: true},
	}}
	if _, err := gitRunner(context.Background(), capped)("worktree", "list", "--porcelain"); !errors.Is(err, execenv.ErrOutputTruncated) {
		t.Fatalf("a truncated porcelain capture = %v, want ErrOutputTruncated", err)
	}
}

// TestGitStatusPromptGoldens pins the system prompt's git block as a COMPLETE
// rendering for the known and unknown status states — the repo's convention for
// prompt prose is a whole-output golden, never fragment matching. The gate is
// the structured promptData.GitStatusUnknown; the structured status state
// itself (whether the counts were obtained) is pinned by
// TestSnapshotGitRefusesATruncatedStatus.
func TestGitStatusPromptGoldens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		unknown bool
	}{
		{"status-known", false},
		{"status-unknown", true},
	} {
		data := promptData{IsGitRepo: true, GitBranch: "main", GitModifiedFiles: 3, GitUntrackedFiles: 2, GitStatusUnknown: tc.unknown}
		out, err := executeSystemPromptTemplate(data)
		if err != nil {
			t.Fatal(err)
		}
		agenttest.CheckGolden(t, filepath.Join("testdata", "gitprompt", tc.name+".md"), []byte(gitFragment(out)+"\n"), *updatePromptGoldens,
			"Regenerate with `go test ./agent -run 'TestGitStatusPromptGoldens$' -count=1 -update-prompt` and read the diff.")
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

// TestSnapshotGitReportsStatusKnownWhenTheStatusWasRead pins the other side of
// the structured state: a status read that succeeded reports its counts as
// known, so the template renders them.
func TestSnapshotGitReportsStatusKnownWhenTheStatusWasRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := truncationGitEnv{results: map[string]execenv.ExecResult{
		"rev-parse --is-inside-work-tree":   {Stdout: "true\n"},
		"rev-parse --abbrev-ref HEAD":       {Stdout: "main\n"},
		"status --porcelain":                {Stdout: " M a\n?? b\n"},
		"log -n 5 --pretty=format:%h%x20%s": {Stdout: "abc first\n"},
	}}
	_, _, mod, untracked, _, statusKnown := snapshotGit(context.Background(), env, root)
	if !statusKnown {
		t.Fatal("a status read that succeeded reported the counts as unknown")
	}
	if mod != 1 || untracked != 1 {
		t.Fatalf("counts = %d modified / %d untracked, want 1/1", mod, untracked)
	}
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
