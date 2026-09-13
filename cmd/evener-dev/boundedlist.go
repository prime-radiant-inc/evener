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
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/evener/internal/devtool/procgroup"
)

// attemptResult is what one bounded run of the command came to.
type attemptResult struct {
	stdout   []byte
	err      error
	timedOut bool
	// interrupted is the signal this helper was sent while the command ran,
	// and zero when it was not.
	interrupted syscall.Signal
	exitCode    int
	pgid        int
}

// runBoundedAttempt runs argv with its own process group, returning when it
// finishes or when timeout passes, whichever comes first. On the bound it sends
// the group SIGTERM, waits grace, and sends SIGKILL.
func runBoundedAttempt(argv []string, timeout, grace time.Duration, stderr io.Writer, latch *signalLatch) attemptResult {
	var out bytes.Buffer
	// The command's stderr goes where this helper's diagnostics go, and exec
	// copies it from a goroutine of its own. Both writers are live at once --
	// the interrupt line below is written while the command is still running --
	// so they share one lock or they corrupt whatever they are writing to.
	guarded := &serialWriter{w: stderr}
	// The context is here because a command built without one is a lint
	// failure; the stopping is procgroup's, which reaches the group rather
	// than the single child exec's own cancel would kill.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Cancel = func() error { return nil }
	cmd.Stdout = &out
	cmd.Stderr = guarded
	if err := procgroup.Start(cmd); err != nil {
		return attemptResult{err: err, exitCode: 1}
	}
	// The child leads its own group, so the group id is its pid.
	result := attemptResult{pgid: cmd.Process.Pid}
	done := make(chan error, 1)
	reaped := make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(reaped)
		done <- err
	}()
	select {
	case err := <-done:
		result.stdout = out.Bytes()
		result.err = err
		result.exitCode = procgroup.ExitCode(cmd.ProcessState)
		stopSurvivors(cmd.Path, result.pgid, grace, guarded)
		result.takeLateSignal(latch)
		return result
	case received := <-latch.waiting():
		// The command runs in a process group of its own, which is what keeps
		// a wedged compiler off this host -- and also what stops the
		// terminal's SIGINT from reaching it, since it is no longer in the
		// shell's foreground group. So this helper passes on what it is sent.
		result.interrupted = latch.receive(received)
		_, _ = fmt.Fprintf(guarded, "bounded-list: %v — stopping %s.\n", received, argv[0])
	case <-time.After(timeout):
		result.timedOut = true
	}
	// SIGTERM, the grace, then SIGKILL -- and nothing at all if the child was
	// reaped first, whose pid may already belong to someone else.
	procgroup.Stop(result.pgid, reaped, grace)
	<-done
	stopSurvivors(cmd.Path, result.pgid, grace, guarded)
	result.stdout = out.Bytes()
	result.exitCode = 1
	if result.interrupted != 0 {
		result.exitCode = 128 + int(result.interrupted)
	}
	result.takeLateSignal(latch)
	return result
}

// signalLatch remembers the first stop signal this helper is sent. One that
// arrives while an attempt is being cleaned up, or in the gap between
// attempts, has nowhere else to be noticed: the select that was watching for
// it has already returned, and the next thing this helper would otherwise do
// is start more work the sender asked it to stop doing.
type signalLatch struct {
	ch   <-chan os.Signal
	held syscall.Signal
}

// waiting is the channel to select on, and nil -- which blocks forever -- when
// there is no latch, which is how the unit tests run an attempt nobody signals.
func (l *signalLatch) waiting() <-chan os.Signal {
	if l == nil {
		return nil
	}
	return l.ch
}

// receive latches a signal the caller has already taken off the channel.
func (l *signalLatch) receive(s os.Signal) syscall.Signal {
	if l == nil {
		if sig, ok := s.(syscall.Signal); ok && sig != 0 {
			return sig
		}
		return syscall.SIGTERM
	}
	if l.held == 0 {
		if sig, ok := s.(syscall.Signal); ok && sig != 0 {
			l.held = sig
		} else {
			l.held = syscall.SIGTERM
		}
	}
	return l.held
}

// poll takes a signal that arrived since anyone last looked, and keeps
// answering with it afterwards.
func (l *signalLatch) poll() syscall.Signal {
	if l == nil {
		return 0
	}
	if l.held != 0 {
		return l.held
	}
	select {
	case s := <-l.ch:
		return l.receive(s)
	default:
		return 0
	}
}

// takeLateSignal turns a signal that arrived during the attempt's cleanup into
// this attempt's answer, so the runner stops rather than retries.
func (r *attemptResult) takeLateSignal(latch *signalLatch) {
	if r.interrupted != 0 {
		return
	}
	if sig := latch.poll(); sig != 0 {
		r.interrupted = sig
		r.exitCode = 128 + int(sig)
	}
}

