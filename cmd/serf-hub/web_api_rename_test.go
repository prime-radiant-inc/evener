package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
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
