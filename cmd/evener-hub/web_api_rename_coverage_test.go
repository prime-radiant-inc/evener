package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestRenameEndedSessionLoadMetaError covers the loadSessionMeta error path
// (lines 102-104) in handleAPIRename.
func TestRenameEndedSessionLoadMetaError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	// Inject a load error.
	oldLoad := loadSessionMetaForRename
	defer func() { loadSessionMetaForRename = oldLoad }()
	loadSessionMetaForRename = func(_, _ string) (schema.SessionMeta, error) {
		return schema.SessionMeta{}, os.ErrNotExist
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB/rename", strings.NewReader(`{"name":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRenameEndedSessionSaveMetaError covers the saveSessionMeta error path
// (lines 109-111) in handleAPIRename.
func TestRenameEndedSessionSaveMetaError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	// Inject a save error.
	oldSave := saveSessionMetaForRename
	defer func() { saveSessionMetaForRename = oldSave }()
	saveSessionMetaForRename = func(_ string, _ schema.SessionMeta) error {
		return os.ErrPermission
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB/rename", strings.NewReader(`{"name":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRenameEndedSessionPokeAttention covers the PokeAttention path
// (lines 116-117) in handleAPIRename.
func TestRenameEndedSessionPokeAttention(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()

	poked := false
	web := NewWebServer(hubcore.WebConfig{
		Past:          past,
		Roster:        hubcore.NewRosterWithEntries(),
		PokeAttention: func() { poked = true },
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB/rename", strings.NewReader(`{"name":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !poked {
		t.Fatalf("PokeAttention was not called")
	}
}

// TestRefreshRenamedMetaLoadError covers the load error fallback path
// (lines 143-147) in refreshRenamedMeta.
func TestRefreshRenamedMetaLoadError(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	// Inject a load error so the fallback path is taken.
	oldLoad := loadSessionMetaForRename
	defer func() { loadSessionMetaForRename = oldLoad }()
	loadSessionMetaForRename = func(_, _ string) (schema.SessionMeta, error) {
		return schema.SessionMeta{}, os.ErrNotExist
	}

	// This should not panic and should use the fallback meta.
	web.refreshRenamedMeta("02wMz5Txv1C3Hut0M8GCeB", "new-name")

	// Verify the meta was updated via the fallback.
	pe, ok := past.Find("02wMz5Txv1C3Hut0M8GCeB")
	if !ok {
		t.Fatalf("session not found in past after refresh")
	}
	if pe.Meta.Name != "new-name" {
		t.Fatalf("meta name = %q, want new-name", pe.Meta.Name)
	}
}

// TestRenameLiveSessionSourceNotFound covers the sourceForThread error path
// (lines 54-56) in handleAPIRename for a live session.
func TestRenameLiveSessionSourceNotFound(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()

	// Make the session appear live via isLiveForRename.
	oldIsLive := isLiveForRename
	defer func() { isLiveForRename = oldIsLive }()
	isLiveForRename = func(s *WebServer, id string) bool { return true }

	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	// No sources registered, so sourceForThread will fail.
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB/rename", strings.NewReader(`{"name":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.handleAPIRename(rec, req, "local:02wMz5Txv1C3Hut0M8GCeB")
	if rec.Code == http.StatusNoContent {
		t.Fatalf("expected error status, got 204")
	}
}

// TestRenameMethodNotAllowed covers the method check path.
func TestRenameMethodNotAllowed(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB/rename", nil)
	rec := httptest.NewRecorder()
	web.handleAPIRename(rec, req, "local:02wMz5Txv1C3Hut0M8GCeB")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405", rec.Code)
	}
}

// TestRenameEmptyName covers the empty-name validation path.
func TestRenameEmptyName(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB/rename", strings.NewReader(`{"name":"  "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.handleAPIRename(rec, req, "local:02wMz5Txv1C3Hut0M8GCeB")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

// TestRenameBadJSON covers the JSON decode error path.
func TestRenameBadJSON(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries()})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:02wMz5Txv1C3Hut0M8GCeB/rename", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.handleAPIRename(rec, req, "local:02wMz5Txv1C3Hut0M8GCeB")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
}

// TestRefreshRenamedMetaPokeAttention covers the PokeAttention path
// in refreshRenamedMeta.
func TestRefreshRenamedMetaPokeAttention(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-0123456789")
	m := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	_, _ = past.Rebuild()

	poked := false
	web := NewWebServer(hubcore.WebConfig{
		Past:          past,
		Roster:        hubcore.NewRosterWithEntries(),
		PokeAttention: func() { poked = true },
	})

	web.refreshRenamedMeta("02wMz5Txv1C3Hut0M8GCeB", "new-name")
	if !poked {
		t.Fatalf("PokeAttention was not called in refreshRenamedMeta")
	}
}

// TestRefreshRenamedMetaNotIndexed covers the path where the session is not
// in the past index (Find returns false), so notified stays false and
// notifyTreeChanged is called.
func TestRefreshRenamedMetaNotIndexed(t *testing.T) {
	past := hubcore.NewPastIndexWithDB(filepath.Join(t.TempDir(), "projects", "*"), filepath.Join(t.TempDir(), "index.db"))
	_, _ = past.Rebuild()
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Session not in index, so refreshRenamedMeta's Find returns false.
	web.refreshRenamedMeta("nonexistent-session-id", "name")

	// notifyTreeChanged should have been called (notified=false).
	assertSingleNotification(t, client, web.appRPC, appwire.NotifyEvenerTreeChanged)
}
