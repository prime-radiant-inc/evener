//go:build linux || darwin

package procgroup

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// childReadyTripwire bounds the readiness poll below. Reaching a fixture's
// first write costs a process creation: 440 samples taken on a host at load
// average ~130 all landed under 65ms, but the two creations the sibling git
// fixture needed have been measured at up to 10s on that same class of host —
// which is exactly where the bare 10s ceiling this replaced would have fired,
// on a fixture that was never in trouble. The bound is a hang guard, never the
// mechanism: the poll's real exits are the file appearing and the child's own
// reap.
const childReadyTripwire = 90 * time.Second

// childWait reaps a started child exactly once and publishes the result, so a
// readiness poll can watch the child's own exit while the test keeps ownership
// of the reap signal Stop needs.
type childWait struct {
	done chan struct{}
	err  error // valid once done is closed
}

func reapChild(cmd *exec.Cmd) *childWait {
	w := &childWait{done: make(chan struct{})}
	go func() {
		w.err = cmd.Wait()
		close(w.done)
	}()
	return w
}

// waitForFile polls for a fixture file the child writes once it is ready,
// so tests synchronize on real child state instead of sleeps. It watches the
// child's own reap alongside the file: a fixture that dies before writing —
// a bad script, a failed exec, a sandbox refusal — names that exit instead of
// spending the whole tripwire and then blaming a file that was never coming.
func waitForFile(t *testing.T, path string, child *childWait) string {
	t.Helper()
	deadline := time.Now().Add(childReadyTripwire)
	for {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
		select {
		case <-child.done:
			t.Fatalf("child exited before writing %s: %v", path, child.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("child never wrote %s within %v", path, childReadyTripwire)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestStartPlacesChildInOwnProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30") //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		Kill(cmd.Process.Pid)
		_ = cmd.Wait()
	}()
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Errorf("child pgid = %d, want its own pid %d", pgid, cmd.Process.Pid)
	}
	if own, _ := syscall.Getpgid(os.Getpid()); pgid == own {
		t.Error("child shares the test's process group; a group signal would hit the test itself")
	}
}

func TestStopTerminatesWholeGroup(t *testing.T) {
	// The child forks a grandchild and publishes its pid; Stop must take
	// out both, or an interrupted lint run leaks linter descendants.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! >"+pidFile+"; wait") //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	child := reapChild(cmd)
	grandchild, err := strconv.Atoi(waitForFile(t, pidFile, child))
	if err != nil {
		t.Fatalf("grandchild pid: %v", err)
	}
	Stop(cmd.Process.Pid, child.done, 5*time.Second)
	<-child.done
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(grandchild) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(grandchild) {
		Kill(cmd.Process.Pid)
		t.Fatalf("grandchild %d survived Stop", grandchild)
	}
}

func TestStopEscalatesToKillWhenTermIgnored(t *testing.T) {
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	cmd := exec.Command("sh", "-c", `trap "" TERM; echo ready >`+readyFile+`; sleep 30`) //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	child := reapChild(cmd)
	waitForFile(t, readyFile, child)
	start := time.Now()
	Stop(cmd.Process.Pid, child.done, 200*time.Millisecond)
	// TRIPWIRE: the KILL is already sent by the time Stop returns, so the reap
	// is one signal delivery away; this only fires if it never lands at all.
	select {
	case <-child.done:
	case <-time.After(10 * time.Second):
		t.Fatal("TERM-ignoring child was never killed")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("Stop returned in %v, before the TERM grace elapsed", elapsed)
	}
	err := child.err
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Errorf("TERM-ignoring child died with %v (state %v), want SIGKILL", err, cmd.ProcessState)
	}
}

