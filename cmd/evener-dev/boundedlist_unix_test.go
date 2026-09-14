//go:build linux || darwin

package dev

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A child that ignores SIGTERM is what the bound exists for: a `go list` wedged
// on a stalled cache volume does not answer the polite signal either.
// The child says when it is running and when it has been sent SIGTERM, so the
// tests wait on it rather than on a number of milliseconds. Local gates are
// macOS and CI is Linux; a sleep that is long enough here is a guess there.
const termProofChild = `trap ': > "${BOUNDED_LIST_TERMED:-/dev/null}"' TERM; : > "${BOUNDED_LIST_READY:-/dev/null}"; while :; do sleep 0.05; done`

// signalWhenReady sends sig once path appears, and returns the check the test
// goroutine calls afterwards. The waiting happens off the test's goroutine and
// the failing happens on it: t.Fatal from anywhere else leaves the test to
// stall until its timeout rather than fail.
func signalWhenReady(path, label string, signals chan<- os.Signal, sig syscall.Signal) func(*testing.T) {
	watch := make(chan error, 1)
	go func() {
		err := awaitFileErr(path, label)
		if err == nil {
			signals <- sig
		}
		watch <- err
	}()
	return func(t *testing.T) {
		t.Helper()
		if err := <-watch; err != nil {
			t.Fatal(err)
		}
	}
}

// readinessFiles gives the child its two paths and returns them. The child
// inherits this process's environment, so t.Setenv is how they arrive.
func readinessFiles(t *testing.T) (ready, termed string) {
	t.Helper()
	dir := t.TempDir()
	ready, termed = filepath.Join(dir, "ready"), filepath.Join(dir, "termed")
	t.Setenv("BOUNDED_LIST_READY", ready)
	t.Setenv("BOUNDED_LIST_TERMED", termed)
	return ready, termed
}

func TestBoundedAttemptPassesAFastChildsOutputThrough(t *testing.T) {
	var stderr bytes.Buffer
	result := runBoundedAttempt([]string{"sh", "-c", "echo one; echo two"}, 5*time.Second, time.Second, &stderr, nil)
	if result.err != nil {
		t.Fatalf("err = %v, stderr = %q", result.err, stderr.String())
	}
	if result.timedOut {
		t.Fatal("timedOut = true for a child that finished at once")
	}
	if got := string(result.stdout); got != "one\ntwo\n" {
		t.Fatalf("stdout = %q, want %q", got, "one\ntwo\n")
	}
}

func TestBoundedAttemptStopsAGroupThatIgnoresTerm(t *testing.T) {
	var stderr bytes.Buffer
	start := time.Now()
	result := runBoundedAttempt([]string{"sh", "-c", termProofChild}, 200*time.Millisecond, 500*time.Millisecond, &stderr, nil)
	if !result.timedOut {
		t.Fatalf("timedOut = false, want true; err = %v", result.err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the bound took %s, which is not a bound", elapsed)
	}
	// The group, not just the leader: the child ignored SIGTERM, so only the
	// escalation to SIGKILL can have emptied it.
	requireGroupGone(t, result.pgid)
}

func TestBoundedAttemptStopsAChildTheLeaderLeftBehind(t *testing.T) {
	var stderr bytes.Buffer
	// The leader exits at once, and the child it leaves behind ignores SIGTERM
	// and holds neither end of the pipe, so the wait returns with the group
	// still populated. Being reaped says nothing about the group.
	const orphanMaker = `sh -c 'trap "" TERM; sleep 60' >/dev/null 2>&1 & exit 0`
	result := runBoundedAttempt([]string{"sh", "-c", orphanMaker}, 5*time.Second, 300*time.Millisecond, &stderr, nil)
	if result.timedOut {
		t.Fatalf("timedOut = true, want the leader's own prompt exit; stderr = %q", stderr.String())
	}
	if result.err != nil {
		t.Fatalf("err = %v, stderr = %q", result.err, stderr.String())
	}
	requireGroupGone(t, result.pgid)
}

// requireGroupGone waits for the process group to empty, which is the only
// evidence that the attempt stopped everything it started.
func requireGroupGone(t *testing.T, pgid int) {
	t.Helper()
	if pgid <= 1 {
		t.Fatalf("pgid = %d, so nothing could have been signalled", pgid)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d is still there after the attempt (kill answered %v)", pgid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBoundedListRejectsANonPositiveGrace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedListWith([]string{"-grace", "0s", "--", "true"}, &stdout, &stderr, nil)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for a grace that would SIGKILL at once", code)
	}
	if got := stderr.String(); !strings.Contains(got, "-grace") {
		t.Fatalf("stderr = %q, want it to name -grace", got)
	}
}

func TestBoundedListRetriesThenFailsWithADiagnostic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedListWith([]string{"-timeout", "200ms", "-attempts", "2", "-grace", "500ms", "--", "sh", "-c", termProofChild}, &stdout, &stderr, nil)
	if code == 0 {
		t.Fatalf("exit code = 0 for a command that never finished; stderr = %q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "timed out after 200ms on each of 2 attempts") {
		t.Fatalf("stderr = %q, want it to name the budget and the attempts", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing: no attempt produced a list", stdout.String())
	}
}

func TestBoundedListKeepsAFailedCommandsStatusAndDoesNotRetry(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedListWith([]string{"-timeout", "5s", "-attempts", "3", "--", "sh", "-c", "echo broken >&2; exit 3"}, &stdout, &stderr, nil)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3: a command that decided something is not a timeout", code)
	}
	if got := stderr.String(); !strings.Contains(got, "broken") {
		t.Fatalf("stderr = %q, want the command's own diagnostic", got)
	}
	if strings.Count(stderr.String(), "broken") != 1 {
		t.Fatalf("stderr = %q, want one attempt only", stderr.String())
	}
}

func TestBoundedListWritesTheListOnSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedListWith([]string{"-timeout", "5s", "-attempts", "2", "--", "sh", "-c", "echo primeradiant.com/evener"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "primeradiant.com/evener\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestBoundedAttemptForwardsAnInterruptToTheGroup(t *testing.T) {
	var stderr bytes.Buffer
	// The command runs in a process group of its own, so a signal sent to this
	// helper does not reach it the way it reached a `go list` the shell ran in
	// its foreground group. It has to be passed on.
	ready, _ := readinessFiles(t)
	signals := make(chan os.Signal, 1)
	sent := signalWhenReady(ready, "the child never started", signals, syscall.SIGTERM)
	result := runBoundedAttempt([]string{"sh", "-c", termProofChild}, 30*time.Second, 300*time.Millisecond, &stderr, &signalLatch{ch: signals})
	sent(t)
	if result.interrupted != syscall.SIGTERM {
		t.Fatalf("interrupted = %v, want SIGTERM; timedOut = %v", result.interrupted, result.timedOut)
	}
	if result.exitCode != 143 {
		t.Fatalf("exitCode = %d, want 143", result.exitCode)
	}
	if result.timedOut {
		t.Fatal("timedOut = true: the 30s bound was nowhere near")
	}
	requireGroupGone(t, result.pgid)
}

func TestBoundedListDoesNotRetryAfterAnInterrupt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ready, _ := readinessFiles(t)
	signals := make(chan os.Signal, 1)
	sent := signalWhenReady(ready, "the child never started", signals, syscall.SIGINT)
	code := boundedListWith([]string{"-timeout", "30s", "-attempts", "3", "-grace", "300ms", "--", "sh", "-c", termProofChild}, &stdout, &stderr, signals)
	sent(t)
	if code != 130 {
		t.Fatalf("exit code = %d, want 130 for SIGINT; stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "retrying") {
		t.Fatalf("stderr = %q: an interrupt is an answer about this run, not one to retry", stderr.String())
	}
}

func TestBoundedListPassesThroughWhatATimedOutCommandPrinted(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// Half a package list is the evidence a caller has to work from, and the
	// old code threw it away with the attempt.
	code := boundedListWith([]string{"-timeout", "300ms", "-attempts", "1", "-grace", "200ms", "--",
		"sh", "-c", "echo primeradiant.com/evener/agent; sleep 60"}, &stdout, &stderr, nil)
	if code != 124 {
		t.Fatalf("exit code = %d, want 124 for a timeout; stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "primeradiant.com/evener/agent\n" {
		t.Fatalf("stdout = %q, want what the command printed before the bound", got)
	}
	if got := stderr.String(); !strings.Contains(got, "on 1 attempt") {
		t.Fatalf("stderr = %q, want it to say one attempt in the singular", got)
	}
}

func TestBoundedAttemptTakesASignalThatArrivesDuringCleanup(t *testing.T) {
	var stderr bytes.Buffer
	// The bound passes, the group is sent SIGTERM and ignores it, and the
	// attempt spends the grace waiting to escalate. A signal arriving in that
	// window is watched for by nobody: the select that would have caught it
	// returned when the bound did.
	_, termed := readinessFiles(t)
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	// The child writes that file when the bound's SIGTERM reaches it, which is
	// the moment the cleanup's grace begins: a signal sent then lands in the
	// window nobody is watching.
	sent := signalWhenReady(termed, "the child was never sent SIGTERM", signals, syscall.SIGTERM)
	result := runBoundedAttempt([]string{"sh", "-c", termProofChild}, 200*time.Millisecond, 5*time.Second, &stderr, latch)
	sent(t)
	if result.interrupted != syscall.SIGTERM {
		t.Fatalf("interrupted = %v, want the signal that arrived during cleanup", result.interrupted)
	}
	if result.exitCode != 143 {
		t.Fatalf("exitCode = %d, want 143 rather than the timeout's own status", result.exitCode)
	}
	requireGroupGone(t, result.pgid)
}

func TestBoundedListStopsWhenASignalLandsBetweenAttempts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// The signal lands while attempt 1 is being killed off, after the select
	// that was watching for it has returned. The run must end there rather
	// than announce a retry and start attempt 2.
	_, termed := readinessFiles(t)
	signals := make(chan os.Signal, 1)
	sent := signalWhenReady(termed, "the child was never sent SIGTERM", signals, syscall.SIGTERM)
	code := boundedListWith([]string{"-timeout", "200ms", "-attempts", "3", "-grace", "5s", "--",
		"sh", "-c", termProofChild}, &stdout, &stderr, signals)
	sent(t)
	if code != 143 {
		t.Fatalf("exit code = %d, want 143; stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "retrying") {
		t.Fatalf("stderr = %q: the run was told to stop, and announced a retry instead", stderr.String())
	}
}

func TestSignalLatchHoldsTheFirstSignalItIsGiven(t *testing.T) {
	signals := make(chan os.Signal, 2)
	latch := &signalLatch{ch: signals}
	if got := latch.poll(); got != 0 {
		t.Fatalf("poll on an empty latch = %v, want none", got)
	}
	signals <- syscall.SIGHUP
	signals <- syscall.SIGINT
	if got := latch.poll(); got != syscall.SIGHUP {
		t.Fatalf("poll = %v, want SIGHUP", got)
	}
	// The first one is the answer from then on: the run is already stopping.
	if got := latch.poll(); got != syscall.SIGHUP {
		t.Fatalf("second poll = %v, want SIGHUP again", got)
	}
	if got := latch.receive(syscall.SIGTERM); got != syscall.SIGHUP {
		t.Fatalf("receive after latching = %v, want SIGHUP", got)
	}
}

func TestBoundedAttemptForwardsTheSignalItWasSentNotATermInstead(t *testing.T) {
	var stderr bytes.Buffer
	// The child answers SIGHUP and ignores SIGTERM, so its own output says
	// which one reached it. A helper that always sent SIGTERM would kill it
	// after the grace with nothing on stdout.
	const hupAnswerer = `trap 'echo got-hup; exit 0' HUP; trap "" TERM; : > "${BOUNDED_LIST_READY:-/dev/null}"; while :; do sleep 0.05; done`
	ready, _ := readinessFiles(t)
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	sent := signalWhenReady(ready, "the child never started", signals, syscall.SIGHUP)
	result := runBoundedAttempt([]string{"sh", "-c", hupAnswerer}, 30*time.Second, time.Second, &stderr, latch)
	sent(t)
	if result.interrupted != syscall.SIGHUP {
		t.Fatalf("interrupted = %v, want SIGHUP", result.interrupted)
	}
	if result.exitCode != 129 {
		t.Fatalf("exitCode = %d, want 129 (128+SIGHUP)", result.exitCode)
	}
	if got := string(result.stdout); !strings.Contains(got, "got-hup") {
		t.Fatalf("stdout = %q, want the child's own word that it was sent SIGHUP", got)
	}
	requireGroupGone(t, result.pgid)
}

// TestBoundedListEscapeeHelper is not a test: it is the middle of three
// processes that make a reap impossible to finish. Re-executed from this test
// binary as the attempt's command, it starts a grandchild in a process group
// of its own -- so the attempt's group kill cannot reach it -- hands it this
// process's stdout, and then holds. The grandchild keeps that pipe open after
// the group is killed, which is what a child stuck in an uninterruptible
// kernel wait does to its parent's wait.
func TestBoundedListEscapeeHelper(t *testing.T) {
	if os.Getenv("BOUNDED_LIST_ESCAPEE") == "" {
		t.Skip("helper process entry point; runs only under the give-up tests")
	}
	child := exec.Command(os.Args[0], "-test.run=TestBoundedListSleeperHelper$")
	child.Env = append(os.Environ(), "BOUNDED_LIST_SLEEPER=1")
	// Both of the attempt's pipes, so the grandchild can outlive the attempt
	// holding either one.
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	// Its own group: the attempt kills the group this process leads, and this
	// grandchild has to survive that to hold the pipe.
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatalf("starting the escapee: %v", err)
	}
	// Wait until the grandchild is actually running and holding the pipe, say
	// so, and exit. From this moment the attempt's wait cannot finish however
	// the attempt ends, because the pipe has an owner in another process
	// group -- which is the state a child stuck in the kernel produces.
	awaitFile(t, os.Getenv("BOUNDED_LIST_SLEEPER_READY"), "the grandchild never started")
	if err := os.WriteFile(os.Getenv("BOUNDED_LIST_ESCAPEE_READY"), nil, 0o644); err != nil {
		t.Fatalf("writing the escapee's ready file: %v", err)
	}
	if hold := os.Getenv("BOUNDED_LIST_ESCAPEE_HOLD"); hold != "" {
		waited, err := time.ParseDuration(hold)
		if err != nil {
			t.Fatalf("BOUNDED_LIST_ESCAPEE_HOLD=%q: %v", hold, err)
		}
		time.Sleep(waited)
	}
	if code := os.Getenv("BOUNDED_LIST_ESCAPEE_EXIT"); code != "" {
		status, err := strconv.Atoi(code)
		if err != nil {
			t.Fatalf("BOUNDED_LIST_ESCAPEE_EXIT=%q: %v", code, err)
		}
		// The point of this exit: the command has decided, and its output is
		// still draining through the grandchild's copy of the pipe.
		os.Exit(status)
	}
}

// TestBoundedListSleeperHelper is the grandchild: it holds the inherited pipe
// for long enough to outlast the attempt's reap grace, and exits on its own so
// nothing has to signal it.
func TestBoundedListSleeperHelper(t *testing.T) {
	if os.Getenv("BOUNDED_LIST_SLEEPER") == "" {
		t.Skip("helper process entry point; runs only under the give-up tests")
	}
	if err := os.WriteFile(os.Getenv("BOUNDED_LIST_SLEEPER_READY"), nil, 0o644); err != nil {
		t.Fatalf("writing the grandchild's ready file: %v", err)
	}
	if goAhead := os.Getenv("BOUNDED_LIST_SLEEPER_SHOUT"); goAhead != "" {
		shoutIntoTheKeptPipe(t, goAhead)
		return
	}
	hold := 3 * time.Second
	if value := os.Getenv("BOUNDED_LIST_SLEEPER_HOLD"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("BOUNDED_LIST_SLEEPER_HOLD=%q: %v", value, err)
		}
		hold = parsed
	}
	time.Sleep(hold)
}

