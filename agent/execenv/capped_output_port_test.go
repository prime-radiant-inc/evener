package execenv

// Command-level capture-cap scenarios ported from the parked PR #4568 branch
// (commits 556d751cb5, e974047618, 9408a02d35): the capture seam, an endless
// writer, and per-stream stderr capping. The buffer's own contract is pinned
// with it in agent/internal/cappedoutput.

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cappedTestEnv returns an environment whose buffered-command capture cap is
// capBytes, the injectable seam production code reads per command.
func cappedTestEnv(t *testing.T, root string, capBytes int) *LocalExecutionEnvironment {
	t.Helper()
	env := NewLocalExecutionEnvironment(root)
	env.outputCaptureCap = &capBytes
	return env
}

// TestOutputCaptureCapSeamDefaultsAndRidesWorkingDirectory pins the seam
// contract and its WithWorkingDirectory propagation, which main landed (via
// #4621) without a test: the production default is 8MiB, a set seam overrides
// it, a non-positive seam falls back, and the re-rooting clone carries it.
func TestOutputCaptureCapSeamDefaultsAndRidesWorkingDirectory(t *testing.T) {
	t.Parallel()
	env := NewLocalExecutionEnvironment(t.TempDir())
	if got := env.outputCaptureCapBytes(); got != maxCapturedOutputBytes {
		t.Fatalf("default capture cap = %d, want the %d-byte production default", got, maxCapturedOutputBytes)
	}
	small := 4096
	env.outputCaptureCap = &small
	if got := env.outputCaptureCapBytes(); got != small {
		t.Fatalf("capture cap = %d, want the seam's %d", got, small)
	}
	for _, invalid := range []int{0, -1} {
		env.outputCaptureCap = &invalid
		if got := env.outputCaptureCapBytes(); got != maxCapturedOutputBytes {
			t.Fatalf("capture cap for seam %d = %d, want the production default", invalid, got)
		}
	}
	env.outputCaptureCap = &small
	child := env.WithWorkingDirectory(t.TempDir())
	if got := child.outputCaptureCapBytes(); got != small {
		t.Fatalf("re-rooted child capture cap = %d, want the parent's %d", got, small)
	}
}

// endlessWriterRuntime is the scripted stand-in for a firehose command that
// never exits on its own: Start floods the captured stdout with more than the
// cap in one deterministic in-process write — no dependence on how fast a real
// writer fills the pipe, the flake `yes` brought to loaded runners — and Wait
// blocks until the runtime is signalled, the way a real endless writer runs
// until the timeout kills it.
type endlessWriterRuntime struct {
	config     commandRuntimeConfig
	terminated chan struct{}
	stop       sync.Once
}

func newEndlessWriterRuntime() *endlessWriterRuntime {
	return &endlessWriterRuntime{terminated: make(chan struct{})}
}

func (c *endlessWriterRuntime) Args() []string                        { return []string{"endless-writer"} }
func (c *endlessWriterRuntime) Configure(config commandRuntimeConfig) { c.config = config }
func (c *endlessWriterRuntime) Start() error {
	_, _ = c.config.Stdout.Write(bytes.Repeat([]byte("ENDLESS-WRITE-"), 200_000)) // ~2.8MiB, far past any test cap
	return nil
}
func (c *endlessWriterRuntime) Wait() error {
	<-c.terminated
	return nil
}
func (c *endlessWriterRuntime) PID() int                   { return 0 }
func (c *endlessWriterRuntime) ExitCode(error) (int, bool) { return 0, false }
func (c *endlessWriterRuntime) Terminate() {
	c.stop.Do(func() { close(c.terminated) })
}
func (c *endlessWriterRuntime) Kill() { c.Terminate() }

// endlessWriterFactory routes both command shapes to the endless writer.
type endlessWriterFactory struct{}

func (endlessWriterFactory) Shell(string) commandRuntime { return newEndlessWriterRuntime() }
func (endlessWriterFactory) Argv(string, ...string) commandRuntime {
	return newEndlessWriterRuntime()
}

