package dev

// bounded-list runs a command under a time bound and stops it as a process
// group when the bound passes.
//
// It exists for one failure: `go list ./...` on a runner whose Go caches sit on
// a stalled volume blocks forever, and the gate that waits for it hangs until CI
// kills the job — a required check failing with no diagnostic, on a tree whose
// code was fine. A bound turns that into a named failure, and a retry turns a
// merely slow runner back into a pass, since a killed attempt still warms the
// caches for the next one.
//
// The command runs in a process group of its own, and the bound stops that
// group rather than the one process this helper spawned: `go list` forks
// compilers, and a child that outlives its parent goes on holding the build and
// module cache locks that every later run on the host needs. SIGTERM first, then
// SIGKILL after a grace, because a wedged process does not answer the first.

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

// attemptResult is what one bounded run of the command came to.
type attemptResult struct {
	stdout   []byte
	err      error
	timedOut bool
	exitCode int
	pgid     int
}

// runBoundedAttempt runs argv with its own process group, returning when it
// finishes or when timeout passes, whichever comes first. On the bound it sends
// the group SIGTERM, waits grace, and sends SIGKILL.
func runBoundedAttempt(argv []string, timeout, grace time.Duration, stderr io.Writer) attemptResult {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &out
	cmd.Stderr = stderr
	grouped := isolateProcessGroup(cmd)
	// Cancelling the context is how the bound is delivered, and it has to reach
	// the group, not the one child: the default cancel kills that child alone.
	// Once the child has been reaped its pid can belong to something else, so a
	// cancel that arrives after the wait returned signals nothing.
	var reaped atomic.Bool
	cmd.Cancel = func() error {
		if !reaped.Load() {
			stopProcessGroup(cmd, false)
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return attemptResult{err: err, exitCode: 1}
	}
	// The child is its own group leader, so its pgid is its pid.
	var result attemptResult
	if grouped {
		result.pgid = cmd.Process.Pid
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		reaped.Store(true)
		result.stdout = out.Bytes()
		result.err = err
		result.exitCode = exitCodeOf(err)
		stopSurvivors(cmd, result.pgid, grace, stderr)
		return result
	case <-time.After(timeout):
	}
	result.timedOut = true
	cancel()
	select {
	case <-done:
	case <-time.After(grace):
		// Still there, so it is not going to answer SIGTERM. The leader has not
		// been reaped yet, which is what makes this group id still ours to
		// signal.
		stopProcessGroup(cmd, true)
		<-done
	}
	reaped.Store(true)
	stopSurvivors(cmd, result.pgid, grace, stderr)
	result.stdout = out.Bytes()
	result.exitCode = 1
	return result
}

// stopSurvivors stops whatever is still in the command's process group once the
// leader is gone. The leader being reaped says nothing about the group: a
// leader that handles SIGTERM and exits can leave a child that ignores it, and
// that child goes on holding the build and module cache locks.
//
// Signalling a group id whose leader has been reaped is safe exactly while the
// group is not empty. A pid cannot be reused while it is still a live group's
// id, so any answer to kill(-pgid, 0) other than ESRCH means the group is the
// one this attempt created -- and if the answer is ESRCH there is nobody to
// signal and nothing is sent.
func stopSurvivors(cmd *exec.Cmd, pgid int, grace time.Duration, stderr io.Writer) {
	if pgid == 0 || !processGroupExists(pgid) {
		return
	}
	_, _ = fmt.Fprintf(stderr, "bounded-list: %s left processes running in its group; stopping them.\n", cmd.Path)
	stopProcessGroup(cmd, false)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processGroupExists(pgid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if processGroupExists(pgid) {
		stopProcessGroup(cmd, true)
	}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}
	return 1
}

func boundedListMain(args []string) int {
	return boundedList(args, os.Stdout, os.Stderr)
}

// boundedList is the subcommand: run the command under the bound, retry a
// timed-out attempt up to the attempt count, and write the list it produced.
func boundedList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bounded-list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", 60*time.Second, "how long one attempt may take")
	attempts := fs.Int("attempts", 3, "how many attempts a timed-out command gets")
	grace := fs.Duration("grace", 5*time.Second, "how long the group has to answer SIGTERM before SIGKILL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		_, _ = fmt.Fprintln(stderr, "bounded-list: give it a command to run, after --")
		return 2
	}
	if *attempts < 1 || *timeout <= 0 || *grace <= 0 {
		_, _ = fmt.Fprintln(stderr, "bounded-list: -attempts must be at least 1, and -timeout and -grace positive")
		return 2
	}
	for attempt := 1; attempt <= *attempts; attempt++ {
		result := runBoundedAttempt(argv, *timeout, *grace, stderr)
		if !result.timedOut {
			// Whatever it decided, it decided: a command that exits non-zero
			// has an answer about its input, and repeating it repeats the
			// answer.
			_, _ = stdout.Write(result.stdout)
			return result.exitCode
		}
		if attempt < *attempts {
			_, _ = fmt.Fprintf(stderr, "bounded-list: attempt %d of %d timed out after %s; retrying.\n",
				attempt, *attempts, *timeout)
		}
	}
	_, _ = fmt.Fprintf(stderr, "bounded-list: %s timed out after %s on each of %d attempts.\n",
		argv[0], *timeout, *attempts)
	return 1
}
