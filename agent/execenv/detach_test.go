package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
)

func TestLocalExecutionEnvironmentImplementsDetachedExecutor(t *testing.T) {
	var _ DetachedExecutor = (*LocalExecutionEnvironment)(nil)
	var _ DetachSupportReporter = (*LocalExecutionEnvironment)(nil)
}

// TestDetachSupportedMatchesDetachCommandsRefusal pins the capability a caller
// reads BEFORE launching anything — the answer a model is given about whether
// mode:"detached" is worth trying — against the refusal DetachCommand actually
// makes. A sandbox wrapper is the case that matters: it is present on every
// --sandbox run, and advertising detach there sends the model at a call that
// only ever returns ErrDetachUnsupported.
func TestDetachSupportedMatchesDetachCommandsRefusal(t *testing.T) {
	platformSupports := runtime.GOOS == "linux" || runtime.GOOS == "darwin"

	plain := NewLocalExecutionEnvironment(t.TempDir())
	if got := plain.DetachSupported(); got != platformSupports {
		t.Fatalf("DetachSupported() = %v on an unwrapped environment, want %v", got, platformSupports)
	}

	wrapped := NewLocalExecutionEnvironment(t.TempDir())
	wrapped.Wrapper = &sandbox.Wrapper{}
	if wrapped.DetachSupported() {
		t.Fatal("DetachSupported() = true with a sandbox wrapper provisioned, but DetachCommand refuses there")
	}
	if _, err := wrapped.DetachCommand(context.Background(), "true", "", nil); !errors.Is(err, ErrDetachUnsupported) {
		t.Fatalf("DetachCommand under a wrapper = %v, want ErrDetachUnsupported; the reported capability and the refusal have drifted", err)
	}
}

func TestDetachCommandRejectsCancelledContextBeforeStart(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := env.DetachCommand(ctx, "true", "", nil)
	if !errors.Is(err, context.Canceled) || got.PID != 0 {
		t.Fatalf("DetachCommand = (%+v, %v), want zero/context.Canceled", got, err)
	}
}

func TestDetachedProcessSysProcAttrReportsPlatformSupport(t *testing.T) {
	attr, ok := detachedProcessSysProcAttr()
	wantOK := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	if ok != wantOK {
		t.Fatalf("detachedProcessSysProcAttr support = %v, want %v", ok, wantOK)
	}
	if ok && attr == nil {
		t.Fatal("detachedProcessSysProcAttr reported support with nil attributes")
	}
	if !ok && attr != nil {
		t.Fatalf("detachedProcessSysProcAttr = %#v on unsupported platform, want nil", attr)
	}
}

func TestDetachCommandRejectsSandboxWrapperBeforeStart(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached execution is unsupported on this platform")
	}

	root := t.TempDir()
	markerPath := filepath.Join(root, "started")
	env := NewLocalExecutionEnvironment(root)
	env.Wrapper = &sandbox.Wrapper{}
	command := "printf started > " + strconv.Quote(markerPath)

	got, err := env.DetachCommand(context.Background(), command, "", nil)
	if !errors.Is(err, ErrDetachUnsupported) || got.PID != 0 {
		t.Fatalf("DetachCommand = (%+v, %v), want zero/ErrDetachUnsupported", got, err)
	}
	if _, statErr := os.Stat(markerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("detached command started before wrapper refusal: %v", statErr)
	}
}

