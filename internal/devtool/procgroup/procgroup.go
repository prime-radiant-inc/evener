//go:build linux || darwin

// Package procgroup runs a tool's child processes in their own process
// groups, so a stop reaches every forked descendant rather than just the
// direct child, and stops them TERM-first with a bounded KILL escalation.
// It is the Go home of the stop_children/process-group discipline the shell
// runners each hand-rolled.
package procgroup

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Start starts cmd in its own process group. The caller keeps ownership of
// Wait; Stop only signals.
func Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return cmd.Start()
}

// Terminate TERMs the whole group. Best-effort: a group already gone is not
// an error anyone can act on.
func Terminate(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGTERM) }

// Kill KILLs the whole group.
func Kill(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGKILL) }

// Stop TERMs the group, waits for the caller to reap the direct child
// (signalled by closing reaped), and KILLs the group if that takes longer
// than grace. The grace is a tripwire for children that ignore TERM, not a
// pause every stop pays: a cooperative child releases Stop the moment it is
// reaped. Stop returns once the child is reaped or the KILL is sent; the
// caller's Wait still owns the final reap.
//
// A child already reaped gets nothing: its pid may belong to a recycled,
// unrelated process group by now, which is exactly the wrong target for a
// group TERM. The window between this check and the Terminate below is
// microseconds wide and requires an immediate pid wraparound — the same
// residue the old wave runner documented; closing it fully
// would need waitid(WNOWAIT), which pure Go doesn't expose. The shell
// runner's window was zero only because a single-threaded shell cannot
// reap concurrently with its own stop loop.
func Stop(pgid int, reaped <-chan struct{}, grace time.Duration) {
	select {
	case <-reaped:
		return
	default:
	}
	Terminate(pgid)
	select {
	case <-reaped:
	case <-time.After(grace):
		Kill(pgid)
	}
}

// ExitCode maps a reaped child's state to a shell-style exit code: the exit
// status for a normal exit, 128+signal for a signal death, 1 when the state
// carries neither (which pure Go cannot decompose further).
func ExitCode(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
