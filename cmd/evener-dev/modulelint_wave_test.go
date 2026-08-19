//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func countGlob(t *testing.T, pattern string) int {
	t.Helper()
	got, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	return len(got)
}

func TestLintRunBoundsConcurrencyToWaves(t *testing.T) {
	// Real children publish a started marker and block until the release
	// file exists. With LINT_PARALLEL=2, both wave-1 children must start
	// together and no third child may start until wave 1 is released.
	//
	// The bound is proven by ordering, not by a window: each spawn records
	// whether the release file already existed when the runner asked for
	// its command, so a runner that spawned everything at once is caught by
	// module three's record no matter how the machine is loaded. The live
	// marker count below is a second, weaker look at the same moment.
	sync := t.TempDir()
	release := filepath.Join(sync, "release")
	type spawn struct {
		module     string
		afterFirst bool
	}
	var spawns []spawn // written only on the runner's goroutine, read after it finishes
	newCmd := func(module string) *exec.Cmd {
		_, err := os.Stat(release)
		spawns = append(spawns, spawn{module: module, afterFirst: err == nil})
		script := fmt.Sprintf(`: >%s; while [ ! -f %s ]; do sleep 0.05; done`,
			filepath.Join(sync, "started."+module), release)
		return exec.Command("sh", "-c", script) //nolint:noctx // lifecycle managed by the runner's process-group stop
	}
	r, out, _ := newTestRun(t, []string{"one", "two", "three", "four", "five"}, 2, newCmd)
	code := make(chan int, 1)
	go func() { code <- r.run() }()
	started := func() int { return countGlob(t, filepath.Join(sync, "started.*")) }
	waitFor(t, "wave 1 to start", func() bool { return started() == 2 })
	if got := started(); got != 2 {
		t.Errorf("%d children started while wave 1 held, want the wave bound of 2", got)
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := <-code; got != 0 {
		t.Fatalf("run = %d, want 0 (output: %s)", got, out.String())
	}
	if got := started(); got != 5 {
		t.Errorf("%d modules ran, want all 5", got)
	}
	want := []spawn{
		{module: "one", afterFirst: false},
		{module: "two", afterFirst: false},
		{module: "three", afterFirst: true},
		{module: "four", afterFirst: true},
		{module: "five", afterFirst: true},
	}
	if !reflect.DeepEqual(spawns, want) {
		t.Errorf("spawn order/timing = %+v, want %+v (a module past the wave bound spawned before wave 1 was released)", spawns, want)
	}
}

func TestLintRunKeepsCheckingAfterAnEarlyWaveFails(t *testing.T) {
	// The whole point of the aggregate runner is one pass that reports
	// every module's findings: a failure in wave 1 must not stop wave 2
	// from being checked, and the summary must name both failures in
	// MODULES order. LINT_PARALLEL=2 over five modules puts the first
	// failure in wave 1 and the second in wave 3.
	modules := []string{"one", "two", "three", "four", "five"}
	ran := t.TempDir()
	failures := map[string]int{"one": 3, "five": 7}
	newCmd := func(module string) *exec.Cmd {
		script := fmt.Sprintf("echo stdout:%s; : >%s; exit %d",
			module, filepath.Join(ran, "ran."+module), failures[module])
		return exec.Command("sh", "-c", script) //nolint:noctx // lifecycle managed by the runner's process-group stop
	}
	r, out, _ := newTestRun(t, modules, 2, newCmd)
	if code := r.run(); code != 1 {
		t.Fatalf("run = %d, want 1 (output: %s)", code, out.String())
	}
	for _, module := range modules {
		if _, err := os.Stat(filepath.Join(ran, "ran."+module)); err != nil {
			t.Errorf("module %s was never checked after the wave-1 failure: %v", module, err)
		}
	}
	text := out.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (findings: 2/5 modules: one five)" {
		t.Errorf("final line = %q", last)
	}
	if n := summaryCount(text); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	oneIdx := strings.Index(text, "----- one -----")
	fiveIdx := strings.Index(text, "----- five -----")
	if oneIdx < 0 || fiveIdx < 0 || oneIdx > fiveIdx {
		t.Errorf("failure fences missing or out of MODULES order in %q", text)
	}
	if strings.Contains(text, "stdout:three") {
		t.Errorf("a passing later-wave module's chatter leaked into the replay: %q", text)
	}
}

