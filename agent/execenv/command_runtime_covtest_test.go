package execenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCommandOutputWriteError covers the commandOutputWriteError type's
// Error and Unwrap methods (lines 77-78, 79-80).
func TestCommandOutputWriteError(t *testing.T) {
	inner := errors.New("write failed")
	e := &commandOutputWriteError{err: inner}
	if e.Error() != "write failed" {
		t.Fatalf("Error() = %q, want 'write failed'", e.Error())
	}
	if !errors.Is(e, inner) {
		t.Fatal("Unwrap should return the inner error")
	}
}

// TestCommandOutputWriter covers the commandOutputWriter's Write method,
// including the error-wrapping path (lines 85-90).
func TestCommandOutputWriter(t *testing.T) {
	// Successful write.
	buf := &mockWriter{buf: []byte{}}
	w := commandOutputWriter{destination: buf}
	n, err := w.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	if string(buf.buf) != "hello" {
		t.Fatalf("buf = %q", buf.buf)
	}

	// Error write — should wrap in commandOutputWriteError.
	ew := &mockWriter{err: errors.New("disk full")}
	w = commandOutputWriter{destination: ew}
	_, err = w.Write([]byte("hello"))
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := errors.AsType[*commandOutputWriteError](err); !ok {
		t.Fatalf("error = %T, want *commandOutputWriteError", err)
	}
}

// TestCommandWaitError covers the commandWaitError function's three branches
// (lines 252-259).
func TestCommandWaitError(t *testing.T) {
	processErr := errors.New("process failed")
	outputErr := errors.New("output failed")
	lifecycleErr := errors.New("lifecycle failed")

	// outputErr takes priority.
	if got := commandWaitError(processErr, outputErr, lifecycleErr); !errors.Is(got, outputErr) {
		t.Fatalf("got %v, want outputErr", got)
	}

	// lifecycleErr is next.
	if got := commandWaitError(processErr, nil, lifecycleErr); !errors.Is(got, lifecycleErr) {
		t.Fatalf("got %v, want lifecycleErr", got)
	}

	// processErr is the fallback.
	if got := commandWaitError(processErr, nil, nil); !errors.Is(got, processErr) {
		t.Fatalf("got %v, want processErr", got)
	}
}

// TestForcedCloseOutputError covers the forcedCloseOutputError function
// (lines 241-249).
func TestForcedCloseOutputError(t *testing.T) {
	c := &systemCommandRuntime{}

	// commandOutputWriteError is returned as-is.
	writeErr := &commandOutputWriteError{err: errors.New("write failed")}
	if got := c.forcedCloseOutputError(writeErr); !errors.Is(got, writeErr) {
		t.Fatal("expected commandOutputWriteError to be returned as-is")
	}

	// os.ErrClosed is suppressed to nil.
	if got := c.forcedCloseOutputError(os.ErrClosed); got != nil {
		t.Fatalf("expected nil for ErrClosed, got %v", got)
	}

	// Other errors are returned as-is.
	otherErr := errors.New("some other error")
	if got := c.forcedCloseOutputError(otherErr); !errors.Is(got, otherErr) {
		t.Fatal("expected other error to be returned as-is")
	}
}

// TestSystemCommandRuntime_PID_NilProcess covers the PID method when
// cmd.Process is nil (line 263-264).
func TestSystemCommandRuntime_PID_NilProcess(t *testing.T) {
	c := &systemCommandRuntime{cmd: exec.Command("true")}
	// Process is nil before Start is called.
	if got := c.PID(); got != 0 {
		t.Fatalf("PID() = %d, want 0 for nil process", got)
	}
}

// TestIsSubmoduleGitDirShape covers the submodule shape detection function
// (lines 206-215).
func TestIsSubmoduleGitDirShape(t *testing.T) {
	// A path under .git/modules/... is a submodule git dir.
	submodulePath := filepath.Join("repo", ".git", "modules", "sub")
	if !isSubmoduleGitDirShape(submodulePath) {
		t.Fatalf("expected %q to be a submodule git dir", submodulePath)
	}

	// A regular path is not.
	if isSubmoduleGitDirShape("/regular/path") {
		t.Fatal("expected /regular/path to NOT be a submodule git dir")
	}

	// Root path is not.
	if isSubmoduleGitDirShape("/") {
		t.Fatal("expected / to NOT be a submodule git dir")
	}
}

// TestPointerTarget covers the pointerTarget function (lines 195-203).
func TestPointerTarget(t *testing.T) {
	// Valid gitdir pointer with absolute path.
	content := "gitdir: /some/absolute/path"
	got, ok := pointerTarget(content, "/ancestor")
	if !ok || got != "/some/absolute/path" {
		t.Fatalf("pointerTarget = %q, %v", got, ok)
	}

	// Valid gitdir pointer with relative path — resolved against ancestor.
	content = "gitdir: relative/path"
	got, ok = pointerTarget(content, "/ancestor")
	if !ok || got != filepath.Clean(filepath.Join("/ancestor", "relative/path")) { //nolint:gocritic // test needs absolute path
		t.Fatalf("pointerTarget = %q, %v", got, ok)
	}

	// Invalid pointer — not a gitdir prefix.
	content = "not a pointer"
	_, ok = pointerTarget(content, "/ancestor")
	if ok {
		t.Fatal("expected false for invalid pointer")
	}
}

// TestIsLocalEnv covers isLocalEnv (lines 278-280).
func TestIsLocalEnv(t *testing.T) {
	if !isLocalEnv(NewLocalExecutionEnvironment(t.TempDir())) {
		t.Fatal("expected LocalExecutionEnvironment to be local")
	}
	if isLocalEnv(nil) {
		t.Fatal("expected nil env to not be local")
	}
}

// TestIsNilExecutionEnvironment covers the nil detection (lines 27-37).
func TestIsNilExecutionEnvironment(t *testing.T) {
	if !isNilExecutionEnvironment(nil) {
		t.Fatal("expected nil to be nil")
	}
	if isNilExecutionEnvironment(NewLocalExecutionEnvironment(t.TempDir())) {
		t.Fatal("expected local env to not be nil")
	}
}

// --- Test helpers ---

type mockWriter struct {
	buf []byte
	err error
}

func (m *mockWriter) Write(p []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	m.buf = append(m.buf, p...)
	return len(p), nil
}
