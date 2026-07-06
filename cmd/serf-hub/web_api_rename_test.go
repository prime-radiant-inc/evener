package main

import (
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

func TestRenameEndedSessionEditsMetaAndRefreshesIndex(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "p1")
	m := schema.SessionMeta{ID: "01A", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01A/rename", strings.NewReader(`{"name":"new title"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := past.Find("01A")
	if got.Meta.Name != "new title" || got.Meta.NameSource != "user" {
		t.Fatalf("ended rename must edit meta + refresh index, got %+v", got.Meta)
	}
	// The persisted file must also reflect the new name.
	on, _ := schema.LoadSessionMeta(stateDir, "01A")
	if on.Name != "new title" {
		t.Fatalf("meta file not updated: %+v", on)
	}
}

func TestRenameLiveRaceDaemonFailureHardFails(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{ID: "01RACE", UpdatedAt: time.Now(), EnvInfo: schema.EnvironmentInfo{WorkingDir: proj}}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(dir, "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 91, Address: "127.0.0.1:4591", WorkingDir: proj})
	r := hubcore.NewRoster(runDir, fakeProber{sessionID: "01RACE", status: appwire.ThreadStatusIdle})
	r.Refresh() // session reads as live, keyed by the bare session id in the roster
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: r, Past: idx})
	// scriptedAppSource.SetThreadName returns Unavailable — the daemon rename fails.
	web.sources.Add(&scriptedAppSource{id: "local", thread: appwire.Thread{ID: "01RACE", SessionID: "01RACE", Source: "local", CWD: proj, Serf: appwire.SerfThread{Ref: "local:01RACE"}}})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:01RACE/rename", strings.NewReader(`{"name":"new"}`))
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
	web.handleAPIRename(rec, req, "local:01RACE")
	if rec.Code == http.StatusNoContent {
		t.Fatalf("a failed daemon rename on a live-racing session must NOT silently succeed via a meta edit; status=%d", rec.Code)
	}
	// The persisted meta must not have been edited behind the live session's back.
	meta, err := schema.LoadSessionMeta(proj, "01RACE")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name == "new" {
		t.Fatalf("meta was edited despite the live-race daemon failure (T18): %+v", meta)
	}
}
