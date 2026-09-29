//go:build linux || darwin

package execenv

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// drainingCommandRuntime models the SAFE-06 hazard deterministically: Wait stays
// outstanding after cancellation (as it does when a writer outside the command's
// process group holds the output pipe open), while a copier keeps writing into
// the captured buffer until a drain/close stops and joins it. It records whether
// the drain ran so a regression can assert the buffers are never read before it.
type drainingCommandRuntime struct {
	config commandRuntimeConfig

	pid            int
	startCalls     int
	waitCalls      int
	terminateCalls int
	killCalls      int
	drainCalls     int

	waitBlock  chan struct{}
	waitDone   chan struct{}
	drained    chan struct{}
	copierDone chan struct{}

	drainOnce  sync.Once
	waitOnce   sync.Once
	copierOnce sync.Once
}

func newDrainingCommandRuntime() *drainingCommandRuntime {
	return &drainingCommandRuntime{
		pid:        4242,
		waitBlock:  make(chan struct{}),
		waitDone:   make(chan struct{}),
		drained:    make(chan struct{}),
		copierDone: make(chan struct{}),
	}
}

func (c *drainingCommandRuntime) Args() []string { return nil }

func (c *drainingCommandRuntime) Configure(config commandRuntimeConfig) { c.config = config }

func (c *drainingCommandRuntime) Start() error {
	c.startCalls++
	if c.config.Stdout != nil {
		go func() {
			defer c.copierOnce.Do(func() { close(c.copierDone) })
			for {
				select {
				case <-c.drained:
					return
				default:
					_, _ = c.config.Stdout.Write([]byte("copier"))
				}
			}
		}()
	}
	return nil
}

func (c *drainingCommandRuntime) Wait() error {
	c.waitCalls++
	<-c.waitBlock
	c.waitOnce.Do(func() { close(c.waitDone) })
	return nil
}

func (c *drainingCommandRuntime) PID() int { return c.pid }

func (c *drainingCommandRuntime) ExitCode(error) (int, bool) { return 0, false }

func (c *drainingCommandRuntime) Terminate() { c.terminateCalls++ }

func (c *drainingCommandRuntime) Kill() { c.killCalls++ }

// drainBufferedOutput mirrors the production runtime's bounded join: it stops
// the copier and waits for it to exit before returning, so any buffer access
// after the drain cannot race a live write.
func (c *drainingCommandRuntime) drainBufferedOutput() error {
	c.drainCalls++
	c.drainOnce.Do(func() { close(c.drained) })
	<-c.copierDone
	return nil
}

// The fake must satisfy the same optional capability production is asserted
// against; a signature drift here is what let an earlier revision pass while
// the production barrier silently failed to engage.
var _ bufferedOutputDrainer = (*drainingCommandRuntime)(nil)

type drainingCommandFactory struct{ runtime *drainingCommandRuntime }

func (f drainingCommandFactory) Shell(string) commandRuntime { return f.runtime }

func (f drainingCommandFactory) Argv(string, ...string) commandRuntime { return f.runtime }

// TestExecArgvDrainsBufferedOutputBeforeReadingWhenWaitOutstanding pins the
// completion barrier for SAFE-06: when cancellation leaves Wait outstanding
// after the bounded termination waits, execPreparedCommand must force-close and
// join the runtime's buffered output copier before reading the captured buffers
// (and before releasing PID ownership on return). Without the barrier the code
// reads the buffers while the copier is still writing them — an unsynchronized
// race — and the drain is never requested.
func TestExecArgvDrainsBufferedOutputBeforeReadingWhenWaitOutstanding(t *testing.T) {
	root := t.TempDir()
	command := newDrainingCommandRuntime()
	t.Cleanup(func() {
		// Release the deliberately-outstanding Wait and copier so nothing leaks
		// past the test; both are idempotent once the barrier already ran.
		command.drainOnce.Do(func() { close(command.drained) })
		close(command.waitBlock)
		<-command.copierDone
	})

	env := NewLocalExecutionEnvironment(root)
	env.EnvPolicy = EnvPolicyNone
	env.commandFactory = drainingCommandFactory{runtime: command}
	grace := 20 * time.Millisecond
	env.terminationGrace = &grace

	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	result, err := env.ExecArgv(ctx, "tool", nil, 10_000, "", nil)
	if err == nil {
		t.Fatal("ExecArgv err = nil, want the cancellation error")
	}
	if command.drainCalls != 1 {
		t.Fatalf("drain calls = %d, want 1: buffered output must be drained before the buffers are read", command.drainCalls)
	}
	select {
	case <-command.waitDone:
		t.Fatal("Wait returned; the test never exercised the abandoned-copier path")
	default:
	}
	if command.terminateCalls != 1 || command.killCalls != 1 {
		t.Fatalf("termination signals terminate=%d kill=%d, want 1/1", command.terminateCalls, command.killCalls)
	}
	// The drain joined the copier, so reading the buffers here is race-free and
	// the captured output is the copier's, not a torn read.
	if !strings.Contains(result.Stdout, "copier") {
		t.Fatalf("buffered stdout = %q, want the copier's drained output", result.Stdout)
	}
}

// TestExecCommandBufferedReturnsWhenChildHoldsOutputPipe exercises the owned
// buffered lifecycle end to end: a same-group child inherits stdout and outlives
// its leader for far longer than the command's grace. Before the owned-pipe
// change os/exec's own copier kept Wait blocked on the still-open pipe for the
// child's whole lifetime; now the runtime owns the pipe, terminates the group
// within the termination grace (mirroring streaming), reclaims the child, and
// still captures the leader's output.
func TestExecCommandBufferedReturnsWhenChildHoldsOutputPipe(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	grace := 200 * time.Millisecond
	env.terminationGrace = &grace

	done := make(chan ExecResult, 1)
	start := time.Now()
	go func() {
		res, _ := env.ExecCommand(context.Background(), `sleep 30 & echo CHILD=$!; printf 'LEADER\n'`, 30_000, "", nil)
		done <- res
	}()

	var childPID int
	select {
	case res := <-done:
		if !strings.Contains(res.Stdout, "LEADER") {
			t.Fatalf("buffered stdout = %q, want the leader's output", res.Stdout)
		}
		if rest, ok := strings.CutPrefix(res.Stdout, "CHILD="); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				childPID, _ = strconv.Atoi(fields[0])
			}
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("buffered ExecCommand took %s; a child holding the pipe should be reclaimed within the grace, not run for its 30s lifetime", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("buffered ExecCommand did not return while a same-group child held the output pipe")
	}

	if childPID <= 0 {
		t.Fatal("did not capture the backgrounded child pid")
	}
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(childPID, 0) != syscall.ESRCH && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(childPID, 0); err != syscall.ESRCH {
		t.Fatalf("backgrounded child %d survived after buffered Wait returned: same-group writers must be reclaimed, not abandoned", childPID)
	}
}
