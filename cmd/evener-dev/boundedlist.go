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
	// stuck records that the command's process group could not be cleared:
	// either the direct child outlived SIGKILL, which only a child stuck in
	// the kernel does, or the group still had members after the sweep killed
	// it. Both mean the same to the runner -- there is nothing to retry, and
	// another attempt would add a second stuck process to the first.
	stuck    bool
	exitCode int
	pgid     int
}

// runBoundedAttempt runs argv with its own process group, returning when it
// finishes or when timeout passes, whichever comes first. On the bound it sends
// the group SIGTERM, waits grace, and sends SIGKILL.
func runBoundedAttempt(argv []string, timeout, grace time.Duration, stderr io.Writer, latch *signalLatch) attemptResult {
	var out syncBuffer
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
	// The output goes through pipes this attempt owns, and the copying is done
	// here rather than by exec. Handing exec an io.Writer makes cmd.Wait wait
	// for its own copiers too, so the wait returns when the output stops
	// arriving rather than when the process exits -- and then the bound cannot
	// tell a command that is still running from one whose last bytes are in
	// flight. With pipes, `reaped` closes at the exit, and the drain is a
	// separate wait this attempt can bound on its own terms.
	feeds, err := openOutputPipes(cmd)
	if err != nil {
		_, _ = fmt.Fprintf(guarded, "bounded-list: %v\n", err)
		return attemptResult{err: err, exitCode: 1}
	}
	if err := procgroup.Start(cmd); err != nil {
		// The gate reads this log and nothing else; a start that failed with
		// nothing written is a module that failed for no stated reason.
		feeds.closeAll()
		_, _ = fmt.Fprintf(guarded, "bounded-list: %v\n", err)
		return attemptResult{err: err, exitCode: 1}
	}
	drained := feeds.copyInto(&out, guarded)
	// The child leads its own group, so the group id is its pid.
	result := attemptResult{pgid: cmd.Process.Pid}
	// The wait publishes its answer before it says the child is gone, so
	// anything that sees `reaped` closed can read that answer: a close-then-
	// send order leaves a window where the command has finished and nothing
	// can tell, which at the bound reads as a timeout.
	reaped := make(chan struct{})
	waited := &waitResult{}
	go func() {
		err := cmd.Wait()
		waited.publish(err)
		close(reaped)
	}()
	// completed is the attempt that ended by the command's own decision,
	// whether that was noticed while waiting or at the moment the bound
	// expired.
	completed := func(err error) attemptResult {
		// The process is gone; its last bytes may not be. Wait for them, and
		// no longer than the grace: a pipe still held open by something that
		// escaped the group would otherwise hold this attempt as well.
		drainErr := awaitDrain(drained, grace, latch)
		result.stdout = out.Bytes()
		exitCode := procgroup.ExitCode(cmd.ProcessState)
		if drainErr == nil {
			drainErr = feeds.copyErr()
		}
		if drainErr != nil && exitCode == 0 {
			// The list this attempt would hand on is not the list the command
			// wrote, and a caller cannot tell the difference from a short one.
			_, _ = fmt.Fprintf(guarded, "bounded-list: %s finished, but its output could not be read in full: %v\n", argv[0], drainErr)
			exitCode = 1
		}
		result.completeWith(exitCode, err, latch)
		if !stopSurvivors(realGroupStopper, cmd.Path, result.pgid, grace, guarded) {
			// The command answered, but its group is still there holding the
			// caches; another attempt would add a second one.
			result.stuck = true
			result.exitCode = 124
		}
		// Cleanup takes a grace or two, and a signal that arrives during it is
		// watched for by nobody. Latching it here is what stops the runner
		// from starting another attempt after the operator said stop -- even
		// for a command that had already failed on its own.
		if sig := latch.poll(); sig != 0 && result.interrupted == 0 {
			result.interrupted = sig
		}
		if result.stuck && result.interrupted != 0 {
			// The same rule as the give-up path: an interrupt is what was
			// asked for, and 124 would send the caller to their caches.
			result.exitCode = 128 + int(result.interrupted)
		}
		return result
	}
	select {
	case <-reaped:
		return completed(waited.err())
	case received := <-latch.waiting():
		// A signal and the command's own finish can be ready together, and
		// select picks between ready cases at random: a command that finished
		// is not an interrupted one, whatever arrived at the same moment.
		if finished, err := finishedFirst(reaped, waited); finished {
			latch.receive(received)
			return completed(err)
		}
		// The command runs in a process group of its own, which is what keeps
		// a wedged compiler off this host -- and also what stops the
		// terminal's SIGINT from reaching it, since it is no longer in the
		// shell's foreground group. So this helper passes on what it is sent.
		result.interrupted = latch.receive(received)
		_, _ = fmt.Fprintf(guarded, "bounded-list: %v — stopping %s.\n", received, argv[0])
	case <-time.After(timeout):
		// `reaped` closes when the process exits, not when its output stops
		// arriving, so this arm means the command is still running: it gets
		// stopped now, with no grace spent guessing.
		result.timedOut = true
	}
	// The signal this helper was sent, if it was sent one, then SIGTERM, the
	// grace, and SIGKILL -- and nothing at all if the child was reaped first,
	// whose pid may already belong to someone else.
	procgroup.StopWith(result.pgid, result.interrupted, reaped, grace)
	if !reapOrGiveUp(reaped, grace, argv[0], guarded) {
		// Nothing this process can do reaches a child the kernel will not let
		// go of. The sweep is skipped on purpose: it would spend another grace
		// signalling a group that cannot answer, and leaving now hands the
		// child to init, which is the one thing that can still clean it up.
		_ = awaitDrain(drained, grace, latch)
		result.stdout = out.Bytes()
		result.stuck = true
		result.exitCode = 124
		// A stuck child does not change what the operator asked for: an
		// interrupt is still an interrupt, and reporting it as a timeout sends
		// the caller looking at its caches for a signal it sent itself.
		result.takeLateSignal(latch)
		if result.interrupted != 0 {
			result.exitCode = 128 + int(result.interrupted)
		}
		return result
	}
	if !stopSurvivors(realGroupStopper, cmd.Path, result.pgid, grace, guarded) {
		result.stuck = true
	}
	_ = awaitDrain(drained, grace, latch)
	result.stdout = out.Bytes()
	result.exitCode = 1
	if result.stuck {
		result.exitCode = 124
	}
	if result.interrupted != 0 {
		result.exitCode = 128 + int(result.interrupted)
	}
	result.takeLateSignal(latch)
	return result
}

