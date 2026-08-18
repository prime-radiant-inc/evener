//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These run the real golangci-lint — never a stand-in. Where `make lint`
// runs, the binary exists; a machine without it skips with the reason.

func requireGolangciLint(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: runs the real golangci-lint")
	}
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skipf("golangci-lint not installed: %v", err)
	}
}

func writeFixtureModule(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture.example/lint\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestModuleLintPassesACleanModuleWithRealGolangciLint(t *testing.T) {
	requireGolangciLint(t)
	dir := writeFixtureModule(t, "package fixture\n\n// Ok exists to be lint-clean.\nfunc Ok() int { return 1 }\n")
	t.Setenv("TMPDIR", t.TempDir())
	var out, errOut strings.Builder
	code := moduleLint(lintEnv(map[string]string{"MODULES": dir}), &out, &errOut, nil, 5*time.Second)
	if code != 0 {
		t.Fatalf("moduleLint = %d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("clean module output = %q, want two quiet lines", out.String())
	}
	if !regexp.MustCompile(`^PASS lint \(1 modules, \d+s\)$`).MatchString(lines[len(lines)-1]) {
		t.Errorf("summary = %q", lines[len(lines)-1])
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("clean integration run left scratch: %v", left)
	}
}

func TestModuleLintHonorsARealSignalThroughTheBinary(t *testing.T) {
	// The in-process interrupt test injects into the channel seam, so it
	// cannot notice a signal dropped from the Notify list. This one sends
	// a real SIGTERM to the built binary mid-run. Child-reap mechanics are
	// pinned in-process (TestLintRunInterruptStopsChildrenAndSummarizes);
	// here the pin is delivery: exit 143, the interrupted summary, and no
	// scratch left. The TERM lands during golangci-lint's startup — the
	// scratch dir appears milliseconds into the run and the linter takes
	// hundreds of milliseconds, so the margin is wide.
	requireGolangciLint(t)
	dir := writeFixtureModule(t, "package fixture\n\n// Ok exists to be lint-clean.\nfunc Ok() int { return 1 }\n")
	tmp := t.TempDir()
	cmd := exec.Command(buildSerfDev(t), "module-lint") //nolint:noctx // stopped by the SIGTERM under test, reaped by Wait
	var kept []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMPDIR=") && !strings.HasPrefix(kv, "MODULES=") && !strings.HasPrefix(kv, "LINT_PARALLEL=") {
			kept = append(kept, kv)
		}
	}
	kept = append(kept, "TMPDIR="+tmp, "MODULES="+dir)
	cmd.Env = kept
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		scratch, err := filepath.Glob(filepath.Join(tmp, "evener-module-lint.*"))
		if err == nil && len(scratch) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never minted its scratch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case <-waitErr:
	case <-time.After(15 * time.Second):
		t.Fatal("the binary survived SIGTERM past the escalation window")
	}
	if code := cmd.ProcessState.ExitCode(); code != 143 {
		t.Errorf("binary exited %d, want 143 (output: %s)", code, out.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (interrupted: SIGTERM)" {
		t.Errorf("final line = %q", last)
	}
	left, err := filepath.Glob(filepath.Join(tmp, "evener-module-lint.*"))
	if err != nil || len(left) != 0 {
		t.Errorf("interrupted binary left scratch: %v (err %v)", left, err)
	}
}

func TestModuleLintReportsARealFindingWithRealGolangciLint(t *testing.T) {
	requireGolangciLint(t)
	// An unused unexported function is a default-linter (unused) finding in
	// any configuration this repo has ever run.
	dir := writeFixtureModule(t, "package fixture\n\nfunc orphaned() int { return 1 }\n")
	t.Setenv("TMPDIR", t.TempDir())
	var out, errOut strings.Builder
	code := moduleLint(lintEnv(map[string]string{"MODULES": dir}), &out, &errOut, nil, 5*time.Second)
	if code != 1 {
		t.Fatalf("moduleLint = %d, want 1\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "----- "+dir+" -----") {
		t.Errorf("no replay fence for the failed module in %q", text)
	}
	if !strings.Contains(text, "orphaned") {
		t.Errorf("the real finding's identifier is missing from the replay: %q", text)
	}
	pointer := regexp.MustCompile(`(?m)^full logs: (.+)$`).FindStringSubmatch(text)
	if pointer == nil {
		t.Fatalf("no retained-log pointer in %q", text)
	}
	if _, err := os.Stat(pointer[1]); err != nil {
		t.Errorf("retained-log pointer names a missing directory: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (findings: 1/1 modules: "+dir+")" {
		t.Errorf("summary = %q", last)
	}
}
