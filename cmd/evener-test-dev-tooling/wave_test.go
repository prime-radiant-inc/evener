//go:build linux || darwin

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeSuite(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name+"-selftest.sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAllSuitesPassPrintsPassAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "alpha", "echo alpha-ran\nexit 0\n")
	writeSuite(t, dir, "beta", "exit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"alpha", "beta"}, KillGrace: time.Second, Out: &out})
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	for _, want := range []string{"PASS  alpha", "PASS  beta"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "alpha-ran") {
		t.Fatalf("passing suite's log must not be replayed:\n%s", out.String())
	}
}

func TestFailingSuiteReplaysItsLogOnce(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "alpha", "exit 0\n")
	writeSuite(t, dir, "beta", "echo beta-broke\nexit 3\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"alpha", "beta"}, KillGrace: time.Second, Out: &out})
	if code != 1 {
		t.Fatalf("exit %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "PASS  alpha") {
		t.Fatalf("alpha should still pass:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "FAIL  beta") {
		t.Fatalf("missing FAIL beta:\n%s", out.String())
	}
	if got := strings.Count(out.String(), "beta-broke"); got != 1 {
		t.Fatalf("failing log replayed %d times, want 1:\n%s", got, out.String())
	}
}

func TestSuiteLeavingTempFilesFails(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "tidy", "exit 0\n")
	writeSuite(t, dir, "messy", "touch \"$TMPDIR/leak\"\nexit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"tidy", "messy"}, KillGrace: time.Second, Out: &out})
	if code != 1 {
		t.Fatalf("exit %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "FAIL  messy") {
		t.Fatalf("missing FAIL messy:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "leaked") || !strings.Contains(out.String(), "leak") {
		t.Fatalf("leak diagnosis missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PASS  tidy") {
		t.Fatalf("tidy should pass:\n%s", out.String())
	}
}

func TestSuiteGetsPrivateTmpdir(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report")
	if err := os.Mkdir(report, 0o755); err != nil {
		t.Fatal(err)
	}
	// Each suite records the TMPDIR it saw OUTSIDE that TMPDIR (leaving it
	// clean), so the test can compare after the wave completes.
	writeSuite(t, dir, "one", "echo \"$TMPDIR\" >"+report+"/one\nexit 0\n")
	writeSuite(t, dir, "two", "echo \"$TMPDIR\" >"+report+"/two\nexit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"one", "two"}, KillGrace: time.Second, Out: &out})
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	oneTmp := readTrimmed(t, filepath.Join(report, "one"))
	twoTmp := readTrimmed(t, filepath.Join(report, "two"))
	if oneTmp == "" || oneTmp == twoTmp {
		t.Fatalf("suites must see distinct private TMPDIRs, got %q and %q", oneTmp, twoTmp)
	}
	if _, err := os.Stat(oneTmp); !os.IsNotExist(err) {
		t.Fatalf("run dir should be removed after the wave, %s still exists", oneTmp)
	}
}

// TestSuiteWithSlashInNameHandlesSubdirPaths proves the wave runner correctly
// handles suite names containing slashes (subdir-prefixed, e.g.
// "gate/run-module-tests"). The MkdirAll in runSuite creates the parent
// directory for both the tmp dir and the log file.
func TestSuiteWithSlashInNameHandlesSubdirPaths(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "group/suite", "exit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"group/suite"}, KillGrace: time.Second, Out: &out})
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "PASS  group/suite") {
		t.Fatalf("missing PASS group/suite:\n%s", out.String())
	}
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// mkFixtureFifos creates the named FIFOs under dir and returns dir.
func mkFixtureFifos(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := syscall.Mkfifo(filepath.Join(dir, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fifoReadTripwire bounds a FIFO read that should complete in milliseconds. It
// is a tripwire, never the synchronisation mechanism: the wave's exit is what
// actually tells us a writer is never coming (see readFifoLineOrExit). Generous
// on purpose, so a loaded machine never trips it before the real signal lands.
const fifoReadTripwire = 60 * time.Second

// postExitGraceMin is the minimum grace for descendants that outlived the wave.
// An orphan already past its TERM trap writes within milliseconds, so this
// only elapses when no writer exists at all. It is also the whole grace for a
// caller with no deadline to scale against.
const postExitGraceMin = 5 * time.Second

// postExitGraceMax caps how far the deadline can stretch the grace. Without a
// ceiling, one absent writer spends nearly the entire remaining test budget
// before it says so: measured at 1m45s under `-timeout 2m`, and it would be
// 9m45s of the gate's 10m. A descendant that is alive writes in milliseconds,
// so past a minute the extra waiting buys no new information — it only moves
// the cost of the diagnosis onto every other test in the binary.
const postExitGraceMax = 60 * time.Second

// deadlineReportReserve is held back from the test deadline so a genuinely
// absent writer surfaces as this file's clean one-line error, not as the test
// runner's deadline panic — the panic kills the whole binary and buries the
// diagnosis under a goroutine dump.
const deadlineReportReserve = 15 * time.Second

// graceFor is how long readFifoLineOrExit keeps waiting after the wave has
// exited: the time left until the test's own deadline, less the reserve that
// keeps the report ahead of the runner's panic, clamped between
// postExitGraceMin and postExitGraceMax. A zero deadline means the caller has
// none (`go test` without -timeout), which is the floor case, as is a deadline
// already inside the reserve.
func graceFor(deadline, now time.Time) time.Duration {
	if deadline.IsZero() {
		return postExitGraceMin
	}
	return min(max(deadline.Sub(now)-deadlineReportReserve, postExitGraceMin), postExitGraceMax)
}

// waveRun is a running wave whose exit any number of waiters can observe.
// A plain exit-code channel could be received only once, which is why the FIFO
// reads could not consult it and blocked forever instead.
type waveRun struct {
	done chan struct{}
	code int
}

// wait blocks until the wave exits and returns its exit code.
func (w *waveRun) wait() int {
	<-w.done
	return w.code
}

// readFifoLineOrExit blocks until one line arrives on the FIFO, the wave exits,
// or the tripwire fires.
//
// Opening a FIFO for reading blocks until a writer appears, which is the sync
// this fixture wants and also the hazard: a suite that dies before writing
// leaves the open blocked forever. So the open runs on its own goroutine and
// races the wave's exit. Once the wave is done no writer can ever appear, and
// that is reported immediately with the exit code rather than as a timeout.
//
// deadline is the caller's own test deadline (t.Deadline(), zero if it has
// none); graceFor turns it into how long the post-exit wait may run.
//
// The goroutine may outlive this call, blocked in open() on a FIFO nobody will
// ever write to. That is deliberate: it holds only a file descriptor, the test
// binary is about to exit, and the alternative (a non-blocking open plus a
// readiness poll) would reintroduce polling where an awaitable event exists.
func readFifoLineOrExit(path string, run *waveRun, tripwire time.Duration, deadline time.Time) (string, error) {
	type result struct {
		line string
		err  error
	}
	lines := make(chan result, 1)
	go func() {
		f, err := os.Open(path)
		if err != nil {
			lines <- result{err: err}
			return
		}
		defer f.Close()
		buf := make([]byte, 256)
		n, err := f.Read(buf)
		lines <- result{line: strings.TrimSpace(string(buf[:n])), err: err}
	}()

	select {
	case got := <-lines:
		return got.line, got.err
	case <-run.done:
		// Wave exit does NOT prove a writer is never coming: a forked descendant
		// is reaped via the process group and can still write after runWave
		// returns (TestForkedDescendantIsReaped depends on exactly that). So exit
		// demotes the wait to a bounded grace instead of ending it. The grace only
		// ever elapses on a genuinely absent writer, which is the case that used
		// to hang until the package timeout.
		//
		// A live-but-slow descendant gets the time the test can spare (see
		// graceFor); only a genuinely absent writer ever pays it in full.
		grace := graceFor(deadline, time.Now())
		select {
		case got := <-lines:
			return got.line, got.err
		case <-time.After(grace):
			return "", fmt.Errorf("wave exited (code %d) and nothing wrote to %s within %s of its exit",
				run.code, filepath.Base(path), grace)
		}
	case <-time.After(tripwire):
		return "", fmt.Errorf("no line on %s within %s and the wave is still running", filepath.Base(path), tripwire)
	}
}

// readFifoLine is readFifoLineOrExit's fatal wrapper for the fixture tests. It
// hands over the test's own deadline, which is what lets a slow descendant use
// the time this test can spare instead of the bare floor.
func readFifoLine(t *testing.T, path string, run *waveRun) string {
	t.Helper()
	deadline, _ := t.Deadline()
	line, err := readFifoLineOrExit(path, run, fifoReadTripwire, deadline)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

// startWave runs runWave in a goroutine and returns the running wave.
func startWave(cfg waveConfig) *waveRun {
	run := &waveRun{done: make(chan struct{})}
	go func() {
		run.code = runWave(cfg)
		close(run.done)
	}()
	return run
}

func TestInterruptTermsProcessGroupAndExits143(t *testing.T) {
	fx := mkFixtureFifos(t, "ready", "events", "hold")
	dir := t.TempDir()
	writeSuite(t, dir, "holder",
		"trap 'echo suite-termed >"+fx+"/events; exit 143' TERM\n"+
			"echo ready >"+fx+"/ready\n"+
			"read _ <"+fx+"/hold\n")
	signals := make(chan os.Signal, 1)
	var out bytes.Buffer
	run := startWave(waveConfig{ScriptsDir: dir, Suites: []string{"holder"}, KillGrace: time.Minute, Out: &out, Signals: signals})
	if got := readFifoLine(t, filepath.Join(fx, "ready"), run); got != "ready" {
		t.Fatalf("readiness handshake got %q", got)
	}
	signals <- syscall.SIGTERM
	if got := readFifoLine(t, filepath.Join(fx, "events"), run); got != "suite-termed" {
		t.Fatalf("suite never saw TERM, got %q", got)
	}
	if code := run.wait(); code != 143 {
		t.Fatalf("exit %d, want 143; output:\n%s", code, out.String())
	}
}

func TestForkedDescendantIsReaped(t *testing.T) {
	fx := mkFixtureFifos(t, "ready", "ready2", "events", "hold", "hold2")
	dir := t.TempDir()
	writeSuite(t, dir, "forker",
		"( trap 'echo grandchild-termed >"+fx+"/events; exit 0' TERM\n"+
			"  echo up >"+fx+"/ready2\n"+
			"  read _ <"+fx+"/hold2 ) &\n"+
			"echo ready >"+fx+"/ready\n"+
			"read _ <"+fx+"/hold\n")
	signals := make(chan os.Signal, 1)
	var out bytes.Buffer
	run := startWave(waveConfig{ScriptsDir: dir, Suites: []string{"forker"}, KillGrace: time.Minute, Out: &out, Signals: signals})
	readFifoLine(t, filepath.Join(fx, "ready"), run)
	readFifoLine(t, filepath.Join(fx, "ready2"), run)
	signals <- syscall.SIGTERM
	if got := readFifoLine(t, filepath.Join(fx, "events"), run); got != "grandchild-termed" {
		t.Fatalf("forked descendant never saw TERM, got %q", got)
	}
	if code := run.wait(); code != 143 {
		t.Fatalf("exit %d, want 143; output:\n%s", code, out.String())
	}
}

func TestTermIgnoringSuiteIsKilledAfterGrace(t *testing.T) {
	fx := mkFixtureFifos(t, "ready", "hold")
	dir := t.TempDir()
	writeSuite(t, dir, "stubborn",
		"trap '' TERM\n"+
			"echo ready >"+fx+"/ready\n"+
			"read _ <"+fx+"/hold\n")
	signals := make(chan os.Signal, 1)
	var out bytes.Buffer
	run := startWave(waveConfig{ScriptsDir: dir, Suites: []string{"stubborn"}, KillGrace: 50 * time.Millisecond, Out: &out, Signals: signals})
	readFifoLine(t, filepath.Join(fx, "ready"), run)
	signals <- syscall.SIGTERM
	// runWave only returns once the KILLed suite is reaped; no clock here
	// beyond the injected grace.
	if code := run.wait(); code != 143 {
		t.Fatalf("exit %d, want 143; output:\n%s", code, out.String())
	}
}

func TestMktempTMinusTIsCaughtByLeakCheck(t *testing.T) {
	// macOS mktemp -t ignores TMPDIR (docs/developing-evener/testing.md, kata cqne): without
	// the runner's mktemp shim these suites would write to the real per-user
	// temp dir and the leak check could never see it.
	dir := t.TempDir()
	writeSuite(t, dir, "leaky", "mktemp -d -t evener-leak-probe >/dev/null\nexit 0\n")
	writeSuite(t, dir, "tidy", "d=$(mktemp -d -t evener-tidy-probe)\nrmdir \"$d\"\nexit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"leaky", "tidy"}, KillGrace: time.Second, Out: &out})
	if code != 1 {
		t.Fatalf("exit %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "FAIL  leaky") || !strings.Contains(out.String(), "leaked") {
		t.Fatalf("leak via mktemp -t not caught:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PASS  tidy") {
		t.Fatalf("tidy suite should pass:\n%s", out.String())
	}
}

func TestWaveCompletesDespiteBlockedLeakCheck(t *testing.T) {
	// Regression test for kata p8ts: the leak check (os.ReadDir on suite's tmp)
	// is post-reap bookkeeping that could block on a wedged filesystem. Before
	// the fix, if a leak check hung, the entire runSuite goroutine would hang,
	// blocking that suite's done <- i send and preventing the wave from collecting
	// results. The wave would become unkillable: the suite process is already
	// reaped, so signal forwarding can't reach it.
	//
	// This test injects a slow/blocking leak check and verifies that:
	// 1. The wave returns at all (not hung) — proved structurally by
	//    runWave's call returning, backstopped by the test binary's own
	//    hang detection, rather than by a wall-clock ceiling: process
	//    spawn and goroutine scheduling for the two suites are
	//    load-dependent, so a fixed elapsed-time bound flakes under
	//    parallel test-suite load (docs/developing-evener/testing.md, "Flakes and Timeouts").
	// 2. The blocked suite is reported as failed (timeout)
	// 3. Other suites still pass
	// 4. The wave exits with failure code (one suite failed)

	// Inject a tiny timeout and a selective blocking leak check, scoped to
	// this test's own waveConfig. Only the "wedged" suite's leak check
	// blocks; the "normal" suite gets the real (fast) leak check so it can
	// pass despite the other timing out. Nothing to restore: the injection
	// lives in a local cfg value, not package state.
	blockForever := make(chan struct{})
	checkLeaksFn := func(dir string) []string {
		if strings.Contains(dir, "wedged") {
			<-blockForever // Block only the wedged suite
			return nil
		}
		return checkLeaks(dir) // Normal suites get real fast leak check
	}

	dir := t.TempDir()
	// A suite that exits 0 and leaves no files. Its injected leak check will block.
	writeSuite(t, dir, "wedged", "exit 0\n")
	// A normal suite that should pass despite the wedged suite blocking.
	writeSuite(t, dir, "normal", "exit 0\n")

	var out bytes.Buffer
	// runWave only returns once the blocked leak check times out and both
	// suites are collected; no clock here beyond the injected timeout. That
	// return (rather than a hang caught by the test binary's own timeout) is
	// what proves the wave isn't wedged.
	code := runWave(waveConfig{
		ScriptsDir:        dir,
		Suites:            []string{"wedged", "normal"},
		KillGrace:         time.Second,
		Out:               &out,
		checkLeaksFn:      checkLeaksFn,
		checkLeaksTimeout: 50 * time.Millisecond,
	})

	outStr := out.String()

	// Verify the blocked suite reports as failed with timeout message.
	if !strings.Contains(outStr, "FAIL  wedged") {
		t.Fatalf("blocked suite must report FAIL, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "leak check timed out") {
		t.Fatalf("blocked suite must mention timeout, got:\n%s", outStr)
	}

	// Verify the normal suite still passes.
	if !strings.Contains(outStr, "PASS  normal") {
		t.Fatalf("normal suite must pass despite wedged suite, got:\n%s", outStr)
	}

	// Verify the wave exits with failure (one suite failed).
	if code == 0 {
		t.Fatalf("wave must exit nonzero when a suite fails; got %d", code)
	}
}

// TestReadFifoLineFailsWhenWaveExitsWithoutWriting pins the mechanism behind a
// flake that cost a whole module: readFifoLine opened a FIFO with no ceiling and
// no awareness of the wave, so a suite that died before writing left the test
// blocked in os.Open until the 600s package timeout, taking every other test in
// the package down with it.
//
// The wave's exit is the awaitable completion that was going unawaited: once the
// wave is done, no writer is ever coming, and the read can fail immediately with
// a real diagnosis instead of a timeout mystery.
//
// Restoring the unbounded os.Open, or dropping the run argument, hangs this test.
func TestReadFifoLineFailsWhenWaveExitsWithoutWriting(t *testing.T) {
	fx := mkFixtureFifos(t, "never-written")
	dead := &waveRun{done: make(chan struct{}), code: 7}
	close(dead.done) // the wave is already over; nobody will ever open for write

	start := time.Now()
	_, err := readFifoLineOrExit(filepath.Join(fx, "never-written"), dead, time.Minute, time.Time{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error when the wave exited without writing")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("error should name the wave's exit code so the failure is diagnosable, got: %v", err)
	}
	// The tripwire is a minute; returning anywhere near it means the wave-exit
	// path is not wired and we fell through to the ceiling instead.
	if elapsed > 10*time.Second {
		t.Errorf("took %v: fell through to the tripwire instead of noticing the wave had exited", elapsed)
	}
}

// TestReadFifoLineWaitsForWriterThatOutlivesTheWave guards the other half of the
// contract. A forked descendant is reaped via the process group, not by the wave
// goroutine, so it can legitimately write AFTER runWave has returned — which is
// exactly what TestForkedDescendantIsReaped exercises. Treating wave exit as
// proof that no writer will ever appear turns that into a spurious failure,
// observed at 1 run in 5 while developing this fix.
//
// So wave exit demotes the read to a bounded grace rather than ending it.
func TestReadFifoLineWaitsForWriterThatOutlivesTheWave(t *testing.T) {
	fx := mkFixtureFifos(t, "late")
	path := filepath.Join(fx, "late")
	dead := &waveRun{done: make(chan struct{}), code: 143}
	close(dead.done) // the wave is already over ...

	go func() { // ... but an orphaned descendant still writes
		time.Sleep(150 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString("late-descendant\n")
	}()

	line, err := readFifoLineOrExit(path, dead, time.Minute, time.Time{})
	if err != nil {
		t.Fatalf("read failed on a writer that outlived the wave: %v", err)
	}
	if line != "late-descendant" {
		t.Fatalf("got %q, want %q", line, "late-descendant")
	}
}

// TestGraceFor pins the arithmetic that turns a test deadline into a post-exit
// grace. It is a table test on the pure function rather than a wall-clock one
// because every interesting case here is minutes long: the only honest way to
// exercise the cap is to hand it a deadline nine minutes out.
func TestGraceFor(t *testing.T) {
	t.Parallel()

	now := time.Now()
	for _, tc := range []struct {
		name     string
		deadline time.Time
		want     time.Duration
	}{
		{"no deadline is the floor", time.Time{}, postExitGraceMin},
		{"a deadline inside the reserve is the floor", now.Add(deadlineReportReserve / 2), postExitGraceMin},
		{"an expired deadline is the floor", now.Add(-time.Minute), postExitGraceMin},
		{"less than the floor past the reserve is still the floor", now.Add(deadlineReportReserve + time.Second), postExitGraceMin},
		{"the remainder past the reserve becomes the grace", now.Add(deadlineReportReserve + 30*time.Second), 30 * time.Second},
		{"a distant deadline is capped", now.Add(deadlineReportReserve + 9*time.Minute), postExitGraceMax},
	} {
		if got := graceFor(tc.deadline, now); got != tc.want {
			t.Errorf("%s: graceFor(deadline %v from now) = %v, want %v", tc.name, tc.deadline.Sub(now), got, tc.want)
		}
	}
}

// TestReadFifoLineGraceFollowsTheTestDeadline is the wiring half: TestGraceFor
// proves the arithmetic, and this proves readFifoLineOrExit actually waits the
// duration that arithmetic returns rather than the floor it used to.
//
// It costs its own grace in wall clock, which is why the deadline here is the
// smallest one that produces a visibly extended grace instead of a realistic
// one. Pinning the elapsed time is the whole point: hand the same case a
// readFifoLineOrExit that ignores the deadline and it returns at the 5-second
// floor, a second before this test will accept.
func TestReadFifoLineGraceFollowsTheTestDeadline(t *testing.T) {
	t.Parallel()

	fx := mkFixtureFifos(t, "never-written")
	dead := &waveRun{done: make(chan struct{}), code: 9}
	close(dead.done) // the wave is already over; nobody will ever open for write

	grace := postExitGraceMin + 2*time.Second
	start := time.Now()
	_, err := readFifoLineOrExit(filepath.Join(fx, "never-written"), dead, time.Minute,
		start.Add(deadlineReportReserve+grace))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error when the wave exited without writing")
	}
	if elapsed < postExitGraceMin+time.Second {
		t.Errorf("waited %v: the deadline's %v of grace was ignored and the floor (%v) was used instead: %v",
			elapsed, grace, postExitGraceMin, err)
	}
	if elapsed > grace+10*time.Second {
		t.Errorf("waited %v, well past the %v of grace the deadline allows: %v", elapsed, grace, err)
	}
}

// forkSurvivorSuite writes a suite whose subshell forks a long-lived
// descendant and records its pid, then exits with the given status. The
// subshell exits immediately, so `sleep` is reparented to pid 1 but remains
// in the suite's process group. The pid lands outside the suite's private
// TMPDIR so the temp-file leak check plays no part in these tests.
func forkSurvivorSuite(t *testing.T, dir, name, pidFile, exit string) {
	t.Helper()
	writeSuite(t, dir, name, "( sleep 120 & echo $! >"+pidFile+" )\nexit "+exit+"\n")
}

// recordedSurvivorPid reads the pid a forkSurvivorSuite fixture recorded and
// registers a cleanup KILL so a red run of the test never leaves a
// two-minute sleep behind in CI.
func recordedSurvivorPid(t *testing.T, pidFile string) int {
	t.Helper()
	pidStr := readTrimmed(t, pidFile)
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		t.Fatalf("descendant did not record a numeric pid (got %q): %v", pidStr, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// requireReaped asserts the descendant is gone (fully reaped, so no orphan at
// ppid 1 either), giving the OS a brief window to finish the kill.
func requireReaped(t *testing.T, pid int, out *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return // descendant was reaped: the behavior we want.
		} else if err != nil {
			t.Fatalf("unexpected error probing descendant pid %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("issue #161: wave leaked an orphaned descendant (pid %d) still alive after runWave returned; wave output:\n%s",
		pid, out.String())
}

// TestGreenWaveReapsForkedDescendant pins both halves of issue #161's
// done-when for a green exit: a suite that exits 0 but leaves a live process
// in its process group must have that process killed AND must fail the wave
// loudly — the process-group counterpart of the leaked-temp-files check.
// Killing silently would leave the leaking suite's bug invisible forever.
func TestGreenWaveReapsForkedDescendant(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	forkSurvivorSuite(t, dir, "orphan", pidFile, "0")

	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"orphan"}, KillGrace: time.Second, Out: &out})
	pid := recordedSurvivorPid(t, pidFile)

	if code != 1 {
		t.Fatalf("wave exited %d, want 1: a suite that leaks a live process must fail the wave; output:\n%s",
			code, out.String())
	}
	if !strings.Contains(out.String(), "FAIL  orphan") {
		t.Fatalf("leaking suite must be reported FAIL:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "orphan passed but left process(es) alive in its process group") {
		t.Fatalf("loud process-leak diagnosis naming the suite is missing:\n%s", out.String())
	}
	requireReaped(t, pid, &out)
}

// TestRedWaveReapsForkedDescendant pins the other exit path of issue #161: a
// suite that fails on its own (nonzero exit, no shutdown signal) must still
// have its process group swept — the done-when says no process rooted in any
// run's tree survives its run's exit, not only green ones. The suite is
// already red on its own exit code; the sweep just guarantees no survivor.
func TestRedWaveReapsForkedDescendant(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	forkSurvivorSuite(t, dir, "redleak", pidFile, "1")

	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"redleak"}, KillGrace: time.Second, Out: &out})
	pid := recordedSurvivorPid(t, pidFile)

	if code != 1 {
		t.Fatalf("wave exited %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "FAIL  redleak") {
		t.Fatalf("missing FAIL redleak:\n%s", out.String())
	}
	requireReaped(t, pid, &out)
}
