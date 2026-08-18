package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/internal/devtool/procgroup"
	"primeradiant.com/serf/internal/devtool/report"
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

func (l *lintRun) run() int {
	rep := report.New(l.stdout, "lint")
	start := time.Now()
	_, _ = fmt.Fprintf(l.stdout, "lint: checking %d modules\n", len(l.modules))

	logdir, err := os.MkdirTemp("", "serf-module-lint.*")
	if err != nil {
		_, _ = fmt.Fprintf(l.stderr, "lint: %v\n", err)
		rep.Fail(report.Setup, "unable to create temporary log directory")
		return 1
	}
	keepLogs := false
	defer func() {
		if !keepLogs {
			_ = os.RemoveAll(logdir)
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
		l.runWave(logdir, first, last, statuses)
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
	for i := range l.modules {
		if statuses[i] == 0 {
			_ = os.Remove(lintLogPath(logdir, i))
		}
	}
	names := make([]string, 0, len(failed))
	for _, i := range failed {
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

// runWave starts modules[first:last] together and waits for all of them.
func (l *lintRun) runWave(logdir string, first, last int, statuses []int) {
	done := make(chan reapResult, last-first)
	pending := 0
	for i := first; i < last; i++ {
		cmd := l.newCmd(l.modules[i])
		logf, err := os.Create(lintLogPath(logdir, i))
		if err != nil {
			statuses[i] = 1
			continue
		}
		cmd.Stdout = logf
		cmd.Stderr = logf
		if err := procgroup.Start(cmd); err != nil {
			_, _ = fmt.Fprintf(logf, "lint: %v\n", err)
			_ = logf.Close()
			statuses[i] = 1
			continue
		}
		pending++
		go func(i int, cmd *exec.Cmd, logf *os.File) {
			_ = cmd.Wait()
			_ = logf.Close()
			done <- reapResult{index: i, status: procgroup.ExitCode(cmd.ProcessState)}
		}(i, cmd, logf)
	}
	for ; pending > 0; pending-- {
		r := <-done
		statuses[r.index] = r.status
	}
}

func lintLogPath(logdir string, index int) string {
	return filepath.Join(logdir, strconv.Itoa(index)+".log")
}

func lintMain(args []string) int {
	return 1
}