func TestLintRunInterruptStopsChildrenAndSummarizes(t *testing.T) {
	sync := t.TempDir()
	pidFile := filepath.Join(sync, "child.pid")
	newCmd := func(module string) *exec.Cmd {
		script := fmt.Sprintf(`trap 'exit 143' TERM; echo $$ >%s; sleep 30 & wait $!`, pidFile)
		return exec.Command("sh", "-c", script) //nolint:noctx // lifecycle managed by the runner's process-group stop
	}
	signals := make(chan os.Signal, 1)
	r, out, _ := newTestRun(t, []string{"one"}, 1, newCmd)
	r.signals = signals
	code := make(chan int, 1)
	go func() { code <- r.run() }()
	waitFor(t, "the child to publish its pid", func() bool {
		b, err := os.ReadFile(pidFile)
		return err == nil && strings.HasSuffix(string(b), "\n")
	})
	signals <- syscall.SIGTERM
	var got int
	select {
	case got = <-code:
	case <-time.After(15 * time.Second):
		t.Fatal("interrupted run never exited")
	}
	if got != 143 {
		t.Errorf("run = %d, want 143", got)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (interrupted: SIGTERM)" {
		t.Errorf("final line = %q", last)
	}
	if n := summaryCount(out.String()); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	pidText, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the interrupted child to be gone", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("interrupted run left scratch behind: %v", left)
	}
}

func TestLintRunVanishedScratchIsResultsLost(t *testing.T) {
	// The scratch directory going away under a live run is not a lint
	// finding, and it must be said once, with the path and cause class,
	// instead of one bare diagnostic per dependent step. The test (which
	// owns this TMPDIR) removes the runner's own scratch at module three's
	// spawn — the run must stop there, not start four or five.
	sync := t.TempDir()
	newCmd := func(module string) *exec.Cmd {
		if module == "three" {
			scratch, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "evener-module-lint.*"))
			if err != nil || len(scratch) != 1 {
				t.Errorf("could not find the runner's scratch to remove: %v %v", scratch, err)
			} else if err := os.RemoveAll(scratch[0]); err != nil {
				t.Errorf("removing runner scratch: %v", err)
			}
		}
		script := ": >" + filepath.Join(sync, "ran."+module)
		return exec.Command("sh", "-c", script) //nolint:noctx // lifecycle managed by the runner's process-group stop
	}
	r, out, errOut := newTestRun(t, []string{"one", "two", "three", "four", "five"}, 1, newCmd)
	if code := r.run(); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if n := strings.Count(errOut.String(), "disappeared mid-run"); n != 1 {
		t.Errorf("vanish diagnostic appears %d times, want once: %q", n, errOut.String())
	}
	if !strings.Contains(errOut.String(), "TMPDIR reaper") {
		t.Errorf("vanish diagnosis does not name the likely cause class: %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "no such file") || strings.Contains(errOut.String(), "No such file") {
		t.Errorf("bare per-step diagnostics leaked: %q", errOut.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (results-lost: 5 modules: one two three four five)" {
		t.Errorf("final line = %q", last)
	}
	if strings.Contains(out.String(), "full logs:") {
		t.Error("retained-log pointer names a directory that is gone")
	}
	if n := summaryCount(out.String()); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	if got := countGlob(t, filepath.Join(sync, "ran.*")); got != 2 {
		t.Errorf("%d modules ran after the loss, want the run to stop at 2", got)
	}
}

func TestLintRunUnrecordableResultIsNarrowResultsLost(t *testing.T) {
	// The scratch directory survives here but module two's log cannot be
	// created: the narrower loss keeps its own shape, naming the module.
	var logdir string
	newCmd := func(module string) *exec.Cmd {
		if module == "two" {
			scratch, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "evener-module-lint.*"))
			if err != nil || len(scratch) != 1 {
				t.Errorf("could not find the runner's scratch: %v %v", scratch, err)
			} else {
				logdir = scratch[0]
				if err := os.Chmod(logdir, 0o500); err != nil {
					t.Errorf("chmod scratch: %v", err)
				}
			}
		}
		return exec.Command("true") //nolint:noctx // lifecycle managed by the runner's process-group stop
	}
	r, out, errOut := newTestRun(t, []string{"one", "two", "three"}, 1, newCmd)
	// Registered after newTestRun's t.TempDir() so this restore runs before
	// the TempDir RemoveAll (cleanups are LIFO) and the read-only scratch
	// can actually be deleted.
	t.Cleanup(func() {
		if logdir != "" {
			_ = os.Chmod(logdir, 0o755)
		}
	})
	if code := r.run(); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "lint: unable to record the result for module two") {
		t.Errorf("narrow loss does not name the module: %q", errOut.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (results-lost: unable to record the result for module two)" {
		t.Errorf("final line = %q", last)
	}
	if n := summaryCount(out.String()); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
}

func TestLintRunHonorsASignalPendingAfterTheLastWave(t *testing.T) {
	// A signal that arrives once the waves are done used to be swallowed:
	// Notify had replaced the default disposition and nothing read the
	// channel again, so the run finished as if never interrupted. With no
	// modules at all the wave loop never runs, which makes this the one
	// deterministic way to hand a pending signal to the post-wave path
	// alone — the checkpoints it pins also run between replays.
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	r, out, _ := newTestRun(t, nil, 1, echoCmd(nil))
	r.signals = signals
	if code := r.run(); code != 143 {
		t.Fatalf("run = %d, want 143 (output: %s)", code, out.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (interrupted: SIGTERM)" {
		t.Errorf("final line = %q", last)
	}
	if n := summaryCount(out.String()); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("post-wave interrupt left scratch behind: %v", left)
	}
}