// survivorShout is what the grandchild writes into the pipe it still holds
// after the attempt that opened it is over.
const survivorShout = "a survivor wrote this"

// shoutIntoTheKeptPipe waits to be told the attempt is over, writes into the
// stderr it inherited from it, and records which way that went. The write goes
// through a duplicate of the descriptor because the runtime turns a broken-pipe
// write on fd 2 itself into a fatal SIGPIPE, and this helper has to outlive the
// write to report it.
func shoutIntoTheKeptPipe(t *testing.T, goAhead string) {
	t.Helper()
	awaitFile(t, goAhead, "the go-ahead for the survivor's write never came")
	fd, err := syscall.Dup(int(os.Stderr.Fd()))
	if err != nil {
		t.Fatalf("duplicating the inherited stderr: %v", err)
	}
	kept := os.NewFile(uintptr(fd), "the attempt's stderr")
	outcome := "wrote"
	if _, err := kept.WriteString(survivorShout + "\n"); err != nil {
		outcome = "refused: " + err.Error()
	}
	if err := os.WriteFile(os.Getenv("BOUNDED_LIST_SLEEPER_RECORD"), []byte(outcome), 0o644); err != nil {
		t.Fatalf("writing the survivor's record: %v", err)
	}
}

// escapeeCommand is the attempt's command for the give-up tests: this test
// binary, re-executed, with nothing ambient involved. It returns the path the
// middle process writes once its grandchild is running, for the cases that
// have to act at that moment rather than after a guessed interval.
func escapeeCommand(t *testing.T) ([]string, string) {
	t.Helper()
	dir := t.TempDir()
	escapeeReady := filepath.Join(dir, "escapee-ready")
	t.Setenv("BOUNDED_LIST_ESCAPEE", "1")
	t.Setenv("BOUNDED_LIST_ESCAPEE_READY", escapeeReady)
	t.Setenv("BOUNDED_LIST_SLEEPER_READY", filepath.Join(dir, "sleeper-ready"))
	return []string{os.Args[0], "-test.run=TestBoundedListEscapeeHelper$"}, escapeeReady
}

func TestBoundedAttemptDoesNotWedgeOnAnEscapedPipeHolder(t *testing.T) {
	var stderr bytes.Buffer
	argv, escapeeReady := escapeeCommand(t)
	// The middle process exits as soon as its grandchild holds the pipe, and
	// the grandchild is in a group of its own, so the attempt's stop cannot
	// reach it. Nothing here may wait for it: the wait delay closes the pipes
	// and the attempt reports what it has. The bound is far away on purpose --
	// what ends this attempt is the delay, not the clock, so the test does not
	// depend on how long a fork takes under the race detector.
	start := time.Now()
	result := runBoundedAttempt(argv, 30*time.Second, 300*time.Millisecond, &stderr, nil)
	awaitFile(t, escapeeReady, "the escapee never reported its grandchild")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the attempt took %s, so it waited for the grandchild's hold rather than giving up on the pipe", elapsed)
	}
	// The command exited 0, and its output did not all arrive: reporting that
	// as success would hand a caller a list it cannot tell from a whole one.
	if result.exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1; stderr = %q", result.exitCode, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "could not be read in full") {
		t.Fatalf("stderr = %q, want it to say the output was lost", got)
	}
}

func TestBoundedAttemptStopsARunningCommandWhoseOutputIsHeldElsewhere(t *testing.T) {
	var stderr bytes.Buffer
	argv, escapeeReady := escapeeCommand(t)
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	// Once the grandchild is running: signalling before that would stop a
	// group with nothing to leave behind.
	// The middle process holds after reporting, so the signal always finds it
	// running: an interrupt that arrives after the command has exited is a
	// different case, and it has its own test.
	t.Setenv("BOUNDED_LIST_ESCAPEE_HOLD", "30s")
	sent := signalWhenReady(escapeeReady, "the escapee never reported its grandchild", signals, syscall.SIGTERM)
	result := runBoundedAttempt(argv, 30*time.Second, 300*time.Millisecond, &stderr, latch)
	sent(t)
	if result.exitCode != 143 {
		t.Fatalf("exitCode = %d, want 143: the run was interrupted while the command was running; stderr = %q", result.exitCode, stderr.String())
	}
	if result.interrupted != syscall.SIGTERM {
		t.Fatalf("interrupted = %v, want the signal latched", result.interrupted)
	}
}

func TestBoundedListDoesNotRetryWhenTheOutputCouldNotBeRead(t *testing.T) {
	var stdout, stderr bytes.Buffer
	argv, escapeeReady := escapeeCommand(t)
	// Same shape: the wait delay ends the attempt, not the bound.
	args := append([]string{"-timeout", "30s", "-attempts", "3", "-grace", "300ms", "--"}, argv...)
	code := boundedListWith(args, &stdout, &stderr, nil)
	awaitFile(t, escapeeReady, "the escapee never reported its grandchild")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %q", code, stderr.String())
	}
	// One attempt: the command answered, and what failed was reading its
	// output, which another attempt would not change.
	if got := strings.Count(stderr.String(), "could not be read in full"); got != 1 {
		t.Fatalf("the attempt was made %d times, want one; stderr = %q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "retrying") {
		t.Fatalf("stderr = %q: nothing here is worth retrying", stderr.String())
	}
}

func TestFinishedFirstPrefersTheCommandsOwnAnswer(t *testing.T) {
	// The bound expiring and the command finishing can be ready together, and
	// select picks at random; the command's answer wins. The ordering the wait
	// goroutine uses is reproduced here: publish, then close.
	waited := &waitResult{}
	answer := errors.New("the command's own answer")
	waited.publish(answer)
	reaped := make(chan struct{})
	close(reaped)
	finished, err := finishedFirst(reaped, waited)
	if !finished {
		t.Fatal("finishedFirst = false for a command that had already finished")
	}
	if !errors.Is(err, answer) {
		t.Fatalf("finishedFirst err = %v, want the published answer", err)
	}
	// And the other order is what it has to refuse: still running, so the
	// bound is the answer.
	if finished, _ := finishedFirst(make(chan struct{}), &waitResult{}); finished {
		t.Fatal("finishedFirst = true for a command that has not finished")
	}
}

// shortWriter accepts the first byte and no more, which is what a full disk or
// a closed pipe looks like to a caller handing over a package list.
type shortWriter struct{ written int }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.written++
	return 1, nil
}

