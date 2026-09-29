package hub

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// pidForPinTest gives every fixture entry its own PID: the roster keys its
// entries by PID.
var pidForPinTest atomic.Int64

func liveForPinTest(id string, running ...string) hubcore.LiveEntry {
	return hubcore.LiveEntry{
		Entry:              rendezvous.Entry{PID: int(pidForPinTest.Add(1)), SessionID: id, WorkingDir: "/work/p"},
		SessionID:          id,
		Status:             appwire.ThreadStatusActive,
		RunningSubagentIDs: running,
	}
}

// countNavigationSnapshots fails the test if the resolver assembles a full
// navigation snapshot: a pin write only needs the root index and the remote
// thread cache.
func countNavigationSnapshots(t *testing.T) *int {
	t.Helper()
	calls := 0
	old := hubNavigationInputs
	hubNavigationInputs = func(s *WebServer, ctx context.Context) navigationSnapshot {
		calls++
		return old(s, ctx)
	}
	t.Cleanup(func() { hubNavigationInputs = old })
	return &calls
}

func TestResolveTopLevelSessionRefRefusesLiveSubagentWithoutMeta(t *testing.T) {
	calls := countNavigationSnapshots(t)
	cache := &hubcore.RemoteThreadCache{}
	web := NewWebServer(hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		// The child's meta never reached the index; only its parent's roster
		// entry says it is a subagent.
		Roster:            hubcore.NewRosterWithEntries(liveForPinTest("01ROOT", "01CHILD"), liveForPinTest("01CHILD")),
		RemoteThreadCache: cache,
	})
	web.injectMetasForTest([]schema.SessionMeta{{ID: "01ROOT", UpdatedAt: timeNowForTest()}})

	for _, ref := range []string{"01CHILD", "local:01CHILD"} {
		if session, err := web.resolveTopLevelSessionRef(t.Context(), ref); err == nil {
			t.Fatalf("resolve %q = %+v, want a refusal for a meta-less live subagent", ref, session)
		}
	}
	session, err := web.resolveTopLevelSessionRef(t.Context(), "01ROOT")
	if err != nil || session.sessionID != "01ROOT" || session.source != "" {
		t.Fatalf("resolve root = %+v, %v", session, err)
	}
	if *calls != 0 {
		t.Fatalf("resolver built %d navigation snapshots, want 0", *calls)
	}
}

func TestResolveTopLevelSessionRefAdmitsLiveSessionWithoutMeta(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		Past:   hubcore.NewPastIndex(""),
		Roster: hubcore.NewRosterWithEntries(liveForPinTest("01FRESH")),
	})
	session, err := web.resolveTopLevelSessionRef(t.Context(), "01FRESH")
	if err != nil || session.sessionID != "01FRESH" {
		t.Fatalf("resolve = %+v, %v; a live root whose meta has not arrived yet is pinnable", session, err)
	}
}

func TestResolveTopLevelSessionRefRefusesForkOriginalWithSeveralChildren(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	now := timeNowForTest()
	web.injectMetasForTest([]schema.SessionMeta{
		{ID: "01ORIG", ForkLabel: "edited", UpdatedAt: now},
		{ID: "01KIDA", ParentSessionID: "01ORIG", DivergenceTurn: 1, UpdatedAt: now},
		{ID: "01KIDB", ParentSessionID: "01ORIG", DivergenceTurn: 1, UpdatedAt: now},
	})
	if session, err := web.resolveTopLevelSessionRef(t.Context(), "01ORIG"); err == nil {
		t.Fatalf("resolve fork original = %+v, want refusal", session)
	}
	if _, err := web.resolveTopLevelSessionRef(t.Context(), "01KIDB"); err != nil {
		t.Fatalf("resolve continuation: %v", err)
	}
}

func TestResolveTopLevelSessionRefPinsRemoteSessionsWithoutASnapshot(t *testing.T) {
	calls := countNavigationSnapshots(t)
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "th_1", Source: "host-a", Evener: appwire.EvenerThread{Ref: "host-a:th_1"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
		{ID: "th_sub", Source: "host-a", Evener: appwire.EvenerThread{Ref: "host-a:th_sub", Kind: "subagent", ParentRef: "host-a:th_1"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
	})
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})

	session, err := web.resolveTopLevelSessionRef(t.Context(), "host-a:th_1")
	if err != nil || session.source != "host-a" || session.sessionID != "th_1" {
		t.Fatalf("resolve remote = %+v, %v", session, err)
	}
	if session, err := web.resolveTopLevelSessionRef(t.Context(), "host-a:th_sub"); err == nil {
		t.Fatalf("resolve remote subagent = %+v, want refusal", session)
	}
	if session, err := web.resolveTopLevelSessionRef(t.Context(), "host-a:missing"); err == nil {
		t.Fatalf("resolve missing remote = %+v, want refusal", session)
	}
	if _, err := web.resolveTopLevelSessionRef(t.Context(), "ghost:th_1"); err == nil {
		t.Fatal("resolve unknown source, want refusal")
	}
	if *calls != 0 {
		t.Fatalf("resolver built %d navigation snapshots, want 0", *calls)
	}
}

func TestResolveTopLevelSessionRefRefusesEmptyRefEvenWithBlankLiveEntry(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		Past:   hubcore.NewPastIndex(""),
		Roster: hubcore.NewRosterWithEntries(liveForPinTest("")),
	})
	if session, err := web.resolveTopLevelSessionRef(t.Context(), ""); err == nil {
		t.Fatalf("resolve empty ref = %+v, want refusal", session)
	}
}
