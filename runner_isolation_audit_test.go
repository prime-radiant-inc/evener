package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The UI qualification runner (scripts/native/ios/run-ui-tests.sh) calls its
// hub "isolated", and on a Mac it runs from an operator's shell. An Evener
// agent session exports EVENER_STATE_DIR, EVENER_CREDENTIALS_CONFIG and
// friends in exactly that shell, and the hub treats an inherited
// EVENER_STATE_DIR as authoritative over every XDG path, so the fixture hub
// would read the operator's real state directory or credentials file while
// the run kept calling itself isolated. The e2e harness solved this for its
// own scripts (e2e_isolate_home in scripts/lib/e2e-lib.sh); the runner must
// drop the same list through the same shared helper, so the two copies
// cannot drift apart.

// evenerEnvOverrides lists every EVENER_* override an operator's shell can
// carry into a fixture run. It must match the helper's list, which the drift
// test below verifies against the library itself rather than against this
// constant.
var evenerEnvOverrides = []string{
	"EVENER_STATE_DIR",
	"EVENER_RUN_DIR",
	"EVENER_HUB_TOKEN",
	"EVENER_HUB_ADDR",
	"EVENER_HUB_LISTEN_ADDR",
	"EVENER_HUB_URL",
	"EVENER_HUB_SPAWNED",
	"EVENER_PROVIDERS_CONFIG",
	"EVENER_CREDENTIALS_CONFIG",
}

// TestE2eUnsetEvenerEnvDropsOperatorOverrides proves the shared helper's
// behavior, not just its text: every override disappears, and unrelated
// variables survive, proving the helper is scoped to the EVENER_* list.
func TestE2eUnsetEvenerEnvDropsOperatorOverrides(t *testing.T) {
	lib, err := filepath.Abs(filepath.Join("scripts", "lib", "e2e-lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lib); err != nil {
		t.Fatalf("e2e-lib.sh not found: %v", err)
	}

	var exports []string
	for _, name := range evenerEnvOverrides {
		exports = append(exports, "export "+name+"=operator-value")
	}
	// A variable outside the EVENER_* list must survive the helper, and so
	// must a variable the helper shares a prefix with.
	script := strings.Join([]string{
		`set -u`,
		`source ` + lib,
		`export EVENER_SOMETHING_ELSE=kept`,
		`export UNRELATED=kept`,
		strings.Join(exports, "\n"),
		`e2e_unset_evener_env`,
		`for v in ` + strings.Join(evenerEnvOverrides, " ") + ` EVENER_SOMETHING_ELSE UNRELATED; do`,
		`  if [ -n "${!v:-}" ]; then echo "SURVIVED $v=${!v}"; else echo "UNSET $v"; fi`,
		`done`,
	}, "\n")

	cmd := exec.Command("bash", "-c", script)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe script failed: %v\n%s", err, out)
	}
	reported := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[0] != "UNSET" && fields[0] != "SURVIVED") {
			t.Fatalf("unexpected probe output %q", line)
		}
		status, name := fields[0], fields[1]
		if i := strings.Index(name, "="); i >= 0 {
			name = name[:i]
		}
		switch name {
		case "UNRELATED":
			if status != "SURVIVED" {
				t.Errorf("helper unset %s, which is outside the EVENER_* list", name)
			}
		case "EVENER_SOMETHING_ELSE":
			if status != "SURVIVED" {
				t.Errorf("helper unset %s, which it does not own: the list is exact", name)
			}
		default:
			wanted := false
			for _, override := range evenerEnvOverrides {
				if name == override {
					wanted = true
					break
				}
			}
			if !wanted {
				t.Fatalf("unexpected variable %q in probe output", name)
			}
			reported[name] = true
			if status != "UNSET" {
				t.Errorf("helper left %s set to the operator's value", name)
			}
		}
	}
	// Every override must have been reported.
	for _, want := range evenerEnvOverrides {
		if !reported[want] {
			t.Errorf("probe output never mentioned %s", want)
		}
	}
}

// TestRunnerIsolationUsesSharedUnsetHelper pins the wiring: the runner drops
// the operator's EVENER_* overrides through the shared e2e-lib helper before
// its environment preparation begins, the harness's e2e_isolate_home uses
// the same helper instead of a second copy of the list, and no script
// carries its own unset list to drift.
func TestRunnerIsolationUsesSharedUnsetHelper(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(data)
	}
	lib := read(filepath.Join("scripts", "lib", "e2e-lib.sh"))
	runner := read(filepath.Join("scripts", "native", "ios", "run-ui-tests.sh"))

	// The helper exists, and its unset list is the complete override set.
	const helperName = "e2e_unset_evener_env() {"
	start := strings.Index(lib, helperName)
	if start < 0 {
		t.Fatalf("e2e-lib.sh defines no %s", strings.TrimSuffix(helperName, " {"))
	}
	end := strings.Index(lib[start:], "\n}")
	if end < 0 {
		t.Fatalf("cannot find the end of %s in e2e-lib.sh", strings.TrimSuffix(helperName, " {"))
	}
	body := lib[start : start+end]
	for _, name := range evenerEnvOverrides {
		if !strings.Contains(body, name) {
			t.Errorf("the shared helper does not unset %s", name)
		}
	}

	// e2e_isolate_home uses the helper and carries no unset list of its own.
	isolate := func(name string) string {
		t.Helper()
		at := strings.Index(lib, name+"() {")
		if at < 0 {
			t.Fatalf("e2e-lib.sh defines no %s", name)
		}
		until := strings.Index(lib[at:], "\n}")
		if until < 0 {
			t.Fatalf("cannot find the end of %s in e2e-lib.sh", name)
		}
		return lib[at : at+until]
	}("e2e_isolate_home")
	if !strings.Contains(isolate, "e2e_unset_evener_env") {
		t.Errorf("e2e_isolate_home does not use the shared helper, so its list can drift")
	}
	if strings.Contains(isolate, "unset EVENER_") {
		t.Errorf("e2e_isolate_home keeps its own unset list alongside the shared helper")
	}

	// The runner sources the library and drops the overrides before its
	// environment preparation (its first XDG export), with no list of its own.
	if !strings.Contains(runner, "scripts/lib/e2e-lib.sh") {
		t.Fatalf("run-ui-tests.sh does not source e2e-lib.sh")
	}
	call := strings.Index(runner, "e2e_unset_evener_env")
	if call < 0 {
		t.Fatalf("run-ui-tests.sh never calls the shared unset helper")
	}
	xdg := strings.Index(runner, "export XDG_")
	if xdg < 0 || call > xdg {
		t.Errorf("the helper call must precede the runner's environment preparation (call at %d, first XDG export at %d)", call, xdg)
	}
	if strings.Contains(runner, "unset EVENER_") {
		t.Errorf("run-ui-tests.sh carries its own unset list alongside the shared helper")
	}
}