func TestCompleteWithKeepsTheCommandsAnswerAndTheSignal(t *testing.T) {
	// The two orderings, injected: a signal waiting when the command finished,
	// and no signal at all. A command that finished is not an interrupted one,
	// so its status stands -- and the latched signal still stops the run,
	// because the runner starts no further attempt once it is set.
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	interrupted := &attemptResult{}
	interrupted.completeWith(3, nil, &signalLatch{ch: signals})
	if interrupted.exitCode != 3 {
		t.Fatalf("exitCode = %d, want 3: what the command decided", interrupted.exitCode)
	}
	if interrupted.interrupted != syscall.SIGTERM {
		t.Fatalf("interrupted = %v, want the signal latched so the run stops", interrupted.interrupted)
	}

	quiet := &attemptResult{}
	quiet.completeWith(3, nil, &signalLatch{ch: make(chan os.Signal)})
	if quiet.exitCode != 3 || quiet.interrupted != 0 {
		t.Fatalf("completeWith with no signal = %+v, want the command's answer and no interrupt", quiet)
	}
}

// commandWithASurvivor is a command that leaves a child in its group that
// ignores SIGTERM and records having been sent one, holds for hold seconds
// once that child is ready, and then exits with the status given. The leader
// waits for the child to say its trap is installed before doing any of that,
// because a SIGTERM that arrives first finds the default disposition and the
// child dies without a word -- the sweep would then have nothing to sweep and
// the test would prove nothing.
//
// The scripts are files rather than -c strings so the quoting is the shell's
// own rather than three layers of escaping.
func commandWithASurvivor(t *testing.T, exit int, hold string) []string {
	t.Helper()
	dir := t.TempDir()
	survivor := filepath.Join(dir, "survivor.sh")
	if err := os.WriteFile(survivor, []byte(
		"trap ': > \"${BOUNDED_LIST_TERMED:-/dev/null}\"' TERM\n"+
			": > \"${BOUNDED_LIST_READY:-/dev/null}\"\n"+
			"while :; do sleep 0.05; done\n"), 0o644); err != nil {
		t.Fatalf("writing the survivor script: %v", err)
	}
	leader := filepath.Join(dir, "leader.sh")
	if err := os.WriteFile(leader, fmt.Appendf(nil,
		"sh %q >/dev/null 2>&1 &\n"+
			"while [ ! -f \"${BOUNDED_LIST_READY:-/dev/null}\" ]; do sleep 0.01; done\n"+
			"sleep %s\n"+
			"exit %d\n", survivor, hold, exit), 0o644); err != nil {
		t.Fatalf("writing the leader script: %v", err)
	}
	return []string{"sh", leader}
}

func TestBoundedListDoesNotRetryAFailedCommandAfterAnInterrupt(t *testing.T) {
	// The command fails on its own and leaves a child that ignores SIGTERM, so
	// the attempt spends its grace sweeping the group -- and the interrupt
	// lands in that window, which nothing is watching. The attempt has to come
	// back carrying both: what the command decided, and the signal, which is
	// what stops the runner from starting another attempt.
	var stderr bytes.Buffer
	_, termed := readinessFiles(t)
	signals := make(chan os.Signal, 1)
	sent := signalWhenReady(termed, "the child was never sent SIGTERM", signals, syscall.SIGTERM)
	failThenLeaveAChild := commandWithASurvivor(t, 3, "0")
	latch := &signalLatch{ch: signals}
	result := runBoundedAttempt(failThenLeaveAChild, 30*time.Second, 5*time.Second, &stderr, latch)
	sent(t)
	if result.exitCode != 3 {
		t.Fatalf("exitCode = %d, want 3: the command's own answer; stderr = %q", result.exitCode, stderr.String())
	}
	if result.interrupted != syscall.SIGTERM {
		t.Fatalf("interrupted = %v, want the signal that arrived during cleanup, which is what stops the runner", result.interrupted)
	}

	// And through the runner: three attempts allowed, one taken, the command's
	// status returned.
	var stdout, runnerErr bytes.Buffer
	_, termedAgain := readinessFiles(t)
	runnerSignals := make(chan os.Signal, 1)
	sentAgain := signalWhenReady(termedAgain, "the child was never sent SIGTERM", runnerSignals, syscall.SIGTERM)
	code := boundedListWith(append([]string{"-timeout", "30s", "-attempts", "3", "-grace", "5s", "--"},
		commandWithASurvivor(t, 3, "0")...), &stdout, &runnerErr, runnerSignals)
	sentAgain(t)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr = %q", code, runnerErr.String())
	}
	if strings.Contains(runnerErr.String(), "retrying") {
		t.Fatalf("stderr = %q: the run was told to stop, and announced a retry instead", runnerErr.String())
	}
}

func TestBoundedListFailsWhenTheListCannotBeHandedOver(t *testing.T) {
	var stderr bytes.Buffer
	stdout := &shortWriter{}
	// A caller that reads a truncated list tests the packages it received and
	// reports a pass for the rest.
	code := boundedListWith([]string{"-timeout", "5s", "-attempts", "1", "--",
		"sh", "-c", "echo primeradiant.com/evener/agent; echo primeradiant.com/evener/llm"}, stdout, &stderr, nil)
	if code == 0 {
		t.Fatalf("exit code = 0 after a short write; stderr = %q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "the command's output: wrote 1 of") {
		t.Fatalf("stderr = %q, want it to name the bytes that did not land", got)
	}
}

func TestReapOrGiveUpSaysWhichItWas(t *testing.T) {
	var stderr bytes.Buffer
	reaped := make(chan struct{})
	close(reaped)
	if !reapOrGiveUp(reaped, time.Second, "sh", true, &stderr) {
		t.Fatal("reapOrGiveUp = false for a child that was reaped")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing said about a reap that happened", stderr.String())
	}
	never := make(chan struct{})
	if reapOrGiveUp(never, 20*time.Millisecond, "sh", true, &stderr) {
		t.Fatal("reapOrGiveUp = true for a child that never reaped")
	}
	if got := stderr.String(); !strings.Contains(got, "did not exit after SIGKILL") {
		t.Fatalf("stderr = %q, want the diagnostic naming the failed reap", got)
	}
	// A child nothing was sent to did not survive a SIGKILL; it is still
	// running, and saying otherwise sends the reader after a kernel problem
	// that is not there.
	var unsignalled bytes.Buffer
	if reapOrGiveUp(never, 20*time.Millisecond, "sh", false, &unsignalled) {
		t.Fatal("reapOrGiveUp = true for a child that never reaped")
	}
	if got := unsignalled.String(); strings.Contains(got, "SIGKILL") ||
		!strings.Contains(got, "nothing this helper sent reached it") {
		t.Fatalf("stderr = %q, want it to say no signal reached the child", got)
	}
}

func TestBoundedAttemptSaysWhyACommandNeverStarted(t *testing.T) {
	var stderr bytes.Buffer
	// The gate reads this log and nothing else, so a start that failed with an
	// empty log is a module that failed for no stated reason.
	result := runBoundedAttempt([]string{filepath.Join(t.TempDir(), "not-a-command")}, time.Second, time.Second, &stderr, nil)
	if result.err == nil {
		t.Fatal("err = nil for a command that cannot be started")
	}
	if result.exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1", result.exitCode)
	}
	if got := stderr.String(); !strings.Contains(got, "bounded-list:") || !strings.Contains(got, "not-a-command") {
		t.Fatalf("stderr = %q, want the failure and the command in it", got)
	}
}

