package execenv

import (
	"errors"
	"io/fs"
	"os/exec"
)

// The shell's exit statuses for a command it could not run, from bash's
// execute_cmd.c: a program it cannot find is EX_NOTFOUND, and every other
// refusal is EX_NOEXEC. POSIX documents both.
const (
	// shellStatusNoExec is 126: the program was found, and the kernel still
	// refused to run it.
	shellStatusNoExec = 126
	// shellStatusNotFound is 127: the shell found no program of that name.
	shellStatusNotFound = 127
)

// shellStartStatus reports the exit status a POSIX shell gives for a start
// failure in which the launcher or the kernel refused to run the program it was
// handed, and ok=false for a start failure a shell never turns into a status.
//
// bash's rule, measured against bash 5.2 on Linux: a `#!` interpreter that is
// missing reports 127, because the kernel answers ENOENT for the script; a
// program it cannot find at all reports 127 as EX_NOTFOUND; and every other
// execve refusal reports 126 as EX_NOEXEC — a missing execute bit, a directory,
// an interpreter that exists but is not executable, an ELF built for another
// machine, a path component that is not a directory, a symlink loop, an
// overlong path, a program held open for writing.
//
// Only a refusal or a name the launcher could not resolve maps. Resource
// exhaustion (EAGAIN, ENOMEM, EMFILE) and a name the launcher rejects outright
// are not outcomes a shell reports a status for, so they keep their plain error
// and stay distinguishable by type: a caller matches *CommandStartError to hear
// "the shell would have said 126 or 127", and anything else is a failure to
// launch. A NUL-bearing name is one a bare `exec.Command` reports as not found,
// so it arrives as 127 the same way an unresolvable name does.
func shellStartStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	// os/exec's own PATH search, for a bare name it could not resolve. That is
	// the shell's "command not found", not a refusal by the kernel.
	if errors.Is(err, exec.ErrNotFound) {
		return shellStatusNotFound, true
	}
	// ENOENT: the program is gone, or the interpreter its `#!` line names is.
	// Either way the shell reports the name as not found.
	if errors.Is(err, fs.ErrNotExist) {
		return shellStatusNotFound, true
	}
	if startRefusedByKernel(err) {
		return shellStatusNoExec, true
	}
	return 0, false
}

// argvRoute reports whether cmd runs a program directly, the route whose start
// failure a shell reports as an exit status. A runtime that does not declare
// itself — a scripted test double, a non-local environment — keeps the plain
// start error.
//
// A sandbox-wrapped runtime is excluded even though it is the argv route: the
// exec that fails is the wrapper's, so the errno describes bwrap or sandbox-exec
// rather than the program. Reporting it as the program's 126/127 would name the
// wrong thing, which is the defect this mapping exists to remove.
func argvRoute(cmd commandRuntime) bool {
	argv, ok := cmd.(interface{ runsArgv() bool })
	return ok && argv.runsArgv()
}

// refusedStart reports a failed start on the argv route as the exit status a
// shell gives for that refusal, and nil for every other outcome: a start failure
// the shell has no status for, and any failure on the shell route.
func refusedStart(cmd commandRuntime, err error) *CommandStartError {
	if !argvRoute(cmd) {
		return nil
	}
	status, ok := shellStartStatus(err)
	if !ok {
		return nil
	}
	return &CommandStartError{Status: status, Err: err}
}

// refusedStreamHandle is the handle StreamCommand returns for a start refusal on
// the argv route: nothing ran, so there is no pid, no signal to send, and no
// output beyond what the launcher wrote — and Wait reports the status the shell
// reports, with no error, exactly as it does for a command that ran and exited
// nonzero.
func refusedStreamHandle(refusal *CommandStartError) *StreamHandle {
	return &StreamHandle{
		Wait:       func() (int, error) { return refusal.Status, nil },
		Signal:     func() {},
		SignalName: func() string { return "" },
	}
}