func TestDetachCommandConfiguresDisownedProcess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached execution is unsupported on this platform")
	}

	root := t.TempDir()
	workingDir := filepath.Join(root, "work")
	if err := os.Mkdir(workingDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	shellPath := filepath.Join(binDir, "fixture-shell")
	if err := os.WriteFile(shellPath, []byte("fixture"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	waitCalled := make(chan struct{}, 1)
	command := &detachedRecordingCommand{
		args:       []string{"fixture-shell", "-c", "serve"},
		pid:        4242,
		waitCalled: waitCalled,
	}
	env := NewLocalExecutionEnvironment(root)
	env.commandFactory = detachedRecordingCommandFactory{command: command}

	got, err := env.DetachCommand(context.Background(), "serve", "work", map[string]string{"PATH": binDir})
	if err != nil {
		t.Fatalf("DetachCommand: %v", err)
	}
	if got.PID != command.pid || command.startCalls != 1 {
		t.Fatalf("DetachCommand = %+v, start calls = %d, want PID %d and one start", got, command.startCalls, command.pid)
	}
	if command.config.Dir != workingDir {
		t.Fatalf("runtime dir = %q, want %q", command.config.Dir, workingDir)
	}
	if command.config.ExecutablePath != shellPath {
		t.Fatalf("runtime executable = %q, want %q", command.config.ExecutablePath, shellPath)
	}
	if command.config.Wrapper != nil {
		t.Fatalf("runtime wrapper = %v, want nil", command.config.Wrapper)
	}
	wantSysProcAttr, _ := detachedProcessSysProcAttr()
	if !reflect.DeepEqual(command.config.SysProcAttr, wantSysProcAttr) {
		t.Fatalf("runtime SysProcAttr = %#v, want %#v", command.config.SysProcAttr, wantSysProcAttr)
	}
	nullDevice, ok := command.config.Stdout.(*os.File)
	if !ok || nullDevice.Name() != os.DevNull {
		t.Fatalf("runtime stdout = %#v, want %s file", command.config.Stdout, os.DevNull)
	}
	if command.config.Stdin != nullDevice || command.config.Stderr != nullDevice {
		t.Fatalf("runtime streams stdin=%v stdout=%v stderr=%v, want one null-device file", command.config.Stdin, command.config.Stdout, command.config.Stderr)
	}
	if _, err := nullDevice.Stat(); err == nil {
		t.Fatal("parent null-device descriptor remains open after detached launch")
	}
	if _, tracked := env.runningPIDs.Load(got.PID); tracked {
		t.Fatalf("detached PID %d was enrolled in cleanup tracking", got.PID)
	}
	select {
	case <-waitCalled:
	case <-time.After(time.Second):
		t.Fatal("detached command was not asynchronously reaped")
	}
}

func TestDetachCommandStartFailureReturnsZeroAndClosesStreams(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached execution is unsupported on this platform")
	}

	startErr := errors.New("start failed")
	command := &detachedRecordingCommand{
		args:       []string{"/fixture/shell", "-c", "serve"},
		startErr:   startErr,
		waitCalled: make(chan struct{}, 1),
	}
	env := NewLocalExecutionEnvironment(t.TempDir())
	env.commandFactory = detachedRecordingCommandFactory{command: command}

	got, err := env.DetachCommand(context.Background(), "serve", "", nil)
	if !errors.Is(err, startErr) || got.PID != 0 {
		t.Fatalf("DetachCommand = (%+v, %v), want zero/start error", got, err)
	}
	nullDevice, ok := command.config.Stdout.(*os.File)
	if !ok {
		t.Fatalf("runtime stdout = %#v, want null-device file", command.config.Stdout)
	}
	if _, err := nullDevice.Stat(); err == nil {
		t.Fatal("parent null-device descriptor remains open after start failure")
	}
	select {
	case <-command.waitCalled:
		t.Fatal("Wait called after command failed to start")
	default:
	}
}

func TestDetachCommandSurvivesCleanup(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached execution is unsupported on this platform")
	}

	root := t.TempDir()
	pidPath := filepath.Join(root, "pid")
	releasePath := filepath.Join(root, "release")
	donePath := filepath.Join(root, "done")
	command := fmt.Sprintf(
		"printf detached-stdout; printf detached-stderr >&2; printf '%%s' $$ > %s; while test ! -e %s; do sleep 0.01; done; printf done > %s",
		strconv.Quote(pidPath), strconv.Quote(releasePath), strconv.Quote(donePath),
	)
	env := NewLocalExecutionEnvironment(root)

	got, err := env.DetachCommand(context.Background(), command, "", nil)
	if err != nil {
		t.Fatalf("DetachCommand: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if released || !t.Failed() || got.PID <= 0 {
			return
		}
		if process, findErr := os.FindProcess(got.PID); findErr == nil {
			_ = process.Kill()
		}
	})

	pidData, ok := waitForTestFile(pidPath, 5*time.Second)
	if !ok {
		t.Fatalf("detached command did not write PID file for returned PID %d", got.PID)
	}
	launchedPID, err := strconv.Atoi(string(pidData))
	if err != nil {
		t.Fatalf("parse launched PID %q: %v", pidData, err)
	}
	if got.PID <= 0 || got.PID != launchedPID {
		t.Fatalf("DetachCommand PID = %d, launched shell PID = %d", got.PID, launchedPID)
	}
	if _, tracked := env.runningPIDs.Load(got.PID); tracked {
		t.Fatalf("detached PID %d was enrolled in cleanup tracking", got.PID)
	}

	env.Cleanup()
	if _, tracked := env.runningPIDs.Load(got.PID); tracked {
		t.Fatalf("detached PID %d entered cleanup tracking", got.PID)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("release detached command: %v", err)
	}
	released = true
	doneData, ok := waitForTestFile(donePath, 5*time.Second)
	if !ok {
		t.Fatalf("detached command PID %d did not survive environment cleanup", got.PID)
	}
	if string(doneData) != "done" {
		t.Fatalf("completion file = %q, want done", doneData)
	}
}

