package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// readBeforeWriteEnv is a minimal execenv.ExecutionEnvironment that reports a fixed set
// of paths as existing and records WriteFile calls. It exists so the
// read-before-write guardrail can be exercised without a execenv.LocalExecutionEnvironment.
type readBeforeWriteEnv struct {
	existing map[string]bool
}

func (e *readBeforeWriteEnv) Initialize() error        { return nil }
func (e *readBeforeWriteEnv) Cleanup()                 {}
func (e *readBeforeWriteEnv) WorkingDirectory() string { return "/work" }
func (e *readBeforeWriteEnv) Platform() string         { return "linux" }
func (e *readBeforeWriteEnv) OSVersion() string        { return "test" }
func (e *readBeforeWriteEnv) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return "", errors.New("not implemented")
}
func (e *readBeforeWriteEnv) WriteFile(path string, content string) (string, error) {
	return "wrote " + path, nil
}
func (e *readBeforeWriteEnv) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return "", errors.New("not implemented")
}
func (e *readBeforeWriteEnv) FileExists(path string) bool { return e.existing[path] }
func (e *readBeforeWriteEnv) Glob(pattern string, basePath string) ([]string, error) {
	return nil, errors.New("not implemented")
}
func (e *readBeforeWriteEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	return "", errors.New("not implemented")
}
func (e *readBeforeWriteEnv) ListDirectory(path string, depth int) ([]execenv.DirEntry, error) {
	return nil, errors.New("not implemented")
}
func (e *readBeforeWriteEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (execenv.ExecResult, error) {
	return execenv.ExecResult{}, errors.New("not implemented")
}

// TestToolDeps_ShellTimeoutClamp drives the shell tool through registerShellTools
// with a fake toolDeps (no real *Session). It proves the seam: the handler reads
// its timeout policy from deps.cmdTimeouts and clamps an over-long request to the
// max, with the clamped value reaching the environment's ExecCommand.
func TestToolDeps_ShellTimeoutClamp(t *testing.T) {
	// Shell has no per-call wait knob (its wait knob is `background`), so the
	// session default command timeout is the value clamped to maxTimeout.
	run := func(t *testing.T, defTimeout, maxTimeout, want int) {
		t.Helper()
		deps := &toolDeps{
			cmdTimeouts: func() (int, int) { return defTimeout, maxTimeout },
		}
		reg := tool.NewRegistry()
		if err := registerShellTools(reg, nil, deps); err != nil {
			t.Fatalf("registerShellTools: %v", err)
		}
		env := &captureEnv{wd: "/work"}
		res := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
			ID:        "c1",
			Name:      "shell",
			Arguments: json.RawMessage(`{"command":"echo hi"}`),
		})
		if res.IsError {
			t.Fatalf("unexpected error: %q", res.Output)
		}
		if got := env.LastTimeoutMS(); got != want {
			t.Fatalf("timeout: got %d, want %d", got, want)
		}
	}

	// Default above the max is clamped to the max.
	t.Run("default clamped to max", func(t *testing.T) { run(t, 120_000, 30_000, 30_000) })
	// Default below the max is used as-is.
	t.Run("default under max", func(t *testing.T) { run(t, 5_000, 30_000, 5_000) })
}

// TestToolDeps_ReadBeforeWriteWarning drives write_file through registerFileTools
// with a fake toolDeps whose readGuard is backed by a tiny in-memory set. It
// proves the guardrail flows through the seam: an existing-but-unread file warns,
// and once tracked as read the warning disappears — no real *Session involved.
func TestToolDeps_ReadBeforeWriteWarning(t *testing.T) {
	const target = "/work/existing.txt"

	env := &readBeforeWriteEnv{existing: map[string]bool{target: true}}

	read := map[string]bool{}
	deps := &toolDeps{
		readGuard: readGuard{
			trackRead: func(path string) { read[path] = true },
			readBeforeWriteWarning: func(path string) string {
				if read[path] {
					return ""
				}
				if !env.FileExists(path) {
					return ""
				}
				return "[WARNING: not read]\n"
			},
		},
	}

	reg := tool.NewRegistry()
	if err := registerFileTools(reg, deps); err != nil {
		t.Fatalf("registerFileTools: %v", err)
	}

	writeArgs, _ := json.Marshal(map[string]any{"file_path": target, "content": "data"})

	// First write: file exists and was never read → warning prepended.
	res := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "c1",
		Name:      "write_file",
		Arguments: writeArgs,
	})
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}
	if !strings.Contains(res.Output, "WARNING") {
		t.Fatalf("expected read-before-write warning, got: %q", res.Output)
	}

	// Mark the file read, then write again: warning must be gone.
	deps.readGuard.TrackRead(target)
	res = reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "c2",
		Name:      "write_file",
		Arguments: writeArgs,
	})
	if res.IsError {
		t.Fatalf("unexpected error: %q", res.Output)
	}
	if strings.Contains(res.Output, "WARNING") {
		t.Fatalf("expected no warning after read, got: %q", res.Output)
	}
}
