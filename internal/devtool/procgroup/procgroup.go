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
func Terminate(pgid int) { _ = signalGroup(pgid, syscall.SIGTERM) }

// Kill KILLs the whole group.
func Kill(pgid int) { _ = signalGroup(pgid, syscall.SIGKILL) }

// signalGroup is the one place a group signal is sent, and the one place the
// identifier is checked. Anything below 2 is refused, because kill's group
// form reads those three numbers as something other than a group:
//
//	0 is the caller's own process group, so the signal comes back to the
//	caller and to whatever shares its group -- for a gate helper, the gate;
//	1 makes kill(-1, sig), which is every process this user may signal on the
//	host, and a gate runner has no business sending one of those;
//	negatives are not groups at all.
//
// None of the three can be a group this package started: Start makes the child
// a group leader, so the group id is a pid the kernel handed out, and no
// kernel hands out 0 or 1 for a child.
//
// It answers with what the kernel said: nil for a signal delivered, ESRCH for
// a group that was already gone, and EPERM for one this process may not
// signal -- which is a group that is there.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return syscall.EINVAL
	}
	return syscall.Kill(-pgid, sig)
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
	// The same three numbers signalGroup refuses, for the same reason: pgid 1
	// asks after every process on the host, which answers yes and means
	// nothing, and 0 asks after the caller's own group, which is always there.
	if err := signalGroup(pgid, 0); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	return true
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
func Stop(pgid int, reaped <-chan struct{}, grace time.Duration) (bool, error) {
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
// It reports whether the group was stopped by this call. False means nothing
// was sent to it: the child was already reaped when the stop was asked for, or
// the group was gone by the time the first signal went out. The caller is then
// holding the command's own exit status rather than a death this stop caused,
// and a caller that decides between "the command answered" and "I stopped it"
// has to be able to tell those apart. On that answer StopWith also waits out
// the caller's reap, bounded by the grace: the status it is sending the caller
// to read only exists once the child has been waited for.
//
// The error is whatever the kernel refused a signal with, other than the group
// being gone, which is not a refusal but an answer. EPERM is the one that
// matters: the group is there and this process may not signal all of it, so
// the stop returns true and says why the group may still be running.
func StopWith(pgid int, sig syscall.Signal, reaped <-chan struct{}, grace time.Duration) (bool, error) {
	return stopWith(signalGroup, pgid, sig, reaped, grace)
}

// stopWith is StopWith with the sending injected, so the answers a kernel
// gives only under conditions a test cannot arrange -- a refusal, a group that
// went away between two signals -- can be given to it directly.
func stopWith(send func(int, syscall.Signal) error, pgid int, sig syscall.Signal, reaped <-chan struct{}, grace time.Duration) (bool, error) {
	if pgid <= 1 {
		// Not a group this package started; signalGroup says why.
		return false, nil
	}
	select {
	case <-reaped:
		return false, nil
	default:
	}
	var refused error
	// delivered records that a signal of ours did reach the group. Only a
	// delivered signal makes a stop: a child that exits while every signal
	// this call made was refused exited on its own, and its caller is holding
	// the command's own answer rather than a death this stop caused.
	delivered := false
	signal := func(s syscall.Signal) (gone bool) {
		err := send(pgid, s)
		switch {
		case err == nil:
			delivered = true
		case errors.Is(err, syscall.ESRCH):
			// A group that is gone after a signal of ours landed is a group
			// this stop emptied.
			return !delivered
		case refused == nil:
			refused = err
		}
		return false
	}
	if sig != 0 && sig != syscall.SIGTERM {
		if signal(sig) {
			awaitReap(reaped, grace)
			return false, refused
		}
		select {
		case <-reaped:
			return delivered, refused
		case <-time.After(grace):
		}
	}
	if signal(syscall.SIGTERM) {
		awaitReap(reaped, grace)
		return false, refused
	}
	select {
	case <-reaped:
	case <-time.After(grace):
		signal(syscall.SIGKILL)
	}
	return delivered, refused
}

// awaitReap waits out the caller's own reap, bounded by the grace. It is what
// a stop owes a caller it is about to tell that nothing was stopped: that
// caller's next move is to read the child's own status, and only the reap
// makes one available.
func awaitReap(reaped <-chan struct{}, grace time.Duration) {
	select {
	case <-reaped:
	case <-time.After(grace):
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
	if sig, killed := DiedOfSignal(state); killed {
		return 128 + int(sig)
	}
	return 1
}

// DiedOfSignal is the signal a child was killed by, and whether it was killed
// at all. A child that chose its own exit status answers false, which is the
// difference a caller needs between a command that produced a result and one
// something else ended -- including one this caller ended itself.
func DiedOfSignal(state *os.ProcessState) (syscall.Signal, bool) {
	if state == nil {
		return 0, false
	}
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	return ws.Signal(), true
}
