//go:build serffuzz

package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// FuzzGitPathResolutionProgram drives both structural and scripted-fallback
// repository resolution. Fixtures are only .git directory/pointer layouts; the
// fallback uses ExecutionEnvironment's existing command boundary and never runs
// Git. The oracle distinguishes active worktree roots from shared main roots,
// validates cache separation, rejects non-ancestor responses, and checks the
// directory-chain contract used by prompt construction.
func FuzzGitPathResolutionProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{0xff, 0x10, 0x7f},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 48 {
			program = program[:48]
		}
		first := runGitPathResolutionProgram(t, program)
		second := runGitPathResolutionProgram(t, program)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("git path resolution is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type gitPathResolutionTrace struct {
	WorktreeRoot string
	MainRoot     string
	FallbackRoot string
	Submodule    string
	Chains       [][]string
	Calls        []string
}

func runGitPathResolutionProgram(t *testing.T, program []byte) gitPathResolutionTrace {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	worktree := filepath.Join(base, "worktree")
	subdir := filepath.Join(worktree, "nested", gitPathProgramComponent(program))
	mainGit := filepath.Join(main, ".git")
	worktreeGitdir := filepath.Join(mainGit, "worktrees", "lane")
	for _, dir := range []string{mainGit, worktreeGitdir, subdir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make git fixture %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+worktreeGitdir+"\n"), 0o644); err != nil {
		t.Fatalf("write linked-worktree pointer: %v", err)
	}

	trace := gitPathResolutionTrace{}
	local := NewLocalExecutionEnvironment(subdir)
	worktreeRoot := GitRootOrEmpty(local, subdir)
	mainRoot := ResolveMainRepoRoot(local, subdir)
	if worktreeRoot != worktree {
		t.Fatalf("active worktree root = %q, want %q", worktreeRoot, worktree)
	}
	if mainRoot != main {
		t.Fatalf("main root = %q, want %q", mainRoot, main)
	}
	// Replays prove the two caches are independent and remain stable after the
	// first structural resolution.
	if got := GitRootOrEmpty(local, subdir); got != worktreeRoot {
		t.Fatalf("cached worktree root = %q, want %q", got, worktreeRoot)
	}
	if got := ResolveMainRepoRoot(local, subdir); got != mainRoot {
		t.Fatalf("cached main root = %q, want %q", got, mainRoot)
	}
	trace.WorktreeRoot = gitPathProgramRelative(base, worktreeRoot)
	trace.MainRoot = gitPathProgramRelative(base, mainRoot)

	if got := GitRootOrEmpty(NewLocalExecutionEnvironment(filepath.Join(base, "not-repo")), filepath.Join(base, "not-repo")); got != "" {
		t.Fatalf("non-repository structural root = %q, want empty", got)
	}

	// The git-binary fallbacks run through a scripted ExecutionEnvironment, so
	// every response shape is deterministic and no command reaches the host.
	fallbackRoot := filepath.Join(base, "fallback")
	if err := os.MkdirAll(filepath.Join(fallbackRoot, ".git"), 0o755); err != nil {
		t.Fatalf("make fallback fixture: %v", err)
	}
	fallbackCalls := []string{}
	fallbackEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(_ context.Context, command string, _ int, cwd string, _ map[string]string) (ExecResult, error) {
			fallbackCalls = append(fallbackCalls, command+"@"+cwd)
			switch command {
			case "git rev-parse --show-toplevel":
				return ExecResult{Stdout: fallbackRoot + "\n"}, nil
			case "git rev-parse --git-common-dir":
				return ExecResult{Stdout: filepath.Join(fallbackRoot, ".git") + "\n"}, nil
			default:
				return ExecResult{}, errors.New("unexpected git fallback command")
			}
		},
	}
	if got := GitRootOrEmpty(fallbackEnv, filepath.Join(fallbackRoot, "child")); got != fallbackRoot {
		t.Fatalf("scripted GitRootOrEmpty = %q, want %q", got, fallbackRoot)
	}
	if got := ResolveMainRepoRoot(fallbackEnv, fallbackRoot); got != fallbackRoot {
		t.Fatalf("scripted ResolveMainRepoRoot = %q, want %q", got, fallbackRoot)
	}
	trace.FallbackRoot = gitPathProgramRelative(base, fallbackRoot)

	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("make outside fixture: %v", err)
	}
	outsideEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(context.Context, string, int, string, map[string]string) (ExecResult, error) {
			return ExecResult{Stdout: outside}, nil
		},
	}
	if got := GitRootOrEmpty(outsideEnv, fallbackRoot); got != "" {
		t.Fatalf("outside fallback root = %q, want empty", got)
	}

	// A non-worktree common dir forces the submodule recovery query. The common
	// candidate deliberately lacks a .git entry, so accepting it would collapse
	// every submodule to the superproject instead of its own worktree root.
	submodule := filepath.Join(base, "submodule")
	if err := os.MkdirAll(submodule, 0o755); err != nil {
		t.Fatalf("make submodule fixture: %v", err)
	}
	submoduleCalls := []string{}
	submoduleEnv := &fakeExecEnv{
		workDir: submodule,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			submoduleCalls = append(submoduleCalls, command)
			switch command {
			case "git rev-parse --git-common-dir":
				return ExecResult{Stdout: ".git/modules/submodule\n"}, nil
			case "git rev-parse --show-toplevel":
				return ExecResult{Stdout: submodule + "\n"}, nil
			default:
				return ExecResult{ExitCode: 1}, errors.New("unexpected submodule git query")
			}
		},
	}
	if got := ResolveMainRepoRoot(submoduleEnv, submodule); got != submodule {
		t.Fatalf("submodule fallback root = %q, want %q", got, submodule)
	}
	if got, want := submoduleCalls, []string{"git rev-parse --git-common-dir", "git rev-parse --show-toplevel"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("submodule fallback calls = %v, want %v", got, want)
	}
	trace.Submodule = gitPathProgramRelative(base, submodule)

	// Scripted response-shape failures make the fallback's fail-closed behavior
	// reproducible without a Git binary. These are distinct protocol contracts:
	// an unavailable command, blank output, and a failed secondary query all
	// resolve to no repository rather than trusting partial data.
	failedRootEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(context.Context, string, int, string, map[string]string) (ExecResult, error) {
			return ExecResult{}, errors.New("scripted git unavailable")
		},
	}
	if got := GitRootOrEmpty(failedRootEnv, fallbackRoot); got != "" {
		t.Fatalf("failed GitRootOrEmpty = %q, want empty", got)
	}
	blankRootEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(context.Context, string, int, string, map[string]string) (ExecResult, error) {
			return ExecResult{Stdout: " \n\t"}, nil
		},
	}
	if got := GitRootOrEmpty(blankRootEnv, fallbackRoot); got != "" {
		t.Fatalf("blank GitRootOrEmpty = %q, want empty", got)
	}

	failedCommonEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(context.Context, string, int, string, map[string]string) (ExecResult, error) {
			return ExecResult{ExitCode: 128}, nil
		},
	}
	if got := ResolveMainRepoRoot(failedCommonEnv, filepath.Join(base, "no-structural-root")); got != "" {
		t.Fatalf("failed common-dir root = %q, want empty", got)
	}
	blankCommonEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(context.Context, string, int, string, map[string]string) (ExecResult, error) {
			return ExecResult{Stdout: " \n\t"}, nil
		},
	}
	if got := ResolveMainRepoRoot(blankCommonEnv, filepath.Join(base, "no-structural-root")); got != "" {
		t.Fatalf("blank common-dir root = %q, want empty", got)
	}

	primaryCandidate := filepath.Join(base, "binary-candidate")
	primaryCWD := filepath.Join(base, "binary-cwd")
	if err := os.MkdirAll(filepath.Join(primaryCandidate, ".git"), 0o755); err != nil {
		t.Fatalf("make primary candidate: %v", err)
	}
	if err := os.MkdirAll(primaryCWD, 0o755); err != nil {
		t.Fatalf("make primary cwd: %v", err)
	}
	primaryEnv := &fakeExecEnv{
		workDir: primaryCWD,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			if command != "git rev-parse --git-common-dir" {
				t.Fatalf("primary candidate unexpectedly queried %q", command)
			}
			return ExecResult{Stdout: filepath.Join(primaryCandidate, ".git")}, nil
		},
	}
	if got, want := ResolveMainRepoRoot(primaryEnv, primaryCWD), resolveClean(primaryCandidate); got != want {
		t.Fatalf("primary binary candidate root = %q, want %q", got, want)
	}

	missingCommon := filepath.Join(base, "no-common", ".git")
	failedTopEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			if command == "git rev-parse --git-common-dir" {
				return ExecResult{Stdout: missingCommon}, nil
			}
			return ExecResult{ExitCode: 128}, nil
		},
	}
	if got := ResolveMainRepoRoot(failedTopEnv, filepath.Join(base, "no-structural-top")); got != "" {
		t.Fatalf("failed show-toplevel root = %q, want empty", got)
	}
	blankTopEnv := &fakeExecEnv{
		workDir: fallbackRoot,
		exec: func(_ context.Context, command string, _ int, _ string, _ map[string]string) (ExecResult, error) {
			if command == "git rev-parse --git-common-dir" {
				return ExecResult{Stdout: missingCommon}, nil
			}
			return ExecResult{Stdout: " \n\t"}, nil
		},
	}
	if got := ResolveMainRepoRoot(blankTopEnv, filepath.Join(base, "no-structural-top")); got != "" {
		t.Fatalf("blank show-toplevel root = %q, want empty", got)
	}

	if got := mainRootCandidateFromCommonDir(worktree, mainGit); got != main {
		t.Fatalf("absolute common candidate = %q, want %q", got, main)
	}
	if got := mainRootCandidateFromCommonDir(main, ".git"); got != main {
		t.Fatalf("relative common candidate = %q, want %q", got, main)
	}
	if !gitEntryResolvesToCommon(main, mainGit) {
		t.Fatalf("main .git did not resolve to its common directory")
	}
	if gitEntryResolvesToCommon(submodule, ".git/modules/submodule") {
		t.Fatal("submodule candidate without .git entry unexpectedly resolved to common dir")
	}
	if got := resolveClean(filepath.Join(main, "missing", "..", "known")); got != filepath.Join(main, "known") {
		t.Fatalf("resolveClean missing path = %q", got)
	}

	chains := [][]string{
		DirsFromRootToCwd(main, main),
		DirsFromRootToCwd(main, filepath.Join(main, "a", "b")),
		DirsFromRootToCwd(main, outside),
	}
	if got, want := chains[0], []string{main}; !reflect.DeepEqual(got, want) {
		t.Fatalf("equal root chain = %v, want %v", got, want)
	}
	if got, want := chains[1], []string{main, filepath.Join(main, "a"), filepath.Join(main, "a", "b")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descendant chain = %v, want %v", got, want)
	}
	if got, want := chains[2], []string{outside}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outside chain = %v, want %v", got, want)
	}
	if got, want := DirsFromRootToCwd("relative/root", filepath.Join(base, "absolute-cwd")), []string{filepath.Join(base, "absolute-cwd")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelatable directory chain = %v, want %v", got, want)
	}
	for _, chain := range chains {
		trace.Chains = append(trace.Chains, gitPathProgramRelativeSlice(base, chain))
	}
	trace.Calls = append(trace.Calls, gitPathProgramRelativeSlice(base, fallbackCalls)...)

	// This setter is intentionally paired with its restore closure. It proves the
	// timeout used by the scripted fallbacks is derived from the one policy value,
	// with no wait or real subprocess involved.
	restore := SetGitExecTimeoutForTesting(17 * time.Millisecond)
	if got := gitExecTimeoutMS(); got != 17 {
		restore()
		t.Fatalf("gitExecTimeoutMS = %d, want 17", got)
	}
	restore()
	return trace
}

func gitPathProgramComponent(program []byte) string {
	if len(program) == 0 {
		return "empty"
	}
	return "part-" + strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(string(program[:1]))
}

func gitPathProgramRelative(base, value string) string {
	rel, err := filepath.Rel(base, value)
	if err != nil {
		return value
	}
	return rel
}

func gitPathProgramRelativeSlice(base string, values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ReplaceAll(value, base, "$ROOT")
	}
	return result
}
