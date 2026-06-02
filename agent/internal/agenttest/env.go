package agenttest

import (
	"context"
	"strings"

	"primeradiant.com/serf/agent/execenv"
)

// FakeEnv is a minimal execenv.ExecutionEnvironment for tests. WorkingDirectory
// returns WorkDir; ExecCommand answers `git rev-parse --show-toplevel` with
// GitRoot when it is set and fails every other command; all filesystem
// operations are inert. It is enough to drive code that only needs a working
// directory and git-root probing (e.g. MCP config discovery).
type FakeEnv struct {
	WorkDir string
	GitRoot string
}

func (f *FakeEnv) Initialize() error { return nil }
func (f *FakeEnv) Cleanup()          {}

func (f *FakeEnv) WorkingDirectory() string { return f.WorkDir }
func (f *FakeEnv) Platform() string         { return "test" }
func (f *FakeEnv) OSVersion() string        { return "test" }

func (f *FakeEnv) ExecCommand(_ context.Context, command string, _ int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	if f.GitRoot != "" && strings.Contains(command, "git rev-parse --show-toplevel") {
		return execenv.ExecResult{Stdout: f.GitRoot, ExitCode: 0}, nil
	}
	return execenv.ExecResult{ExitCode: 1}, nil
}

func (f *FakeEnv) ReadFile(string, *int, *int) (string, error)           { return "", nil }
func (f *FakeEnv) WriteFile(string, string) (string, error)              { return "", nil }
func (f *FakeEnv) EditFile(string, string, string, bool) (string, error) { return "", nil }
func (f *FakeEnv) FileExists(string) bool                                { return false }
func (f *FakeEnv) Glob(string, string) ([]string, error)                 { return nil, nil }
func (f *FakeEnv) Grep(string, string, string, bool, int, string) (string, error) {
	return "", nil
}
func (f *FakeEnv) ListDirectory(string, int) ([]execenv.DirEntry, error) { return nil, nil }
