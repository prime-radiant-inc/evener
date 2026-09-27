package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's turn end and never lend it to the
// root's in-process subagent aliases: a child's row is not the root's turn.
func TestLocalDaemonEntriesFromRosterCarryTheTurnEndOnlyOnTheRoot(t *testing.T) {
	ended := time.Date(2026, 9, 26, 11, 58, 0, 123_000_000, time.UTC)
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusIdle,
		RunningSubagentIDs: []string{"sess_child"}, LastTurnEndedAt: ended,
	}})
	if len(entries) != 2 || entries[0].LastTurnEndedAt != ended.UnixMilli() || entries[1].LastTurnEndedAt != 0 {
		t.Fatalf("entries = %+v, want the root at %d and its alias with none", entries, ended.UnixMilli())
	}
}

// A controller reads a remote host's turn end off its list row, so the remote
// live row carries turn_ended_at and unseen exactly like a local one (S4).
func TestNavigationRemoteLiveRowCarriesTurnEndAndUnseen(t *testing.T) {
	ended := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "th_1", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener: appwire.EvenerThread{Ref: "host-a:th_1", LastTurnEndedAt: ended.UnixMilli()},
	}})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "host-a"}, online: true})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", RemoteThreadCache: cache, Past: hubcore.NewPastIndex("")})
	web.sources = registry

	snapshot := web.navigationSnapshotInputs(t.Context())
	tree := hubBuildNavigationTree(snapshot.metas, snapshot.live, map[hubcore.ArchiveKey]bool{}, snapshot.projects)
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, web.apiTreeSources(), hubapi.AttentionSummary{}, snapshot.live, nil, nil, nil, nil)
	inputs.SessionSeen = hubcore.SessionSeenSnapshot{Epoch: ended.Add(-time.Hour)}
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	row := navigationProjectedSummary(t, projection, "host-a:th_1")
	if row.TurnEndedAt == nil || !row.TurnEndedAt.Equal(ended) || !row.Unseen {
		t.Fatalf("remote row = %+v, want turn end %v and unseen", row, ended)
	}
}