// signalOnFence wraps the run's stdout and delivers a signal the moment the
// first replay fence is written — deterministically inside the replay loop,
// after every earlier checkpoint has already come up empty.
type signalOnFence struct {
	strings.Builder
	signals chan<- os.Signal
	fired   bool
}

func (w *signalOnFence) Write(p []byte) (int, error) {
	if !w.fired && strings.Contains(string(p), "-----") {
		w.fired = true
		w.signals <- syscall.SIGTERM
	}
	return w.Builder.Write(p)
}

func TestLintRunHonorsASignalDuringTheLastReplay(t *testing.T) {
	// The common swallow case: one failed module, and the signal lands
	// while its log is being replayed. Every pre-replay checkpoint has
	// already run, so only a checkpoint after the loop can drain it; the
	// shell trap fired here too, between commands, with exit 143.
	signals := make(chan os.Signal, 1)
	out := &signalOnFence{signals: signals}
	t.Setenv("TMPDIR", t.TempDir())
	var errOut strings.Builder
	r := &lintRun{
		modules:  []string{"one"},
		parallel: 1,
		stdout:   out,
		stderr:   &errOut,
		linter:   "sh",
		newCmd:   echoCmd(map[string]int{"one": 7}),
		signals:  signals,
		grace:    5 * time.Second,
	}
	if code := r.run(); code != 143 {
		t.Fatalf("run = %d, want 143 (output: %s)", code, out.String())
	}
	text := out.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (interrupted: SIGTERM)" {
		t.Errorf("final line = %q", last)
	}
	if strings.Contains(text, "full logs:") {
		t.Error("an interrupted replay still printed the retained-log pointer")
	}
	if n := summaryCount(text); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("interrupt during replay left scratch behind: %v", left)
	}
}

func TestLintRunReclaimsAbandonedScratchAtStartup(t *testing.T) {
	// A SIGKILLed run's scratch has no trap to clean it; the NEXT run of the
	// same tool reclaims it (dead-pid scoped, covscratch rules — the rules
	// themselves are pinned by internal/devtool/scratch's own tests; this
	// pins that module-lint actually goes through them).
	r, _, errOut := newTestRun(t, []string{"one"}, 1, echoCmd(nil))
	tmp := os.Getenv("TMPDIR")
	deadChild := exec.Command("true") //nolint:noctx // exits immediately; reaped by Run
	if err := deadChild.Run(); err != nil {
		t.Fatal(err)
	}
	deadPid := deadChild.Process.Pid
	abandoned := filepath.Join(tmp, fmt.Sprintf("evener-module-lint.%d", deadPid))
	if err := os.MkdirAll(filepath.Join(abandoned, "0.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	keeper := filepath.Join(tmp, "evener-module-lint.notapid")
	if err := os.Mkdir(keeper, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := r.run(); code != 0 {
		t.Fatalf("run = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("dead-pid scratch %s survived the next run (stat err %v)", abandoned, err)
	}
	if _, err := os.Stat(keeper); err != nil {
		t.Errorf("non-pid-suffixed sibling was wrongly reclaimed: %v", err)
	}
}

func TestModuleLintInvalidParallelIsTwoLineSetupFailure(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var out, errOut strings.Builder
	got := moduleLint(lintEnv(map[string]string{"MODULES": ". agent", "LINT_PARALLEL": "08"}), &out, &errOut, nil, time.Second)
	if got != 2 {
		t.Fatalf("moduleLint = %d, want 2", got)
	}
	combined := errOut.String() + out.String()
	lines := strings.Split(strings.TrimSuffix(combined, "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("invalid LINT_PARALLEL wrote %d lines, want a diagnostic and a summary: %q", len(lines), combined)
	}
	if !strings.Contains(lines[0], "LINT_PARALLEL must be a positive integer") {
		t.Errorf("diagnostic line = %q", lines[0])
	}
	if lines[1] != "FAIL lint (setup: LINT_PARALLEL must be a positive integer without leading zeroes)" {
		t.Errorf("summary line = %q", lines[1])
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("setup refusal created scratch: %v", left)
	}
}

func TestLintSignalNamesAndExits(t *testing.T) {
	cases := []struct {
		sig  syscall.Signal
		name string
		code int
	}{
		{syscall.SIGHUP, "SIGHUP", 129},
		{syscall.SIGINT, "SIGINT", 130},
		{syscall.SIGTERM, "SIGTERM", 143},
	}
	for _, c := range cases {
		name, code := lintSignalNameAndExit(c.sig)
		if name != c.name || code != c.code {
			t.Errorf("lintSignalNameAndExit(%v) = (%s, %d), want (%s, %d)", c.sig, name, code, c.name, c.code)
		}
	}
}
