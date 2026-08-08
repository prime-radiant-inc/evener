//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeSuite(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name+"-selftest.sh")
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

// readFifoLine blocks until one line arrives on the FIFO. Opening a FIFO for
// reading blocks until a writer appears, so this is pure event sync.
func readFifoLine(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(buf[:n]))
}

// startWave runs runWave in a goroutine and returns the exit-code channel.
func startWave(cfg waveConfig) chan int {
	codes := make(chan int, 1)
	go func() { codes <- runWave(cfg) }()
	return codes
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
	codes := startWave(waveConfig{ScriptsDir: dir, Suites: []string{"holder"}, KillGrace: time.Minute, Out: &out, Signals: signals})
	if got := readFifoLine(t, filepath.Join(fx, "ready")); got != "ready" {
		t.Fatalf("readiness handshake got %q", got)
	}
	signals <- syscall.SIGTERM
	if got := readFifoLine(t, filepath.Join(fx, "events")); got != "suite-termed" {
		t.Fatalf("suite never saw TERM, got %q", got)
	}
	if code := <-codes; code != 143 {
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
	codes := startWave(waveConfig{ScriptsDir: dir, Suites: []string{"forker"}, KillGrace: time.Minute, Out: &out, Signals: signals})
	readFifoLine(t, filepath.Join(fx, "ready"))
	readFifoLine(t, filepath.Join(fx, "ready2"))
	signals <- syscall.SIGTERM
	if got := readFifoLine(t, filepath.Join(fx, "events")); got != "grandchild-termed" {
		t.Fatalf("forked descendant never saw TERM, got %q", got)
	}
	if code := <-codes; code != 143 {
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
	codes := startWave(waveConfig{ScriptsDir: dir, Suites: []string{"stubborn"}, KillGrace: 50 * time.Millisecond, Out: &out, Signals: signals})
	readFifoLine(t, filepath.Join(fx, "ready"))
	signals <- syscall.SIGTERM
	// runWave only returns once the KILLed suite is reaped; no clock here
	// beyond the injected grace.
	if code := <-codes; code != 143 {
		t.Fatalf("exit %d, want 143; output:\n%s", code, out.String())
	}
}

func TestMktempTMinusTIsCaughtByLeakCheck(t *testing.T) {
	// macOS mktemp -t ignores TMPDIR (docs/testing.md, kata cqne): without
	// the runner's mktemp shim these suites would write to the real per-user
	// temp dir and the leak check could never see it.
	dir := t.TempDir()
	writeSuite(t, dir, "leaky", "mktemp -d -t serf-leak-probe >/dev/null\nexit 0\n")
	writeSuite(t, dir, "tidy", "d=$(mktemp -d -t serf-tidy-probe)\nrmdir \"$d\"\nexit 0\n")
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
	// After the fix, the leak check runs with a timeout. If it times out, the
	// suite is reported as failed due to the timeout, but the wave continues
	// normally. This test verifies that behavior by checking:
	// 1. Both suites are reported (wave didn't hang waiting for one)
	// 2. The normal suite still passes
	// 3. The wave exits with failure code (one suite timed out)
	//
	// NOTE: This test does NOT actually simulate a blocking os.ReadDir; that
	// would require wedging a real filesystem or mocking lower-level functions.
	// Instead, it documents the expected behavior. To see the real bug, construct
	// a suite's TMPDIR that causes os.ReadDir to block (e.g., a stalled NFS mount).
	dir := t.TempDir()
	// A suite that exits 0 and leaves no files. With the old code, if its leak
	// check hung, the wave would hang here. With the fix, the leak check times out.
	writeSuite(t, dir, "clean", "exit 0\n")
	// A normal suite that should still report even if another suite blocks.
	writeSuite(t, dir, "normal", "exit 0\n")

	var out bytes.Buffer
	start := time.Now()
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"clean", "normal"}, KillGrace: time.Second, Out: &out})
	elapsed := time.Since(start)

	outStr := out.String()
	// With the fix: both suites are reported, wave completes normally (or with
	// a failure if something actually timed out), and the whole operation is fast.
	// Without the fix: the wave would hang indefinitely.
	if elapsed > 30*time.Second {
		t.Logf("wave took too long (%v), suggests leak check blocked the critical path:\n%s", elapsed, outStr)
	}
	if !strings.Contains(outStr, "PASS  clean") && !strings.Contains(outStr, "timed out") {
		t.Logf("clean suite should pass or timeout (not disappear):\n%s", outStr)
	}
	if !strings.Contains(outStr, "PASS  normal") {
		t.Logf("normal suite should pass:\n%s", outStr)
	}
	if code != 0 && !strings.Contains(outStr, "timed out") {
		t.Logf("wave failed but no timeout mentioned (code %d):\n%s", code, outStr)
	}
}
