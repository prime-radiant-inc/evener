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

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
)

// timeNowForTest is a fixed clock for tests that need a deterministic
// decided_at timestamp without depending on wall-clock time.
func timeNowForTest() time.Time {
	return time.Unix(1_700_000_000, 0)
}

func postFavorite(t *testing.T, web *WebServer, kind, id string, favorited bool) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"kind":%q,"id":%q,"favorited":%t}`, kind, id, favorited)
	return postJSON(t, web.Handler(), "/api/favorite", body)
}

func TestAPIFavoriteRejectsSessionKindAfterNamedPins(t *testing.T) {
	rr := postFavorite(t, NewWebServer(hubcore.WebConfig{Favorite: hubcore.NewFavoriteStore(t.TempDir() + "/index.db")}), "session", "local:s1", true)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "/api/session-pin") {
		t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
	}
}

func TestFavoriteEndpointSetsDecision(t *testing.T) {
	dir := t.TempDir()
	fav := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Favorite: fav})
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(`{"kind":"project","id":"project-test-0123456789","favorited":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := fav.Favorites()
	if !got[hubcore.ArchiveKey{Kind: "project", ID: "project-test-0123456789"}] {
		t.Fatalf("favorite not persisted: %v", got)
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
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Favorite: fav})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := http.Post(hub.URL+"/api/favorite", "application/json", strings.NewReader(`{"kind":"project","id":"project-test-0123456789","favorited":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	assertSingleNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}
