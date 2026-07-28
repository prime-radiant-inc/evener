package main

import (
	"context"
	"fmt"
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
	"primeradiant.com/serf/identifier"
)

// timeNowForTest is a fixed clock for tests that need a deterministic
// decided_at timestamp without depending on wall-clock time.
func timeNowForTest() time.Time {
	return time.Unix(1_700_000_000, 0)
}

func TestFavoriteEndpointSetsDecision(t *testing.T) {
	dir := t.TempDir()
	fav := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{ID: "01A", UpdatedAt: timeNowForTest()}})
	web := NewWebServer(hubcore.WebConfig{Favorite: fav, Past: past})
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(`{"kind":"session","id":"local:01A","favorited":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := fav.Favorites()
	if !got[hubcore.ArchiveKey{Kind: "session", ID: "01A"}] {
		t.Fatalf("favorite not persisted: %v", got)
	}
}

func TestFavoriteEndpointRejectsNonTopLevelSession(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "top", UpdatedAt: timeNowForTest()},
		{ID: "sub", ParentSessionID: "top", IsSubagent: true, UpdatedAt: timeNowForTest()},
		{ID: "orphan-fork", ForkLabel: "before edit", UpdatedAt: timeNowForTest()},
		{ID: "nested-fork", ForkLabel: "before another edit", UpdatedAt: timeNowForTest()},
		{ID: "branch", ParentSessionID: "nested-fork", UpdatedAt: timeNowForTest()},
	})
	fav := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	web := NewWebServer(hubcore.WebConfig{Favorite: fav, Past: past})

	for _, id := range []string{"local:sub", "local:nested-fork", "cluster:deadbeef"} {
		req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(
			`{"kind":"session","id":"`+id+`","favorited":true}`,
		))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q status=%d body=%s, want 400", id, rec.Code, rec.Body.String())
		}
	}
	got, err := fav.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("rejected ids wrote favorite decisions: %v", got)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(
		`{"kind":"session","id":"local:orphan-fork","favorited":true}`,
	))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("orphan fork status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	got, err = fav.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !got[hubcore.ArchiveKey{Kind: "session", ID: "orphan-fork"}] {
		t.Fatalf("orphan fork favorite not persisted: %v", got)
	}
}

func TestFavoriteEndpointAcceptsCappedAwayRemoteTopLevelSession(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	now := time.Unix(1_700_000_000, 0)
	threads := make([]appwire.Thread, 0, 60)
	for i := range 60 {
		threads = append(threads, appwire.Thread{
			ID: fmt.Sprintf("remote-%02d", i), Source: "remote",
			CreatedAt: now.Add(time.Duration(i) * time.Minute).Unix(),
			UpdatedAt: now.Add(time.Duration(i) * time.Minute).Unix(),
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		})
	}
	cache.Store(threads)
	fav := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	web := NewWebServer(hubcore.WebConfig{Favorite: fav, Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(
		`{"kind":"session","id":"remote:remote-00","favorited":true}`,
	))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capped-away remote status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	got, err := fav.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !got[hubcore.ArchiveKey{Kind: "session", ID: "remote:remote-00"}] {
		t.Fatalf("capped-away remote favorite not persisted: %v", got)
	}
}

func TestFavoriteEndpointRejectsRemoteSubagent(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{
			ID: "parent", Source: "remote", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
			Serf: appwire.SerfThread{Ref: "remote:parent", Kind: "session"},
		},
		{
			ID: "child", Source: "remote", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
			Serf: appwire.SerfThread{Ref: "remote:child", ParentRef: "remote:parent", Kind: "subagent"},
		},
	})
	fav := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	web := NewWebServer(hubcore.WebConfig{Favorite: fav, Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(
		`{"kind":"session","id":"remote:child","favorited":true}`,
	))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("remote subagent status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	got, err := fav.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("rejected remote subagent wrote favorite decisions: %v", got)
	}
}

func TestUnarchiveProjectUsesCanonicalID(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "foo")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	_ = store.Set("project", project.ID, true, timeNowForTest())
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})
	body := `{"kind":"project","id":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `","archived":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	got, _ := store.Decisions()
	if v := got[hubcore.ArchiveKey{Kind: "project", ID: project.ID}]; v {
		t.Fatalf("canonical project row should be false, got true")
	}
}

func TestFavoriteEndpointBroadcastsTreeChangedExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	fav := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{ID: "01A", UpdatedAt: timeNowForTest()}})
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Favorite: fav, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := http.Post(hub.URL+"/api/favorite", "application/json", strings.NewReader(`{"kind":"session","id":"local:01A","favorited":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	assertSingleNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}
