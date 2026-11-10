package repoaudit

import (
	"os"
	"path/filepath"
	"strings"
)

// runnerIsolationUsesSharedUnsetHelper pins the wiring: the UI qualification
// runner (scripts/native/ios/run-ui-tests.sh) drops the operator's EVENER_*
// overrides through the shared e2e-lib helper before its environment
// preparation begins, the harness's e2e_isolate_home uses the same helper
// instead of a second copy of the list, and no script carries its own unset
// list to drift. An Evener agent session exports EVENER_STATE_DIR and friends
// in exactly the shell the runner is started from, and the hub treats them as
// authoritative, so a runner that skipped the helper would read the operator's
// real state while calling itself isolated. The root
// TestE2eUnsetEvenerEnvDropsOperatorOverrides proves the helper unsets the
// whole override list.
func runnerIsolationUsesSharedUnsetHelper(a *audit) {
	read := func(rel string) string {
		data, err := os.ReadFile(rel)
		if err != nil {
			a.Fatalf("reading %s: %v", rel, err)
		}
		return string(data)
	}
	lib := read(filepath.Join("scripts", "lib", "e2e-lib.sh"))
	runner := read(filepath.Join("scripts", "native", "ios", "run-ui-tests.sh"))

	// e2e_isolate_home uses the helper and carries no unset list of its own.
	isolate := func(name string) string {
		at := strings.Index(lib, name+"() {")
		if at < 0 {
			a.Fatalf("e2e-lib.sh defines no %s", name)
		}
		until := strings.Index(lib[at:], "\n}")
		if until < 0 {
			a.Fatalf("cannot find the end of %s in e2e-lib.sh", name)
		}
		return lib[at : at+until]
	}("e2e_isolate_home")
	if !strings.Contains(isolate, "e2e_unset_evener_env") {
		a.Errorf("e2e_isolate_home does not use the shared helper, so its list can drift")
	}
	if strings.Contains(isolate, "unset EVENER_") {
		a.Errorf("e2e_isolate_home keeps its own unset list alongside the shared helper")
	}

	// The runner sources the library and drops the overrides before its
	// environment preparation (its first XDG export), with no list of its own.
	if !strings.Contains(runner, "scripts/lib/e2e-lib.sh") {
		a.Fatalf("run-ui-tests.sh does not source e2e-lib.sh")
	}
	call := strings.Index(runner, "e2e_unset_evener_env")
	if call < 0 {
		a.Fatalf("run-ui-tests.sh never calls the shared unset helper")
	}
	xdg := strings.Index(runner, "export XDG_")
	if xdg < 0 || call > xdg {
		a.Errorf("the helper call must precede the runner's environment preparation (call at %d, first XDG export at %d)", call, xdg)
	}
	if strings.Contains(runner, "unset EVENER_") {
		a.Errorf("run-ui-tests.sh carries its own unset list alongside the shared helper")
	}
}