func TestStopReturnsWithoutKillWhenChildHonorsTerm(t *testing.T) {
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	cmd := exec.Command("sh", "-c", `trap "exit 143" TERM; echo ready >`+readyFile+`; while :; do sleep 1; done`) //nolint:noctx // lifecycle managed by the process-group Stop under test
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	child := reapChild(cmd)
	waitForFile(t, readyFile, child)
	// The grace is a tripwire, not a mechanism: a child that honors TERM
	// must release Stop long before it, or interrupted runs would always
	// stall for the full escalation window.
	start := time.Now()
	Stop(cmd.Process.Pid, child.done, 30*time.Second)
	<-child.done
	if elapsed := time.Since(start); elapsed > 25*time.Second {
		t.Errorf("Stop took %v against a cooperative child; it waited out the grace instead of the reap", elapsed)
	}
	if code := cmd.ProcessState.ExitCode(); code != 143 {
		t.Errorf("cooperative child exited %d, want 143", code)
	}
}

func TestStopNeverSignalsAnAlreadyReapedChild(t *testing.T) {
	// After a child is reaped its pid can be recycled by an unrelated
	// process group; a Stop that signals first and checks later aims TERM
	// at whoever owns the number now. The decoy here is a live group WE
	// own, standing in for that stranger: with the reaped channel already
	// closed, Stop must send it nothing. (The shell runner was immune by
	// construction — single-threaded blank-after-wait; the Go guard's
	// remaining check-vs-reap window is microseconds and needs an
	// immediate pid wraparound.)
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	marker := filepath.Join(dir, "signalled")
	decoy := exec.Command("sh", "-c", `trap ": >`+marker+`; exit 143" TERM; echo ready >`+readyFile+`; while :; do sleep 1; done`) //nolint:noctx // stopped explicitly at the end of the test
	if err := Start(decoy); err != nil {
		t.Fatalf("Start: %v", err)
	}
	decoyChild := reapChild(decoy)
	waitForFile(t, readyFile, decoyChild)
	alreadyReaped := make(chan struct{})
	close(alreadyReaped)
	Stop(decoy.Process.Pid, alreadyReaped, 50*time.Millisecond)
	// A signal in flight would hit the trap well within this window; the
	// sleep bounds how long a wrongly-sent TERM has to land, not a
	// mechanism the pass depends on.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("Stop signalled a child it was told is already reaped (marker stat err %v)", err)
	}
	if !pidAlive(decoy.Process.Pid) {
		t.Error("decoy died during a Stop that should have sent nothing")
	}
	Stop(decoy.Process.Pid, decoyChild.done, 5*time.Second)
	<-decoyChild.done
}

func TestExitCodeMapsSignalDeathsLikeAShell(t *testing.T) {
	cmd := exec.Command("sleep", "30") //nolint:noctx // the test kills it directly
	if err := Start(cmd); err != nil {
		t.Fatalf("Start: %v", err)
	}
	Kill(cmd.Process.Pid)
	_ = cmd.Wait()
	if got := ExitCode(cmd.ProcessState); got != 128+int(syscall.SIGKILL) {
		t.Errorf("ExitCode after SIGKILL = %d, want %d", got, 128+int(syscall.SIGKILL))
	}

	ok := exec.Command("true") //nolint:noctx // exits on its own
	if err := Start(ok); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = ok.Wait()
	if got := ExitCode(ok.ProcessState); got != 0 {
		t.Errorf("ExitCode after clean exit = %d, want 0", got)
	}
}

