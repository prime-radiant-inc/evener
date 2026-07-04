package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// timeNowForTest is a fixed clock for tests that need a deterministic
// decided_at timestamp without depending on wall-clock time.
func timeNowForTest() time.Time {
	return time.Unix(1_700_000_000, 0)
}

func TestFavoriteEndpointSetsDecision(t *testing.T) {
	dir := t.TempDir()
	fav := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Favorite: fav, Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(`{"kind":"session","id":"01A","favorited":true}`))
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

func TestUnarchiveProjectDropsLegacyBasenameRow(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	_ = store.Set("project", "foo", true, timeNowForTest()) // legacy basename row
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{"kind":"project","id":"/a/foo","archived":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	got, _ := store.Decisions()
	if _, present := got[hubcore.ArchiveKey{Kind: "project", ID: "foo"}]; present {
		t.Fatalf("legacy basename row should be dropped on un-archive: %v", got)
	}
	if v := got[hubcore.ArchiveKey{Kind: "project", ID: "/a/foo"}]; v {
		t.Fatalf("path row should be false, got true")
	}
}
