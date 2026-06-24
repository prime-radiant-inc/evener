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
// schemas).
func BenchmarkNewSession(b *testing.B) {
	c := benchClient()
	prof := provider.NewOpenAIProfile("gpt-5.2")
	for i := 0; i < b.N; i++ {
		dir := b.TempDir()
		sess, err := NewSession(c, prof, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 1})
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
	for i := 0; i < b.N; i++ {
		_ = execenv.GitRootOrEmpty(env, dir)
	}
}

func BenchmarkProfileToolRegistry(b *testing.B) {
	prof := provider.NewOpenAIProfile("gpt-5.2")
	for i := 0; i < b.N; i++ {
		_ = newProfileToolRegistry(prof)
	}
}
