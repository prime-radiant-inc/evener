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
	inRepo, branch, mod, untracked, _ := snapshotGit(context.Background(), env, root)
	if !inRepo || branch != "main" {
		t.Fatalf("snapshotGit on a truncated status = inRepo %v branch %q, want true/main", inRepo, branch)
	}
	if mod != 0 || untracked != 0 {
		t.Fatalf("a truncated status capture yielded %d modified / %d untracked, want 0/0 (refused, not decoded)", mod, untracked)
	}
}
