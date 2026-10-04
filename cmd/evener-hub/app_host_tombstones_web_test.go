package hub

// Web-side registry tests for slice S11's tree merge (registry spec 08 §15):
// after a removal the refresh re-applies the tombstone's retained rows, the
// tree forces them non-live with capabilities disabled (never through the
// sourceOnline fail-open), the manifest's sources array drops the removed
// host, the cache publication keeps tombstone rows until a re-add's
// new-generation publication supersedes them, and the source registry's
// SetOnRemove hook fires.

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHostTombstoneTreeMergeAndManifest pins acceptance 1 and 6: a removal
// leaves the host's rows in the tree marked stale/non-actionable, the refresh
// re-merges them (never drops them), and the manifest's sources array drops
// the host while its registration removal fires the fan-out hook.
func TestHostTombstoneTreeMergeAndManifest(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/hub.toml"
	cfg, _, _ := hostManageWiringConfig(t, configPath, nil, detachRefusingRunner{})
	cfg.RemoteHostClientIfAttached = nil
	cfg.RemoteThreadCache = &hubcore.RemoteThreadCache{}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	// The scripted source stands in for the attached host: one live session.
	scripted := appwire.Thread{ID: "t1", SessionID: "s1", Source: "web-side", CWD: "/srv/ws", Name: "side session",
		Status: appwire.ThreadStatus{Type: "active"}, UpdatedAt: 42}
	web.sources.Remove("web-side")
	web.sources.Add(&scriptedAppSource{id: "web-side", thread: scripted})
	if fetch := web.refreshRemoteThreadSnapshot(context.Background()); len(fetch.threads) != 1 {
		t.Fatalf("control walk threads = %+v, want the scripted row", fetch.threads)
	}

	// The source-registry removal hook is what stops the host-admin fan-outs;
	// pin that the removal fires it. The retained capture is pinned to a
	// one-row projection bound so the truncation indicator travels too.
	web.hostManage.cfg.policy.tombstoneMaxRows = 1
	web.hostManage.cfg.lastGoodThreads = func(string) []appwire.Thread {
		return retainedTestRows("web-side", 2, 100)
	}
	var removedSources []string
	web.sources.SetOnRemove(func(source appsource.Source) { removedSources = append(removedSources, source.ID()) })

	if err := client.Request(context.Background(), appwire.MethodEvenerHostRemove, wireRemoveRequest(t, client, "web-side"), nil); err != nil {
		t.Fatalf("evener/host/remove: %v", err)
	}
	if len(removedSources) != 1 || removedSources[0] != "web-side" {
		t.Fatalf("SetOnRemove fired for %v, want the removed host once", removedSources)
	}

	// A refresh AFTER the removal re-merges the tombstone's retained rows —
	// never drops them — tagged with the tombstone's name.
	fetch := web.refreshRemoteThreadSnapshot(context.Background())
	var merged []appwire.Thread
	for _, thread := range fetch.threads {
		if thread.Source == "web-side" {
			merged = append(merged, thread)
		}
	}
	// The tombstone's projection bound was pinned to one row above, so the
	// merge publishes the newest captured row only.
	if len(merged) != 1 || merged[0].ID != "web-side-t001" {
		t.Fatalf("refresh after remove threads = %+v, want the tombstone's retained row", fetch.threads)
	}
	if _, tagged := fetch.tombstones["web-side"]; !tagged {
		t.Fatalf("fetch.tombstones = %v, want the tombstone tag for web-side", fetch.tombstones)
	}
	if snapshot, ok := fetch.sources["web-side"]; !ok || !snapshot.Tombstoned || !snapshot.RowsTruncated {
		t.Fatalf("fetch.sources[web-side] = %+v (ok %v), want the tombstoned source entry with the truncation indicator", snapshot, ok)
	}

	// The list row carries the same indicator (`rowsTruncated: true`).
	var typedList appwire.HostListResponse
	var listRow appwire.HostRow
	if err := client.Request(context.Background(), appwire.MethodEvenerHostList, appwire.EmptyParams{}, &typedList); err != nil {
		t.Fatalf("evener/host/list: %v", err)
	}
	for _, row := range typedList.Hosts {
		if row.Name == "web-side" {
			listRow = row
		}
	}
	if !listRow.Removed || !listRow.RowsTruncated || listRow.RetainedRows == nil || *listRow.RetainedRows != 1 {
		t.Fatalf("list tombstone row = %+v, want removed with rowsTruncated true and retainedRows 1", listRow)
	}

	// Publish this walk the way the background refresher does, then read the
	// tree through the cache pipeline: the publication must keep the tombstone
	// rows, and the tree consumes the tag: the row stays in metas — metadata
	// retained — but is NOT live, so its capabilities stay disabled.
	web.cfg.RemoteThreadCache.StoreWalkSnapshot(hubcore.RemoteThreadSnapshot{
		Threads:  fetch.threads,
		Complete: fetch.complete,
		Sources:  fetch.sources,
	}, fetch.sourceGenerations)
	cached := web.remoteThreadFetch(context.Background())
	found := false
	for _, thread := range cached.threads {
		if thread.ID == "web-side-t001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cache read dropped the tombstone rows: %+v", cached.threads)
	}
	if _, tagged := cached.tombstones["web-side"]; !tagged {
		t.Fatalf("cache read lost the tombstone tag: %v", cached.tombstones)
	}
	inputs := web.navigationSnapshotInputs(context.Background())
	inMetas := false
	for _, meta := range inputs.metas {
		if meta.ID == "web-side:web-side-t001" {
			inMetas = true
			if meta.OriginalPrompt != "session 1" {
				t.Fatalf("tombstone row metadata = %+v, want the retained label", meta)
			}
		}
	}
	if !inMetas {
		t.Fatalf("tombstone row missing from metas: %+v", inputs.metas)
	}
	for _, entry := range inputs.live {
		if entry.SessionID == "web-side:web-side-t001" {
			t.Fatalf("tombstone row was projected live: %+v", entry)
		}
	}
	// The manifest's sources array drops the removed host: it is the
	// registered-source set, never the tombstone merge.
	for _, source := range web.apiTreeSources() {
		if source.ID == "web-side" {
			t.Fatalf("manifest still carries the removed host: %+v", web.apiTreeSources())
		}
	}

	// A concurrent re-add supersedes the merge: the tombstone is purged and
	// the next publication carries no tombstone rows or tag for the name.
	oldGeneration, _ := web.cfg.RemoteThreadCache.SourceGeneration("web-side")
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws2.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add (re-add): %v", err)
	}
	newGeneration, ok := web.cfg.RemoteThreadCache.SourceGeneration("web-side")
	if !ok || newGeneration == oldGeneration {
		t.Fatalf("re-add did not re-register the source under a new generation (old %d, new %d)", oldGeneration, newGeneration)
	}
	after := web.refreshRemoteThreadSnapshot(context.Background())
	if _, tagged := after.tombstones["web-side"]; tagged {
		t.Fatal("re-add left the tombstone tag in the publication")
	}
	for _, thread := range after.threads {
		if thread.ID == "web-side-t001" {
			t.Fatalf("re-add left the old incarnation's retained row in the walk: %+v", thread)
		}
	}
}

