package repoaudit

import (
	"os"
	"strings"
)

// projectionWorkPolicyLivesInTheHost keeps the package's projection-work
// tracker free of test-harness policy (#1891). The 4-second stall tripwire and
// the macrotask yield serve the web's flush helper, not mutation projection;
// living in shipped package code forced three host ports every host had to
// bind. The package exposes track/clear/whenIdle; the host test helper owns the
// bound and the hop.
func projectionWorkPolicyLivesInTheHost(a *audit) {
	packagePath := "appwire-client/typescript/state/mutation/projectionWork.ts"
	body, err := os.ReadFile(packagePath)
	if err != nil {
		a.Fatalf("read %s: %v", packagePath, err)
	}
	source := string(body)
	for _, forbidden := range []string{"STALL_TRIPWIRE_MS", "setTimeout", "yieldMacrotask", "MutationProjectionWorkPorts"} {
		if strings.Contains(source, forbidden) {
			a.Errorf("%s still carries %q; the tripwire and macrotask yield are host test policy (#1891), so move them to the web flush helper", packagePath, forbidden)
		}
	}
	if !strings.Contains(source, "whenIdle") {
		a.Errorf("%s no longer exposes whenIdle; the host helper needs it to wait for tracked work", packagePath)
	}

	hostPath := "cmd/evener-hub/frontend/src/panes/session/composer/queue/testing/flushPendingTurnsProjection.ts"
	host, err := os.ReadFile(hostPath)
	if err != nil {
		a.Fatalf("read %s: %v", hostPath, err)
	}
	for _, want := range []string{"STALL_TRIPWIRE_MS", "whenProjectionWorkIdleForTests"} {
		if !strings.Contains(string(host), want) {
			a.Errorf("%s does not own %q; the host test helper is where the tripwire and yield belong (#1891)", hostPath, want)
		}
	}
}
