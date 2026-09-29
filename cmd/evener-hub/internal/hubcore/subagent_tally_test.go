package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheTreesSubagentTally: the probe keeps the
// listed root's whole-tree tally (S3).
func fuzzScenarioStatusProber_KeepsTheTreesSubagentTally(t *testing.T) {
	want := appwire.SubagentTally{Running: 2, Failed: 1, Done: 57}
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_tally",
		state:     appwire.ThreadStatusActive,
		setup: func(srv *server.Server) {
			srv.SetSubagentTallyFunc(func() (appwire.SubagentTally, bool) { return want, true })
		},
	})
	if got := prober.Probe(entry); !got.OK || got.Subagents != want {
		t.Fatalf("probe = %+v, want the tally %+v", got, want)
	}
}

// fuzzScenarioRoster_FingerprintMovesWithTheSubagentTally: a subagent failing
// changes the row's last line, so navigation must invalidate. The counts move
// only on a delegate's lifecycle, never on a probe tick.
func fuzzScenarioRoster_FingerprintMovesWithTheSubagentTally(t *testing.T) {
	entry := func(tally appwire.SubagentTally) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusActive, Subagents: tally}}
	}
	running := entry(appwire.SubagentTally{Running: 2})
	oneFailed := entry(appwire.SubagentTally{Running: 1, Failed: 1})
	oneDone := entry(appwire.SubagentTally{Running: 1, Done: 1})
	if rosterFingerprint(running) == rosterFingerprint(oneFailed) || rosterFingerprint(oneFailed) == rosterFingerprint(oneDone) {
		t.Fatal("the roster fingerprint held when a subagent ended")
	}
}

// fuzzScenarioBuildTree_RootRowsCarryTheTreesSubagentTally: a live root's
// Live, project and NeedsYou rows carry its tally from one closure, and so does
// the flat Live row of a live session the past index has no meta for yet;
// an ended session carries none.
func fuzzScenarioBuildTree_RootRowsCarryTheTreesSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tally := appwire.SubagentTally{Running: 1, Failed: 1, Done: 2}
	metas := []schema.SessionMeta{
		{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01ROOT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	metaless := appwire.SubagentTally{Done: 4}
	live := []LiveEntry{
		{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"01CHILD"}, Subagents: tally},
		{PID: 2, SessionID: "01METALESS", Status: appwire.ThreadStatusIdle, Subagents: metaless},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	leafTally, leafListed := appwire.SubagentTally{}, false
	for _, row := range tree.Live {
		if row.ID == "01METALESS" {
			leafTally, leafListed = row.Subagents, true
		}
	}
	if !leafListed || leafTally != metaless {
		t.Fatalf("meta-less Live row tally = %+v (listed %v), want %+v", leafTally, leafListed, metaless)
	}
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ROOT")
	if !inLive || !inProject || len(tree.NeedsYou) != 1 || tree.NeedsYou[0].ID != "01ROOT" {
		t.Fatalf("rows of the awaiting root: live=%v project=%v needsYou=%+v", inLive, inProject, tree.NeedsYou)
	}
	for name, row := range map[string]TreeNode{"Live": liveRow, "project": projectRow, "NeedsYou": tree.NeedsYou[0]} {
		if row.Subagents != tally {
			t.Fatalf("%s row tally = %+v, want %+v", name, row.Subagents, tally)
		}
	}
	if _, _, ended, found := liveAndProjectRowsFor(tree, "01ENDED"); !found || ended.Subagents != (appwire.SubagentTally{}) {
		t.Fatalf("ended session's row = %+v (found %v), want no tally", ended, found)
	}
}

// fuzzScenarioBuildTree_CrashedRootCarriesNoSubagentTally: a crash-retained
// entry keeps its daemon's last probe, but that daemon runs nothing, so none of
// its rows carries a tally: the same rule that drops a crashed entry's listed
// children.
func fuzzScenarioBuildTree_CrashedRootCarriesNoSubagentTally(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{{ID: "01CRASHED", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{{PID: 1, SessionID: "01CRASHED", Status: "errored", Crashed: true, Subagents: appwire.SubagentTally{Running: 2, Failed: 1}}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01CRASHED")
	if !inLive || !inProject || len(tree.NeedsYou) != 1 || tree.NeedsYou[0].ID != "01CRASHED" {
		t.Fatalf("rows of the errored crashed root: live=%v project=%v needsYou=%+v", inLive, inProject, tree.NeedsYou)
	}
	for name, row := range map[string]TreeNode{"Live": liveRow, "project": projectRow, "NeedsYou": tree.NeedsYou[0]} {
		if row.Subagents != (appwire.SubagentTally{}) {
			t.Fatalf("%s row carries %+v, want no tally: a crashed root runs nothing", name, row.Subagents)
		}
	}
}

// fuzzScenarioBuildTree_SubagentFailuresNeverRaiseTheRootsOwnState: a
// subagent's failure counts on its coordinator's tally and nowhere else. The
// coordinator's own state, attention level and Needs you membership follow only
// its own status, and its project header stays calm (Jesse: the coordinator
// row is red only if the coordinator failed).
func fuzzScenarioBuildTree_SubagentFailuresNeverRaiseTheRootsOwnState(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tally := appwire.SubagentTally{Failed: 2, Done: 1}
	metas := []schema.SessionMeta{{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}
	live := []LiveEntry{{PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusIdle, Subagents: tally}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ROOT")
	if !inLive || !inProject || liveRow.Subagents != tally {
		t.Fatalf("Live row %+v (%v), project row found %v: the root must carry %+v", liveRow.Subagents, inLive, inProject, tally)
	}
	if liveRow.State != "idle" || projectRow.State != "idle" {
		t.Fatalf("root states = %q (Live) and %q (project), want idle: subagent failures raised the coordinator", liveRow.State, projectRow.State)
	}
	if len(tree.NeedsYou) != 0 {
		t.Fatalf("NeedsYou = %+v, want empty: subagent failures put the coordinator in Needs you", tree.NeedsYou)
	}
	for _, project := range tree.Projects {
		if project.RollupState != "idle" || project.RollupAttn != 0 {
			t.Fatalf("project %q rollup = %q with %d needing attention, want idle and 0", project.Name, project.RollupState, project.RollupAttn)
		}
	}
	attention, summary := DeriveAttention(metas, live, map[ArchiveKey]bool{})
	if level := attention["01ROOT"].Level; level != "idle" || summary.Error != 0 || summary.NeedsYou != 0 {
		t.Fatalf("attention level %q, summary %+v: want idle with no error and no needs-you", level, summary)
	}
}
