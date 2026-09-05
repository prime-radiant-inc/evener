package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// gitRootProbeRuntime stands in for the `git rev-parse --show-toplevel` fork
// gitRootUncached falls back to. Wait holds briefly before answering, so a call
// whose context is already cancelled resolves that way deterministically rather
// than racing an instantly-finished command; Terminate releases it the way a
// SIGTERM'd git would end.
type gitRootProbeRuntime struct {
	stdout     string
	config     commandRuntimeConfig
	terminated chan struct{}
	once       sync.Once
}

func (c *gitRootProbeRuntime) Args() []string                     { return nil }
func (c *gitRootProbeRuntime) Configure(cfg commandRuntimeConfig) { c.config = cfg }
func (c *gitRootProbeRuntime) Start() error                       { return nil }
func (c *gitRootProbeRuntime) PID() int                           { return 0 }
func (c *gitRootProbeRuntime) ExitCode(error) (int, bool)         { return 0, false }
func (c *gitRootProbeRuntime) Terminate()                         { c.once.Do(func() { close(c.terminated) }) }
func (c *gitRootProbeRuntime) Kill()                              { c.Terminate() }

func (c *gitRootProbeRuntime) Wait() error {
	select {
	case <-c.terminated:
		return errors.New("git terminated before it answered")
	case <-time.After(50 * time.Millisecond):
	}
	if c.config.Stdout != nil {
		_, _ = c.config.Stdout.Write([]byte(c.stdout))
	}
	return nil
}

// gitRootProbeFactory hands out a fresh probe per command, counting the forks
// so a test can tell a memoized answer from a re-resolved one.
type gitRootProbeFactory struct {
	stdout string
	forks  *int
}

func (f gitRootProbeFactory) new() commandRuntime {
	*f.forks++
	return &gitRootProbeRuntime{stdout: f.stdout, terminated: make(chan struct{})}
}

func (f gitRootProbeFactory) Shell(string) commandRuntime           { return f.new() }
func (f gitRootProbeFactory) Argv(string, ...string) commandRuntime { return f.new() }

// gitRootFallbackEnv builds an environment whose cwd defeats the structural
// resolution — a ".git" that is neither a directory nor a parseable gitdir
// pointer — so GitRootOrEmptyContext has to fall back to forking git, which the
// probe factory answers.
func gitRootFallbackEnv(t *testing.T) (env *LocalExecutionEnvironment, root string, forks *int) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("neither a directory nor a gitdir pointer\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	forks = new(int)
	env = NewLocalExecutionEnvironment(root)
	env.EnvPolicy = EnvPolicyNone
	env.commandFactory = gitRootProbeFactory{stdout: root + "\n", forks: forks}
	return env, root, forks
}

// A git-root resolution that never got an answer — its context was cancelled,
// git could not be run, the fork timed out — says nothing about the directory.
// Memoizing it makes the environment believe that directory is not a git
// repository for the rest of its life, and the environment outlives the request
// whose cancellation caused it. Only a definitive answer may be cached.
func TestGitRootOrEmptyContext_TransientFailureIsNotMemoized(t *testing.T) {
	env, root, forks := gitRootFallbackEnv(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := GitRootOrEmptyContext(cancelled, env, root); got != "" {
		t.Fatalf("cancelled resolution = %q, want empty", got)
	}
	if *forks != 1 {
		t.Fatalf("cancelled resolution forked %d times, want 1", *forks)
	}

	got := GitRootOrEmptyContext(context.Background(), env, root)

	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("resolution after a cancelled one = %q, want %q: the cancelled call memoized its non-answer", got, want)
	}
	if *forks != 2 {
		t.Errorf("resolutions forked %d times, want 2: the second call took a cached non-answer instead of re-resolving", *forks)
	}
}

// The other half of the contract: a definitive answer IS memoized, so the
// fix above does not turn every resolution into a fork.
func TestGitRootOrEmptyContext_DefinitiveAnswerIsMemoized(t *testing.T) {
	env, root, forks := gitRootFallbackEnv(t)

	first := GitRootOrEmptyContext(context.Background(), env, root)
	second := GitRootOrEmptyContext(context.Background(), env, root)

	if first == "" || first != second {
		t.Fatalf("resolutions = %q then %q, want the same non-empty root", first, second)
	}
	if *forks != 1 {
		t.Errorf("resolutions forked %d times, want 1: the definitive answer was not memoized", *forks)
	}
}
