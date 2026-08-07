package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// this check is what enforces it. Returns the process exit code.
func runWave(cfg waveConfig) int {
	runDir, err := os.MkdirTemp("", "serf-selftest.")
	if err != nil {
		fmt.Fprintf(cfg.Out, "serf-selftest: %v\n", err)
		return 1
	}
	defer os.RemoveAll(runDir)

	results := make([]suiteResult, len(cfg.Suites))
	done := make(chan int, len(cfg.Suites))
	for i, name := range cfg.Suites {
		go func(i int, name string) {
			results[i] = runSuite(cfg, runDir, name)
			done <- i
		}(i, name)
	}
	for range cfg.Suites {
		<-done
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

// runSuite runs one suite script with a private TMPDIR, capturing combined
// output to <runDir>/<name>.log, and leak-checks the TMPDIR on success.
func runSuite(cfg waveConfig, runDir, name string) suiteResult {
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
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return suiteResult{exitCode: 1, failure: fmt.Sprintf("serf-selftest: %s: %v", name, err)}
	}
	err = cmd.Wait()
	seconds := int(time.Since(start).Round(time.Second).Seconds())
	if err != nil {
		return suiteResult{exitCode: cmd.ProcessState.ExitCode(), seconds: seconds}
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