// waitResult is where the wait goroutine publishes the command's own answer.
// It is written once, before the reaped channel is closed, and read only after
// that channel is seen closed -- the close is the happens-before edge, so the
// reader never sees a half-written answer and never sees an empty one for a
// command that has in fact finished.
type waitResult struct {
	mu      sync.Mutex
	waitErr error
}

func (w *waitResult) publish(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.waitErr = err
}

func (w *waitResult) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.waitErr
}

// finishedFirst takes the command's own answer when the command has already
// finished. The bound expiring and the command finishing can be ready at the
// same moment, and select picks between ready cases at random, so without this
// a command that finished microseconds before the bound would be called a
// timeout: retried, and reported as 124 on the last attempt.
func finishedFirst(reaped <-chan struct{}, waited *waitResult) (bool, error) {
	select {
	case <-reaped:
		return true, waited.err()
	default:
		return false, nil
	}
}

// reapOrGiveUp waits for the reap that the stop above should have produced,
// and says so when it does not come. A child in an uninterruptible kernel wait
// -- a stalled volume is exactly how one gets there, and a stalled volume is
// this helper's whole subject -- does not die of SIGKILL, and waiting on it
// forever is the unbounded hang the bound exists to replace.
func reapOrGiveUp(reaped <-chan struct{}, grace time.Duration, name string, stderr io.Writer) bool {
	select {
	case <-reaped:
		return true
	case <-time.After(grace):
		_, _ = fmt.Fprintf(stderr,
			"bounded-list: %s did not exit after SIGKILL within %s; it is stuck in the kernel, and is left to init.\n",
			name, grace)
		return false
	}
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

// completeWith records what the command itself decided, and latches any signal
// that arrived while it was deciding. The signal still stops the run -- the
// runner starts no further attempt once it is set -- but it does not overwrite
// the command's answer: a `go list` that finished is not an interrupted one,
// whatever arrived at the same moment.
func (r *attemptResult) completeWith(exitCode int, err error, latch *signalLatch) {
	r.err = err
	r.exitCode = exitCode
	if r.interrupted == 0 {
		r.interrupted = latch.poll()
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

// syncBuffer collects the command's stdout, which exec fills from a goroutine
// of its own. An attempt that gives up on an unreapable child reads it while
// that goroutine may still be writing.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// Bytes is a copy, so the caller reads something the copier cannot grow under
// it.
func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// outputPipes is the command's stdout and stderr, carried by pipes this
// attempt owns. exec would copy them itself if it were handed writers, and
// cmd.Wait would then wait for those copies -- which is the difference between
// a wait that ends at the process's exit and one that ends when its output
// stops arriving.
type outputPipes struct {
	stdoutR, stderrR *os.File
	stdoutW, stderrW *os.File

	mu   sync.Mutex
	fail error
}

// openOutputPipes attaches a pipe to each of the command's streams.
func openOutputPipes(cmd *exec.Cmd) (*outputPipes, error) {
	feeds := &outputPipes{}
	var err error
	if feeds.stdoutR, feeds.stdoutW, err = os.Pipe(); err != nil {
		return nil, fmt.Errorf("opening a pipe for the command's output: %w", err)
	}
	if feeds.stderrR, feeds.stderrW, err = os.Pipe(); err != nil {
		feeds.closeAll()
		return nil, fmt.Errorf("opening a pipe for the command's diagnostics: %w", err)
	}
	cmd.Stdout, cmd.Stderr = feeds.stdoutW, feeds.stderrW
	return feeds, nil
}

func (o *outputPipes) closeAll() {
	for _, f := range []*os.File{o.stdoutR, o.stdoutW, o.stderrR, o.stderrW} {
		if f != nil {
			_ = f.Close()
		}
	}
}

// copyInto starts the copying and returns a channel closed when both streams
// have ended, which is when the command's last byte has arrived.
func (o *outputPipes) copyInto(stdout io.Writer, stderr io.Writer) <-chan struct{} {
	// The write ends belong to the child now: holding a copy here would keep
	// the reads open forever after it exits.
	_ = o.stdoutW.Close()
	_ = o.stderrW.Close()
	var copies sync.WaitGroup
	copies.Add(2)
	carry := func(dst io.Writer, src *os.File) {
		defer copies.Done()
		defer func() { _ = src.Close() }()
		if _, err := io.Copy(dst, src); err != nil {
			o.mu.Lock()
			if o.fail == nil {
				o.fail = err
			}
			o.mu.Unlock()
		}
	}
	go carry(stdout, o.stdoutR)
	go carry(stderr, o.stderrR)
	drained := make(chan struct{})
	go func() {
		copies.Wait()
		close(drained)
	}()
	return drained
}

// copyErr is whatever went wrong while carrying the output, and nil when
// nothing did.
func (o *outputPipes) copyErr() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.fail
}

// awaitDrain waits for the command's last bytes, for no longer than the grace,
// and latches a signal that arrives meanwhile: every wait in this attempt is
// also a place an operator can interrupt it.
func awaitDrain(drained <-chan struct{}, grace time.Duration, latch *signalLatch) error {
	for {
		select {
		case <-drained:
			return nil
		case received := <-latch.waiting():
			latch.receive(received)
		case <-time.After(grace):
			return fmt.Errorf("the output was still arriving %s after the command ended", grace)
		}
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

// groupStopper is the process-group half of an attempt, injected so the case
// that cannot be produced on demand -- a group that outlives SIGKILL -- can
// still be tested.
type groupStopper struct {
	exists    func(int) bool
	terminate func(int)
	kill      func(int)
}

var realGroupStopper = groupStopper{
	exists:    procgroup.Exists,
	terminate: procgroup.Terminate,
	kill:      procgroup.Kill,
}

// stopSurvivors stops whatever is still in the command's process group once
// the leader is gone, and reports whether the group is empty afterwards. The
// leader being reaped says nothing about the group: a leader that handles
// SIGTERM and exits can leave a child that ignores it, and that child goes on
// holding the build and module cache locks.
//
// Asking after the reap is safe exactly while the group is not empty, which is
// what procgroup.Exists answers. A pid is not recycled while it is a live
// group's id, so a group that answers kill(-pgid, 0) is the group this attempt
// started, until its last member is gone -- and once it is gone the sweep
// sends nothing, because Exists says so first. The window between that check
// and the signal is microseconds against a pid space handed out in sequence,
// and closing it would mean probing process identity, which is a great deal of
// machinery for a race nothing here has been able to produce.
//
// A group still there after the SIGKILL is the same situation as a child that
// could not be reaped, and gets the same answer: false, and the caller stops
// rather than starting an attempt that would leave a second one behind.
func stopSurvivors(g groupStopper, name string, pgid int, grace time.Duration, stderr io.Writer) bool {
	if !g.exists(pgid) {
		return true
	}
	_, _ = fmt.Fprintf(stderr, "bounded-list: %s left processes running in its group; stopping them.\n", name)
	g.terminate(pgid)
	if waitForGroupToGo(g, pgid, grace) {
		return true
	}
	g.kill(pgid)
	if waitForGroupToGo(g, pgid, grace) {
		return true
	}
	_, _ = fmt.Fprintf(stderr,
		"bounded-list: process group %d is still there after SIGKILL; it is stuck in the kernel, and is left to init.\n", pgid)
	return false
}

// waitForGroupToGo polls until the group is empty or the grace runs out.
func waitForGroupToGo(g groupStopper, pgid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if !g.exists(pgid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
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
  124                 every attempt timed out (coreutils' timeout convention),
                      or the command left a process group that could not be
                      cleared -- either way, nothing to retry
  128+signal          this helper was interrupted and stopped the command
  1                   the command could not be started, or its output could
                      not be handed on in full
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
	if !procgroup.Supported {
		// Everything this subcommand does is stopping a process group: without
		// them it would run the command and call whatever happened a bound.
		_, _ = fmt.Fprintln(stderr, "bounded-list needs a platform with process groups (linux, darwin)")
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
		case result.stuck && result.interrupted == 0:
			// Retrying stacks another `go list` on the volume that already
			// has one stuck on it, and the attempt has said why on stderr.
			if !forwardOutput(stdout, result.stdout, stderr) {
				return 1
			}
			return 124
		case result.interrupted != 0:
			// An interrupt is an answer about this run, not about the command:
			// retrying it would be the opposite of what was asked.
			if !forwardOutput(stdout, result.stdout, stderr) {
				return 1
			}
			return result.exitCode
		case !result.timedOut:
			// Whatever it decided, it decided: a command that exits non-zero
			// has an answer about its input, and repeating it repeats the
			// answer.
			if !forwardOutput(stdout, result.stdout, stderr) {
				return 1
			}
			return result.exitCode
		case attempt < *attempts:
			_, _ = fmt.Fprintf(stderr, "bounded-list: attempt %d of %d timed out after %s; retrying.\n",
				attempt, *attempts, *timeout)
		default:
			_, _ = fmt.Fprintf(stderr, "bounded-list: %s timed out after %s on %s.\n",
				argv[0], *timeout, attemptCount(*attempts))
			// Whatever it managed to print before the bound is the evidence a
			// caller has to work from, so it is passed through here too.
			if !forwardOutput(stdout, result.stdout, stderr) {
				return 1
			}
			return 124
		}
	}
	return 124
}

// forwardOutput hands the caller what the command printed, and says so when it
// could not. A caller reading a truncated package list tests the packages it
// received and reports a pass for the rest, which is the one failure this
// helper must never produce quietly.
func forwardOutput(stdout io.Writer, data []byte, stderr io.Writer) bool {
	n, err := stdout.Write(data)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "bounded-list: wrote %d of %d bytes of the command's output: %v\n", n, len(data), err)
		return false
	}
	if n != len(data) {
		// io.Writer may return a short count with no error at all, and a
		// caller reading the short list cannot tell.
		_, _ = fmt.Fprintf(stderr, "bounded-list: wrote %d of %d bytes of the command's output\n", n, len(data))
		return false
	}
	return true
}

// attemptCount reads the way the sentence around it needs it to.
func attemptCount(attempts int) string {
	if attempts == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("each of %d attempts", attempts)
}
