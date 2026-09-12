package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// loadAwareHelper is the shared sizing library every gate stream sources.
const loadAwareHelper = "scripts/lib/load-aware-workers.sh"

// runLoadAwareHelper sources the helper and invokes call with args in one
// POSIX shell, returning trimmed stdout. The caller supplies core count and
// load average explicitly wherever the helper accepts them, so an assertion
// never depends on the load of the machine running the test.
func runLoadAwareHelper(t *testing.T, call string, args ...string) string {
	t.Helper()
	if _, err := os.Stat(loadAwareHelper); err != nil {
		t.Fatalf("stat %s: %v", loadAwareHelper, err)
	}
	script := `. "$1" && shift && ` + call
	shellArgs := append([]string{"-c", script, "--", loadAwareHelper}, args...)
	out, err := exec.Command("sh", shellArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("sh %s: %v\noutput:\n%s", strings.Join(shellArgs, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestLoadAwareWorkersSizesToSpareCapacity pins the sizing function. The
// ceiling is what a caller asks for on an idle machine (four for the frontend
// vitest pool); as the 1-minute load average rises the result falls toward one
// so a machine shared by concurrent gate runs is not sized as if each run were
// alone. Load is rounded UP before it is subtracted, so a partly busy core
// already costs a worker.
func TestLoadAwareWorkersSizesToSpareCapacity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cap   string
		cores string
		load  string
		want  string
	}{
		{"idle machine keeps the ceiling", "4", "16", "0", "4"},
		{"fractional idle load keeps the ceiling", "4", "16", "0.4", "4"},
		{"busy but not saturated keeps the ceiling", "4", "16", "12.0", "4"},
		{"one core past the ceiling backs off one", "4", "16", "12.1", "3"},
		{"heavily loaded backs off to one", "4", "16", "43.27", "1"},
		{"oversubscribed never drops below one", "4", "16", "99", "1"},
		{"auto ceiling is the core count", "0", "16", "0", "16"},
		{"auto ceiling still backs off under load", "0", "16", "6", "10"},
		{"ceiling above the core count is clamped", "64", "4", "0", "4"},
		{"agent ceiling holds under moderate load", "6", "16", "10", "6"},
		{"agent ceiling backs off past it", "6", "16", "10.1", "5"},
		{"unreadable load keeps the caller ceiling", "4", "16", "not-a-number", "4"},
		{"unreadable core count keeps the caller ceiling", "4", "garbage", "0", "4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runLoadAwareHelper(t, `load_aware_workers "$@"`, tc.cap, tc.cores, tc.load)
			if got != tc.want {
				t.Errorf("load_aware_workers %s %s %s = %q, want %q", tc.cap, tc.cores, tc.load, got, tc.want)
			}
		})
	}
}

// TestLoadAwareWorkersRejectsMalformedCeiling keeps a bad argument loud: a
// caller that passes a non-numeric ceiling has a bug, and silently printing a
// worker count would hide it behind a test run that just looks slow.
func TestLoadAwareWorkersRejectsMalformedCeiling(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("sh", "-c", `. "$1" && shift && load_aware_workers "$@"`, "--",
		loadAwareHelper, "many", "16", "0").CombinedOutput()
	if err == nil {
		t.Fatalf("load_aware_workers with a malformed ceiling exited zero; output = %q", out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("malformed ceiling printed %q; a rejected argument must produce no worker count", out)
	}
}

// TestLoadAwareCoresReportsThisMachine checks the detector answers with a
// positive integer here, which is the branch the default (no explicit core
// count) takes on every real gate run.
func TestLoadAwareCoresReportsThisMachine(t *testing.T) {
	t.Parallel()
	got := runLoadAwareHelper(t, `load_aware_cores`)
	if got == "" {
		t.Skip("no CPU detector on this host; the unknown-core fallback is covered by the table test")
	}
	n, err := strconv.Atoi(got)
	if err != nil || n < 1 {
		t.Fatalf("load_aware_cores printed %q, want a positive integer", got)
	}
}

// TestLoadAwareCgroupCoresClampsToQuota pins the quota parse. A container
// whose cgroup allows fewer CPUs than the host advertises must size to the
// quota: overstating the core count leaves cores - load above the ceiling at
// any load, which silently disables the back-off this library exists for.
func TestLoadAwareCgroupCoresClampsToQuota(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		quota, period string
		want          string
	}{
		{"two whole CPUs", "200000", "100000", "2"},
		{"one whole CPU", "100000", "100000", "1"},
		{"a partial CPU rounds up", "150000", "100000", "2"},
		{"less than one CPU is still one", "50000", "100000", "1"},
		{"unlimited v2 spelling", "max", "100000", ""},
		{"unlimited v1 spelling", "-1", "100000", ""},
		{"zero period is unreadable", "200000", "0", ""},
		{"missing quota is unreadable", "", "100000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runLoadAwareHelper(t, `load_aware_cgroup_cores "$@"`, tc.quota, tc.period)
			if got != tc.want {
				t.Errorf("load_aware_cgroup_cores %q %q = %q, want %q", tc.quota, tc.period, got, tc.want)
			}
		})
	}
}

// TestRunModuleTestsUsesLoadAwareBudgets guards the wiring: the Go gate's
// parallelism budgets must size to spare capacity through this library rather
// than a fixed number, or the helper is dead code and a fleet of concurrent
// runs goes back to each claiming the whole machine.
func TestRunModuleTestsUsesLoadAwareBudgets(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("scripts", "gate", "run-module-tests.sh"))
	if err != nil {
		t.Fatalf("read run-module-tests.sh: %v", err)
	}
	// Comments are stripped before matching: the header explains this wiring,
	// and a substring assertion against raw text would pass on that prose even
	// if the executable lines stopped calling the helper (testing.md: an
	// assertion that matches its own comment proves nothing).
	var body strings.Builder
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	// The budgets are pinned as whole assignments rather than as a mere mention
	// of the helper: a line that computed the count and discarded it, or wrote
	// a fixed 6 back, would satisfy a substring check while silently reverting
	// the gate to fixed concurrency. The helper's sizing behavior is covered by
	// the table tests above.
	for _, want := range []string{
		"load-aware-workers.sh",
		"ROOT_P=${ROOT_P-$(load_aware_workers 6)}",
		"AGENT_PARALLEL=${AGENT_PARALLEL-$(load_aware_workers 6)}",
		"AGENT_P=${AGENT_P-$(load_aware_workers 4)}",
	} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("run-module-tests.sh does not contain %q outside comments; its -p/-parallel budgets must be sized from spare capacity", want)
		}
	}
}
