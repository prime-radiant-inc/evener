package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/devtool/procgroup"
	"primeradiant.com/serf/internal/devtool/report"
	"primeradiant.com/serf/internal/devtool/scratch"
)

// The defaults are the interface run-module-lint.sh shipped with; the
// Makefile's lint-golangci target overrides MODULES with $(GO_MODULES), and
// operators rely on the bare invocation checking the canonical set.
const (
	defaultLintModules  = ". agent llm auth envvars invariant identifier"
	defaultLintParallel = 4
)

type lintConfig struct {
	Modules  []string
	Parallel int
}

// parseLintConfig reads the env-only interface: MODULES (whitespace-split,
// caller order preserved) and LINT_PARALLEL. An empty variable means its
// default. The returned diagnostic is non-empty exactly when the
// configuration is unusable; the caller prints it and summarizes as a setup
// failure without creating anything.
func parseLintConfig(getenv func(string) string) (lintConfig, string) {
	modules := getenv("MODULES")
	if modules == "" {
		modules = defaultLintModules
	}
	parallel := defaultLintParallel
	if v := getenv("LINT_PARALLEL"); v != "" {
		n, err := strconv.Atoi(v)
		if !isPositiveInteger(v) || err != nil {
			return lintConfig{}, "lint: LINT_PARALLEL must be a positive integer without leading zeroes (got " + v + ")"
		}
		parallel = n
	}
	return lintConfig{Modules: strings.Fields(modules), Parallel: parallel}, ""
}

// isPositiveInteger accepts exactly the shell contract's ^[1-9][0-9]*$:
// strconv would take "08" as 8, silently blessing a value the interface
// rejects.
func isPositiveInteger(s string) bool {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return false
	}
	for _, d := range s[1:] {
		if d < '0' || d > '9' {
			return false
		}
	}
	return true
}

// lintRun is one module-lint run: waves of parallel children, each a real
// linter process in its own group, logs per module, one summary at the end.
type lintRun struct {
	modules  []string
	parallel int
	stdout   io.Writer
	stderr   io.Writer
	linter   string
	newCmd   func(module string) *exec.Cmd
	signals  <-chan os.Signal
	grace    time.Duration
}

// reapResult carries one child's verdict from its wait goroutine.
type reapResult struct {
	index  int
	status int
}

// waveEnd reports why a wave ended before its verdicts were all recorded.
// The zero value is a wave that completed normally.
type waveEnd struct {
	interrupted bool
	sig         syscall.Signal
	vanished    bool
	lost        string
}

