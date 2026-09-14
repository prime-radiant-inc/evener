//go:build linux || darwin

package dev

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	go func() {
		awaitFile(t, ready, "the child never started")
		signals <- syscall.SIGTERM
	}()
	result := runBoundedAttempt([]string{"sh", "-c", termProofChild}, 30*time.Second, 300*time.Millisecond, &stderr, &signalLatch{ch: signals})
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
	go func() {
		awaitFile(t, ready, "the child never started")
		signals <- syscall.SIGINT
	}()
	code := boundedListWith([]string{"-timeout", "30s", "-attempts", "3", "-grace", "300ms", "--", "sh", "-c", termProofChild}, &stdout, &stderr, signals)
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
	go func() {
		// The child writes this when the bound's SIGTERM reaches it, which is
		// the moment the cleanup's grace begins: a signal sent now lands in
		// the window nobody is watching.
		awaitFile(t, termed, "the child was never sent SIGTERM")
		signals <- syscall.SIGTERM
	}()
	result := runBoundedAttempt([]string{"sh", "-c", termProofChild}, 200*time.Millisecond, 5*time.Second, &stderr, latch)
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
	go func() {
		awaitFile(t, termed, "the child was never sent SIGTERM")
		signals <- syscall.SIGTERM
	}()
	code := boundedListWith([]string{"-timeout", "200ms", "-attempts", "3", "-grace", "5s", "--",
		"sh", "-c", termProofChild}, &stdout, &stderr, signals)
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
	go func() {
		awaitFile(t, ready, "the child never started")
		signals <- syscall.SIGHUP
	}()
	result := runBoundedAttempt([]string{"sh", "-c", hupAnswerer}, 30*time.Second, time.Second, &stderr, latch)
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
	child.Stdout = os.Stdout
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
	time.Sleep(3 * time.Second)
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

func TestBoundedAttemptGivesUpOnAChildItCannotReap(t *testing.T) {
	var stderr bytes.Buffer
	argv, escapeeReady := escapeeCommand(t)
	// The middle process exits as soon as its grandchild holds the pipe, so
	// the unreapable state is entered by the command itself rather than at
	// some elapsed time. The bound below only has to outlast that fork, which
	// takes single-digit milliseconds here: a second is a tripwire two orders
	// of magnitude clear of it, and a machine slower than that fails this test
	// loudly rather than passing it for the wrong reason.
	start := time.Now()
	result := runBoundedAttempt(argv, time.Second, 300*time.Millisecond, &stderr, nil)
	awaitFile(t, escapeeReady, "the escapee never reported its grandchild")
	if !result.stuck {
		t.Fatalf("stuck = false after %s; the attempt waited for a child it could not reap", time.Since(start))
	}
	if result.exitCode != 124 {
		t.Fatalf("exitCode = %d, want 124", result.exitCode)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the attempt took %s, which is not a bound", elapsed)
	}
	if got := stderr.String(); !strings.Contains(got, "stuck in the kernel") {
		t.Fatalf("stderr = %q, want it to say the child could not be reaped", got)
	}
	// The sweep is skipped in this state, so nothing here waited another grace
	// on a group that cannot answer.
	if strings.Contains(stderr.String(), "left processes running in its group") {
		t.Fatalf("stderr = %q, want no survivor sweep after giving up", stderr.String())
	}
}

func TestBoundedAttemptKeepsTheInterruptWhenItCannotReap(t *testing.T) {
	var stderr bytes.Buffer
	// An interrupt is what the operator asked for; a child the kernel will not
	// let go of does not turn that into a timeout, whose diagnostic would send
	// them looking at their caches for a signal they sent themselves.
	argv, escapeeReady := escapeeCommand(t)
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	go func() {
		// Once the grandchild is running: signalling before that would stop a
		// group that has nothing to leave behind.
		awaitFile(t, escapeeReady, "the escapee never reported its grandchild")
		signals <- syscall.SIGTERM
	}()
	result := runBoundedAttempt(argv, 30*time.Second, 300*time.Millisecond, &stderr, latch)
	if !result.stuck {
		t.Fatal("stuck = false; this case needs the child that cannot be reaped")
	}
	if result.exitCode != 143 {
		t.Fatalf("exitCode = %d, want 143: the run was interrupted, not timed out", result.exitCode)
	}
}

func TestBoundedListDoesNotRetryAChildItCannotReap(t *testing.T) {
	var stdout, stderr bytes.Buffer
	argv, escapeeReady := escapeeCommand(t)
	// Same shape as the attempt-level case: the command puts itself into the
	// unreapable state and exits, and the bound is the tripwire that notices.
	args := append([]string{"-timeout", "1s", "-attempts", "3", "-grace", "300ms", "--"}, argv...)
	code := boundedListWith(args, &stdout, &stderr, nil)
	awaitFile(t, escapeeReady, "the escapee never reported its grandchild")
	if code != 124 {
		t.Fatalf("exit code = %d, want 124; stderr = %q", code, stderr.String())
	}
	// Another attempt would stack a second stuck process on the volume that
	// already has one.
	if got := strings.Count(stderr.String(), "did not exit after SIGKILL"); got != 1 {
		t.Fatalf("gave up %d times, want exactly one attempt; stderr = %q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "retrying") {
		t.Fatalf("stderr = %q: a child that cannot be reaped is not retried", stderr.String())
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

// failingCommandWithASurvivor is a command that exits 3 and leaves a child in
// its group that ignores SIGTERM and records having been sent one. The leader
// waits for the child to say its trap is installed before exiting, because a
// SIGTERM that arrives first finds the default disposition and the child dies
// without a word -- the sweep would then have nothing to sweep and the test
// would prove nothing.
//
// The scripts are files rather than -c strings so the quoting is the shell's
// own rather than three layers of escaping.
func failingCommandWithASurvivor(t *testing.T) []string {
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
			"exit 3\n", survivor), 0o644); err != nil {
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
	go func() {
		awaitFile(t, termed, "the child was never sent SIGTERM")
		signals <- syscall.SIGTERM
	}()
	failThenLeaveAChild := failingCommandWithASurvivor(t)
	latch := &signalLatch{ch: signals}
	result := runBoundedAttempt(failThenLeaveAChild, 30*time.Second, 5*time.Second, &stderr, latch)
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
	go func() {
		awaitFile(t, termedAgain, "the child was never sent SIGTERM")
		runnerSignals <- syscall.SIGTERM
	}()
	code := boundedListWith(append([]string{"-timeout", "30s", "-attempts", "3", "-grace", "5s", "--"},
		failingCommandWithASurvivor(t)...), &stdout, &runnerErr, runnerSignals)
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
	if got := stderr.String(); !strings.Contains(got, "of the command's output") {
		t.Fatalf("stderr = %q, want it to name the bytes that did not land", got)
	}
}

func TestReapOrGiveUpSaysWhichItWas(t *testing.T) {
	var stderr bytes.Buffer
	reaped := make(chan struct{})
	close(reaped)
	if !reapOrGiveUp(reaped, time.Second, "sh", &stderr) {
		t.Fatal("reapOrGiveUp = false for a child that was reaped")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing said about a reap that happened", stderr.String())
	}
	never := make(chan struct{})
	if reapOrGiveUp(never, 20*time.Millisecond, "sh", &stderr) {
		t.Fatal("reapOrGiveUp = true for a child that never reaped")
	}
	if got := stderr.String(); !strings.Contains(got, "did not exit after SIGKILL") {
		t.Fatalf("stderr = %q, want the diagnostic naming the failed reap", got)
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
	if result.err == nil {
		t.Fatal("err = nil, want the copy failure the wait returned")
	}
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