// waitForTestFile polls until path holds a non-empty value, or timeout.
//
// Non-empty is the condition, not mere existence: every caller waits on a file
// a shell writes with a truncate-then-write redirect (`printf ... > path`), so
// the file is observable as zero bytes before the content lands. Returning on
// existence alone hands that window back as a successful read.
func waitForTestFile(path string, timeout time.Duration) ([]byte, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data, true
		}
		select {
		case <-deadline.C:
			return nil, false
		case <-ticker.C:
		}
	}
}

type detachedRecordingCommandFactory struct {
	command *detachedRecordingCommand
}

func (f detachedRecordingCommandFactory) Shell(string) commandRuntime { return f.command }

func (f detachedRecordingCommandFactory) Argv(string, ...string) commandRuntime { return f.command }

type detachedRecordingCommand struct {
	args       []string
	pid        int
	startErr   error
	waitCalled chan struct{}
	config     commandRuntimeConfig
	startCalls int
}

func (c *detachedRecordingCommand) Args() []string { return c.args }

func (c *detachedRecordingCommand) Configure(config commandRuntimeConfig) { c.config = config }

func (c *detachedRecordingCommand) Start() error {
	c.startCalls++
	return c.startErr
}

func (c *detachedRecordingCommand) Wait() error {
	if c.waitCalled != nil {
		select {
		case c.waitCalled <- struct{}{}:
		default:
		}
	}
	return nil
}

func (c *detachedRecordingCommand) PID() int { return c.pid }

func (c *detachedRecordingCommand) ExitCode(error) (int, bool) { return 0, false }

func (c *detachedRecordingCommand) Terminate() {}

func (c *detachedRecordingCommand) Kill() {}

// TestWaitForTestFileWaitsForContent pins the helper's real contract. Both call
// sites want a value (a PID, then "done"), and the shell writes each with a
// truncate-then-write redirect, so the file exists for a moment while still
// empty. A helper that returns on mere existence hands that empty window back as
// success, which surfaced as two different spurious failures under load:
//
//	parse launched PID "": strconv.Atoi: parsing "": invalid syntax
//	completion file = "", want done
//
// Returning early on a zero-byte read fails this test.
func TestWaitForTestFileWaitsForContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = os.WriteFile(path, []byte("42"), 0o600)
	}()
	data, ok := waitForTestFile(path, 5*time.Second)
	if !ok {
		t.Fatalf("waitForTestFile timed out on a file that gained content")
	}
	if string(data) != "42" {
		t.Fatalf("waitForTestFile returned %q, want %q — it returned before the write landed", data, "42")
	}
}
