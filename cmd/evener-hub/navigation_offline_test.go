package hub

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// TestNavigationOfflineSourceRowsCarryOfflineField pins the fleet view's
// distinct "source unreachable" projection (component 06, spec §item 3):
// a folded row whose source is offline carries the offline marker, a live row
// from an online source and a local row never do, and the marker is absent
// from the wire for every other row so their shaping is unchanged.
//
// The offline fact is keyed off the row's source identity (its ref host), not
// the row's own state: the offline row here reports the same "active" remote
// status as the online one and still gets the marker, and the live/local rows
// never do. The marker is independent of Dormant, which keeps its never-run
// meaning rather than doubling as a reachability signal.
func TestNavigationOfflineSourceRowsCarryOfflineField(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "offline-thread", Source: "remote-offline", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
		{ID: "online-thread", Source: "remote-online", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
	})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-offline"}, online: false})
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-online"}, online: true})
	now := time.Now()
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{
		ID:        "local-past",
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Minute),
	}})
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", RemoteThreadCache: cache, Past: past})
	web.sources = registry

	snapshot := web.navigationSnapshotInputs(t.Context())
	tree := hubBuildNavigationTree(snapshot.metas, snapshot.live, map[hubcore.ArchiveKey]bool{}, snapshot.projects)
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, tree, web.apiTreeSources(), hubapi.AttentionSummary{}, snapshot.live, nil, nil, nil, nil)
	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}

	// The manifest reports the source offline (pinned by
	// TestCovAPITreeSourcesReportsOfflineSource); this test pins the folded
	// rows carrying that same fact as their own marker.
	offline := navigationProjectedSummary(t, projection, "remote-offline:offline-thread")
	online := navigationProjectedSummary(t, projection, "remote-online:online-thread")
	local := navigationProjectedSummary(t, projection, "local:local-past")

	if !offline.Offline {
		t.Fatalf("offline source's folded row is not marked offline: %#v", offline)
	}
	if online.Offline {
		t.Fatalf("online source's live row is marked offline: %#v", online)
	}
	if local.Offline {
		t.Fatalf("local row is marked offline: %#v", local)
	}
	offlineFields := navigationSummaryJSONFields(t, offline)
	if got := string(offlineFields["offline"]); got != "true" {
		t.Fatalf("offline row JSON offline = %q, want true (row = %#v)", got, offline)
	}
	if _, ok := navigationSummaryJSONFields(t, online)["offline"]; ok {
		t.Fatalf("live row from an online source carries offline; want the key absent: %#v", online)
	}
	if _, ok := navigationSummaryJSONFields(t, local)["offline"]; ok {
		t.Fatalf("local row carries offline; want the key absent: %#v", local)
	}
}

func navigationProjectedSummary(t *testing.T, projection navigationProjection, ref string) hubapi.NavigationSessionSummary {
	t.Helper()
	location, ok := projection.locations[ref]
	if !ok || location.Session == nil {
		refs := make([]string, 0, len(projection.locations))
		for key := range projection.locations {
			refs = append(refs, key)
		}
		sort.Strings(refs)
		t.Fatalf("no projected session for %q; projected refs: %v", ref, refs)
	}
	return *location.Session
}

func navigationSummaryJSONFields(t *testing.T, row hubapi.NavigationSessionSummary) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal summary %s: %v", raw, err)
	}
	return fields
}