// TestHostTombstoneMergeDoesNotShadowLiveRows pins the review finding's fix: a
// tombstone still in the manager's store while the name is registered again (a
// re-add landing mid-walk) must not shadow the just-read live rows — the walk
// enumerated the name as a source, so its fresh rows are the current
// registration's and the tombstone merge skips it.
func TestHostTombstoneMergeDoesNotShadowLiveRows(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/hub.toml"
	cfg, _, _ := hostManageWiringConfig(t, configPath, nil, detachRefusingRunner{})
	cfg.RemoteHostClientIfAttached = nil
	cfg.RemoteThreadCache = &hubcore.RemoteThreadCache{}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.Request(context.Background(), appwire.MethodEvenerHostAdd, appwire.HostAddParams{Entry: appwire.HostEntry{Name: "web-side", Address: "ws.example"}}, nil); err != nil {
		t.Fatalf("evener/host/add: %v", err)
	}
	scripted := appwire.Thread{ID: "live-t1", Source: "web-side", CWD: "/srv/ws", Name: "live session", Status: appwire.ThreadStatus{Type: "active"}, UpdatedAt: 10}
	web.sources.Remove("web-side")
	web.sources.Add(&scriptedAppSource{id: "web-side", thread: scripted})
	// The mid-walk state: the store still holds a tombstone for the name the
	// registry now registers (the re-add's purge has not rewritten yet).
	web.hostManage.cfg.store.setRecordMaps(map[string]HostTombstone{
		"web-side": {
			Name: "web-side",
			Entry: HostConfig{
				Name: "web-side", SSH: "ws.example",
			},
			Origin:        hostOriginHubTOML,
			Generation:    1,
			IncarnationID: "00000000-0000-4000-8000-000000000001",
			PresenceEpoch: 2,
			RemovedAt:     time.Now().UTC().Format(time.RFC3339),
			Rows:          []string{`{"id":"stale-t1","source":"web-side","updatedAt":5}`},
		},
	}, nil)
	fetch := web.refreshRemoteThreadSnapshot(context.Background())
	if _, tagged := fetch.tombstones["web-side"]; tagged {
		t.Fatalf("the merge shadowed a registered, just-read source with its tombstone: %v", fetch.tombstones)
	}
	found := false
	for _, thread := range fetch.threads {
		if thread.ID == "stale-t1" {
			t.Fatal("the tombstone's stale rows were merged over the live rows")
		}
		if thread.ID == "live-t1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the live rows were dropped: %+v", fetch.threads)
	}
	if snapshot, ok := fetch.sources["web-side"]; !ok || snapshot.Tombstoned {
		t.Fatalf("fetch.sources[web-side] = %+v (ok %v), want the live source entry", snapshot, ok)
	}
}