func TestStopSurvivorsGivesUpOnAGroupThatOutlivesSigkill(t *testing.T) {
	var stderr bytes.Buffer
	// A group that survives SIGKILL cannot be produced on demand -- that is
	// the point of SIGKILL -- so the probe is injected. Everything else is the
	// real path: TERM, the grace, KILL, the grace again.
	killed := 0
	stubborn := groupStopper{
		exists:    func(int) bool { return true },
		terminate: func(int) {},
		kill:      func(int) { killed++ },
	}
	if stopSurvivors(stubborn, "go", 4242, 20*time.Millisecond, &stderr) {
		t.Fatal("stopSurvivors = true for a group that never went away")
	}
	if killed != 1 {
		t.Fatalf("SIGKILL sent %d times, want exactly one escalation", killed)
	}
	if got := stderr.String(); !strings.Contains(got, "process group 4242 is still there after SIGKILL") {
		t.Fatalf("stderr = %q, want the diagnostic naming the group", got)
	}
}

func TestStopSurvivorsStopsWhenTheGroupGoes(t *testing.T) {
	var stderr bytes.Buffer
	// Gone after the TERM: no escalation, nothing said about SIGKILL.
	alive := true
	polite := groupStopper{
		exists:    func(int) bool { return alive },
		terminate: func(int) { alive = false },
		kill:      func(int) { t.Error("SIGKILL sent to a group that answered SIGTERM") },
	}
	if !stopSurvivors(polite, "go", 4242, time.Second, &stderr) {
		t.Fatal("stopSurvivors = false for a group that went away")
	}
	if got := stderr.String(); !strings.Contains(got, "left processes running in its group") {
		t.Fatalf("stderr = %q, want the survivors named", got)
	}
	if strings.Contains(stderr.String(), "after SIGKILL") {
		t.Fatalf("stderr = %q, want nothing said about an escalation that did not happen", stderr.String())
	}
	// And an empty group is silent altogether.
	var quiet bytes.Buffer
	empty := groupStopper{exists: func(int) bool { return false }}
	if !stopSurvivors(empty, "go", 4242, time.Second, &quiet) || quiet.Len() != 0 {
		t.Fatalf("an empty group said %q, want nothing", quiet.String())
	}
}

// failingOnceWriter fails the first write and records the rest, which is what
// a pipe that breaks mid-copy looks like to the attempt: the command's own
// output is lost, and the diagnostic about it still has somewhere to go.
type failingOnceWriter struct {
	failed bool
	kept   bytes.Buffer
}

func (w *failingOnceWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, errors.New("the pipe broke")
	}
	return w.kept.Write(p)
}

func TestBoundedAttemptFailsWhenTheCommandsOutputCouldNotBeRead(t *testing.T) {
	stderr := &failingOnceWriter{}
	// The command exits 0 and writes to stderr, whose copy fails: the wait
	// then returns an error that is not the command's status. Reporting
	// success here would hand a caller a list it cannot tell from a complete
	// one.
	result := runBoundedAttempt([]string{"sh", "-c", "echo something >&2; echo primeradiant.com/evener"}, 30*time.Second, time.Second, stderr, nil)
	if result.exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1: the command exited 0, but its output did not arrive", result.exitCode)
	}
	if got := stderr.kept.String(); !strings.Contains(got, "could not be read in full") {
		t.Fatalf("stderr = %q, want the attempt to say the output was lost", got)
	}
}

func TestBoundedAttemptKeepsAnExitErrorsStatus(t *testing.T) {
	var stderr bytes.Buffer
	// The other half of the same branch: a command that exits non-zero returns
	// an *exec.ExitError, and that status is the answer, not 1.
	result := runBoundedAttempt([]string{"sh", "-c", "exit 7"}, 30*time.Second, time.Second, &stderr, nil)
	if result.exitCode != 7 {
		t.Fatalf("exitCode = %d, want 7: what the command decided", result.exitCode)
	}
	if strings.Contains(stderr.String(), "could not be read in full") {
		t.Fatalf("stderr = %q, want nothing said about an output failure that did not happen", stderr.String())
	}
}

func TestBoundedAttemptIsDecidedAtTheCommandsExitNotAtItsLastByte(t *testing.T) {
	// The attempt owns the copying, so the wait ends when the process is
	// reaped rather than when its output stops arriving: a command whose last
	// bytes are still in flight -- here, held by a grandchild in another
	// process group -- has already decided, and the bound never enters into
	// it. The drain that follows is bounded on its own.
	for _, tc := range []struct {
		name string
		exit string
		want int
	}{
		{name: "a clean exit", exit: "0", want: 0},
		{name: "a failure of its own", exit: "3", want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			argv, _ := escapeeCommand(t)
			t.Setenv("BOUNDED_LIST_ESCAPEE_EXIT", tc.exit)
			// The grandchild holds the pipe for three seconds; the grace
			// outlasts it, so the drain finishes and the output is whole.
			start := time.Now()
			result := runBoundedAttempt(argv, 30*time.Second, 10*time.Second, &stderr, nil)
			if result.timedOut {
				t.Fatalf("timedOut = true for a command that exited on its own; stderr = %q", stderr.String())
			}
			if result.exitCode != tc.want {
				t.Fatalf("exitCode = %d, want %d; stderr = %q", result.exitCode, tc.want, stderr.String())
			}
			if result.stuck {
				t.Fatalf("stuck = true for a command that finished on its own; stderr = %q", stderr.String())
			}
			if elapsed := time.Since(start); elapsed > 20*time.Second {
				t.Fatalf("the attempt took %s: the drain is bounded by the grace, not by the holder", elapsed)
			}
		})
	}
}

func TestBoundedListDoesNotRetryACommandThatDecided(t *testing.T) {
	var stdout, stderr bytes.Buffer
	argv, _ := escapeeCommand(t)
	t.Setenv("BOUNDED_LIST_ESCAPEE_EXIT", "3")
	// Three attempts are allowed; the command decided, so one is taken.
	args := append([]string{"-timeout", "30s", "-attempts", "3", "-grace", "10s", "--"}, argv...)
	code := boundedListWith(args, &stdout, &stderr, nil)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3: what the command decided; stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "retrying") {
		t.Fatalf("stderr = %q: a command that decided is not retried", stderr.String())
	}
}

func TestBoundedAttemptTakesTheExitThatRacedTheBound(t *testing.T) {
	// The timer and the exit can be ready at the same moment, and select picks
	// between ready cases at random, so the timeout arm asks first whether the
	// command has already finished. The ordering is injected, because it
	// cannot be arranged from outside: a command that sleeps exactly as long
	// as the bound loses the race on every machine I can run this on.
	waited := &waitResult{}
	answer := errors.New("the command's own answer")
	waited.publish(answer)
	reaped := make(chan struct{})
	close(reaped)
	finished, err := finishedFirst(reaped, waited)
	if !finished {
		t.Fatal("finishedFirst = false for a command that had already exited: the timeout arm would call it a timeout and retry it")
	}
	if !errors.Is(err, answer) {
		t.Fatalf("finishedFirst err = %v, want the answer the command published", err)
	}
	// And the other ordering, which is the one that must still be a timeout.
	if finished, _ := finishedFirst(make(chan struct{}), &waitResult{}); finished {
		t.Fatal("finishedFirst = true for a command that is still running")
	}
}

