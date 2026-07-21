package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

const (
	renameSessionID     = "02wMz5Txv1C3Hut0M8GCeB"
	renameRaceSessionID = "02wMz5Txv2enqVTitaig6F"
)

func TestRenameEndedSessionEditsMetaAndRefreshesIndex(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: renameSessionID, Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:"+renameSessionID+"/rename", strings.NewReader(`{"name":"new title"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := past.Find(renameSessionID)
	if got.Meta.Name != "new title" || got.Meta.NameSource != "user" {
		t.Fatalf("ended rename must edit meta + refresh index, got %+v", got.Meta)
	}
	// The persisted file must also reflect the new name.
	on, _ := schema.LoadSessionMeta(stateDir, renameSessionID)
	if on.Name != "new title" {
		t.Fatalf("meta file not updated: %+v", on)
	}
}

func TestRenameLiveRaceDaemonFailureHardFails(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "project-rename-race-0123456789")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{ID: renameRaceSessionID, UpdatedAt: time.Now(), EnvInfo: schema.EnvironmentInfo{WorkingDir: proj}}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(dir, "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 91, Address: "127.0.0.1:4591", WorkingDir: proj})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: renameRaceSessionID, status: appwire.ThreadStatusIdle})
	r.Refresh() // session reads as live, keyed by the bare session id in the roster
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})
	// scriptedAppSource.SetThreadName returns Unavailable — the daemon rename fails.
	web.sources.Add(&scriptedAppSource{id: "local", thread: appwire.Thread{ID: renameRaceSessionID, SessionID: renameRaceSessionID, Source: "local", CWD: proj, Serf: appwire.SerfThread{Ref: "local:" + renameRaceSessionID}}})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:"+renameRaceSessionID+"/rename", strings.NewReader(`{"name":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	// Call handleAPIRename directly with the "local:"-prefixed id rather than
	// going through web.Handler().ServeHTTP: the HTTP dispatcher
	// (handleAPISession) strips the "local:" prefix to the bare session id for
	// local refs before calling handleAPIRename, so isLive's Roster.Find(id)
	// and the ended-path recheck's Roster.Find(canonicalRouteID(id)) resolve
	// to the identical bare key and can never disagree — the top-level
	// isLive() branch (already hard-fails on a daemon error) always wins, and
	// the ended-path live-race branch this test targets is unreachable via the
	// real HTTP route. Passing the still-prefixed id directly reproduces the
	// mismatch: isLive("local:01RACE") misses (Roster is keyed by "01RACE"),
	// so the top-level branch is skipped, while canonicalRouteID strips the
	// prefix for the ended-path recheck and DOES find the roster entry —
	// exactly the daemon-races-back-to-live scenario T18 describes.
	web.handleAPIRename(rec, req, "local:"+renameRaceSessionID)
	if rec.Code == http.StatusNoContent {
		t.Fatalf("a failed daemon rename on a live-racing session must NOT silently succeed via a meta edit; status=%d", rec.Code)
	}
	// The persisted meta must not have been edited behind the live session's back.
	meta, err := schema.LoadSessionMeta(proj, renameRaceSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name == "new" {
		t.Fatalf("meta was edited despite the live-race daemon failure (T18): %+v", meta)
	}
}

func TestRenameEndedSessionBroadcastsTreeChangedExactlyOnce(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: renameSessionID, Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	defer hub.Close()
	// Mirror runMain's composed wiring (main.go): PastIndex.UpdateMeta's own
	// onChange hook is the sole broadcast source for this path — the handler
	// no longer calls notifyTreeChanged directly (it would double-broadcast).
	past.SetOnChange(func() { notifyTreeChanged(web.appRPC) })
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := http.Post(hub.URL+"/api/sessions/local:"+renameSessionID+"/rename", "application/json", strings.NewReader(`{"name":"new title"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	assertSingleNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}

// TestRefreshRenamedMetaBroadcastsTreeChangedExactlyOnce covers the
// live-daemon rename path's success site: handleAPIRename delegates both the
// live and became-live branches to refreshRenamedMeta after a successful
// daemon SetThreadName, so this calls it directly rather than scripting a
// fake daemon through scriptedAppSource (which only models SetThreadName
// failure today).
func TestRefreshRenamedMetaBroadcastsTreeChangedExactlyOnce(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: renameSessionID, Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past})
	// Mirror runMain's composed wiring (main.go) — see comment above.
	past.SetOnChange(func() { notifyTreeChanged(web.appRPC) })
	hubHTTP := httptest.NewServer(http.HandlerFunc(web.appRPC.ServeWebSocket))
	defer hubHTTP.Close()
	client := dialHubRPC(t, hubHTTP)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Simulate the daemon's out-of-process meta rewrite (the real
	// SetThreadName handler) that refreshRenamedMeta re-reads: without an
	// actual on-disk change, loadSessionMetaForRename reloads the identical
	// meta already indexed, UpdateMeta sees no delta, and PastIndex's
	// onChange correctly never fires — this write is what makes the reload a
	// genuine change instead of a no-op.
	m.Name = "new title"
	m.NameSource = "user"
	m.UpdatedAt = time.Unix(1_700_000_100, 0)
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}

	web.refreshRenamedMeta(renameSessionID, "new title")

	assertSingleNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}

// TestRefreshRenamedMetaBroadcastsTreeChangedExactlyOnceWhenSessionNotIndexed
// covers the miss path: a live-daemon rename for a session PastIndex has
// never indexed (e.g. renamed within the first Rebuild interval of being
// spawned). Find's own on-miss self-heal (an internal Rebuild) still can't
// find it, so UpdateMeta is never called and the composed onChange hook
// never fires — refreshRenamedMeta's trailing notifyTreeChanged must be the
// sole, single source of the broadcast for a rename that genuinely
// succeeded on the daemon side.
func TestRefreshRenamedMetaBroadcastsTreeChangedExactlyOnceWhenSessionNotIndexed(t *testing.T) {
	root := t.TempDir()
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	// Seed once, matching runMain's startup rebuild, so Find's internal
	// self-heal rebuild below sees no further delta (nothing on disk changed
	// between the two scans) and correctly stays silent.
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past})
	past.SetOnChange(func() { notifyTreeChanged(web.appRPC) }) // mirrors runMain's composed wiring
	hubHTTP := httptest.NewServer(http.HandlerFunc(web.appRPC.ServeWebSocket))
	defer hubHTTP.Close()
	client := dialHubRPC(t, hubHTTP)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	web.refreshRenamedMeta(renameSessionID, "new title") // renameSessionID was never written to disk or indexed

	assertSingleNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}

// TestRenameNotFoundBroadcastsNothing pins the invariant's other half: a
// request that fails outright (here, a rename for a session absent from both
// the roster and the past index — a genuine 404, not a mutation) must
// broadcast zero notifications.
func TestRenameNotFoundBroadcastsNothing(t *testing.T) {
	root := t.TempDir()
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	// Seed once, matching runMain's startup rebuild and the miss-case test
	// above: an UNSEEDED PastIndex's first-ever Rebuild always looks like a
	// delta (the empty-content fingerprint differs from the zero-value
	// initial one), which would make Find's internal self-heal rebuild
	// (triggered below by the 404 lookup) fire a spurious broadcast that has
	// nothing to do with this test's rename request.
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	defer hub.Close()
	past.SetOnChange(func() { notifyTreeChanged(web.appRPC) })
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := http.Post(hub.URL+"/api/sessions/local:"+renameRaceSessionID+"/rename", "application/json", strings.NewReader(`{"name":"new title"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}

	assertNoNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}
