package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
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
	runDir, err := os.MkdirTemp("", "serf-selftest.")
	if err != nil {
		fmt.Fprintf(cfg.Out, "serf-selftest: %v\n", err)
		return 1
	}
	defer os.RemoveAll(runDir)

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
			fmt.Fprintf(cfg.Out, "PASS  %-26s %ds\n", name, r.seconds)
			continue
		}
		fail = 1
		fmt.Fprintf(cfg.Out, "FAIL  %-26s\n", name)
		if r.failure != "" {
			fmt.Fprintf(cfg.Out, "%s\n", r.failure)
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
// signals against Wait returning, so a signal never targets a process group
// the kernel may have reused.
func runSuite(cfg waveConfig, runDir, name string, shutdown <-chan struct{}) suiteResult {
	tmp := filepath.Join(runDir, name, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return suiteResult{exitCode: 1, failure: fmt.Sprintf("serf-selftest: %s: %v", name, err)}
	}
	logFile, err := os.Create(filepath.Join(runDir, name+".log"))
	if err != nil {
		return suiteResult{exitCode: 1, failure: fmt.Sprintf("serf-selftest: %s: %v", name, err)}
	}
	defer logFile.Close()

	cmd := exec.Command(filepath.Join(cfg.ScriptsDir, name+"-selftest.sh"))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(environWithout("TMPDIR"), "TMPDIR="+tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return suiteResult{exitCode: 1, failure: fmt.Sprintf("serf-selftest: %s: %v", name, err)}
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
			syscall.Kill(-pgid, syscall.SIGTERM)
		}
		mu.Unlock()
		select {
		case <-reaped:
		case <-time.After(cfg.KillGrace):
			mu.Lock()
			if !finished {
				syscall.Kill(-pgid, syscall.SIGKILL)
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
			failure: fmt.Sprintf("serf-selftest: %s passed but leaked temp files: %s",
				name, strings.Join(leftovers, ", ")),
			seconds: seconds,
		}
	}
	return suiteResult{seconds: seconds}
}

// environWithout returns the current environment minus any existing setting
// of the named variable, so the caller's replacement is the only one.
func environWithout(name string) []string {
	env := os.Environ()
	kept := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
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
	defer f.Close()
	io.Copy(out, f)
}