// withStopperStub replaces the process-group half of an attempt for the length
// of one test, leaving the rest of the attempt real.
func withStopperStub(t *testing.T, stop func(int, syscall.Signal, <-chan struct{}, time.Duration) (bool, error)) {
	t.Helper()
	original := realGroupStopper
	t.Cleanup(func() { realGroupStopper = original })
	stubbed := original
	stubbed.stop = stop
	realGroupStopper = stubbed
}

// reapedBeforeTheStop stands in for a stop that found the child already gone:
// it waits for the reap the real one would have seen, and reports that it
// stopped nothing. The real stop answers this way twice over -- the reap
// beating the check, and the group answering ESRCH when the signal goes out --
// and the attempt cannot tell those apart, which is the point of the answer.
func reapedBeforeTheStop(_ int, _ syscall.Signal, reaped <-chan struct{}, _ time.Duration) (bool, error) {
	<-reaped
	return false, nil
}

func TestBoundedListKeepsTheStatusOfACommandThatExitedAsTheBoundLanded(t *testing.T) {
	// The reap can land after the timeout arm has asked whether the command
	// finished and before the stop reaches the group: microseconds wide, and
	// not arrangeable from outside, so the ordering is injected. Nothing was
	// signalled, so what the command exited with is its own answer -- not a
	// timeout, and not something to retry.
	withStopperStub(t, reapedBeforeTheStop)
	var stdout, stderr bytes.Buffer
	// The command outlives the bound and then exits by itself; with the stop
	// sending nothing, no signal of this helper's could have ended it.
	args := []string{"-timeout", "100ms", "-attempts", "3", "-grace", "5s", "--", "sh", "-c", "sleep 0.3; exit 7"}
	code := boundedListWith(args, &stdout, &stderr, nil)
	if code != 7 {
		t.Fatalf("exit code = %d, want 7: the command's own status; stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "retrying") {
		t.Fatalf("stderr = %q: a command that exited is not retried", stderr.String())
	}
	if strings.Contains(stderr.String(), "timed out") {
		t.Fatalf("stderr = %q: the command exited before anything was sent to it", stderr.String())
	}
}

func TestBoundedAttemptKeepsTheStatusOfACommandThatExitedAsTheInterruptLanded(t *testing.T) {
	// The same window on the interrupt arm: 128+signal names a death this
	// helper caused, and it caused none here. The interrupt still ends the
	// run -- the runner starts no further attempt -- it just does not decide
	// the status.
	withStopperStub(t, reapedBeforeTheStop)
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	var stderr bytes.Buffer
	result := runBoundedAttempt([]string{"sh", "-c", "sleep 0.3; exit 7"},
		30*time.Second, 5*time.Second, &stderr, &signalLatch{ch: signals})
	if result.exitCode != 7 {
		t.Fatalf("exitCode = %d, want 7: the command's own status; stderr = %q", result.exitCode, stderr.String())
	}
	if result.interrupted != syscall.SIGTERM {
		t.Fatalf("interrupted = %v, want the signal latched so the run stops", result.interrupted)
	}
	if result.timedOut || result.stuck {
		t.Fatalf("timedOut = %v, stuck = %v for a command that exited on its own", result.timedOut, result.stuck)
	}
}

func TestASurvivorCannotWriteIntoTheNextAttempt(t *testing.T) {
	// The copiers read the attempt's pipes, and a process that escaped the
	// attempt's group still holds the write ends. If the attempt leaves its
	// read ends open, that copier outlives it -- writing, whenever the
	// survivor does, into the stderr the next attempt is reporting through.
	argv, escapeeReady := escapeeCommand(t)
	dir := t.TempDir()
	goAhead := filepath.Join(dir, "go-ahead")
	record := filepath.Join(dir, "survivor-record")
	t.Setenv("BOUNDED_LIST_SLEEPER_SHOUT", goAhead)
	t.Setenv("BOUNDED_LIST_SLEEPER_RECORD", record)
	// One stderr for both attempts, which is what the runner has.
	var stderr syncBuffer
	first := runBoundedAttempt(argv, 30*time.Second, 200*time.Millisecond, &stderr, nil)
	awaitFile(t, escapeeReady, "the escapee never reported its grandchild")
	if first.exitCode != 1 {
		t.Fatalf("the first attempt's exitCode = %d, want 1: the survivor held its output; stderr = %q",
			first.exitCode, stderr.Bytes())
	}
	// The attempt is over and the survivor still holds the write ends.
	if err := os.WriteFile(goAhead, nil, 0o644); err != nil {
		t.Fatalf("writing the go-ahead: %v", err)
	}
	if second := runBoundedAttempt([]string{"sh", "-c", "sleep 1"},
		30*time.Second, 200*time.Millisecond, &stderr, nil); second.exitCode != 0 {
		t.Fatalf("the second attempt's exitCode = %d, want 0; stderr = %q", second.exitCode, stderr.Bytes())
	}
	awaitFile(t, record, "the survivor never said how its write went")
	outcome, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("reading the survivor's record: %v", err)
	}
	if !strings.HasPrefix(string(outcome), "refused:") {
		t.Fatalf("the survivor's write %s, want it refused: the attempt closes its read ends when it ends", outcome)
	}
	if got := string(stderr.Bytes()); strings.Contains(got, survivorShout) {
		t.Fatalf("stderr = %q: a survivor's bytes landed in a later attempt's diagnostics", got)
	}
}

