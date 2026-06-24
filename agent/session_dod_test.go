package agent

import (
	"context"
	"errors"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// toolCallEndOutput extracts the output from a TOOL_CALL_END event,
// returning Output (success) when set, otherwise Error (failure).
func toolCallEndOutput(ev events.SessionEvent) string {
	d, ok := ev.Data.(events.ToolCallEndData)
	if !ok {
		return ""
	}
	if d.Output != "" {
		return d.Output
	}
	return d.Error
}

type errAdapter struct {
	name  string
	err   error
	calls int
}

func (a *errAdapter) Name() string { return a.name }
func (a *errAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	_ = req
	a.calls++
	return llm.Response{}, a.err
}
func (a *errAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

type flaky429Adapter struct {
	name      string
	failCount int
	calls     int
}

func (a *flaky429Adapter) Name() string { return a.name }
func (a *flaky429Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	_ = req
	a.calls++
	if a.calls <= a.failCount {
		return llm.Response{}, llm.ErrorFromHTTPStatus(a.name, 429, "rate limited", nil, nil)
	}
	return finalResponse("ok"), nil
}
func (a *flaky429Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

type captureEnv struct {
	wd string

	mu        sync.Mutex
	lastCmd   string
	lastTOms  int
	lastWdArg string
	calls     []captureCall // all ExecCommand invocations
}

type captureCall struct {
	Command   string
	TimeoutMS int
	WorkDir   string
}

func (e *captureEnv) Initialize() error { return nil }
func (e *captureEnv) Cleanup()          {}

func (e *captureEnv) WorkingDirectory() string { return e.wd }
func (e *captureEnv) Platform() string         { return "linux" }
func (e *captureEnv) OSVersion() string        { return "test" }

func (e *captureEnv) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return "", errors.New("not implemented")
}
func (e *captureEnv) WriteFile(path string, content string) (string, error) {
	return "", errors.New("not implemented")
}
func (e *captureEnv) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return "", errors.New("not implemented")
}
func (e *captureEnv) FileExists(path string) bool { return false }
func (e *captureEnv) Glob(pattern string, basePath string) ([]string, error) {
	return nil, errors.New("not implemented")
}
func (e *captureEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	return "", errors.New("not implemented")
}
func (e *captureEnv) ListDirectory(path string, depth int) ([]execenv.DirEntry, error) {
	return nil, errors.New("not implemented")
}
func (e *captureEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (execenv.ExecResult, error) {
	_ = ctx
	_ = envVars
	e.mu.Lock()
	e.lastCmd = command
	e.lastTOms = timeoutMS
	e.lastWdArg = workingDir
	e.calls = append(e.calls, captureCall{Command: command, TimeoutMS: timeoutMS, WorkDir: workingDir})
	e.mu.Unlock()
	return execenv.ExecResult{Stdout: "ok", Stderr: "", ExitCode: 0, TimedOut: false, DurationMS: 1}, nil
}

func (e *captureEnv) LastTimeoutMS() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastTOms
}

// TimeoutForCommand returns the timeout used for the first ExecCommand call matching cmd.
func (e *captureEnv) TimeoutForCommand(cmd string) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.calls {
		if strings.Contains(c.Command, cmd) {
			return c.TimeoutMS, true
		}
	}
	return 0, false
}

type timeoutEnv struct {
	wd string
}

func (e *timeoutEnv) Initialize() error { return nil }
func (e *timeoutEnv) Cleanup()          {}

func (e *timeoutEnv) WorkingDirectory() string { return e.wd }
func (e *timeoutEnv) Platform() string         { return "linux" }
func (e *timeoutEnv) OSVersion() string        { return "test" }
func (e *timeoutEnv) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return "", errors.New("not implemented")
}
func (e *timeoutEnv) WriteFile(path string, content string) (string, error) {
	return "", errors.New("not implemented")
}
func (e *timeoutEnv) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return "", errors.New("not implemented")
}
func (e *timeoutEnv) FileExists(path string) bool { return false }
func (e *timeoutEnv) Glob(pattern string, basePath string) ([]string, error) {
	return nil, errors.New("not implemented")
}
func (e *timeoutEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	return "", errors.New("not implemented")
}
func (e *timeoutEnv) ListDirectory(path string, depth int) ([]execenv.DirEntry, error) {
	return nil, errors.New("not implemented")
}
func (e *timeoutEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (execenv.ExecResult, error) {
	_ = ctx
	_ = workingDir
	_ = envVars
	// Pretend git isn't available for this environment (session snapshot + doc discovery fall back cleanly).
	if strings.HasPrefix(strings.TrimSpace(command), "git ") {
		return execenv.ExecResult{ExitCode: 1}, errors.New("not a git repo")
	}
	return execenv.ExecResult{
		Stdout:     "partial output\n",
		Stderr:     "",
		ExitCode:   124,
		TimedOut:   true,
		DurationMS: int64(timeoutMS),
	}, context.DeadlineExceeded
}