func (l *lintRun) run() int {
	rep := report.New(l.stdout, "lint")
	start := time.Now()
	_, _ = fmt.Fprintf(l.stdout, "lint: checking %d modules\n", len(l.modules))

	// Acquire reclaims this tool's dead-pid leftovers (a SIGKILLed run has
	// no trap) before minting serf-module-lint.<pid>. A findings run keeps
	// the directory deliberately — the logs are the only record of why — so
	// that path skips Release and the NEXT run reclaims it, the covscratch
	// discipline the coverage runners follow.
	scratchDir, err := scratch.Acquire("serf-module-lint", l.stderr)
	if err != nil {
		_, _ = fmt.Fprintf(l.stderr, "lint: %v\n", err)
		rep.Fail(report.Setup, "unable to create temporary log directory")
		return 1
	}
	logdir := scratchDir.Path()
	keepLogs := false
	defer func() {
		if !keepLogs {
			scratchDir.Release()
		}
	}()

	if _, err := exec.LookPath(l.linter); err != nil {
		_, _ = fmt.Fprintf(l.stderr, "lint: %v\n", err)
		rep.Fail(report.NotChecked, fmt.Sprintf("%d modules: %s", len(l.modules), strings.Join(l.modules, " ")))
		return 127
	}

	statuses := make([]int, len(l.modules))
	for first := 0; first < len(l.modules); first += l.parallel {
		last := min(first+l.parallel, len(l.modules))
		// Nothing in this run deletes anything under logdir before cleanup,
		// so its disappearance is the scratch space going away rather than
		// a lint finding; stop at the loss instead of starting more waves.
		if _, err := os.Stat(logdir); err != nil {
			return l.vanishedExit(rep, logdir)
		}
		switch end := l.runWave(logdir, first, last, statuses); {
		case end.interrupted:
			name, code := lintSignalNameAndExit(end.sig)
			rep.Fail(report.Interrupted, name)
			return code
		case end.vanished:
			return l.vanishedExit(rep, logdir)
		case end.lost != "":
			_, _ = fmt.Fprintf(l.stderr, "lint: unable to record the result for module %s\n", end.lost)
			rep.Fail(report.ResultsLost, "unable to record the result for module "+end.lost)
			return 1
		}
	}

	// Waves consult the signal channel while they wait; past this point
	// nothing else would, and Notify has replaced the default disposition,
	// so an unread signal would be swallowed and the run would finish as if
	// never interrupted. These checkpoints (here and between replays) give
	// the post-wave path the same granularity the shell trap had between
	// commands. All children are reaped by now, so honoring the signal is
	// just cleanup and the interrupted summary.
	if code, interrupted := l.pendingSignal(rep); interrupted {
		return code
	}

	var failed []int
	for i := range l.modules {
		if statuses[i] != 0 {
			failed = append(failed, i)
		}
	}
	if len(failed) == 0 {
		rep.Pass(fmt.Sprintf("%d modules, %ds", len(l.modules), int(time.Since(start).Seconds())))
		return 0
	}
	// The retained-log pointer must never name a directory that is gone.
	if _, err := os.Stat(logdir); err != nil {
		return l.vanishedExit(rep, logdir)
	}
	for i := range l.modules {
		if statuses[i] == 0 {
			_ = os.Remove(lintLogPath(logdir, i))
		}
	}
	names := make([]string, 0, len(failed))
	for _, i := range failed {
		if code, interrupted := l.pendingSignal(rep); interrupted {
			return code
		}
		names = append(names, l.modules[i])
		f, err := os.Open(lintLogPath(logdir, i))
		if err != nil {
			continue
		}
		report.Replay(l.stdout, l.modules[i], f)
		_ = f.Close()
	}
	report.RetainedPointer(l.stdout, logdir)
	keepLogs = true
	rep.Fail(report.Findings, fmt.Sprintf("%d/%d modules: %s", len(failed), len(l.modules), strings.Join(names, " ")))
	return 1
}

// runWave starts modules[first:last] together, waits for all of them, and
// relays an interrupt to every running child's process group before
// reporting it.
func (l *lintRun) runWave(logdir string, first, last int, statuses []int) waveEnd {
	type child struct {
		pgid   int
		reaped chan struct{}
	}
	done := make(chan reapResult, last-first)
	var started []child
	pending := 0
	var end waveEnd
	for i := first; i < last; i++ {
		cmd := l.newCmd(l.modules[i])
		logf, err := os.Create(lintLogPath(logdir, i))
		if err != nil {
			if _, statErr := os.Stat(logdir); statErr != nil {
				end.vanished = true
			} else {
				end.lost = l.modules[i]
			}
			break
		}
		cmd.Stdout = logf
		cmd.Stderr = logf
		if err := procgroup.Start(cmd); err != nil {
			// An unstartable module (a missing directory, most likely) is
			// that module's failure, not the run's: the error is its log.
			_, _ = fmt.Fprintf(logf, "lint: %v\n", err)
			_ = logf.Close()
			statuses[i] = 1
			continue
		}
		reaped := make(chan struct{})
		started = append(started, child{pgid: cmd.Process.Pid, reaped: reaped})
		pending++
		go func(i int, cmd *exec.Cmd, logf *os.File, reaped chan struct{}) {
			_ = cmd.Wait()
			_ = logf.Close()
			close(reaped)
			done <- reapResult{index: i, status: procgroup.ExitCode(cmd.ProcessState)}
		}(i, cmd, logf, reaped)
	}
	stopAll := func() {
		for _, c := range started {
			// A reaped child's pid may already be recycled; skip it here
			// (and procgroup.Stop re-checks) so a group TERM never aims at
			// a stale identifier. See procgroup.Stop for the residual
			// microsecond check-vs-reap window this leaves.
			select {
			case <-c.reaped:
				continue
			default:
			}
			go procgroup.Stop(c.pgid, c.reaped, l.grace)
		}
	}
	if end.vanished || end.lost != "" {
		stopAll()
	}
	for pending > 0 {
		select {
		case r := <-done:
			statuses[r.index] = r.status
			pending--
		case sig := <-l.signals:
			if s, ok := sig.(syscall.Signal); ok {
				end.sig = s
			} else {
				end.sig = syscall.SIGTERM
			}
			end.interrupted = true
			stopAll()
			for pending > 0 {
				r := <-done
				statuses[r.index] = r.status
				pending--
			}
		}
	}
	return end
}

