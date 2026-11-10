package dev

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// cpuRowSeconds returns the seconds of the CPU row the rows carry for pkg.
func cpuRowSeconds(t *testing.T, rows []string, pkg string) float64 {
	t.Helper()
	for _, row := range rows {
		if secs, ok := strings.CutPrefix(row, "CPU\t"+pkg+"\t"); ok {
			v, err := strconv.ParseFloat(secs, 64)
			if err != nil {
				t.Fatalf("CPU row %q: %v", row, err)
			}
			return v
		}
	}
	t.Fatalf("no CPU row for %s in:\n%s", pkg, strings.Join(rows, "\n"))
	return 0
}

// goTestUnderCPUExec runs the fixture module's go test -json with the built
// evener-dev as its -exec wrapper, exactly as run-module-tests.sh does, and
// returns the stream and go test's exit error.
func goTestUnderCPUExec(t *testing.T, run string, env ...string) ([]byte, error) {
	t.Helper()
	bin := buildEvenerDev(t)
	isolateToolchainEnv(t)
	t.Setenv("GOWORK", "off")
	cmd := exec.Command("go", "test", "-json", "-count=1", "-run", run, "-exec", bin+" dev test-cpu-exec", ".") //nolint:noctx // one bounded fixture run, reaped by Output
	cmd.Dir = fixtureModule(t)
	cmd.Env = append(cmd.Environ(), env...)
	return cmd.Output()
}

// The wrapper's contract through the real toolchain: go test runs the
// fixture's test binary under it, and the package's own stream carries the CPU
// the binary spent, which the parser turns into the package's CPU row.
func TestTestCPUExecPutsThePackagesCPUInItsStream(t *testing.T) {
	out, err := goTestUnderCPUExec(t, "^TestFixtureBurn$", "SHARD_FIXTURE_BURN=300ms")
	if err != nil {
		t.Fatalf("go test under the wrapper: %v\n%s", err, out)
	}
	rows, err := parseGoTestJSON(bytes.NewReader(out), []string{"shardfixture"})
	if err != nil {
		t.Fatalf("parsing the wrapped stream: %v\n%s", err, out)
	}
	if cpu := cpuRowSeconds(t, rows, "shardfixture"); cpu < 0.3 {
		t.Fatalf("CPU row = %vs, want at least the 0.3s the fixture burned", cpu)
	}
}

// A sharded package's CPU row is every shard process's CPU summed: the shards
// split one package's work, so the package spent what they spent together.
func TestShardRunnerSumsTheShardsCPUIntoThePackageRow(t *testing.T) {
	cfg, stdout, stderr, _ := e2eConfig(t)
	cfg.noSurvey = true
	cfg.flags = []string{"-count=1"}
	cfg.jsonDir = t.TempDir()
	cfg.jsonPackage = "shardfixture"
	t.Setenv("SHARD_FIXTURE_BURN", "300ms")
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("runShards = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(cfg.jsonDir, "agent.package.tsv"))
	if err != nil {
		t.Fatalf("reading the package row: %v", err)
	}
	if cpu := cpuRowSeconds(t, strings.Split(strings.TrimSpace(string(data)), "\n"), "shardfixture"); cpu < 0.3 {
		t.Fatalf("CPU row = %vs, want at least the 0.3s the burning shard spent", cpu)
	}
}

// A failing test binary must still fail its package: the wrapper hands back
// the binary's exit status rather than its own.
func TestTestCPUExecKeepsTheTestBinarysFailure(t *testing.T) {
	out, err := goTestUnderCPUExec(t, "^TestFixtureBeta$", "SHARD_FIXTURE_FAIL=beta")
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("go test over a failing test under the wrapper = %v, want a failing exit\n%s", err, out)
	}
	if !bytes.Contains(out, []byte(`"Action":"fail","Package":"shardfixture","Elapsed"`)) {
		t.Fatalf("the package did not fail in the stream:\n%s", out)
	}
	rows, err := parseGoTestJSON(bytes.NewReader(out), []string{"shardfixture"})
	if err != nil {
		t.Fatalf("parsing the wrapped stream: %v\n%s", err, out)
	}
	cpuRowSeconds(t, rows, "shardfixture")
}

// A signal sent to the wrapper reaches the test binary, so a gate stopping the
// wrapper stops the tests and the binary's own exit status comes back.
func TestTestCPUExecForwardsSignalsToTheTestBinary(t *testing.T) {
	bin := buildEvenerDev(t)
	cmd := exec.Command(bin, "dev", "test-cpu-exec", "/bin/sh", "-c", `trap 'exit 7' TERM; echo ready; while :; do sleep 0.05; done`) //nolint:noctx // the test signals and reaps it itself
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() || lines.Text() != "ready" {
		t.Fatalf("the child never announced itself: %v", lines.Err())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for lines.Scan() {
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("wrapper exit = %v, want the child's trapped exit status 7", err)
	}
}
