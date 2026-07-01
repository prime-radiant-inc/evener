package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func w3sub_shellReg(t *testing.T, s *Session) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	deps := &toolDeps{cmdTimeouts: func() (int, int) { return 5000, 30000 }}
	if err := registerShellTools(reg, s, deps); err != nil {
		t.Fatalf("registerShellTools: %v", err)
	}
	return reg
}

func w3sub_call(t *testing.T, reg *tool.Registry, env execenv.ExecutionEnvironment, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	return reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: "c1", Name: name, Arguments: raw})
}

// The shell handler rejects a negative max_runtime_ms in parseShellToolArgs
// before it ever touches the environment (session_tools_shell.go:131).
func TestW3Sub_RegisterShellTools_ParseError(t *testing.T) {
	reg := w3sub_shellReg(t, nil)
	res := w3sub_call(t, reg, &captureEnv{wd: "/work"}, "shell", map[string]any{
		"command": "echo hi", "max_runtime_ms": -5,
	})
	if !res.IsError {
		t.Fatalf("expected a parse error, got: %q", res.Output)
	}
}

// A StreamingExecutor environment with no initialized JobManager fails fast
// (session_tools_shell.go:135). The local execenv is a StreamingExecutor.
func TestW3Sub_RegisterShellTools_StreamingNoJobManager(t *testing.T) {
	reg := w3sub_shellReg(t, nil) // nil session -> nil jobManager
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	res := w3sub_call(t, reg, env, "shell", map[string]any{"command": "echo hi"})
	if !res.IsError || !strings.Contains(res.Output, "JobManager") {
		t.Fatalf("expected an uninitialized-JobManager error, got: %q", res.Output)
	}
}

// list_dir parses its optional offset and limit arguments before delegating to
// the environment (session_tools_shell.go:158, 162).
func TestW3Sub_RegisterShellTools_ListDirOffsetLimit(t *testing.T) {
	reg := w3sub_shellReg(t, nil)
	// captureEnv.ListDirectory returns an error, which is fine: the offset/limit
	// arg-parsing arms run first, and the error arm is exercised too.
	res := w3sub_call(t, reg, &captureEnv{wd: "/work"}, "list_dir", map[string]any{
		"path": "/work", "depth": 2.0, "offset": 3.0, "limit": 5.0,
	})
	if !res.IsError {
		t.Fatalf("expected the environment error to surface, got: %q", res.Output)
	}
}

// grep parses its optional case_insensitive and max_results arguments before
// delegating to the environment (session_tools_shell.go:182, 186).
func TestW3Sub_RegisterShellTools_GrepArgs(t *testing.T) {
	reg := w3sub_shellReg(t, nil)
	res := w3sub_call(t, reg, &captureEnv{wd: "/work"}, "grep", map[string]any{
		"pattern": "x", "path": "/work", "case_insensitive": true, "max_results": 5.0, "output_mode": "content",
	})
	if !res.IsError {
		t.Fatalf("expected the environment error to surface, got: %q", res.Output)
	}
}

// glob surfaces an environment error from its lookup (session_tools_shell.go:207).
func TestW3Sub_RegisterShellTools_GlobError(t *testing.T) {
	reg := w3sub_shellReg(t, nil)
	res := w3sub_call(t, reg, &captureEnv{wd: "/work"}, "glob", map[string]any{
		"pattern": "*.go", "path": "/work",
	})
	if !res.IsError {
		t.Fatalf("expected a glob error, got: %q", res.Output)
	}
}
