package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// fuzzScenarioStatusProber_KeepsTheLastTurnEndedTime: the probe keeps the
// listed root's turn end, which decides Finished versus Idle (S4).
func fuzzScenarioStatusProber_KeepsTheLastTurnEndedTime(t *testing.T) {
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_turn_ended",
		state:     appwire.ThreadStatusAwaiting,
		source:    wireProbeEnvelopeSource{meta: schema.SessionMeta{ID: "th_turn_ended", LastTurnEndedAt: ended}},
	})
	if got := prober.Probe(entry); !got.OK || !got.LastTurnEndedAt.Equal(ended) {
		t.Fatalf("probe = %+v, want LastTurnEndedAt %v", got, ended)
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheTurnEndMoves: a turn that
// starts and ends between two probes leaves Status awaiting on both and moves
// only the turn end; navigation must still invalidate, or the row never turns
// Finished (S4).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheTurnEndMoves(t *testing.T) {
	ended := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	entry := func(at time.Time) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusAwaiting, LastTurnEndedAt: at}}
	}
	if rosterFingerprint(entry(ended)) == rosterFingerprint(entry(ended.Add(4*time.Second))) {
		t.Fatal("the roster fingerprint held when a turn ended between two probes")
	}
}

// fuzzScenarioBuildTree_EveryRowCarriesTheTurnEndedTime: a live session's
// Live, project and NeedsYou rows and a meta-less live leaf carry its turn end
// from one closure; an ended session carries none.
func fuzzScenarioBuildTree_EveryRowCarriesTheTurnEndedTime(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Minute)
	metas := []schema.SessionMeta{
		{ID: "01LIVE", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01LIVE", Status: appwire.ThreadStatusAwaiting, LastTurnEndedAt: ended},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusAwaiting, LastTurnEndedAt: ended},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01LIVE")
	if !inLive || !inProject || !liveRow.TurnEndedAt.Equal(ended) || !projectRow.TurnEndedAt.Equal(ended) {
		t.Fatalf("Live row %v (%v), project row %v (%v): both must carry %v", liveRow.TurnEndedAt, inLive, projectRow.TurnEndedAt, inProject, ended)
	}
	needsYou := false
	for _, row := range tree.NeedsYou {
		if row.ID == "01LIVE" {
			needsYou = row.TurnEndedAt.Equal(ended)
		}
	}
	if !needsYou {
		t.Fatalf("NeedsYou = %+v, want the awaiting session carrying %v", tree.NeedsYou, ended)
	}
	leaf := false
	for _, row := range tree.Live {
		if row.ID == "01NOMETA" {
			leaf = row.TurnEndedAt.Equal(ended)
		}
	}
	if !leaf {
		t.Fatalf("Live = %+v, want the meta-less leaf carrying %v", tree.Live, ended)
	}
	_, _, endedRow, found := liveAndProjectRowsFor(tree, "01ENDED")
	if !found || !endedRow.TurnEndedAt.IsZero() {
		t.Fatalf("ended session's row = %+v (found %v), want no turn end", endedRow, found)
	}
}
