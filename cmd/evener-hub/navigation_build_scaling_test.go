package hub

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

func mustSessionID(tb testing.TB) string {
	tb.Helper()
	id, err := identifier.NewSessionID()
	if err != nil {
		tb.Fatal(err)
	}
	return id
}

// navigationScalingFixture is a hub with many restartRequired daemons and many
// past sessions, some in fork chains. It reproduces the shape that made one
// navigation build cost (past sessions x roster size).
func navigationScalingFixture(tb testing.TB, daemons, pastSessions int) *WebServer {
	tb.Helper()
	runDir := tb.TempDir()
	peer := protocolMismatchPeer(tb)
	for i := range daemons {
		id := mustSessionID(tb)
		writeRendezvous(tb, runDir, rendezvous.Entry{PID: 2000 + i, Protocol: "evener-appwire-v3", ThreadID: id, SessionID: id, WorkspaceRef: "local:" + id, Endpoint: peer})
	}
	roster := liveClaimRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()

	now := time.Now()
	metas := make([]schema.SessionMeta, 0, pastSessions)
	var previous string
	for i := range pastSessions {
		id := mustSessionID(tb)
		meta := schema.SessionMeta{ID: id, CreatedAt: now.Add(-time.Duration(i+1) * time.Minute), UpdatedAt: now.Add(-time.Duration(i) * time.Minute)}
		meta.EnvInfo.WorkingDir = fmt.Sprintf("/nonexistent/project-%d", i%60)
		// Every third session forks the one before it, so ancestry walks have depth.
		if i%3 != 0 && previous != "" {
			meta.ParentSessionID = previous
		}
		previous = id
		metas = append(metas, meta)
	}
	past := hubcore.NewPastIndex("")
	past.SeedForTest(metas)
	return NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: past, Roster: roster})
}

// One navigation build lists the roster a constant number of times, however
// many past sessions it resolves ownership for.
func TestNavigationSnapshotListsRosterOncePerBuild(t *testing.T) {
	web := navigationScalingFixture(t, 20, 300)
	listings := 0
	old := hubRosterList
	hubRosterList = func(r *hubcore.Roster) []hubcore.LiveEntry {
		listings++
		return old(r)
	}
	t.Cleanup(func() { hubRosterList = old })

	snapshot := web.navigationSnapshotInputs(t.Context())
	restartRequired := 0
	for _, entry := range snapshot.live {
		if entry.Status == appwire.ThreadStatusRestartRequired {
			restartRequired++
		}
	}
	if restartRequired < 20 {
		t.Fatalf("fixture has %d restartRequired live entries, want at least 20", restartRequired)
	}
	if listings > 1 {
		t.Errorf("navigation build listed the roster %d times for 300 past sessions, want at most 1", listings)
	}
}

func BenchmarkNavigationSnapshotInputs(b *testing.B) {
	web := navigationScalingFixture(b, 150, 5000)
	b.ResetTimer()
	for range b.N {
		web.navigationSnapshotInputs(b.Context())
	}
}

func BenchmarkNavigationCapture(b *testing.B) {
	web := navigationScalingFixture(b, 150, 5000)
	source := webNavigationSource{web: web}
	b.ResetTimer()
	for range b.N {
		if _, err := source.Capture(b.Context(), "generation", time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}

// The indexed lookup a navigation build uses must give every ownership walk
// the answer the live roster gives a single request.
func TestDaemonIndexMatchesLiveRosterOwnership(t *testing.T) {
	ids := map[string]string{}
	for _, name := range []string{"restart", "idle", "unconfirmed", "forkOfRestart", "forkOfIdle", "forkOfUnconfirmed", "root", "orphanFork", "missingParent"} {
		ids[name] = mustSessionID(t)
	}
	missing := mustSessionID(t)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 3001, ThreadID: ids["restart"], SessionID: ids["restart"], WorkspaceRef: "local:" + ids["restart"]})
	// The idle daemon's route survives a clear: its workspace ref names a session it no longer runs.
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 3002, ThreadID: ids["idle"], SessionID: ids["idle"], WorkspaceRef: "local:" + ids["orphanFork"]})
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 3003, ThreadID: ids["unconfirmed"], SessionID: ids["unconfirmed"], WorkspaceRef: "local:" + ids["unconfirmed"]})
	verdicts := map[string]hubcore.ProbeResult{
		ids["restart"]: {SessionID: ids["restart"], Status: appwire.ThreadStatusRestartRequired, ProtocolMismatch: true, OK: true},
		ids["idle"]:    {SessionID: ids["idle"], Status: "idle", OK: true},
	}
	roster := liveClaimRoster(runDir, forceStopProberFunc(func(entry rendezvous.Entry) hubcore.ProbeResult { return verdicts[entry.SessionID] }))
	roster.Refresh()
	if len(roster.UnconfirmedEntries()) != 1 || len(roster.List()) != 2 {
		t.Fatalf("fixture roster: %d live, %d unconfirmed", len(roster.List()), len(roster.UnconfirmedEntries()))
	}

	fork := func(name, parent string) schema.SessionMeta {
		return schema.SessionMeta{ID: ids[name], ParentSessionID: parent, JobTreeRootSessionID: parent}
	}
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		fork("forkOfRestart", ids["restart"]),
		fork("forkOfIdle", ids["idle"]),
		fork("forkOfUnconfirmed", ids["unconfirmed"]),
		{ID: ids["root"]},
		fork("orphanFork", ids["root"]),
		fork("missingParent", missing),
	})
	cfg := hubcore.WebConfig{Past: past, Roster: roster}
	index := newDaemonIndex(roster, roster.Snapshot())

	for name, id := range ids {
		for _, verifyAncestry := range []bool{false, true} {
			wantOwner, wantRestart, wantErr := walkDaemonOwner(t.Context(), cfg, rosterDaemons{roster}, id, verifyAncestry)
			gotOwner, gotRestart, gotErr := walkDaemonOwner(t.Context(), cfg, index, id, verifyAncestry)
			if gotOwner.PID != wantOwner.PID || gotOwner.SessionID != wantOwner.SessionID || gotRestart != wantRestart || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Errorf("%s verifyAncestry=%v: indexed=(%d %s %v %v), roster=(%d %s %v %v)", name, verifyAncestry,
					gotOwner.PID, gotOwner.SessionID, gotRestart, gotErr, wantOwner.PID, wantOwner.SessionID, wantRestart, wantErr)
			}
		}
	}
}
