package hub

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
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

// A past session with incomplete ancestry that no incompatible or unconfirmed
// daemon can own must not withdraw rename from every other local session.
func TestNavigationOrphanSubagentDoesNotDisableRename(t *testing.T) {
	web := navigationScalingFixture(t, 2, 0)
	rootID, orphanID, missingParentID := mustSessionID(t), mustSessionID(t), mustSessionID(t)
	now := time.Now()
	web.cfg.Past.SeedForTest([]schema.SessionMeta{
		{ID: rootID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
		{ID: orphanID, ParentSessionID: missingParentID, IsSubagent: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
	})
	snapshot, err := webNavigationSource{web: web}.Capture(t.Context(), "generation", now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Inputs.Renameable[rootID] && !snapshot.Inputs.Renameable[localAppRef(rootID)] {
		t.Errorf("unrelated orphaned subagent disabled rename for %s: %v", rootID, snapshot.Inputs.Renameable)
	}
}

// Ownership verification follows the descendants of restartRequired daemons,
// not every past session.
func TestNavigationOwnershipWalksOnlyAffectedSessions(t *testing.T) {
	web := navigationScalingFixture(t, 3, 300)
	daemon := web.cfg.Roster.List()[0]
	child, grandchild := mustSessionID(t), mustSessionID(t)
	now := time.Now()
	metas := web.cfg.Past.AllMetas()
	metas = append(metas,
		schema.SessionMeta{ID: child, ParentSessionID: daemon.SessionID, JobTreeRootSessionID: daemon.SessionID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		schema.SessionMeta{ID: grandchild, ParentSessionID: child, JobTreeRootSessionID: daemon.SessionID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
	)
	web.cfg.Past.SeedForTest(metas)
	walks := 0
	old := hubRosterList
	hubRosterList = func(r *hubcore.Roster) []hubcore.LiveEntry {
		walks++
		return old(r)
	}
	t.Cleanup(func() { hubRosterList = old })

	snapshot := web.navigationSnapshotInputs(t.Context())
	// The fixture writes no delegate descriptors, so verifying the affected
	// descendants must fail and surface, exactly as a missing descriptor does.
	if snapshot.ownershipErr == nil {
		t.Error("unverifiable descendants of a restartRequired daemon reported no ownership error")
	}
	if walks > 4 {
		t.Errorf("ownership walked %d sessions for 2 affected descendants among 302 past sessions", walks)
	}
}
