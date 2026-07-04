package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// newBody returns an io.Reader suitable for httptest.NewRequest's body
// argument from a raw JSON string.
func newBody(body string) *strings.Reader {
	return strings.NewReader(body)
}

func writeSession(t *testing.T, stateDir, id, wd string) {
	t.Helper()
	m := schema.SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0), EnvInfo: schema.EnvironmentInfo{WorkingDir: wd}}
	if err := schema.SaveSessionMeta(stateDir, m); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(stateDir, "sessions")
	for _, suffix := range []string{".transcript.jsonl", ".log.jsonl"} {
		if err := os.WriteFile(filepath.Join(sess, id+suffix), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sess, id), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDeleteRemovesFilesAndScrubs(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "sha1")
	writeSession(t, stateDir, "01A", "/w/proj")
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	_ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	_ = archive.Set("session", "01A", true, time.Unix(1_700_000_000, 0))
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	body := `{"key":"` + hubcore.ProjectSlug("/w/proj") + `","workingDir":"/w/proj"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Deleted []string                      `json:"deleted"`
		Skipped []struct{ ID, Reason string } `json:"skipped"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Deleted) != 1 {
		t.Fatalf("want 1 deleted ref, got %+v", resp)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", "01A"+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed", suffix)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", "01A")); !os.IsNotExist(err) {
		t.Fatal("per-session dir should be removed")
	}
	d, _ := archive.Decisions()
	if _, present := d[hubcore.ArchiveKey{Kind: "session", ID: "01A"}]; present {
		t.Fatalf("session archive row should be scrubbed: %v", d)
	}
}

func TestProjectDeleteRejectsKeyWorkingDirMismatch(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "sha1")
	writeSession(t, stateDir, "01A", "/w/proj")
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	_ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	body := `{"key":"` + hubcore.ProjectSlug("/w/proj") + `","workingDir":"/w/WRONG"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch must be rejected 400, got %d", rec.Code)
	}
}

func TestProjectDeleteRefusesWhenLive(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "sha1")
	writeSession(t, stateDir, "01A", "/w/proj")
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	_ = past.Rebuild()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: "01A", Status: "active"})
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
	body := `{"key":"` + hubcore.ProjectSlug("/w/proj") + `","workingDir":"/w/proj"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("live project delete must 409, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", "01A.meta.json")); os.IsNotExist(err) {
		t.Fatal("nothing should be removed when refused")
	}
}

// TestProjectDeleteSkipsOnRemoveFailure forces a non-ENOENT os.Remove error
// (permission denied on the containing sessions/ dir) and asserts the
// session lands ONLY in skipped: never also in deleted, its decision rows
// are left intact, and its files are left in place so the delete is
// cleanly retriable.
func TestProjectDeleteSkipsOnRemoveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based permission test is meaningless as root")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "sha1")
	writeSession(t, stateDir, "01A", "/w/proj")
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	_ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	_ = archive.Set("session", "01A", true, time.Unix(1_700_000_000, 0))
	_ = favorite.Set("session", "01A", true, time.Unix(1_700_000_000, 0))
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	sessDir := filepath.Join(stateDir, "sessions")
	if err := os.Chmod(sessDir, 0o555); err != nil {
		t.Fatalf("chmod sessions dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(sessDir, 0o755); err != nil {
			t.Errorf("restore sessions dir perms: %v", err)
		}
	})

	body := `{"key":"` + hubcore.ProjectSlug("/w/proj") + `","workingDir":"/w/proj"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Deleted []string            `json:"deleted"`
		Skipped []projectDeleteSkip `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	for _, id := range resp.Deleted {
		if id == "01A" {
			t.Fatalf("01A must not appear in deleted when its files could not be removed: %+v", resp)
		}
	}
	skippedCount := 0
	for _, sk := range resp.Skipped {
		if sk.ID == "01A" {
			skippedCount++
		}
	}
	if skippedCount != 1 {
		t.Fatalf("want exactly 1 skipped entry for 01A, got %d: %+v", skippedCount, resp)
	}

	ad, _ := archive.Decisions()
	if _, present := ad[hubcore.ArchiveKey{Kind: "session", ID: "01A"}]; !present {
		t.Fatal("session archive row must survive a failed removal (retriable)")
	}
	fd, _ := favorite.Favorites()
	if _, present := fd[hubcore.ArchiveKey{Kind: "session", ID: "01A"}]; !present {
		t.Fatal("session favorite row must survive a failed removal (retriable)")
	}
	if _, err := os.Stat(filepath.Join(sessDir, "01A.meta.json")); err != nil {
		t.Fatalf(".meta.json must still exist after a failed removal: %v", err)
	}
}
