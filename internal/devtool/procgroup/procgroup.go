//go:build linux || darwin

// Package procgroup runs a tool's child processes in their own process
// groups, so a stop reaches every forked descendant rather than just the
// direct child, and stops them TERM-first with a bounded KILL escalation.
// It is the Go home of the stop_children/process-group discipline the shell
// runners each hand-rolled.
package procgroup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Supported says whether this platform has the process groups everything here
// depends on. A caller whose whole purpose is stopping a group can refuse to
// run rather than no-op its way through.
const Supported = true

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
func Terminate(pgid int) { signalGroup(pgid, syscall.SIGTERM) }

// Kill KILLs the whole group.
func Kill(pgid int) { signalGroup(pgid, syscall.SIGKILL) }

// signalGroup is the one place a group signal is sent, and the one place the
// identifier is checked. 0 means the caller's own process group and negatives
// are not groups: either would deliver the signal to processes the caller
// never started -- with 0, to the caller itself and to whatever else shares
// its group, which for a gate helper is the gate.
func signalGroup(pgid int, sig syscall.Signal) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, sig)
}

// Exists reports whether the group still has members. It is the one question
// that can be asked safely after the direct child has been reaped: a pid
// cannot be recycled while it is still a live group's id, so anything but
// ESRCH means this is still the group the caller started. EPERM is a yes --
// the group is there, this process just may not signal all of it.
//
// A group whose only remaining members are zombies also reads as alive, until
// whatever adopted them reaps them. Measured on darwin (24 runs of a leader
// that starts a grandchild in its group, the grandchild exiting first and the
// leader exiting without waiting): the group answers for 3-12ms after the
// leader is reaped, and then launchd has reaped the orphan and it answers
// ESRCH. It never survived to a second poll, let alone to a grace. Linux was
// not measured here -- this repo has no Linux host and its Docker daemon is
// down -- but init reaps a reparented orphan the same way; what would change
// the picture is a subreaper that adopts and does not reap.
//
// So the cost of not distinguishing zombies is one survivor diagnostic and one
// 10ms poll. It is not the caller's stuck-and-124 path: that needs the group
// to outlast both graces, which needs something that holds zombies
// indefinitely. Probing process state per platform buys nothing against a
// measured 3-12ms, and would have to be right on two kernels to buy it.
func Exists(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

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
// microseconds wide and requires an immediate pid wraparound; closing it
// fully would need waitid(WNOWAIT), which pure Go doesn't expose. The shell
// runner's window was zero only because a single-threaded shell cannot
// reap concurrently with its own stop loop.
func Stop(pgid int, reaped <-chan struct{}, grace time.Duration) bool {
	return StopWith(pgid, syscall.SIGTERM, reaped, grace)
}

// StopWith is Stop for a caller that was itself signalled: the group is sent
// that signal first, and gets the grace to act on it, before the TERM-then-KILL
// escalation starts. A child that distinguishes SIGHUP from SIGTERM -- a
// runner asked to reopen its logs, a shell asked to hang up -- sees what the
// operator actually sent rather than a TERM this layer chose on its behalf.
// The wait is therefore at most two graces when the forwarded signal is not
// SIGTERM itself, which is the price of passing on what was sent.
//
// It reports whether the group was signalled at all. False means the child
// was already reaped when the stop was asked for, and the caller is holding
// the command's own exit status rather than the death this stop would have
// caused: a caller that decides between "the command answered" and "I stopped
// it" has to be able to tell those apart, and only the check below knows
// which side of it the reap landed on.
func StopWith(pgid int, sig syscall.Signal, reaped <-chan struct{}, grace time.Duration) bool {
	if pgid <= 0 {
		// 0 is this process's own group and negatives are not groups; both
		// would send the signal somewhere the caller did not start.
		return false
	}
	select {
	case <-reaped:
		return false
	default:
	}
	if sig != 0 && sig != syscall.SIGTERM {
		signalGroup(pgid, sig)
		select {
		case <-reaped:
			return true
		case <-time.After(grace):
		}
	}
	Terminate(pgid)
	select {
	case <-reaped:
	case <-time.After(grace):
		Kill(pgid)
	}
	return true
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