// TestExistsAnswersForTheGroupNotTheLeader is the question stopSurvivors asks
// after its direct child has been reaped: a leader can exit and leave the
// group populated.
func TestExistsAnswersForTheGroupNotTheLeader(t *testing.T) {
	if got := Exists(0); got {
		t.Fatal("Exists(0) = true: pgid 0 is this process's own group, never a child's")
	}
	cmd := exec.Command("sh", "-c", "sleep 30")
	if err := Start(cmd); err != nil {
		t.Fatalf("starting the fixture: %v", err)
	}
	pgid := cmd.Process.Pid
	if pgid == syscall.Getpgrp() {
		t.Fatalf("the fixture shares this process's group (%d); refusing to signal it", pgid)
	}
	if !Exists(pgid) {
		t.Fatalf("Exists(%d) = false while the group is running", pgid)
	}
	// Cleaned up by that group id alone.
	Kill(pgid)
	_ = cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for Exists(pgid) {
		if time.Now().After(deadline) {
			t.Fatalf("Exists(%d) still true after the group was killed", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestProcessGroupGuardHelper is not a test: it is the process the guard test
// below re-executes, in a process group of its own. Every entry point here can
// send a signal to a whole group, and the guard under test is what keeps pgid
// 0 -- the caller's own group -- from being one of them. Running the check in
// a child means a guard that is not there signals that child alone, rather
// than the test binary, the go test that started it, and the terminal or CI
// job they all share.
func TestProcessGroupGuardHelper(t *testing.T) {
	call := os.Getenv("PROCGROUP_GUARD_CALL")
	if call == "" {
		t.Skip("helper process entry point; runs only under the guard test")
	}
	pgid, err := strconv.Atoi(os.Getenv("PROCGROUP_GUARD_PGID"))
	if err != nil {
		t.Fatalf("PROCGROUP_GUARD_PGID=%q: %v", os.Getenv("PROCGROUP_GUARD_PGID"), err)
	}
	// Never closed: a stop that signalled a group would wait here for a reap
	// that is not coming, which the elapsed check below is what catches.
	never := make(chan struct{})
	start := time.Now()
	switch call {
	case "Terminate":
		Terminate(pgid)
	case "Kill":
		Kill(pgid)
	case "Stop":
		Stop(pgid, never, 30*time.Second)
	case "StopWith":
		StopWith(pgid, syscall.SIGTERM, never, 30*time.Second)
	default:
		t.Fatalf("PROCGROUP_GUARD_CALL=%q is not one of the guarded entry points", call)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("%s(%d) took %s: it waited on a group it should have refused to signal", call, pgid, elapsed)
	}
}

// TestTheGroupSignalsRefuseAPgidThatIsNotAGroup pins the guard on every entry
// point that signals: 0 is the caller's own process group, 1 makes kill(-1),
// which is every process this user may signal on the host, and negatives are
// not groups, so a signal built from any of them goes to processes this
// package never started. A helper that survives its call and exits 0 is the
// whole assertion: without the guard, Terminate(0) and Kill(0) kill it where
// it stands, and the stops wait out a reap that cannot come.
//
// The 1 case is not mutation-proved, and deliberately: removing the guard and
// running it would broadcast SIGTERM and then SIGKILL to every process this
// user owns, on the machine running the tests. What is proved instead, and
// safely, is the guard in the one place that sends -- see
// TestSignalGroupRefusesTheNumbersThatAreNotGroups, which asks with signal 0,
// the one that delivers nothing even when it reaches everything.
func TestTheGroupSignalsRefuseAPgidThatIsNotAGroup(t *testing.T) {
	for _, call := range []string{"Terminate", "Kill", "Stop", "StopWith"} {
		for _, pgid := range []string{"0", "1", "-1"} {
			t.Run(call+"("+pgid+")", func(t *testing.T) {
				var log bytes.Buffer
				helper := exec.Command(os.Args[0], "-test.run=TestProcessGroupGuardHelper$") //nolint:noctx // its own process group, stopped by that group id below
				helper.Env = append(os.Environ(), "PROCGROUP_GUARD_CALL="+call, "PROCGROUP_GUARD_PGID="+pgid)
				helper.Stdout, helper.Stderr = &log, &log
				if err := Start(helper); err != nil {
					t.Fatalf("starting the helper: %v", err)
				}
				child := reapChild(helper)
				select {
				case <-child.done:
				case <-time.After(childReadyTripwire):
					group := helper.Process.Pid
					if group == syscall.Getpgrp() {
						t.Fatalf("the helper shares this process's group (%d); refusing to signal it", group)
					}
					// Cleaned up by that group id alone.
					Kill(group)
					<-child.done
					t.Fatalf("the helper never finished; its output was %q", log.String())
				}
				if child.err != nil {
					t.Fatalf("%s(%s) in a child of its own: %v; its output was %q", call, pgid, child.err, log.String())
				}
			})
		}
	}
}

// noSuchGroup is a group id no kernel can hand out: darwin's default pid
// ceiling is 99999 and Linux's highest possible pid_max is 4194304, so a
// signal aimed here has nowhere to go by construction -- which is what makes
// it safe to aim one. Measured on darwin: kill(-1<<30, sig) answers ESRCH for
// signal 0 and for SIGTERM, not EINVAL.
const noSuchGroup = 1 << 30

// TestStopWithReportsAGroupThatWasGoneBeforeTheSignal pins the difference
// between a stop and a signal that found nothing. The caller's next move
// depends on it: a child this stop killed is 128+signal, and a child that was
// already gone is whatever it exited with.
func TestStopWithReportsAGroupThatWasGoneBeforeTheSignal(t *testing.T) {
	// Never closed: the reap this stop is about to send its caller to read has
	// not happened, so the wait below is the grace, in full.
	never := make(chan struct{})
	grace := 200 * time.Millisecond
	start := time.Now()
	stopped, err := StopWith(noSuchGroup, syscall.SIGTERM, never, grace)
	elapsed := time.Since(start)
	if stopped {
		t.Error("StopWith reported a stop of a group that was not there to stop")
	}
	if err != nil {
		t.Errorf("StopWith err = %v; a group that is gone is an answer, not a refusal", err)
	}
	if elapsed < grace {
		t.Errorf("StopWith returned in %s, before waiting out the reap its caller needs", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Errorf("StopWith took %s: the wait for that reap is bounded by the grace", elapsed)
	}
	// And the same through Stop, which is where the lint runner's stops go.
	if stopped, err := Stop(noSuchGroup, never, 50*time.Millisecond); stopped || err != nil {
		t.Errorf("Stop = %v, %v; want no stop reported and no refusal", stopped, err)
	}
}

// TestSignalGroupRefusesTheNumbersThatAreNotGroups asks the sender directly,
// with signal 0 -- which delivers nothing, and so can be asked of the numbers
// whose whole problem is where a real signal would land. 1 is the one that
// matters: kill(-1, sig) is every process this user may signal, and a guard
// that stops at 0 lets a gate runner send it.
func TestSignalGroupRefusesTheNumbersThatAreNotGroups(t *testing.T) {
	for _, pgid := range []int{-1, 0, 1} {
		if err := signalGroup(pgid, 0); !errors.Is(err, syscall.EINVAL) {
			t.Errorf("signalGroup(%d, 0) = %v, want it refused as not a group", pgid, err)
		}
		if Exists(pgid) {
			t.Errorf("Exists(%d) = true: a number that is not a group has no members to report", pgid)
		}
	}
	// And a real group still answers, so the floor did not swallow the
	// question it exists to let through.
	cmd := exec.Command("sh", "-c", "sleep 30") //nolint:noctx // stopped by its own group id below
	if err := Start(cmd); err != nil {
		t.Fatalf("starting the fixture: %v", err)
	}
	pgid := cmd.Process.Pid
	if pgid == syscall.Getpgrp() {
		t.Fatalf("the fixture shares this process's group (%d); refusing to signal it", pgid)
	}
	if err := signalGroup(pgid, 0); err != nil {
		t.Errorf("signalGroup(%d, 0) = %v for a group that is running", pgid, err)
	}
	Kill(pgid)
	_ = cmd.Wait()
}

// TestStopWithCountsOnlyASignalItManagedToSend is the child that exits on its
// own while every signal the stop made was refused. Nothing this stop did
// ended it, so the caller is holding the command's own answer -- and 128+the
// signal, which is what a reported stop turns into upstream, would name a
// death that did not happen. The refusal is injected: producing EPERM for real
// means signalling a group this test does not own.
func TestStopWithCountsOnlyASignalItManagedToSend(t *testing.T) {
	reaped := make(chan struct{})
	var sent []syscall.Signal
	refuse := func(_ int, s syscall.Signal) error {
		sent = append(sent, s)
		// The child exits by itself while the stop is waiting out its grace.
		close(reaped)
		return syscall.EPERM
	}
	stopped, err := stopWith(refuse, 4242, syscall.SIGTERM, reaped, 5*time.Second)
	if stopped {
		t.Error("StopWith reported a stop it was refused permission to make")
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Errorf("err = %v, want the refusal reported", err)
	}
	if len(sent) != 1 || sent[0] != syscall.SIGTERM {
		t.Errorf("signals sent = %v, want one TERM: the reap ends the escalation", sent)
	}

	// The other half of the same rule: a signal that lands makes a stop.
	landed := make(chan struct{})
	deliver := func(_ int, _ syscall.Signal) error { close(landed); return nil }
	if stopped, err := stopWith(deliver, 4242, syscall.SIGTERM, landed, 5*time.Second); !stopped || err != nil {
		t.Errorf("stopWith with a delivered signal = %v, %v; want a stop and no refusal", stopped, err)
	}
}

// TestProcessGroupOrphanHelper is not a test: it is the leader process the
// zombie-group test re-executes. It starts a grandchild in its own group,
// waits until that grandchild has exited, and exits without reaping it -- so
// the group it led is left holding a zombie for whatever adopts it to clear.
func TestProcessGroupOrphanHelper(t *testing.T) {
	gone := os.Getenv("PROCGROUP_ORPHAN_GONE")
	if gone == "" {
		t.Skip("helper process entry point; runs only under the zombie-group test")
	}
	// No Setpgid: the grandchild stays in the group this process leads, which
	// is what makes it the group's last member.
	grandchild := exec.Command("sh", "-c", ": > "+gone) //nolint:noctx // never waited for: the zombie it leaves is the fixture
	if err := grandchild.Start(); err != nil {
		t.Fatalf("starting the grandchild: %v", err)
	}
	deadline := time.Now().Add(childReadyTripwire)
	for {
		if _, err := os.Stat(gone); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the grandchild never wrote %s", gone)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The file is written just before the exit; this is what makes the
	// grandchild a zombie rather than a process still on its way out. The test
	// this serves does not depend on it -- a live member is a live group, and
	// the assertion is about how fast the group empties either way.
	time.Sleep(50 * time.Millisecond)
}

// TestExistsAnswersNoOnceAZombieOnlyGroupIsAdopted is the measurement the
// Exists comment rests on, made on whatever platform runs it. A leader that
// exits without reaping its grandchild leaves a group whose only member is a
// zombie, and kill(-pgid, 0) answers for a zombie: the attempt's "the group is
// still there, so this host has a survivor" decision is built on that window
// being short, which holds only while something adopts and reaps the orphan.
// On darwin it was measured at 3-12ms; this test is what says what it is on
// the platform CI runs.
func TestExistsAnswersNoOnceAZombieOnlyGroupIsAdopted(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "grandchild-gone")
	leader := exec.Command(os.Args[0], "-test.run=TestProcessGroupOrphanHelper$") //nolint:noctx // its own process group, cleaned by that id below
	leader.Env = append(os.Environ(), "PROCGROUP_ORPHAN_GONE="+gone)
	var log bytes.Buffer
	leader.Stdout, leader.Stderr = &log, &log
	if err := Start(leader); err != nil {
		t.Fatalf("starting the leader: %v", err)
	}
	pgid := leader.Process.Pid
	if pgid == syscall.Getpgrp() {
		t.Fatalf("the fixture shares this process's group (%d); refusing to signal it", pgid)
	}
	child := reapChild(leader)
	select {
	case <-child.done:
	case <-time.After(childReadyTripwire):
		Kill(pgid)
		<-child.done
		t.Fatalf("the leader never exited; its output was %q", log.String())
	}
	if child.err != nil {
		t.Fatalf("the leader exited with %v; its output was %q", child.err, log.String())
	}
	// From here the leader is reaped and only the orphan can be answering.
	const grace = 5 * time.Second
	start := time.Now()
	for Exists(pgid) {
		if time.Since(start) > grace {
			// Cleaned up by that group id alone.
			Kill(pgid)
			t.Fatalf("the group still answered %s after its leader was reaped: a zombie-only group reads as alive on this platform for longer than a stop's grace, so the attempt would call a finished command stuck", time.Since(start))
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Logf("the zombie-only group answered for %s after its leader was reaped", time.Since(start))
}