func TestAwaitDrainDoesNotRestartItsGraceForEverySignal(t *testing.T) {
	// Output that never arrives, and signals arriving faster than the grace.
	// One deadline for the whole wait is what ends it: a grace started afresh
	// each time round the loop would be outrun by the signals, and an
	// operator sending more of them would extend the wait they are trying to
	// cut short.
	signals := make(chan os.Signal, 1)
	feeding := make(chan struct{})
	defer close(feeding)
	go func() {
		for {
			select {
			case <-feeding:
				return
			case signals <- syscall.SIGINT:
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- awaitDrain(make(chan struct{}), 200*time.Millisecond, &signalLatch{ch: signals})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("awaitDrain returned nil for output that never arrived")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("awaitDrain never returned against a 200ms grace: every signal started it over")
	}
}

// signallingWriter delivers a signal while the run is handing over the
// command's output: the last window in which one can arrive, and the one the
// select that was watching for signals has already left.
type signallingWriter struct {
	buf     bytes.Buffer
	signals chan<- os.Signal
	sig     syscall.Signal
	sent    bool
}

func (w *signallingWriter) Write(p []byte) (int, error) {
	if !w.sent {
		w.sent = true
		w.signals <- w.sig
	}
	return w.buf.Write(p)
}

func TestBoundedListTakesASignalThatLandsWhileItHandsOverTheOutput(t *testing.T) {
	// The command succeeded, and the operator interrupted the run while its
	// output was being written. Reporting the command's own success there
	// tells the caller the work it asked to stop was finished for it.
	signals := make(chan os.Signal, 1)
	stdout := &signallingWriter{signals: signals, sig: syscall.SIGINT}
	var stderr bytes.Buffer
	args := []string{"-timeout", "30s", "-attempts", "1", "-grace", "5s", "--", "sh", "-c", "echo listed"}
	code := boundedListWith(args, stdout, &stderr, signals)
	if want := 128 + int(syscall.SIGINT); code != want {
		t.Fatalf("exit code = %d, want %d: the run was interrupted while its output was handed over; stderr = %q", code, want, stderr.String())
	}
	// The output is still handed over in full: the signal changes the answer
	// about the run, not what the command produced.
	if got := stdout.buf.String(); got != "listed\n" {
		t.Fatalf("stdout = %q, want the command's output", got)
	}
}

func TestBoundedAttemptSaysWhenTheGroupRefusedTheStop(t *testing.T) {
	// EPERM on a group this process does not own all of is the one kill error
	// worth a line: the stop went out, and the group may still be there. It
	// cannot be produced from a test without signalling a group nobody here
	// owns, so it is injected.
	// What the real stop answers when every signal it made was refused: it
	// delivered nothing, and it says what the kernel refused it with.
	withStopperStub(t, func(_ int, _ syscall.Signal, _ <-chan struct{}, _ time.Duration) (bool, error) {
		return false, syscall.EPERM
	})
	var stderr bytes.Buffer
	// The bound fires while the command runs, and the command then exits on
	// its own -- the stub sends nothing, so nothing else could have ended it.
	result := runBoundedAttempt([]string{"sh", "-c", "sleep 0.3"}, 100*time.Millisecond, 5*time.Second, &stderr, nil)
	if got := stderr.String(); !strings.Contains(got, "stopping sh's process group") || !strings.Contains(got, syscall.EPERM.Error()) {
		t.Fatalf("stderr = %q, want the refused stop named with what the kernel said", got)
	}
	// Not a timeout: the stop reached nothing, and the command exited with a
	// status of its own while it was being refused.
	if result.timedOut || result.exitCode != 0 {
		t.Fatalf("timedOut = %v and exitCode = %d, want the command's own answer: every signal this stop made was refused; stderr = %q",
			result.timedOut, result.exitCode, stderr.String())
	}
}

func TestSignalLatchSettleTakesASignalThatLandedAfterTheLastLook(t *testing.T) {
	// The window this closes: the run takes its last look, a signal lands, and
	// the run returns -- at which point signal.Stop discards it and the run
	// reports the command's own status for work the operator stopped. settle
	// stops delivery before it looks, so there is no later signal to lose.
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	if sig := latch.poll(); sig != 0 {
		t.Fatalf("poll = %v before anything was sent", sig)
	}
	signals <- syscall.SIGHUP
	if sig := latch.settle(); sig != syscall.SIGHUP {
		t.Fatalf("settle = %v, want the signal that landed after the last look", sig)
	}
	// It keeps answering with it, and stopping delivery twice is allowed --
	// which is what leaves the caller's own deferred signal.Stop a no-op.
	if sig := latch.settle(); sig != syscall.SIGHUP {
		t.Fatalf("second settle = %v, want the latched signal", sig)
	}
	if sig := (*signalLatch)(nil).settle(); sig != 0 {
		t.Fatalf("settle on no latch = %v, want none", sig)
	}
}

func TestEndCopyingJoinsTheCopiersAndReleasesOneItCannotJoin(t *testing.T) {
	// Joined: the copying finished inside the grace, so the writer is left as
	// it was and the attempt's own last words go through it in order.
	var joined bytes.Buffer
	attached := &serialWriter{w: &joined}
	finished := make(chan struct{})
	close(finished)
	if err := endCopying(&outputPipes{}, finished, attached, 10*time.Second, nil); err != nil {
		t.Fatalf("endCopying with the copying already done: %v", err)
	}
	if attached.released.Load() {
		t.Fatal("the writer was released although the copiers were joined")
	}
	if _, err := attached.Write([]byte("still attached\n")); err != nil {
		t.Fatalf("writing through a joined attempt's writer: %v", err)
	}
	if joined.String() != "still attached\n" {
		t.Fatalf("stderr = %q, want the attempt's own words", joined.String())
	}

	// Stuck: a copier inside a write that has not returned cannot be joined,
	// and it is holding the lock. After the grace the writer is released, so
	// the diagnostics that say all this go round that lock instead of waiting
	// for it -- which is the wait that never ends.
	var stalled bytes.Buffer
	detached := &serialWriter{w: &stalled}
	grace := 200 * time.Millisecond
	start := time.Now()
	err := endCopying(&outputPipes{}, make(chan struct{}), detached, grace, nil)
	if err == nil {
		t.Fatal("endCopying reported a whole drain for output that never arrived")
	}
	if elapsed := time.Since(start); elapsed < grace {
		t.Fatalf("endCopying returned in %s, without waiting out the drain", elapsed)
	}
	if !detached.released.Load() {
		t.Fatal("the writer was not released although the copiers could not be joined")
	}
	detached.mu.Lock() // stand in for the wedged copier that holds it
	done := make(chan error, 1)
	go func() {
		_, err := detached.Write([]byte("the cleanup still has something to say\n"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writing through a released writer: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a released writer waited for the lock a wedged copier holds")
	}
	detached.mu.Unlock()
	if stalled.String() != "the cleanup still has something to say\n" {
		t.Fatalf("stderr = %q, want the cleanup's words to have reached the caller", stalled.String())
	}
}

func TestBoundedListCallsItATimeoutWhenTheStopWasDelivered(t *testing.T) {
	// The leader exits 0 with a descendant still in its group, so the stop's
	// signal is delivered on that descendant's account. Nothing in a wait
	// status says whether the leader ever received it, and the reading that
	// cannot go wrong is the one that does not hand a caller a partial list as
	// a whole one: a delivered stop is a timeout. The sweep still happens.
	withStopperStub(t, func(_ int, _ syscall.Signal, reaped <-chan struct{}, _ time.Duration) (bool, error) {
		<-reaped
		return true, nil
	})
	var stderr bytes.Buffer
	_, _ = readinessFiles(t)
	argv := commandWithASurvivor(t, 0, "0.3")
	result := runBoundedAttempt(argv, 100*time.Millisecond, 10*time.Second, &stderr, nil)
	if !result.timedOut {
		t.Fatalf("timedOut = false although the stop was delivered; stderr = %q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "left processes running in its group") {
		t.Fatalf("stderr = %q, want the survivor swept", got)
	}
	requireGroupGone(t, result.pgid)
}

// gatedWriter holds the one write whose text it is given and takes every
// other. It stands in for a stderr that has stopped taking writes: the host's
// doing, not the command's.
type gatedWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	hold string
	gate chan struct{}
	held chan struct{}
	once sync.Once
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	if w.hold != "" && strings.Contains(string(p), w.hold) {
		w.once.Do(func() { close(w.held) })
		<-w.gate
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *gatedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestBoundedAttemptStillReportsWhenTheStderrItWritesToHasStopped(t *testing.T) {
	// The copier carries the command's stderr into the caller's, and the
	// caller's does not return. It is holding the lock every diagnostic this
	// attempt has left to write, so an attempt that waits for that lock writes
	// nothing, returns nothing, and never releases anything -- the hang the
	// bound exists to replace, produced by the helper itself.
	sink := &gatedWriter{hold: "held-by-the-host", gate: make(chan struct{}), held: make(chan struct{})}
	defer close(sink.gate)
	done := make(chan attemptResult, 1)
	go func() {
		done <- runBoundedAttempt([]string{"sh", "-c", "echo held-by-the-host >&2; exit 0"},
			30*time.Second, 200*time.Millisecond, sink, nil)
	}()
	select {
	case <-sink.held:
	case <-time.After(30 * time.Second):
		t.Fatal("the command's stderr never reached the sink")
	}
	var result attemptResult
	select {
	case result = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the attempt never returned: it waited for a copier wedged inside the caller's stderr")
	}
	// The command exited 0 and its output did not all arrive, which is the
	// one thing the caller must not be told is a whole list.
	if result.exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1: the output could not be read in full; stderr = %q", result.exitCode, sink.String())
	}
	if got := sink.String(); !strings.Contains(got, "could not be read in full") {
		t.Fatalf("stderr = %q, want the attempt's cleanup diagnostics to have landed", got)
	}
}

func TestBoundedAttemptReadsTheStatusOnlyAfterTheWaitHasIt(t *testing.T) {
	// A stop that sent nothing, with the command still running: the answer it
	// points the attempt at lives in cmd.ProcessState, and Wait writes that
	// until it closes `reaped`. Reading it any earlier races the wait, and for
	// a command that has not exited there is no status there to read.
	withStopperStub(t, func(_ int, _ syscall.Signal, _ <-chan struct{}, _ time.Duration) (bool, error) {
		return false, nil
	})
	var stderr bytes.Buffer
	result := runBoundedAttempt([]string{"sh", "-c", "sleep 0.3; exit 7"},
		100*time.Millisecond, 5*time.Second, &stderr, nil)
	if result.exitCode != 7 {
		t.Fatalf("exitCode = %d, want 7: the command's own status, read once the wait had it; stderr = %q",
			result.exitCode, stderr.String())
	}
	if result.timedOut || result.stuck {
		t.Fatalf("timedOut = %v, stuck = %v for a command that exited on its own", result.timedOut, result.stuck)
	}
}

// stalledWriter takes the write and does not return from it, which is what the
// far end of the gate's pipe does once nobody is reading it.
type stalledWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *stalledWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestBoundedListDoesNotWaitForeverToHandTheAnswerOver(t *testing.T) {
	// The command finished and its list is ready; the far end of the pipe it
	// is written to is not reading. Every other wait in this helper is
	// bounded, and the last one was not: the run would end by waiting for a
	// buffer that never drains, which is the hang the bound exists to replace.
	stdout := &stalledWriter{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(stdout.release)
	var stderr bytes.Buffer
	start := time.Now()
	code := boundedListWith([]string{"-timeout", "30s", "-attempts", "1", "-grace", "300ms", "--",
		"sh", "-c", "echo listed"}, stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1: the answer could not be handed over; stderr = %q", code, stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("the run took %s: the hand-over is bounded by the grace", elapsed)
	}
	if got := stderr.String(); !strings.Contains(got, "not reading it any more") {
		t.Fatalf("stderr = %q, want it to name the hand-over that did not finish", got)
	}
	select {
	case <-stdout.entered:
	default:
		t.Fatal("the answer was never written at all")
	}
}

func TestBoundedAttemptSaysWhenTheCommandWroteMoreThanItWillHold(t *testing.T) {
	// What the gate runs with, pinned here: a package list is tens of
	// kilobytes, and the cap is the answer to a command that is not writing
	// one.
	if maxCapturedOutput != 64<<20 {
		t.Fatalf("maxCapturedOutput = %d, want 64 MiB", maxCapturedOutput)
	}
	original := maxCapturedOutput
	t.Cleanup(func() { maxCapturedOutput = original })
	// Lowered so the test writes kilobytes rather than tens of megabytes; what
	// is under test is the cap, not the number.
	maxCapturedOutput = 4096
	var stderr bytes.Buffer
	result := runBoundedAttempt([]string{"sh", "-c", "head -c 100000 /dev/zero"},
		30*time.Second, 5*time.Second, &stderr, nil)
	if result.exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1: what it collected is not what the command wrote; stderr = %q",
			result.exitCode, stderr.String())
	}
	if len(result.stdout) > maxCapturedOutput {
		t.Fatalf("held %d bytes, want no more than the %d it caps at", len(result.stdout), maxCapturedOutput)
	}
	if got := stderr.String(); !strings.Contains(got, "could not be read in full") ||
		!strings.Contains(got, "a package list is not that long") {
		t.Fatalf("stderr = %q, want it to say what it could not hold", got)
	}
}

// termTrappingChild answers SIGTERM by exiting 0, which is a command that was
// stopped and not a command that finished.
const termTrappingChild = `trap ': > "${BOUNDED_LIST_TERMED:-/dev/null}"; exit 0' TERM; : > "${BOUNDED_LIST_READY:-/dev/null}"; while :; do sleep 0.05; done`

func TestBoundedListRetriesACommandThatTrappedTheBoundAndExitedCleanly(t *testing.T) {
	// Its list stops wherever the signal found it, and its 0 says nothing
	// about how far it got. Reading that as a finished enumeration hands the
	// gate a partial package list and calls it whole, which is the one failure
	// this helper must never produce quietly.
	var stdout, stderr bytes.Buffer
	_, termed := readinessFiles(t)
	code := boundedListWith([]string{"-timeout", "200ms", "-attempts", "2", "-grace", "5s", "--",
		"sh", "-c", termTrappingChild}, &stdout, &stderr, nil)
	if code != 124 {
		t.Fatalf("exit code = %d, want 124: it was stopped, not finished; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "retrying") {
		t.Fatalf("stderr = %q, want the attempt retried like any other timeout", stderr.String())
	}
	// And the stop really was delivered, which is the fact the answer rests on.
	awaitFile(t, termed, "the child was never sent SIGTERM")
}

func TestBoundedAttemptStopsTheGroupWhenTheStderrItWritesToHasStopped(t *testing.T) {
	// The copier is wedged inside a write to a caller's stderr that has
	// stopped, so it holds the lock every diagnostic needs, and an interrupt
	// arrives. The stop has to be made and the attempt has to return: an
	// operator who asks for a stop and gets neither has a wedged helper on top
	// of whatever they were stopping.
	sink := &gatedWriter{hold: "held-by-the-host", gate: make(chan struct{}), held: make(chan struct{})}
	defer close(sink.gate)
	ready, _ := readinessFiles(t)
	signals := make(chan os.Signal, 1)
	const child = `echo held-by-the-host >&2; : > "${BOUNDED_LIST_READY:-/dev/null}"; while :; do sleep 0.05; done`
	done := make(chan attemptResult, 1)
	go func() {
		done <- runBoundedAttempt([]string{"sh", "-c", child}, 30*time.Second, 300*time.Millisecond,
			sink, &signalLatch{ch: signals})
	}()
	awaitFile(t, ready, "the child never started")
	select {
	case <-sink.held:
	case <-time.After(30 * time.Second):
		t.Fatal("the child's stderr never reached the sink")
	}
	signals <- syscall.SIGTERM
	var result attemptResult
	select {
	case result = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the attempt never returned: a line it had to write was waiting for a wedged copier's lock")
	}
	if result.interrupted != syscall.SIGTERM || result.exitCode != 143 {
		t.Fatalf("interrupted = %v, exitCode = %d, want the signal forwarded and 143; stderr = %q",
			result.interrupted, result.exitCode, sink.String())
	}
	if got := sink.String(); !strings.Contains(got, "stopping sh") {
		t.Fatalf("stderr = %q, want the stop announced once the copying had ended", got)
	}
	requireGroupGone(t, result.pgid)
}

func TestBoundedListGivesUpOnADiagnosticItsCallerIsNotTakingEither(t *testing.T) {
	// A gate's answer and its log are often the same file, so a caller that
	// has stopped taking the one has stopped taking the other. A run that
	// waits for that diagnostic ends in silence instead of in a report; it
	// gives up on the words and keeps the exit status, which is what the gate
	// acts on.
	sink := &stalledWriter{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(sink.release)
	start := time.Now()
	code := boundedListWith([]string{"-timeout", "30s", "-attempts", "1", "-grace", "300ms", "--",
		"sh", "-c", "echo listed"}, sink, sink, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1: the answer could not be handed over", code)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("the run took %s: both the hand-over and the words about it are bounded", elapsed)
	}
}
