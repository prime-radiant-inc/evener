package hubcore

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestRosterFingerprintPendingAskPairAndStableRepeat(t *testing.T) {
	t.Parallel()
	entry := LiveEntry{SessionID: "session-token", Status: appwire.ThreadStatusAwaiting, PendingAsk: true,
		PendingQuestion: &appwire.PendingQuestion{Question: "question-token", Count: 2},
		PendingAskState: &appwire.PendingAskState{Version: 1, BootGeneration: "boot-one", Revision: 2, CompositeTotal: 1,
			Calls: []appwire.PendingAskCall{{TurnID: "turn-token", CallID: "call-token"}}}}
	fingerprint := func(value LiveEntry) uint64 {
		return rosterFingerprint(map[string]LiveEntry{value.SessionID: value})
	}
	before := fingerprint(entry)
	if fingerprint(CloneLiveEntry(entry)) != before {
		t.Fatal("a separately owned repeat changed the fingerprint")
	}
	for _, change := range []string{"append", "restart"} {
		t.Run(change, func(t *testing.T) {
			next := CloneLiveEntry(entry)
			if change == "append" {
				next.PendingAskState.Revision++
			} else {
				next.PendingAskState.BootGeneration = "boot-two"
			}
			if fingerprint(next) == before {
				t.Fatal("pair-only change did not move the fingerprint")
			}
			if fingerprint(CloneLiveEntry(next)) != fingerprint(next) {
				t.Fatal("stable changed pair did not repeat")
			}
		})
	}
}

func TestRosterRefreshPendingAskPairInvalidatesOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	prober := &runningSubagentProber{result: ProbeResult{SessionID: "session-token", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, OK: true,
		PendingQuestion: &appwire.PendingQuestion{Question: "question-token", Count: 2},
		PendingAskState: &appwire.PendingAskState{Version: 1, BootGeneration: "boot-one", Revision: 2}}}
	roster := NewRoster(dir, prober)
	roster.Refresh()
	changes := 0
	roster.SetOnChange(func() { changes++ })
	for i, pair := range []struct {
		boot     string
		revision uint64
		changes  int
	}{{"boot-one", 2, 0}, {"boot-one", 3, 1}, {"boot-one", 3, 1}, {"boot-two", 3, 2}, {"boot-two", 3, 2}} {
		prober.result.PendingAskState.BootGeneration, prober.result.PendingAskState.Revision = pair.boot, pair.revision
		roster.Refresh()
		if changes != pair.changes {
			t.Fatalf("refresh %d pair=(%s,%d) invalidations=%d want=%d", i, pair.boot, pair.revision, changes, pair.changes)
		}
		live, ok := roster.Find("session-token")
		if !ok || live.PendingAskState == nil || live.PendingAskState.BootGeneration != pair.boot || live.PendingAskState.Revision != pair.revision {
			t.Fatalf("refresh %d lost the probe pair: %+v", i, live.PendingAskState)
		}
	}
}
