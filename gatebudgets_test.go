package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gateBudgetsHelper sizes a worker pool from the machine's spare capacity;
// gateBudgetsLib turns that count into the arguments each gate stream hands its
// test runner. The helper's own arithmetic is covered by real fixtures in
// loadawareworkers_test.go, so the tests here only pin the wiring above it.
const (
	gateBudgetsHelper = "scripts/lib/load-aware-workers.sh"
	gateBudgetsLib    = "scripts/lib/gate-budgets.sh"
)

// runSourcedGate evaluates script in one POSIX shell after asserting both
// libraries exist. The script sources them itself, so each case chooses whether
// the helper is present and which functions of it to replace.
func runSourcedGate(t *testing.T, script string) string {
	t.Helper()
	for _, path := range []string{gateBudgetsHelper, gateBudgetsLib} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sh -c:\n%s\nexit: %v\noutput:\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// stubProbes sources both libraries and replaces the helper's two machine
// probes — load_aware_cores and load_aware_load1 — with fixed values. The
// replacement is a data seam, not a model of the sizing: load_aware_workers
// still runs its real arithmetic against a fixture machine of CORES cores under
// LOAD load, which keeps the result independent of the load of the machine
// running this test without restating the clamping rules here.
func stubProbes(cores, load string) string {
	return ". " + gateBudgetsHelper + "\n" +
		". " + gateBudgetsLib + "\n" +
		"load_aware_cores() { printf '%s' '" + cores + "'; }\n" +
		"load_aware_load1() { printf '%s' '" + load + "'; }\n"
}

// TestGateModuleFlagsFollowTheLoadAwareBudget exercises what the script-text
// assertions this replaced could only guess at: the effective -p/-parallel
// flags run-module-tests.sh hands `go test` for the root and agent modules, and
// how they shrink as the load average rises.
func TestGateModuleFlagsFollowTheLoadAwareBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		load       string
		wantRoot   string
		wantAgent  string
		wantShards string
	}{
		{"idle keeps the historical budgets", "0", "-p 6", "-p 4 -parallel 6", "3 6"},
		{"a loaded machine backs every budget off", "13.5", "-p 2", "-p 2 -parallel 2", "2 2"},
		{"a saturated machine never drops below one", "99", "-p 1", "-p 1 -parallel 1", "1 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runSourcedGate(t, stubProbes("16", tc.load)+
				`gate_init_budgets
printf 'root=%s\nagent=%s\nother=%s\nshards=%s %s\n' \
	"$(gate_module_flags .)" \
	"$(gate_module_flags agent)" \
	"$(gate_module_flags llm)" \
	"$AGENT_SHARD_PARALLEL" "$AGENT_SHARD_SURVEY_PARALLEL"`)
			want := "root=" + tc.wantRoot + "\nagent=" + tc.wantAgent + "\nother=\nshards=" + tc.wantShards
			if got != want {
				t.Errorf("effective gate flags =\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// TestGateModuleFlagsHonorEnvironmentOverrides pins that an explicit value is
// kept as written — test-race sets AGENT_PARALLEL=6 under -race — and that an
// explicitly empty value disables its flag instead of being refilled from the
// budget.
func TestGateModuleFlagsHonorEnvironmentOverrides(t *testing.T) {
	t.Parallel()
	got := runSourcedGate(t, stubProbes("16", "0")+
		`ROOT_P=2; AGENT_PARALLEL=6; AGENT_P=
export AGENT_PARALLEL AGENT_P
gate_init_budgets
printf 'root=%s\nagent=%s\n' "$(gate_module_flags .)" "$(gate_module_flags agent)"`)
	if want := "root=-p 2\nagent=-parallel 6"; got != want {
		t.Errorf("overridden gate flags =\n%s\nwant\n%s", got, want)
	}
}

// TestGateBudgetFallsBackWithoutAReadableAnswer pins the guard the budgets
// lean on: with no helper the answer is the caller's historical default, and a
// helper that prints nothing, prints garbage, or exits non-zero is treated as
// unreadable rather than trusted.
func TestGateBudgetFallsBackWithoutAReadableAnswer(t *testing.T) {
	t.Parallel()

	t.Run("helper absent", func(t *testing.T) {
		t.Parallel()
		got := runSourcedGate(t, ". "+gateBudgetsLib+
			`; printf '%s %s %s\n' "$(gate_budget 4 4)" "$(gate_budget 6 6)" "$(gate_budget 4 99)"`)
		if want := "4 6 99"; got != want {
			t.Errorf("gate_budget without the helper = %q, want %q", got, want)
		}
	})

	for _, tc := range []struct{ name, stub string }{
		{"helper prints nothing", `printf ''`},
		{"helper prints a non-number", `printf '%s' nope`},
		{"helper exits non-zero", `return 3`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runSourcedGate(t, ". "+gateBudgetsLib+"\n"+
				"load_aware_workers() { "+tc.stub+`; }
printf '%s' "$(gate_budget 4 4)"`)
			if got != "4" {
				t.Errorf("gate_budget over a %s helper = %q, want %q", tc.name, got, "4")
			}
		})
	}
}

// TestVitestRunArgsFollowTheLoadAwareBudget is the frontend half of the wiring:
// the flags package.json hands `vitest run` carry the worker count sized to the
// machine's spare capacity, and the pre-helper ceiling of four when the helper
// is unavailable.
func TestVitestRunArgsFollowTheLoadAwareBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		load string
		want string
	}{
		{"idle machine keeps the ceiling", "0", "--maxWorkers=4"},
		{"loaded machine backs off", "13.5", "--maxWorkers=2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := runSourcedGate(t, stubProbes("16", tc.load)+"vitest_run_args"); got != tc.want {
				t.Errorf("vitest_run_args = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("helper unavailable keeps the ceiling", func(t *testing.T) {
		t.Parallel()
		got := runSourcedGate(t, ". "+gateBudgetsLib+"; vitest_run_args")
		if want := "--maxWorkers=4"; got != want {
			t.Errorf("vitest_run_args without the helper = %q, want %q", got, want)
		}
	})
}

// TestRunModuleTestsWiresThroughGateBudgets is the minimal smoke assertion the
// issue asks to keep: the gate script routes its budgets through the shared
// library instead of carrying its own copy. The library's behavior is pinned
// above; this only proves the script still calls into it. Comments are stripped
// first, because the script explains the wiring in prose and matching that prose
// would pass even if the executable lines stopped calling the library
// (testing.md: an assertion that matches its own comment proves nothing).
func TestRunModuleTestsWiresThroughGateBudgets(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("scripts", "gate", "run-module-tests.sh"))
	if err != nil {
		t.Fatalf("read run-module-tests.sh: %v", err)
	}
	var body strings.Builder
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	for _, want := range []string{
		"gate-budgets.sh",
		"gate_init_budgets",
		"gate_module_flags",
	} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("run-module-tests.sh does not contain %q outside comments; its budgets must be sized through the shared gate-budgets.sh wiring", want)
		}
	}
}
