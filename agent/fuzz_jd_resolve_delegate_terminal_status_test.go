//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJdResolveDelegateTerminalStatus drives resolveDelegateTerminalStatus — the
// pure terminal-status decision lifted out of delegateTerminalStatus — over
// arbitrary parent stop overrides and child outcomes. Oracles (beyond
// never-panic):
//   - determinism: the same (stopStatus, stopReason, child) yields the same result;
//   - stop-override precedence: a non-empty stopStatus always wins verbatim,
//     regardless of the child status;
//   - totality on the child path: with no override every child status (including
//     unknown) maps to a terminal job status;
//   - Cancelled mapping: with no override a cancelled child yields the Cancelled
//     status with reason "stopped_by_parent".
func FuzzJdResolveDelegateTerminalStatus(f *testing.F) {
	f.Add(uint8(0), "", uint8(0))
	f.Add(uint8(2), "stopped_by_parent", uint8(2))
	f.Add(uint8(0), "", uint8(3))
	f.Add(uint8(6), "custom", uint8(1))

	f.Fuzz(func(t *testing.T, stopSel uint8, stopReason string, childSel uint8) {
		stopStatuses := []jobstore.Status{
			"", jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusFailed,
			jobstore.StatusCancelled, jobstore.StatusStopped, jobstore.Status("weird"),
		}
		children := []SubagentStatus{
			SubagentCompleted, SubagentFailed, SubagentCancelled, SubagentStatus("unknown"), "",
		}
		stopStatus := stopStatuses[int(stopSel)%len(stopStatuses)]
		child := children[int(childSel)%len(children)]

		gotStatus, gotReason := resolveDelegateTerminalStatus(stopStatus, stopReason, child)
		if s2, r2 := resolveDelegateTerminalStatus(stopStatus, stopReason, child); gotStatus != s2 || gotReason != r2 {
			t.Fatalf("non-deterministic: (%q,%q) vs (%q,%q)", gotStatus, gotReason, s2, r2)
		}

		if stopStatus != "" {
			if gotStatus != stopStatus || gotReason != stopReason {
				t.Fatalf("stop override not honored: got (%q,%q) want (%q,%q)", gotStatus, gotReason, stopStatus, stopReason)
			}
			return
		}

		// Child path (no override): totality — every child maps to a terminal status.
		if !gotStatus.IsTerminal() {
			t.Fatalf("child %q mapped to non-terminal status %q", child, gotStatus)
		}
		if child == SubagentCancelled {
			if gotStatus != jobstore.StatusCancelled || gotReason != "stopped_by_parent" {
				t.Fatalf("cancelled child mapped to (%q,%q), want (%q,%q)", gotStatus, gotReason, jobstore.StatusCancelled, "stopped_by_parent")
			}
		}
	})
}