// serialWriter is one writer two goroutines can use: this helper's own
// diagnostics and the copier exec runs for the command's stderr.
type serialWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *serialWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// stopSurvivors stops whatever is still in the command's process group once
// the leader is gone. The leader being reaped says nothing about the group: a
// leader that handles SIGTERM and exits can leave a child that ignores it, and
// that child goes on holding the build and module cache locks.
//
// Asking after the reap is safe exactly while the group is not empty, which is
// what procgroup.Exists answers: a pid cannot be reused while it is still a
// live group's id, and an empty group answers no, so nothing is sent.
func stopSurvivors(name string, pgid int, grace time.Duration, stderr io.Writer) {
	if !procgroup.Exists(pgid) {
		return
	}
	_, _ = fmt.Fprintf(stderr, "bounded-list: %s left processes running in its group; stopping them.\n", name)
	procgroup.Terminate(pgid)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !procgroup.Exists(pgid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if procgroup.Exists(pgid) {
		procgroup.Kill(pgid)
	}
}

// boundedListUsage is also where the exit statuses are written down, since a
// caller's next move depends on which one it got.
const boundedListUsage = `usage: evener-dev bounded-list [-timeout d] [-attempts n] [-grace d] -- command [args...]

Runs the command under a per-attempt time bound and retries an attempt that
hits it, stopping the command's whole process group when it does. Whatever the
command wrote to stdout is passed through on every outcome.

Exit status:
  the command's own   it finished within the bound
  124                 every attempt timed out (coreutils' timeout convention)
  128+signal          this helper was interrupted and stopped the command
  2                   a usage error
`

func boundedListMain(args []string) int {
	return boundedList(args, os.Stdout, os.Stderr)
}

// boundedList is the subcommand: run the command under the bound, retry a
// timed-out attempt up to the attempt count, and write what it produced.
func boundedList(args []string, stdout, stderr io.Writer) int {
	// TERM, INT and HUP have to be forwarded by hand. The command runs in its
	// own process group, so a signal sent to this helper's group -- Ctrl-C in
	// the terminal, a gate runner stopping its children -- no longer reaches
	// it the way it did when the shell ran `go list` in the foreground group.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	return boundedListWith(args, stdout, stderr, signals)
}

func boundedListWith(args []string, stdout, stderr io.Writer, signals <-chan os.Signal) int {
	fs := flag.NewFlagSet("bounded-list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, boundedListUsage) }
	timeout := fs.Duration("timeout", 60*time.Second, "how long one attempt may take")
	attempts := fs.Int("attempts", 3, "how many attempts a timed-out command gets")
	grace := fs.Duration("grace", 5*time.Second, "how long the group has to answer SIGTERM before SIGKILL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		_, _ = fmt.Fprintln(stderr, "bounded-list: give it a command to run, after --")
		fs.Usage()
		return 2
	}
	if *attempts < 1 || *timeout <= 0 || *grace <= 0 {
		_, _ = fmt.Fprintln(stderr, "bounded-list: -attempts must be at least 1, and -timeout and -grace positive")
		return 2
	}
	latch := &signalLatch{ch: signals}
	for attempt := 1; attempt <= *attempts; attempt++ {
		if sig := latch.poll(); sig != 0 {
			_, _ = fmt.Fprintf(stderr, "bounded-list: %v — not starting attempt %d.\n", sig, attempt)
			return 128 + int(sig)
		}
		result := runBoundedAttempt(argv, *timeout, *grace, stderr, latch)
		switch {
		case result.interrupted != 0:
			// An interrupt is an answer about this run, not about the command:
			// retrying it would be the opposite of what was asked.
			_, _ = stdout.Write(result.stdout)
			return result.exitCode
		case !result.timedOut:
			// Whatever it decided, it decided: a command that exits non-zero
			// has an answer about its input, and repeating it repeats the
			// answer.
			_, _ = stdout.Write(result.stdout)
			return result.exitCode
		case attempt < *attempts:
			_, _ = fmt.Fprintf(stderr, "bounded-list: attempt %d of %d timed out after %s; retrying.\n",
				attempt, *attempts, *timeout)
		default:
			_, _ = fmt.Fprintf(stderr, "bounded-list: %s timed out after %s on %s.\n",
				argv[0], *timeout, attemptCount(*attempts))
			// Whatever it managed to print before the bound is the evidence a
			// caller has to work from, so it is passed through here too.
			_, _ = stdout.Write(result.stdout)
			return 124
		}
	}
	return 124
}

// attemptCount reads the way the sentence around it needs it to.
func attemptCount(attempts int) string {
	if attempts == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("each of %d attempts", attempts)
}
