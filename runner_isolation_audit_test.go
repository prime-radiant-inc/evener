package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// cannot drift apart. `make lint`'s runner-isolation-shared-unset-helper
// check (internal/devtool/repoaudit) holds both callers to the helper.

// evenerEnvOverrides lists every EVENER_* override an operator's shell can
// carry into a fixture run. The test below fails when the helper leaves any
// of them set.
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
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
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
			if !slices.Contains(evenerEnvOverrides, name) {
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
