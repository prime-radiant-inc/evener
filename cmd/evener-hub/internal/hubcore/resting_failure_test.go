package hubcore

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

func testRestingFailure() *appwire.ThreadFailure {
	return &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 401}}
}

// fuzzScenarioStatusProber_KeepsTheRestingFailure: the probe keeps the listed
// root's failure summary, which says why a Failed row failed (S1c).
func fuzzScenarioStatusProber_KeepsTheRestingFailure(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_failed",
		state:     appwire.ThreadStatusSystemError,
		source:    wireProbeEnvelopeSource{failure: testRestingFailure()},
	})
	if got := prober.Probe(entry); !got.OK || !reflect.DeepEqual(got.Failure, testRestingFailure()) {
		t.Fatalf("probe = %+v, want the failure %+v", got, testRestingFailure())
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheFailureMoves: a retried turn
// that fails again between two probes leaves Status systemError on both and
// moves only the failure the row names (S1c).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheFailureMoves(t *testing.T) {
	entry := func(failure *appwire.ThreadFailure) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusSystemError, Failure: failure}}
	}
	title, kind, provider, status := testRestingFailure(), testRestingFailure(), testRestingFailure(), testRestingFailure()
	title.Title = "Usage limit reached"
	kind.Cause.Kind = "transcript_failed_closed"
	provider.Cause.Provider = "lunaroute"
	status.Cause.Status = 429
	for name, moved := range map[string]*appwire.ThreadFailure{"title": title, "cause kind": kind, "provider": provider, "status": status, "absence": nil} {
		if rosterFingerprint(entry(testRestingFailure())) == rosterFingerprint(entry(moved)) {
			t.Errorf("the roster fingerprint held when only the failure's %s moved", name)
		}
	}
}

// fuzzScenarioRoster_EntriesOwnTheirFailure: an entry never aliases the probe's
// failure, and a clone never aliases the entry's.
func fuzzScenarioRoster_EntriesOwnTheirFailure(t *testing.T) {
	failure := testRestingFailure()
	fromProbe := liveEntryFromProbe(rendezvous.Entry{PID: 1, SessionID: "01A"}, ProbeResult{SessionID: "01A", OK: true, Failure: failure})
	failure.Cause.Status = 500
	if fromProbe.Failure.Cause.Status != 401 {
		t.Fatal("liveEntryFromProbe aliased the probe's failure")
	}
	clone := CloneLiveEntry(fromProbe)
	clone.Failure.Cause.Status = 503
	if fromProbe.Failure.Cause.Status != 401 {
		t.Fatal("CloneLiveEntry aliased the entry's failure")
	}
}

// fuzzScenarioBuildTree_EveryFailedRowSaysWhy: a failed session's NeedsYou,
// Live and project rows and a meta-less live leaf name its failure from one
// closure, each with its own copy; a crash-retained entry's rows say it
// crashed, whatever its daemon last reported; a session that is not failed
// names none, whatever its entry still carries (S1c).
func fuzzScenarioBuildTree_EveryFailedRowSaysWhy(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01FAILED", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CRASHED", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01RETRIED", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01FAILED", Status: appwire.ThreadStatusSystemError, Failure: testRestingFailure()},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusSystemError, Failure: testRestingFailure()},
		{PID: 3, SessionID: "01CRASHED", Status: "errored", Crashed: true, Failure: testRestingFailure()},
		{PID: 4, SessionID: "01RETRIED", Status: appwire.ThreadStatusActive, Failure: testRestingFailure()},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	needsYou := map[string]TreeNode{}
	for _, row := range tree.NeedsYou {
		needsYou[row.ID] = row
	}
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01FAILED")
	leaf, inLeaf, _, _ := liveAndProjectRowsFor(tree, "01NOMETA")
	if !inLive || !inProject || !inLeaf {
		t.Fatalf("rows missing: live=%v project=%v leaf=%v", inLive, inProject, inLeaf)
	}
	for name, row := range map[string]TreeNode{"NeedsYou": needsYou["01FAILED"], "Live": liveRow, "project": projectRow, "live-only leaf": leaf} {
		if !reflect.DeepEqual(row.Failure, testRestingFailure()) {
			t.Fatalf("%s row failure = %+v, want %+v", name, row.Failure, testRestingFailure())
		}
	}
	liveRow.Failure.Cause.Status = 500
	if projectRow.Failure.Cause.Status != 401 {
		t.Fatal("two rows of one session share one failure")
	}
	crashed := &appwire.ThreadFailure{Cause: &appwire.DiagnosticCause{Kind: hubapi.NavigationFailureCrashed}}
	if got := needsYou["01CRASHED"].Failure; !reflect.DeepEqual(got, crashed) {
		t.Fatalf("crashed NeedsYou row failure = %+v, want the hub's own crashed cause", got)
	}
	if _, _, retried, found := liveAndProjectRowsFor(tree, "01RETRIED"); !found || retried.Failure != nil {
		t.Fatalf("working session's row = %+v (found %v), want no failure", retried.Failure, found)
	}
}

// fuzzScenarioBuildTree_ACoordinatorsRowNeverShowsASubagentsFailure: a
// coordinator's row is red only when the coordinator itself failed, so it
// never names a subagent's failure (Jesse's answer 12). A failed subagent's
// row names none either: the hub has no live entry for an in-process
// subagent, and its failure shows in the session's Subagents list.
func fuzzScenarioBuildTree_ACoordinatorsRowNeverShowsASubagentsFailure(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01ROOT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{{
		PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive,
		RunningSubagentIDs: []string{"01CHILD"}, RunningSubagentStates: map[string]string{"01CHILD": appwire.ThreadStatusSystemError},
	}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ROOT")
	if !inLive || !inProject || liveRow.Failure != nil || projectRow.Failure != nil {
		t.Fatalf("root rows = Live %+v (%v), project %+v (%v); want both found with no failure", liveRow.Failure, inLive, projectRow.Failure, inProject)
	}
}
