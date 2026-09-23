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
//
// The child gets a minimal environment — PATH and a fixed locale, nothing else.
// gate_init_budgets lets an ambient ROOT_P / AGENT_P / AGENT_PARALLEL /
// AGENT_SHARD_* win over the budget it would otherwise compute, and the gate
// that runs this test exports exactly those: `make test` and `make test-race`
// reach here through run-module-tests.sh, which exports the shard budgets (and
// test-race pins AGENT_PARALLEL=6). Inheriting them would make these assertions
// depend on whichever gate run started them, so the environment is dropped
// rather than trusted to stay clean.
func runSourcedGate(t *testing.T, script string) string {
	t.Helper()
	return runSourcedGateEnv(t, script)
}

// runSourcedGateEnv is runSourcedGate with extra environment entries for the
// cases that need to point PATH at a fixture.
func runSourcedGateEnv(t *testing.T, script string, extraEnv ...string) string {
	t.Helper()
	for _, path := range []string{gateBudgetsHelper, gateBudgetsLib} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = envOverride([]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh -c:\n%s\nexit: %v\noutput:\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// envOverride returns base with each "NAME=value" in overrides replacing the
// existing entry for NAME and otherwise appended. Appending blindly would leave
// the child with two PATH entries and hand the winner to Go's duplicate removal;
// a fixture that means "the child resolves through this PATH" must say so with
// one entry.
func envOverride(base []string, overrides ...string) []string {
	out := append([]string(nil), base...)
	for _, kv := range overrides {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		prefix := name + "="
		replaced := false
		for i, entry := range out {
			if strings.HasPrefix(entry, prefix) {
				out[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
}

// stubProbes sources the helper through gate_source_helper, then replaces the
// helper's two machine probes — load_aware_cores and load_aware_load1 — with
// fixed values. Sourcing through gate_source_helper is what marks the helper as
// present: gate_budget trusts only the function that call sourced, so a probe
// replacement alone would be ignored. The replacement is a data seam, not a
// model of the sizing: load_aware_workers still runs its real arithmetic
// against a fixture machine of CORES cores under LOAD load, which keeps the
// result independent of the load of the machine running this test without
// restating the clamping rules here.
func stubProbes(cores, load string) string {
	return ". " + gateBudgetsLib + "\n" +
		"gate_source_helper " + gateBudgetsHelper + "\n" +
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
	"$AGENT_SHARD_PARALLEL" "$AGENT_SHARD_SURVEY_PARALLEL"
printf 'hub=%s %s\n' "$HUB_SHARD_PARALLEL" "$HUB_SHARD_SURVEY_PARALLEL"
printf 'cli=%s %s\n' "$CLI_SHARD_PARALLEL" "$CLI_SHARD_SURVEY_PARALLEL"`)
			// The hub's shards take the same per-shard and survey widths as the
			// agent's, so both shrink together on a loaded host.
			want := "root=" + tc.wantRoot + "\nagent=" + tc.wantAgent + "\nother=\nshards=" + tc.wantShards + "\nhub=" + tc.wantShards + "\ncli=" + tc.wantShards
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

	t.Run("helper not sourced", func(t *testing.T) {
		t.Parallel()
		got := runSourcedGate(t, ". "+gateBudgetsLib+
			`; printf '%s %s %s\n' "$(gate_budget 4 4)" "$(gate_budget 6 6)" "$(gate_budget 4 99)"`)
		if want := "4 6 99"; got != want {
			t.Errorf("gate_budget without the helper = %q, want %q", got, want)
		}
	})

	// The helper is sourced here — that is what makes gate_budget consult
	// load_aware_workers at all — and then the function itself answers badly.
	for _, tc := range []struct{ name, stub string }{
		{"helper prints nothing", `printf ''`},
		{"helper prints a non-number", `printf '%s' nope`},
		{"helper prints zero", `printf '%s' 0`},
		{"helper exits non-zero", `return 3`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := runSourcedGate(t, ". "+gateBudgetsLib+"\n"+
				"gate_source_helper "+gateBudgetsHelper+"\n"+
				"load_aware_workers() { "+tc.stub+`; }
printf '%s' "$(gate_budget 4 4)"`)
			if got != "4" {
				t.Errorf("gate_budget over a %s helper = %q, want %q", tc.name, got, "4")
			}
		})
	}
}

// TestGateBudgetIgnoresAnAmbientWorkerCommand pins that gate_budget trusts only
// the helper gate_source_helper sourced. `command -v load_aware_workers` alone
// would run an unrelated executable of that name from PATH — a real hazard when
// the helper is missing, since the budget would then come from whatever the
// environment happened to offer instead of the documented fixed default.
func TestGateBudgetIgnoresAnAmbientWorkerCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	decoy := filepath.Join(dir, "load_aware_workers")
	if err := os.WriteFile(decoy, []byte("#!/bin/sh\nprintf '%s' 99\n"), 0o755); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	got := runSourcedGateEnv(t, ". "+gateBudgetsLib+`
printf 'resolved=%s\nbudget=%s\n' "$(command -v load_aware_workers)" "$(gate_budget 4 4)"`,
		"PATH="+dir+":"+os.Getenv("PATH"))
	// The decoy must be resolvable from the child, or the fixture would prove
	// nothing about ignoring it.
	want := "resolved=" + decoy + "\nbudget=4"
	if got != want {
		t.Errorf("with a decoy load_aware_workers on PATH, child = %q, want %q", got, want)
	}
}

// TestVitestRunArgsFollowTheLoadAwareBudget is the frontend half of the wiring:
// the flags package.json hands `vitest run` carry the worker count sized to the
// machine's spare capacity, floored at two, and the pre-helper ceiling of four
// when the helper is unavailable.
func TestVitestRunArgsFollowTheLoadAwareBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		load string
		want string
	}{
		{"idle machine keeps the ceiling", "0", "--maxWorkers=4"},
		{"loaded machine backs off", "13.5", "--maxWorkers=2"},
		// vitest runs a vm pool's files in ONE shared context when it has a
		// single worker, so the budget never goes below two.
		{"saturated machine keeps two workers", "40", "--maxWorkers=2"},
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

// TestGateSourceHelperMakesTheBudgetsLoadAware pins the one call that makes the
// budgets load-aware at all. Sourcing the helper is the wiring the smoke test's
// text match cannot vouch for: without it gate_budget silently answers with
// fixed defaults. Here the real helper is sourced and then its probes are
// replaced, so a loaded fixture machine must shrink the budget, while an
// unreadable helper path must leave the fixed default in place rather than
// abort.
func TestGateSourceHelperMakesTheBudgetsLoadAware(t *testing.T) {
	t.Parallel()

	t.Run("a readable helper sizes the budget to spare capacity", func(t *testing.T) {
		t.Parallel()
		got := runSourcedGate(t, ". "+gateBudgetsLib+"\n"+
			"gate_source_helper "+gateBudgetsHelper+"\n"+
			"load_aware_cores() { printf '%s' 16; }\n"+
			"load_aware_load1() { printf '%s' 13.5; }\n"+
			`printf '%s' "$(gate_budget 4 4)"`)
		if want := "2"; got != want {
			t.Errorf("gate_budget after gate_source_helper = %q, want %q", got, want)
		}
	})

	t.Run("an unreadable helper keeps the fixed default", func(t *testing.T) {
		t.Parallel()
		got := runSourcedGate(t, ". "+gateBudgetsLib+"\n"+
			"gate_source_helper "+gateBudgetsHelper+".missing\n"+
			`printf '%s' "$(gate_budget 4 4)"`)
		if want := "4"; got != want {
			t.Errorf("gate_budget after gate_source_helper on a missing path = %q, want %q", got, want)
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
		"load-aware-workers.sh",
		"gate_source_helper",
		"gate_init_budgets",
		"gate_module_flags",
	} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("run-module-tests.sh does not contain %q outside comments; its budgets must be sized through the shared gate-budgets.sh wiring", want)
		}
	}
}

// TestGateShardConcurrencyFollowsTheLoadAwareBudget pins the total shard
// concurrency the gate exports. The agent-shards runner starts one process per
// shard, and each shard's AGENT_SHARD_PARALLEL bounds only the tests inside it,
// so this budget is what keeps a one-CPU cgroup from getting one process per
// shard. gate_budget 8 8 is min(cores, 8) on an idle machine, so a host with at
// least 8 CPUs still starts every shard at once (unchanged) while a smaller or
// busier one starts fewer.
func TestGateShardConcurrencyFollowsTheLoadAwareBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cores string
		load  string
		want  string
	}{
		{"idle host with at least 8 CPUs starts every shard", "16", "0", "8"},
		{"idle 4-core host is capped by its cores", "4", "0", "4"},
		{"idle one-CPU cgroup starts one shard", "1", "0", "1"},
		{"a loaded host backs the cap off", "16", "13.5", "2"},
	}
	for _, variable := range []string{"AGENT_SHARD_CONCURRENCY", "HUB_SHARD_CONCURRENCY", "CLI_SHARD_CONCURRENCY"} {
		for _, tc := range cases {
			t.Run(variable+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				got := runSourcedGate(t, stubProbes(tc.cores, tc.load)+
					"gate_init_budgets\nprintf '%s' \"$"+variable+"\"")
				if got != tc.want {
					t.Errorf("%s = %q, want %q (cores=%s load=%s)", variable, got, tc.want, tc.cores, tc.load)
				}
			})
		}
	}
}

// TestGateHubShardCountDefaultsToEight pins the hub's shard count: eight shards
// balance evener-hub's ~2100 tests to ~9s each, and the count only partitions
// the tests; how many run at once is HUB_SHARD_CONCURRENCY's job.
func TestGateHubShardCountDefaultsToEight(t *testing.T) {
	t.Parallel()
	got := runSourcedGate(t, stubProbes("16", "0")+"gate_init_budgets\nprintf '%s' \"$HUB_SHARD_COUNT\"")
	if got != "8" {
		t.Errorf("HUB_SHARD_COUNT = %q, want 8", got)
	}
}

// TestGateCLIShardCountDefaultsToSix pins cmd/evener's shard count: six
// cost-balanced shards over its ~340 serve and run lifecycle tests.
func TestGateCLIShardCountDefaultsToSix(t *testing.T) {
	t.Parallel()
	got := runSourcedGate(t, stubProbes("16", "0")+"gate_init_budgets\nprintf '%s' \"$CLI_SHARD_COUNT\"")
	if got != "6" {
		t.Errorf("CLI_SHARD_COUNT = %q, want 6", got)
	}
}

// TestGateShardConcurrencyHonorsEnvironmentOverride pins that an explicit value
// wins over the budget, the way the other *_SHARD_* budgets behave.
func TestGateShardConcurrencyHonorsEnvironmentOverride(t *testing.T) {
	t.Parallel()
	for _, variable := range []string{"AGENT_SHARD_CONCURRENCY", "HUB_SHARD_CONCURRENCY", "CLI_SHARD_CONCURRENCY"} {
		got := runSourcedGateEnv(t, stubProbes("16", "0")+
			"gate_init_budgets\nprintf '%s' \"$"+variable+"\"", variable+"=3")
		if want := "3"; got != want {
			t.Errorf("%s = %q, want %q from the environment", variable, got, want)
		}
	}
}
