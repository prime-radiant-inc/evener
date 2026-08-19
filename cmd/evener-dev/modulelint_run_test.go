//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// echoCmd returns a builder whose children are real shell processes that
// print module-tagged chatter and exit per the failures map — never a fake
// golangci-lint on PATH; the builder seam is the runner's real parameter.
func echoCmd(failures map[string]int) func(module string) *exec.Cmd {
	return func(module string) *exec.Cmd {
		code := failures[module]
		script := fmt.Sprintf("echo stdout:%s; echo stderr:%s >&2; exit %d", module, module, code)
		return exec.Command("sh", "-c", script) //nolint:noctx // lifecycle managed by the runner's process-group stop
	}
}

func newTestRun(t *testing.T, modules []string, parallel int, newCmd func(string) *exec.Cmd) (*lintRun, *strings.Builder, *strings.Builder) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	var out, errOut strings.Builder
	return &lintRun{
		modules:  modules,
		parallel: parallel,
		stdout:   &out,
		stderr:   &errOut,
		linter:   "sh", // the probe is a real lookup; sh is what the fixtures run
		newCmd:   newCmd,
		grace:    5 * time.Second,
	}, &out, &errOut
}

// scratchLeft lists evener-module-lint.* entries under the test's TMPDIR.
func scratchLeft(t *testing.T) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "evener-module-lint.*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return got
}

func summaryCount(s string) int {
	return len(regexp.MustCompile(`(?m)^(PASS|FAIL) lint \(`).FindAllString(s, -1))
}

func TestLintRunAllSuccessIsTwoQuietLines(t *testing.T) {
	r, out, errOut := newTestRun(t, []string{".", "agent", "llm"}, 4, echoCmd(nil))
	if code := r.run(); code != 0 {
		t.Fatalf("run = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("success output = %q, want exactly two lines", out.String())
	}
	if lines[0] != "lint: checking 3 modules" {
		t.Errorf("start line = %q", lines[0])
	}
	if !regexp.MustCompile(`^PASS lint \(3 modules, \d+s\)$`).MatchString(lines[1]) {
		t.Errorf("summary line = %q", lines[1])
	}
	if strings.Contains(out.String(), "stdout:") || strings.Contains(out.String(), "stderr:") {
		t.Error("successful module chatter leaked into the run output")
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("successful run left scratch behind: %v", left)
	}
}

func TestLintRunFindingsReplayFailedLogsInOrder(t *testing.T) {
	r, out, _ := newTestRun(t, []string{"identifier", "agent", "llm", "auth"}, 4,
		echoCmd(map[string]int{"identifier": 7, "llm": 7}))
	if code := r.run(); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	text := out.String()
	for _, needle := range []string{"stdout:identifier", "stderr:identifier", "stdout:llm", "stderr:llm"} {
		if !strings.Contains(text, needle) {
			t.Errorf("failed-module output %q missing from replay", needle)
		}
	}
	for _, absent := range []string{"stdout:agent", "stderr:auth"} {
		if strings.Contains(text, absent) {
			t.Errorf("passing-module output %q leaked into replay", absent)
		}
	}
	idIdx := strings.Index(text, "----- identifier -----")
	llmIdx := strings.Index(text, "----- llm -----")
	if idIdx < 0 || llmIdx < 0 || idIdx > llmIdx {
		t.Errorf("failure fences missing or out of MODULES order in %q", text)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (findings: 2/4 modules: identifier llm)" {
		t.Errorf("final line = %q", last)
	}
	if n := summaryCount(text); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	pointer := regexp.MustCompile(`(?m)^full logs: (.+)$`).FindStringSubmatch(text)
	if pointer == nil {
		t.Fatal("no retained-log pointer in findings output")
	}
	logs, err := filepath.Glob(filepath.Join(pointer[1], "*.log"))
	if err != nil || len(logs) != 2 {
		t.Errorf("retained dir has logs %v (err %v), want exactly the two failed modules'", logs, err)
	}
}

func TestLintRunMissingLinterIsNotChecked(t *testing.T) {
	r, out, errOut := newTestRun(t, []string{".", "agent", "llm"}, 4, nil)
	r.linter = "evener-dev-absent-linter-fixture"
	r.newCmd = func(module string) *exec.Cmd {
		t.Errorf("a check was launched for %s despite the missing linter", module)
		return exec.Command("false") //nolint:noctx // unreachable fixture
	}
	if code := r.run(); code != 127 {
		t.Fatalf("run = %d, want 127", code)
	}
	if n := strings.Count(errOut.String(), "evener-dev-absent-linter-fixture"); n != 1 {
		t.Errorf("missing-linter diagnostic appears %d times in stderr, want once: %q", n, errOut.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (not-checked: 3 modules: . agent llm)" {
		t.Errorf("final line = %q", last)
	}
	if n := summaryCount(out.String()); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
	if left := scratchLeft(t); len(left) != 0 {
		t.Errorf("not-checked run left scratch behind: %v", left)
	}
}

func TestLintRunScratchMintFailureIsSetup(t *testing.T) {
	r, out, errOut := newTestRun(t, []string{".", "agent"}, 4, echoCmd(nil))
	t.Setenv("TMPDIR", "/nonexistent-tmpdir-for-evener-dev-module-lint-test")
	if code := r.run(); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if errOut.Len() == 0 {
		t.Error("mint failure lost the underlying diagnostic")
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; last != "FAIL lint (setup: unable to create temporary log directory)" {
		t.Errorf("final line = %q", last)
	}
	if n := summaryCount(out.String()); n != 1 {
		t.Errorf("summary appears %d times, want exactly once", n)
	}
}
