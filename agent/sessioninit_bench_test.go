package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

func benchClient() *llm.Client {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	return c
}

// BenchmarkNewSession measures full session construction against a real local
// execution environment (the path that forks git rev-parse and compiles tool
// schemas). forceRealIO opts this benchmark out of the test-binary-wide
// no-fsync default (testSpeedIO in session_init.go): its subject IS real
// construction I/O cost, including the jobstore/transcript/installation-ID
// fsyncs every other test in this package is now exempted from.
func BenchmarkNewSession(b *testing.B) {
	c := benchClient()
	prof := provider.NewOpenAIProfile("gpt-5.2")
	for b.Loop() {
		dir := b.TempDir()
		stateDir := b.TempDir()
		sess, err := NewSession(c, prof, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			MaxSubagentDepth: 1,
			StateDir:         stateDir,
			testOnly:         testConfig{forceRealIO: true},
		})
		if err != nil {
			b.Fatalf("NewSession: %v", err)
		}
		sess.Close()
	}
}

// BenchmarkGitRootOrEmpty measures one git-root lookup via the real env (the
// per-fork cost paid ~4x per session today).
func BenchmarkGitRootOrEmpty(b *testing.B) {
	dir := b.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	for b.Loop() {
		_ = execenv.GitRootOrEmpty(env, dir)
	}
}

func BenchmarkProfileToolRegistry(b *testing.B) {
	prof := provider.NewOpenAIProfile("gpt-5.2")
	for b.Loop() {
		_ = newProfileToolRegistry(prof)
	}
}
