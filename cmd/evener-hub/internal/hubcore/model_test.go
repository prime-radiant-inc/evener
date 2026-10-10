package hubcore

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheCurrentModel: the probe keeps the model the
// listed root runs now, which a row names (S17). The rendezvous entry's Model
// is the one the session started on and goes stale after a model switch.
func fuzzScenarioStatusProber_KeepsTheCurrentModel(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_model",
		state:     appwire.ThreadStatusIdle,
		setup: func(srv *server.Server) {
			srv.UpdateSessionInfo("th_model", "gpt-5.6", "codex-jesse-fsck.com")
		},
	})
	entry.Model = "kimi-k3"
	got := prober.Probe(entry)
	if !got.OK || got.CurrentModel != "gpt-5.6" {
		t.Fatalf("probe = %+v, want the current model gpt-5.6", got)
	}
	if live := liveEntryFromProbe(entry, got); live.CurrentModel != "gpt-5.6" || live.Model != "kimi-k3" {
		t.Fatalf("live entry current model %q start model %q, want the probe's beside the entry's own", live.CurrentModel, live.Model)
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheModelMoves: a row names its
// session's model, which a switch moves while the status and every other
// field a row shows hold still (S17).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheModelMoves(t *testing.T) {
	entry := func(model string) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusIdle, CurrentModel: model}}
	}
	if rosterFingerprint(entry("gpt-5.6")) == rosterFingerprint(entry("kimi-k3")) {
		t.Fatal("the roster fingerprint held when only the model moved")
	}
}

// fuzzScenarioBuildTree_RowsNameTheirOwnSessionsModel: a live session's rows
// name its daemon's current model, which outranks both the model it started on
// and the meta the past index may still hold; a live entry no probe has
// reached yet names the model it started on; an ended session's rows name its
// meta's; and a subagent row names none (S17).
func fuzzScenarioBuildTree_RowsNameTheirOwnSessionsModel(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01LIVE", Model: "gpt-5.5", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", Model: "claude-opus-4-7", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", Model: "kimi-k3-mini", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01LIVE", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, Model: "gpt-5.4", SessionID: "01LIVE", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"01CHILD"}, CurrentModel: "gpt-5.6"},
		{PID: 2, Model: "glm-5.3-vision", SessionID: "01UNPROBED", Status: appwire.ThreadStatusAwaiting},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01LIVE")
	if !inLive || !inProject || liveRow.Model != "gpt-5.6" || projectRow.Model != "gpt-5.6" {
		t.Fatalf("Live row %q (%v), project row %q (%v): both must name the daemon's current model", liveRow.Model, inLive, projectRow.Model, inProject)
	}
	for _, row := range tree.NeedsYou {
		if row.ID == "01LIVE" && row.Model != "gpt-5.6" {
			t.Fatalf("NeedsYou row names %q, want the daemon's current model", row.Model)
		}
	}
	if leaf, found, _, _ := liveAndProjectRowsFor(tree, "01UNPROBED"); !found || leaf.Model != "glm-5.3-vision" {
		t.Fatalf("unprobed leaf = %q (found %v), want the model it started on", leaf.Model, found)
	}
	if _, _, ended, found := liveAndProjectRowsFor(tree, "01ENDED"); !found || ended.Model != "claude-opus-4-7" {
		t.Fatalf("ended row = %q (found %v), want its meta's model", ended.Model, found)
	}
}

// A spawned session's first publication names its model too, so its row names
// it before the next scan.
func TestRosterReadSpawnedThreadPublishesTheCurrentModel(t *testing.T) {
	r, entry := newSpawnedRoster(t)
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "01SPAWNED", SessionID: "01SPAWNED", ModelProvider: "gpt-5.6",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok || live.CurrentModel != "gpt-5.6" {
		t.Fatalf("published entry = %+v, want the model the read carried", live)
	}
}

// A live entry that carries neither the model it runs now nor the one it
// started on (a daemon that predates S17, or a probe that has not answered)
// still names the model its meta holds, rather than naming none (#2962).
func TestBuildTree_LiveRowWithoutAModelFallsBackToTheMeta(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01LIVE", Model: "gpt-5.5", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{{PID: 1, SessionID: "01LIVE", Status: appwire.ThreadStatusIdle}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	row, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01LIVE")
	if !inLive || !inProject || row.Model != "gpt-5.5" || projectRow.Model != "gpt-5.5" {
		t.Fatalf("Live row %q (%v), project row %q (%v): a live entry with neither model must name its meta's", row.Model, inLive, projectRow.Model, inProject)
	}
}