// pendingSignal drains one waiting interrupt, if any, and turns it into the
// interrupted summary and exit code. Callers return immediately on true;
// the deferred Release removes the logs.
func (l *lintRun) pendingSignal(rep *report.Reporter) (int, bool) {
	select {
	case sig := <-l.signals:
		s, ok := sig.(syscall.Signal)
		if !ok {
			s = syscall.SIGTERM
		}
		name, code := lintSignalNameAndExit(s)
		rep.Fail(report.Interrupted, name)
		return code, true
	default:
		return 0, false
	}
}

// vanishedExit is the results-lost exit for scratch that went away under a
// live run: one diagnosis with the path and likely cause class, instead of
// one bare diagnostic per dependent step.
func (l *lintRun) vanishedExit(rep *report.Reporter, logdir string) int {
	_, _ = fmt.Fprintf(l.stderr, "lint: the temporary log directory disappeared mid-run: %s\n", logdir)
	// The env name comes from the registry row (envvars_audit_test.go's
	// rule), spliced so the diagnostic keeps the shell runner's exact bytes.
	_, _ = fmt.Fprintf(l.stderr, "lint: nothing in this run removes it before cleanup, so something outside did; a %s reaper under disk pressure is the usual suspect on macOS\n", envvars.TmpDir.Name)
	rep.Fail(report.ResultsLost, fmt.Sprintf("%d modules: %s", len(l.modules), strings.Join(l.modules, " ")))
	return 1
}

func lintLogPath(logdir string, index int) string {
	return filepath.Join(logdir, strconv.Itoa(index)+".log")
}

// lintSignalNameAndExit maps an interrupt to the name its summary prints and
// the 128+signal exit code a shell reports.
func lintSignalNameAndExit(sig syscall.Signal) (string, int) {
	switch sig {
	case syscall.SIGHUP:
		return "SIGHUP", 129
	case syscall.SIGINT:
		return "SIGINT", 130
	case syscall.SIGTERM:
		return "SIGTERM", 143
	}
	return fmt.Sprintf("SIG%d", int(sig)), 128 + int(sig)
}

// moduleLint is the subcommand body behind lintMain, parameterized for tests.
func moduleLint(getenv func(string) string, stdout, stderr io.Writer, signals <-chan os.Signal, grace time.Duration) int {
	cfg, diag := parseLintConfig(getenv)
	if diag != "" {
		_, _ = fmt.Fprintln(stderr, diag)
		report.New(stdout, "lint").Fail(report.Setup, "LINT_PARALLEL must be a positive integer without leading zeroes")
		return 2
	}
	r := &lintRun{
		modules:  cfg.Modules,
		parallel: cfg.Parallel,
		stdout:   stdout,
		stderr:   stderr,
		linter:   "golangci-lint",
		newCmd:   golangciCmd,
		signals:  signals,
		grace:    grace,
	}
	return r.run()
}

func golangciCmd(module string) *exec.Cmd {
	// This runner already bounds child concurrency; disable the linter's
	// process-global exclusion so every child can perform its module check.
	cmd := exec.Command("golangci-lint", "run", "--allow-parallel-runners", "./...") //nolint:noctx // lifecycle is managed by the runner's process-group TERM/KILL escalation
	cmd.Dir = module
	return cmd
}

func lintMain(args []string) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	return moduleLint(os.Getenv, os.Stdout, os.Stderr, signals, 5*time.Second)
}
