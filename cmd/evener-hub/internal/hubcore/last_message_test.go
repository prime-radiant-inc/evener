package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// fuzzScenarioStatusProber_KeepsTheLastMessage: the probe keeps the opening of
// the listed root's last agent message, a Finished row's why line (S1d).
func fuzzScenarioStatusProber_KeepsTheLastMessage(t *testing.T) {
	const message = "Three layouts are ready for review. I recommend B."
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_finished",
		state:     appwire.ThreadStatusIdle,
		source:    wireProbeEnvelopeSource{meta: schema.SessionMeta{ID: "th_finished", LastMessage: message}},
	})
	if got := prober.Probe(entry); !got.OK || got.LastMessage != message {
		t.Fatalf("probe = %+v, want the last message %q", got, message)
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheLastMessageMoves: a row shows
// the opening of its session's last agent message, which can move while the
// status and the turn end hold still (S1d).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheLastMessageMoves(t *testing.T) {
	ended := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	entry := func(message string) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusIdle, LastTurnEndedAt: ended, LastMessage: message}}
	}
	if rosterFingerprint(entry("Three layouts are ready.")) == rosterFingerprint(entry("Two layouts are ready.")) {
		t.Fatal("the roster fingerprint held when only the last message moved")
	}
}

// fuzzScenarioBuildTree_RowsCarryTheirOwnSessionsLastMessage: a live session's
// rows carry its daemon's last message, which outranks the meta the past index
// may still hold; an ended session's rows carry its meta's; a meta-less live
// leaf carries its entry's; and a subagent row carries none, so a coordinator's
// row says what the coordinator said (S1d).
func fuzzScenarioBuildTree_RowsCarryTheirOwnSessionsLastMessage(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01LIVE", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastMessage: "An older message.", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastMessage: "The ended session's last words.", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01LIVE", IsSubagent: true, LastMessage: "The subagent's report.", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01LIVE", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"01CHILD"}, LastMessage: "Three layouts are ready."},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusAwaiting, LastMessage: "A meta-less session's words."},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01LIVE")
	if !inLive || !inProject || liveRow.LastMessage != "Three layouts are ready." || projectRow.LastMessage != "Three layouts are ready." {
		t.Fatalf("Live row %q (%v), project row %q (%v): both must carry the daemon's message", liveRow.LastMessage, inLive, projectRow.LastMessage, inProject)
	}
	for _, row := range tree.NeedsYou {
		if row.ID == "01LIVE" && row.LastMessage != "Three layouts are ready." {
			t.Fatalf("NeedsYou row carries %q, want the daemon's message", row.LastMessage)
		}
	}
	if leaf, found, _, _ := liveAndProjectRowsFor(tree, "01NOMETA"); !found || leaf.LastMessage != "A meta-less session's words." {
		t.Fatalf("meta-less leaf = %q (found %v), want its entry's message", leaf.LastMessage, found)
	}
	if _, _, ended, found := liveAndProjectRowsFor(tree, "01ENDED"); !found || ended.LastMessage != "The ended session's last words." {
		t.Fatalf("ended row = %q (found %v), want its meta's message", ended.LastMessage, found)
	}
}
