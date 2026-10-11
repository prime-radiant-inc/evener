package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheTreesActivity: the probe keeps the listed
// root's pulse meter sample, which evener/activity/read serves (S5).
func fuzzScenarioStatusProber_KeepsTheTreesActivity(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_activity",
		state:     appwire.ThreadStatusActive,
		setup: func(srv *server.Server) {
			srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_activity", Data: events.UserInputData{Text: "go"}})
		},
	})
	got := prober.Probe(entry)
	if !got.OK || got.Activity == nil {
		t.Fatalf("probe = %+v, want the root's activity", got)
	}
	if len(got.Activity.Minutes) != 7 || got.Activity.Minutes[6] < 1 || got.Activity.LastActivityAt == 0 {
		t.Fatalf("activity = %+v, want seven bars with the user message in the newest", got.Activity)
	}
}

// fuzzScenarioRoster_FingerprintIgnoresActivity: the pulse meter moves every
// minute a session works, and hashing it would bump navigation revisions and
// broadcast an invalidation on every probe (S5 ruling 1).
func fuzzScenarioRoster_FingerprintIgnoresActivity(t *testing.T) {
	entry := func(activity *appwire.ThreadActivity) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusActive, Activity: activity}}
	}
	quiet := entry(&appwire.ThreadActivity{Minutes: []int{0, 0, 0, 0, 0, 0, 1}, LastActivityAt: 1})
	busy := entry(&appwire.ThreadActivity{Minutes: []int{0, 0, 0, 0, 0, 9, 40}, LastActivityAt: 2})
	if rosterFingerprint(quiet) != rosterFingerprint(busy) || rosterFingerprint(quiet) != rosterFingerprint(entry(nil)) {
		t.Fatal("the roster fingerprint moved when only the pulse meter did")
	}
}

// fuzzScenarioRoster_EntriesOwnTheirActivity: an entry never aliases the
// probe's sample, and a clone never aliases the entry's.
func fuzzScenarioRoster_EntriesOwnTheirActivity(t *testing.T) {
	sample := &appwire.ThreadActivity{Minutes: []int{0, 0, 0, 0, 0, 0, 1}, LastActivityAt: 1}
	fromProbe := liveEntryFromProbe(rendezvous.Entry{PID: 1, SessionID: "01A"}, ProbeResult{SessionID: "01A", OK: true, Activity: sample})
	sample.Minutes[6] = 99
	if fromProbe.Activity.Minutes[6] != 1 {
		t.Fatal("liveEntryFromProbe aliased the probe's minutes")
	}
	clone := CloneLiveEntry(fromProbe)
	clone.Activity.Minutes[6] = 42
	if fromProbe.Activity.Minutes[6] != 1 {
		t.Fatal("CloneLiveEntry aliased the entry's minutes")
	}
}

// fuzzScenarioLiveRowRef_IsTheRefTheLiveRowCarries: a client joins activity
// to Board rows by ref, so the read spells a session's ref exactly as its Live
// row does, including the workspace ref a daemon keeps across thread/clear.
func fuzzScenarioLiveRowRef_IsTheRefTheLiveRowCarries(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cleared := LiveEntry{PID: 1, SourceID: "local", WorkspaceRef: "local:01WORKSPACE", SessionID: "01CURRENT", Status: appwire.ThreadStatusActive}
	plain := LiveEntry{PID: 2, SessionID: "01PLAIN", Status: appwire.ThreadStatusActive}
	tree := BuildTreeAt(nil, []LiveEntry{cleared, plain}, map[ArchiveKey]bool{}, now)
	rowRefs := map[string]string{}
	for _, row := range tree.Live {
		rowRefs[row.ID] = row.Ref
	}
	if got := LiveRowRef(cleared); got != "local:01WORKSPACE" || rowRefs["01CURRENT"] != got {
		t.Fatalf("LiveRowRef = %q, Live row ref = %q, want both local:01WORKSPACE", got, rowRefs["01CURRENT"])
	}
	// A row with no workspace ref carries none, and navigation spells its ID
	// as a local ref.
	if got := LiveRowRef(plain); got != "local:01PLAIN" || rowRefs["01PLAIN"] != "" {
		t.Fatalf("LiveRowRef = %q, Live row ref = %q, want local:01PLAIN and an empty row ref", got, rowRefs["01PLAIN"])
	}
}
