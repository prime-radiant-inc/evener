//go:build linux || darwin

package dev

import (
	"bytes"
	"errors"
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
const termProofChild = `trap "" TERM; sleep 60`

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
	signals := make(chan os.Signal, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
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
	signals := make(chan os.Signal, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
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
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	go func() {
		time.Sleep(400 * time.Millisecond)
		signals <- syscall.SIGTERM
	}()
	result := runBoundedAttempt([]string{"sh", "-c", termProofChild}, 200*time.Millisecond, 800*time.Millisecond, &stderr, latch)
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
	signals := make(chan os.Signal, 1)
	go func() {
		time.Sleep(350 * time.Millisecond)
		signals <- syscall.SIGTERM
	}()
	code := boundedListWith([]string{"-timeout", "200ms", "-attempts", "3", "-grace", "500ms", "--",
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
	const hupAnswerer = `trap 'echo got-hup; exit 0' HUP; trap "" TERM; while :; do sleep 0.05; done`
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	go func() {
		time.Sleep(150 * time.Millisecond)
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
	// Held, not waited for: this process is killed where it stands.
	time.Sleep(2 * time.Second)
}

// TestBoundedListSleeperHelper is the grandchild: it holds the inherited pipe
// for long enough to outlast the attempt's reap grace, and exits on its own so
// nothing has to signal it.
func TestBoundedListSleeperHelper(t *testing.T) {
	if os.Getenv("BOUNDED_LIST_SLEEPER") == "" {
		t.Skip("helper process entry point; runs only under the give-up tests")
	}
	time.Sleep(3 * time.Second)
}

// escapeeCommand is the attempt's command for the give-up tests: this test
// binary, re-executed, with nothing ambient involved.
func escapeeCommand(t *testing.T) []string {
	t.Helper()
	t.Setenv("BOUNDED_LIST_ESCAPEE", "1")
	return []string{os.Args[0], "-test.run=TestBoundedListEscapeeHelper$"}
}

func TestBoundedAttemptGivesUpOnAChildItCannotReap(t *testing.T) {
	var stderr bytes.Buffer
	start := time.Now()
	result := runBoundedAttempt(escapeeCommand(t), 200*time.Millisecond, 300*time.Millisecond, &stderr, nil)
	if !result.unreaped {
		t.Fatalf("unreaped = false after %s; the attempt waited for a child it could not reap", time.Since(start))
	}
	if result.exitCode != 124 {
		t.Fatalf("exitCode = %d, want 124", result.exitCode)
	}
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
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
	signals := make(chan os.Signal, 1)
	latch := &signalLatch{ch: signals}
	go func() {
		time.Sleep(200 * time.Millisecond)
		signals <- syscall.SIGTERM
	}()
	result := runBoundedAttempt(escapeeCommand(t), 30*time.Second, 300*time.Millisecond, &stderr, latch)
	if !result.unreaped {
		t.Fatal("unreaped = false; this case needs the child that cannot be reaped")
	}
	if result.exitCode != 143 {
		t.Fatalf("exitCode = %d, want 143: the run was interrupted, not timed out", result.exitCode)
	}
}

func TestBoundedListDoesNotRetryAChildItCannotReap(t *testing.T) {
	var stdout, stderr bytes.Buffer
	argv := escapeeCommand(t)
	args := append([]string{"-timeout", "200ms", "-attempts", "3", "-grace", "300ms", "--"}, argv...)
	code := boundedListWith(args, &stdout, &stderr, nil)
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
	// select picks at random; the command's answer wins.
	finished := make(chan error, 1)
	finished <- nil
	if ok, _ := finishedFirst(finished); !ok {
		t.Fatal("finishedFirst = false for a command whose answer was already waiting")
	}
	if ok, _ := finishedFirst(make(chan error)); ok {
		t.Fatal("finishedFirst = true for a command that has not finished")
	}
}

func TestReapOrGiveUpSaysWhichItWas(t *testing.T) {
	var stderr bytes.Buffer
	reaped := make(chan error, 1)
	reaped <- nil
	if !reapOrGiveUp(reaped, time.Second, "sh", &stderr) {
		t.Fatal("reapOrGiveUp = false for a child that was reaped")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want nothing said about a reap that happened", stderr.String())
	}
	never := make(chan error)
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
