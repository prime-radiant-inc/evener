package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/serf/envvars"
)

// waveConfig describes one selftest wave: which suites to run, where their
// scripts live, and how the wave reacts to signals. KillGrace is how long a
// TERMed suite gets to exit before its process group is KILLed.
type waveConfig struct {
	ScriptsDir string
	Suites     []string
	KillGrace  time.Duration
	Out        io.Writer
	Signals    <-chan os.Signal
}

// suiteResult is one suite's outcome. failure is non-empty when the suite
// failed for a reason beyond its exit status (leaked temp files, unrunnable).
type suiteResult struct {
	exitCode int
	failure  string
	seconds  int
}

// runWave runs every suite concurrently, each in its own process group with a
// private TMPDIR, and reports one PASS/FAIL line per suite in cfg.Suites
// order. A failing suite's whole log is replayed. A suite that exits zero but
// leaves anything in its TMPDIR fails: suites clean up after themselves, and
// this check is what enforces it. HUP/INT/TERM on cfg.Signals forward to
// every running suite's process group (KILL after cfg.KillGrace), the wave
// waits for every suite to be reaped, and the exit code is 128+signal.
func runWave(cfg waveConfig) int {
	runDir, err := os.MkdirTemp("", "serf-test-dev-tooling.")
	if err != nil {
		_, _ = fmt.Fprintf(cfg.Out, "serf-test-dev-tooling: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(runDir) }()
	if err := writeMktempShim(runDir); err != nil {
		_, _ = fmt.Fprintf(cfg.Out, "serf-test-dev-tooling: %v\n", err)
		return 1
	}

	// shutdown is closed by the signal listener; every suite's watcher
	// goroutine sees it and signals its own child. waveDone stops the
	// listener on a normal finish.
	shutdown := make(chan struct{})
	waveDone := make(chan struct{})
	var interrupted syscall.Signal
	var interruptOnce sync.Once
	if cfg.Signals != nil {
		go func() {
			select {
			case sig := <-cfg.Signals:
				interruptOnce.Do(func() {
					if s, ok := sig.(syscall.Signal); ok {
						interrupted = s
					} else {
						interrupted = syscall.SIGTERM
					}
					close(shutdown)
				})
			case <-waveDone:
			}
		}()
	}

	results := make([]suiteResult, len(cfg.Suites))
	done := make(chan int, len(cfg.Suites))
	for i, name := range cfg.Suites {
		go func(i int, name string) {
			results[i] = runSuite(cfg, runDir, name, shutdown)
			done <- i
		}(i, name)
	}
	for range cfg.Suites {
		<-done
	}
	close(waveDone)

	select {
	case <-shutdown:
		// Every suite is already reaped (the loop above waited on them all),
		// so cleanup is just the deferred RemoveAll. Match the shell recipe:
		// an interrupted wave reports nothing and exits 128+signal.
		return 128 + int(interrupted)
	default:
	}

	fail := 0
	for i, name := range cfg.Suites {
		r := results[i]
		if r.exitCode == 0 && r.failure == "" {
			_, _ = fmt.Fprintf(cfg.Out, "PASS  %-26s %ds\n", name, r.seconds)
			continue
		}
		fail = 1
		_, _ = fmt.Fprintf(cfg.Out, "FAIL  %-26s\n", name)
		if r.failure != "" {
			_, _ = fmt.Fprintf(cfg.Out, "%s\n", r.failure)
		}
		replayLog(cfg.Out, filepath.Join(runDir, name+".log"))
	}
	return fail
}

// runSuite runs one suite script in its own process group with a private
// TMPDIR, capturing combined output to <runDir>/<name>.log, and leak-checks
// the TMPDIR on success. On shutdown it TERMs the suite's process group so
// forked descendants get the signal too, then KILLs the group if the suite
// has not exited within the grace period. The per-suite mutex orders those
// signals against the post-Wait bookkeeping, narrowing the reuse window to
// the instant between the runtime's reap inside Wait and the finished-flag
// update; closing it fully would need waitid(WNOWAIT), which pure Go
// doesn't expose. The residual window is microseconds wide and requires an
// immediate pid wraparound.
func runSuite(cfg waveConfig, runDir, name string, shutdown <-chan struct{}) suiteResult {
	tmp := filepath.Join(runDir, name, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return suiteResult{exitCode: 1, failure: fmt.Sprintf("serf-test-dev-tooling: %s: %v", name, err)}
	}
	logFile, err := os.Create(filepath.Join(runDir, name+".log"))
	if err != nil {
		return suiteResult{exitCode: 1, failure: fmt.Sprintf("serf-test-dev-tooling: %s: %v", name, err)}
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.CommandContext(context.Background(), filepath.Join(cfg.ScriptsDir, name+"-selftest.sh"))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(environWithout(envvars.TmpDir.Name, envvars.Path.Name),
		envvars.TmpDir.Name+"="+tmp,
		envvars.Path.Name+"="+filepath.Join(runDir, "bin")+":"+os.Getenv(envvars.Path.Name))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return suiteResult{exitCode: 1, failure: fmt.Sprintf("serf-test-dev-tooling: %s: %v", name, err)}
	}

	var mu sync.Mutex
	finished := false
	reaped := make(chan struct{})
	pgid := cmd.Process.Pid
	go func() {
		select {
		case <-shutdown:
		case <-reaped:
			return
		}
		mu.Lock()
		if !finished {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}
		mu.Unlock()
		select {
		case <-reaped:
		case <-time.After(cfg.KillGrace):
			mu.Lock()
			if !finished {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			mu.Unlock()
		}
	}()

	err = cmd.Wait()
	mu.Lock()
	finished = true
	mu.Unlock()
	close(reaped)
	seconds := int(time.Since(start).Round(time.Second).Seconds())
	if err != nil {
		code := cmd.ProcessState.ExitCode()
		if code < 0 {
			// Killed by a signal (no exit status): report as 128+signal the
			// way a shell would.
			if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			} else {
				code = 1
			}
		}
		return suiteResult{exitCode: code, seconds: seconds}
	}
	if leftovers := listDir(tmp); len(leftovers) > 0 {
		return suiteResult{
			failure: fmt.Sprintf("serf-test-dev-tooling: %s passed but leaked temp files: %s",
				name, strings.Join(leftovers, ", ")),
			seconds: seconds,
		}
	}
	return suiteResult{seconds: seconds}
}

// writeMktempShim installs <runDir>/bin/mktemp, which every suite sees first
// on PATH. macOS mktemp -t ignores TMPDIR (docs/testing.md, kata cqne), so
// without this shim a suite's `mktemp -d -t x` would escape its private
// sandbox and the leak check could never see what it left behind. The shim
// rewrites `-t prefix` (and the bare no-template form, which -t underlies)
// against TMPDIR; explicit templates pass through untouched. Suites whose
// fixtures reset PATH bypass the shim; those fixtures fake mktemp themselves.
func writeMktempShim(runDir string) error {
	binDir := filepath.Join(runDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		return err
	}
	shim := fmt.Sprintf(`#!/bin/sh
flags=""
prefix=""
template=""
while [ $# -gt 0 ]; do
	case "$1" in
	-t) shift; prefix="$1" ;;
	-*) flags="$flags $1" ;;
	*) template="$1" ;;
	esac
	shift
done
if [ -n "$template" ]; then
	exec /usr/bin/mktemp $flags "$template"
fi
exec /usr/bin/mktemp $flags "${%s:-/tmp}/${prefix:-tmp}.XXXXXX"
`, envvars.TmpDir.Name)
	return os.WriteFile(filepath.Join(binDir, "mktemp"), []byte(shim), 0o755)
}

// environWithout returns the current environment minus any existing setting
// of the named variables, so the caller's replacements are the only ones.
func environWithout(names ...string) []string {
	env := os.Environ()
	kept := env[:0]
	for _, kv := range env {
		drop := false
		for _, name := range names {
			if strings.HasPrefix(kv, name+"=") {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, kv)
		}
	}
	return kept
}

func listDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{fmt.Sprintf("(unreadable: %v)", err)}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func replayLog(out io.Writer, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = io.Copy(out, f)
}