// TestExecArgv_InfiniteOutputStaysBoundedUntilTheTimeoutKillsIt is the scripted
// replacement for the parked branch's real-`yes` version: it proves the capture
// stays bounded and flagged while an endless writer runs past the 100ms timeout,
// with no dependence on writer speed. Main has no timeout-with-cap test.
func TestExecArgv_InfiniteOutputStaysBoundedUntilTheTimeoutKillsIt(t *testing.T) {
	const capBytes = 64 * 1024
	env := cappedTestEnv(t, t.TempDir(), capBytes)
	env.commandFactory = endlessWriterFactory{}

	start := time.Now()
	res, err := env.ExecArgv(context.Background(), "endless-writer", nil, 100, "", nil)
	dur := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if !res.TimedOut || res.ExitCode != 124 {
		t.Fatalf("res = %+v, want the timeout kill (timed_out, exit 124)", res)
	}
	if dur > 3*time.Second {
		t.Fatalf("timeout handling took %s, want the 100ms timeout plus teardown", dur)
	}
	if !res.StdoutTruncated {
		t.Fatalf("stdout capture is %d bytes without the truncated flag, want the cap to have tripped", len(res.Stdout))
	}
	if len(res.Stdout) > capBytes+64 {
		t.Fatalf("stdout capture is %d bytes, want at most the %d-byte cap plus the marker", len(res.Stdout), capBytes)
	}
	if !strings.HasPrefix(res.Stdout, "ENDLESS-WRITE-") {
		t.Fatalf("stdout capture lost the head of the stream; starts with %q", res.Stdout[:min(len(res.Stdout), 16)])
	}
}

// TestExecCommand_CapsStderrIndependentlyOfStdout pins the per-stream bound main
// does not test: a stdout-only command keeps an exact stdout while a stderr
// firehose is capped and flagged on its own.
func TestExecCommand_CapsStderrIndependentlyOfStdout(t *testing.T) {
	if _, err := execLookPath("head"); err != nil {
		t.Skip("head not available")
	}
	const capBytes = 64 * 1024
	const firehose = 67108864 // 64MiB, far past the cap
	env := cappedTestEnv(t, t.TempDir(), capBytes)

	res, err := env.ExecCommand(context.Background(),
		"printf %s 'OUT-ONLY'; head -c "+strconv.Itoa(firehose)+" /dev/zero 1>&2",
		10_000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v (res=%+v)", err, res)
	}
	if res.Stdout != "OUT-ONLY" {
		t.Fatalf("stdout = %q, want the untouched %d-byte stream", res.Stdout, len("OUT-ONLY"))
	}
	if res.StdoutTruncated {
		t.Fatal("stdout was reported truncated, want the untouched stream")
	}
	if !res.StderrTruncated {
		t.Fatalf("stderr capture of a %d-byte stream reports untruncated", firehose)
	}
	if len(res.Stderr) <= capBytes || len(res.Stderr) > capBytes+64 {
		t.Fatalf("stderr capture is %d bytes, want the %d-byte cap plus a bounded marker between the windows", len(res.Stderr), capBytes)
	}
}

// TestExecCommand_UnderCapPassesThroughWhole pins the end-to-end under-cap path
// main does not test: a command whose output fits the cap reaches the caller
// byte for byte and reports untruncated.
func TestExecCommand_UnderCapPassesThroughWhole(t *testing.T) {
	if _, err := execLookPath("head"); err != nil {
		t.Skip("head not available")
	}
	const capBytes = 256 * 1024
	const headMark = "U-HEAD-9k"
	const tailMark = "-U-TAIL4z"
	nullLen := 200_000
	env := cappedTestEnv(t, t.TempDir(), capBytes)

	res, err := env.ExecCommand(context.Background(),
		"printf %s '"+headMark+"'; head -c "+strconv.Itoa(nullLen)+" /dev/zero; printf %s '"+tailMark+"'",
		10_000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v (res=%+v)", err, res)
	}
	want := headMark + strings.Repeat("\x00", nullLen) + tailMark
	if res.Stdout != want {
		t.Fatalf("under-cap capture is %d bytes, want the exact %d-byte stream byte for byte", len(res.Stdout), len(want))
	}
	if res.StdoutTruncated {
		t.Fatalf("under-cap capture of %d bytes at cap %d reports truncated", len(want), capBytes)
	}
	if res.TimedOut || res.ExitCode != 0 {
		t.Fatalf("command did not complete on its own: %+v", res)
	}
}
