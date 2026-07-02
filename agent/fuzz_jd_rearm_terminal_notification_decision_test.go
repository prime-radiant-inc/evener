//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJdRearmTerminalNotificationDecision drives rearmTerminalNotificationDecision
// — the pure per-record decision lifted out of armPendingTerminalNotifications —
// over adversarial records and listing-session IDs. Oracles (beyond
// never-panic):
//   - determinism: the same (rec, sessionID) yields the same decision;
//   - foreign owner never re-armed: a record owned by another session is never
//     re-armed;
//   - appendEvent implies rearm;
//   - a non-terminal status or an empty terminal generation is never re-armed.
func FuzzJdRearmTerminalNotificationDecision(f *testing.F) {
	f.Add("sess_a", "sess_a", uint8(1), "gen1", uint8(0))
	f.Add("sess_b", "sess_a", uint8(1), "gen1", uint8(1))
	f.Add("", "sess_a", uint8(2), "", uint8(0))
	f.Add("sess_a", "sess_a", uint8(0), "gen1", uint8(0))

	f.Fuzz(func(t *testing.T, ownerID, sessionID string, statusSel uint8, terminalGen string, notifySel uint8) {
		statuses := []jobstore.Status{
			jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusFailed,
			jobstore.StatusCancelled, jobstore.StatusStopped, "",
		}
		notifyStates := []jobstore.NotifyState{
			jobstore.NotifyNotArmed, jobstore.NotifyPending, jobstore.NotifyDelivered, "",
		}
		status := statuses[int(statusSel)%len(statuses)]
		notify := notifyStates[int(notifySel)%len(notifyStates)]

		rec := &jobstore.JobRecord{
			OwnerSessionID: ownerID,
			Status:         status,
			TerminalGen:    terminalGen,
			NotifyState:    notify,
		}

		rearm, appendEvent := rearmTerminalNotificationDecision(rec, sessionID)
		if r2, a2 := rearmTerminalNotificationDecision(rec, sessionID); rearm != r2 || appendEvent != a2 {
			t.Fatalf("non-deterministic: (%v,%v) vs (%v,%v)", rearm, appendEvent, r2, a2)
		}

		if appendEvent && !rearm {
			t.Fatalf("appendEvent without rearm: %+v", rec)
		}

		if ownerID != "" && ownerID != sessionID && rearm {
			t.Fatalf("foreign-owned record re-armed: owner=%q session=%q", ownerID, sessionID)
		}

		if (!status.IsTerminal() || terminalGen == "") && rearm {
			t.Fatalf("non-terminal/empty-gen record re-armed: status=%q gen=%q", status, terminalGen)
		}
	})
}
