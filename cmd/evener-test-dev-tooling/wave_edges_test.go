//go:build linux || darwin

package devtoolingtest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunSuiteMkdirAllFail covers the os.MkdirAll error path in runSuite.
func TestRunSuiteMkdirAllFail(t *testing.T) {
	// Create a file where the suite's tmp directory should go, so MkdirAll fails.
	dir := t.TempDir()
	conflict := filepath.Join(dir, "suite-name")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	shutdown := make(chan struct{})
	defer close(shutdown)
	r := runSuite(waveConfig{
		ScriptsDir: dir,
		KillGrace:  time.Second,
	}, dir, "suite-name", shutdown)
	if r.exitCode != 1 || r.failure == "" {
		t.Fatalf("runSuite with MkdirAll failure should return exit 1 with failure, got %+v", r)
	}
	if !strings.Contains(r.failure, "suite-name") {
		t.Fatalf("failure should name the suite: %q", r.failure)
	}
}

// TestRunSuiteCreateLogFail covers the os.Create error path in runSuite.
func TestRunSuiteCreateLogFail(t *testing.T) {
	// Create the suite tmp dir as a regular file so that Create on the log
	// file in the parent dir works, but the log file path is in a directory
	// that is a file. Actually, we need the log file path's PARENT to be
	// a file. The log file is at <runDir>/<name>.log. So we need runDir to
	// be a file.
	dir := t.TempDir()
	// The log file is at filepath.Join(runDir, name+".log"). If we make
	// runDir a file... but runSuite also does MkdirAll(filepath.Join(runDir, name, "tmp"))
	// first. So we need MkdirAll to succeed but Create to fail.
	// We can do this by making the log file path itself a directory.
	logPath := filepath.Join(dir, "suite-name.log")
	if err := os.MkdirAll(logPath, 0o755); err != nil {
		t.Fatal(err)
	}
	shutdown := make(chan struct{})
	defer close(shutdown)
	r := runSuite(waveConfig{
		ScriptsDir: dir,
		KillGrace:  time.Second,
	}, dir, "suite-name", shutdown)
	if r.exitCode != 1 || r.failure == "" {
		t.Fatalf("runSuite with Create failure should return exit 1 with failure, got %+v", r)
	}
	if !strings.Contains(r.failure, "suite-name") {
		t.Fatalf("failure should name the suite: %q", r.failure)
	}
}

// TestRunWaveNonSyscallSignal covers the path where a non-syscall signal is
// received by the wave's signal handler (line 86-87).
func TestRunWaveNonSyscallSignal(t *testing.T) {
	// Use a real script that sleeps long enough for the signal handler
	// goroutine to process the pre-sent signal before the suite exits.
	// A nonexistent script fails instantly via fork/exec, racing the
	// signal handler and making the exit code nondeterministic.
	dir := t.TempDir()
	script := filepath.Join(dir, "sleeping-selftest.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	var out bytes.Buffer
	// Send a non-syscall signal before the wave starts
	signals <- fakeWaveSignal{}
	code := runWave(waveConfig{
		ScriptsDir: dir,
		Suites:     []string{"sleeping"},
		KillGrace:  time.Second,
		Out:        &out,
		Signals:    signals,
	})
	// The wave should exit with 128+15=143 (SIGTERM for non-syscall signals)
	if code != 143 {
		t.Fatalf("non-syscall signal exit = %d, want 143; output: %s", code, out.String())
	}
}

type fakeWaveSignal struct{}

func (fakeWaveSignal) String() string { return "FAKE" }
func (fakeWaveSignal) Signal()        {}

// TestRunWaveWriteMktempShimFail covers the writeMktempShim error path in
// runWave (line 67-69). We make the runDir's bin a file so the shim write fails.
// However, runWave creates its own temp dir via os.MkdirTemp, so we can't
// directly control it. Instead, we can test writeMktempShim failure indirectly
// by making the system unable to create the bin dir. This is hard to do
// deterministically, so we skip this test.
func TestRunWaveWriteMktempShimFailSkipped(t *testing.T) {
	t.Skip("writeMktempShim failure in runWave requires controlling the temp dir, which is not feasible")
}
