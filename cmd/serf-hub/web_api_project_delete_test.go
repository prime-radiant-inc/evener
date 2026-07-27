package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

const webTestSessionID = "02wMz5Txv1C3Hut0M8GCeB"

var projectDeleteCanonicalSessionIDs = []string{
	"02wMz5Txv1C3Hut0M8GCeB",
	"02wMz5Txv2enqVTitaig6F",
	"02wMz5Txv5aIxgf9yVdd0N",
	"02wMz5Txv733WHFsVy66SR",
}

func TestProjectDeleteRejectsRecomputedIDMismatchAndNoProject(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	for name, body := range map[string]string{
		"mismatch":   `{"key":"` + project.ID + `","working_dir":"` + filepath.Join(root, "other") + `"}`,
		"no-project": `{"key":"no-project","working_dir":"` + projectDir + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			web.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

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
	for _, suffix := range []string{".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		contents := []byte("x\n")
		if suffix == ".api.jsonl" {
			contents = nil
		}
		if err := os.WriteFile(filepath.Join(sess, id+suffix), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sess, id), 0o755); err != nil {
		t.Fatal(err)
	}
}

type failingMkdirAllFS struct {
	afero.Fs
	err error
}

func (f failingMkdirAllFS) MkdirAll(string, os.FileMode) error {
	return f.err
}

func readFavoriteDecisionRows(t *testing.T, dbPath string) map[hubcore.ArchiveKey]bool {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT kind, id, favorited FROM favorite")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	decisions := make(map[hubcore.ArchiveKey]bool)
	for rows.Next() {
		var kind, id string
		var favorited int
		if err := rows.Scan(&kind, &id, &favorited); err != nil {
			t.Fatal(err)
		}
		decisions[hubcore.ArchiveKey{Kind: kind, ID: id}] = favorited == 1
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return decisions
}

func seedProjectDeleteDecisions(t *testing.T, archive *hubcore.ArchiveStore, favorite *hubcore.FavoriteStore, projectID string, sessionIDs ...string) {
	t.Helper()
	for _, id := range sessionIDs {
		if err := archive.Set("session", id, true, timeNowForTest()); err != nil {
			t.Fatal(err)
		}
		if err := favorite.Set("session", id, true, timeNowForTest()); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Set("project", projectID, true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Set("project", projectID, true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
}

func assertProjectDeleteDecisionPresent(t *testing.T, dbPath string, kind, id string, want bool) {
	t.Helper()
	favorites := readFavoriteDecisionRows(t, dbPath)
	got, ok := favorites[hubcore.ArchiveKey{Kind: kind, ID: id}]
	if !ok || got != want {
		t.Fatalf("favorite decision (%s, %s) = (%v, %v), want present=%v value=%v", kind, id, got, ok, true, want)
	}
}

func assertProjectDeleteDecisionAbsent(t *testing.T, dbPath string, kind, id string) {
	t.Helper()
	if _, ok := readFavoriteDecisionRows(t, dbPath)[hubcore.ArchiveKey{Kind: kind, ID: id}]; ok {
		t.Fatalf("favorite decision (%s, %s) should be absent", kind, id)
	}
}

func assertArchiveDecisionPresent(t *testing.T, archive *hubcore.ArchiveStore, kind, id string, want bool) {
	t.Helper()
	decisions, err := archive.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decisions[hubcore.ArchiveKey{Kind: kind, ID: id}]
	if !ok || got != want {
		t.Fatalf("archive decision (%s, %s) = (%v, %v), want present=%v value=%v", kind, id, got, ok, true, want)
	}
}

func assertArchiveDecisionAbsent(t *testing.T, archive *hubcore.ArchiveStore, kind, id string) {
	t.Helper()
	decisions, err := archive.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decisions[hubcore.ArchiveKey{Kind: kind, ID: id}]; ok {
		t.Fatalf("archive decision (%s, %s) should be absent", kind, id)
	}
}

func TestProjectDeleteRemovesFilesAndScrubs(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	sessionsDir := filepath.Join(stateDir, "sessions")
	otherSessionArtifact := filepath.Join(sessionsDir, projectDeleteCanonicalSessionIDs[1]+".api.jsonl")
	prefixCollision := filepath.Join(sessionsDir, webTestSessionID+"-notes.txt")
	unrelatedArtifact := filepath.Join(sessionsDir, "operator-notes.txt")
	for _, path := range []string{otherSessionArtifact, prefixCollision, unrelatedArtifact} {
		if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prefixedDirectory := filepath.Join(sessionsDir, webTestSessionID+".directory")
	if err := os.Mkdir(prefixedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	_, _ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	if err := archive.Set("session", "unrelated-session", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Set("session", "unrelated-session", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := archive.Set("project", "unrelated-project", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Set("project", "unrelated-project", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
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
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed", suffix)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID)); !os.IsNotExist(err) {
		t.Fatal("per-session dir should be removed")
	}
	for _, path := range []string{otherSessionArtifact, prefixCollision, unrelatedArtifact, prefixedDirectory} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated path %s was touched: %v", path, err)
		}
	}
	assertArchiveDecisionAbsent(t, archive, "session", webTestSessionID)
	assertArchiveDecisionAbsent(t, archive, "project", project.ID)
	assertArchiveDecisionPresent(t, archive, "session", "unrelated-session", true)
	assertArchiveDecisionPresent(t, archive, "project", "unrelated-project", true)
	assertProjectDeleteDecisionAbsent(t, dbPath, "session", webTestSessionID)
	assertProjectDeleteDecisionAbsent(t, dbPath, "project", project.ID)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", "unrelated-session", true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", "unrelated-project", true)
}

func TestRemoveFlatProjectSessionArtifactsRejectsInvalidSessionID(t *testing.T) {
	sessionsDir := t.TempDir()
	artifact := filepath.Join(sessionsDir, "invalid.future-artifact")
	if err := os.WriteFile(artifact, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := removeFlatProjectSessionArtifacts(sessionsDir, "invalid"); err == nil {
		t.Fatal("invalid session ID must be rejected")
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("invalid session ID removed an artifact: %v", err)
	}
}

func TestProjectDeleteRemovesCanonicalProjectMembers(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	initProjectDeleteRepo(t, mainDir)
	linkedDir := filepath.Join(root, "linked")
	runProjectDeleteGit(t, mainDir, "worktree", "add", "-q", linkedDir, "-b", "feature")
	nestedDir := filepath.Join(mainDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(root, "alias")
	if err := os.Symlink(linkedDir, aliasDir); err != nil {
		t.Fatal(err)
	}

	project, err := identifier.ResolveProject(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{mainDir, linkedDir, nestedDir, aliasDir}
	projectsRoot := filepath.Join(root, "projects")
	stateDir := filepath.Join(projectsRoot, project.ID)
	for i, path := range paths {
		writeSession(t, stateDir, projectDeleteCanonicalSessionIDs[i], path)
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Deleted []string `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Deleted) != len(paths) {
		t.Fatalf("deleted=%v, want main, linked worktree, nested, and symlink sessions", resp.Deleted)
	}
	for i, id := range projectDeleteCanonicalSessionIDs {
		metaPath := filepath.Join(stateDir, "sessions", id+".meta.json")
		if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
			t.Fatalf("session %d (%s) meta survived canonical project deletion: %v", i, paths[i], err)
		}
	}
}

func TestProjectDeleteResolutionFailureDoesNotPartiallyDelete(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(root, "projects")
	stateDir := filepath.Join(projectsRoot, project.ID)
	writeSession(t, stateDir, projectDeleteCanonicalSessionIDs[0], projectDir)
	writeSession(t, stateDir, projectDeleteCanonicalSessionIDs[1], filepath.Join(root, "missing"))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want resolution failure", rec.Code, rec.Body.String())
	}
	metaPath := filepath.Join(stateDir, "sessions", projectDeleteCanonicalSessionIDs[0]+".meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("valid project session was partially deleted: %v", err)
	}
}

func initProjectDeleteRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runProjectDeleteGit(t, filepath.Dir(dir), "init", "-q", filepath.Base(dir))
	runProjectDeleteGit(t, dir, "-c", "user.name=serf-test", "-c", "user.email=serf-test@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
}

func runProjectDeleteGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestProjectDeleteRejectsKeyWorkingDirMismatch(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	wrongDir := filepath.Join(root, "wrong")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wrongDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	_, _ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})
	body := `{"key":"` + project.ID + `","working_dir":"` + wrongDir + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch must be rejected 400, got %d", rec.Code)
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
}

func TestProjectDeleteRefusesWhenLive(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	_, _ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: webTestSessionID, Status: "active"})
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: roster})
	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("live project delete must 409, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); os.IsNotExist(err) {
		t.Fatal("nothing should be removed when refused")
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
}

func TestProjectDeleteSkipsSessionThatBecomesLive(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	checks := 0
	oldProjectSessionLive := projectSessionLive
	projectSessionLive = func(*hubcore.Roster, string) bool {
		checks++
		return checks > 1
	}
	t.Cleanup(func() { projectSessionLive = oldProjectSessionLive })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
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
		t.Fatal(err)
	}
	if len(resp.Deleted) != 0 || len(resp.Skipped) != 1 || resp.Skipped[0].ID != webTestSessionID {
		t.Fatalf("session that became live must only be skipped: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); err != nil {
		t.Fatalf("live session artifact was removed: %v", err)
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
}

func TestProjectDeleteDoesNotUnlinkSessionReservedAfterLivenessProbe(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	resumeLogger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = resumeLogger.Close() })
	var reserveErr error
	checks := 0
	oldProjectSessionLive := projectSessionLive
	projectSessionLive = func(*hubcore.Roster, string) bool {
		checks++
		if checks == 1 {
			reserveErr = resumeLogger.ReserveSession(webTestSessionID)
		}
		return false
	}
	t.Cleanup(func() { projectSessionLive = oldProjectSessionLive })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if reserveErr != nil {
		t.Fatalf("resume reservation: %v", reserveErr)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Deleted []string            `json:"deleted"`
		Skipped []projectDeleteSkip `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Deleted) != 0 || len(resp.Skipped) != 1 || resp.Skipped[0].ID != webTestSessionID {
		t.Fatalf("reserved session must only be skipped: %+v", resp)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+suffix)); err != nil {
			t.Fatalf("reserved session artifact %s was removed: %v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID)); err != nil {
		t.Fatalf("reserved per-session directory was removed: %v", err)
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
}

func TestProjectDeleteRemovesAPILogOnlyAfterResumeArtifacts(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

	oldRemove := removeProjectSessionFile
	removeProjectSessionFile = func(path string) error {
		if err := oldRemove(path); err != nil {
			return err
		}
		if filepath.Base(path) != webTestSessionID+".api.jsonl" {
			return nil
		}
		contender, err := llm.NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger after API unlink: %v", err)
		}
		defer contender.Close() //nolint:errcheck
		if err := contender.ReserveSession(webTestSessionID); err != nil {
			t.Fatalf("ReserveSession after API unlink: %v", err)
		}
		metaPath := filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")
		if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
			t.Fatalf("metadata still visible after API unlink: %v", err)
		}
		transcriptPath := filepath.Join(stateDir, "sessions", webTestSessionID+".transcript.jsonl")
		if _, err := os.Stat(transcriptPath); !os.IsNotExist(err) {
			t.Fatalf("transcript still visible after API unlink: %v", err)
		}
		return nil
	}
	t.Cleanup(func() { removeProjectSessionFile = oldRemove })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestProjectDeleteSkipsOnRemoveFailure forces a deterministic flat-file
// removal failure and asserts the session lands only in skipped: never also
// in deleted, its decision rows are left intact, and its files are left in
// place so the delete is cleanly retriable.
func TestProjectDeleteSkipsOnRemoveFailure(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	_, _ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	sessDir := filepath.Join(stateDir, "sessions")
	oldRemove := removeProjectSessionFile
	removeProjectSessionFile = func(path string) error {
		if filepath.Base(path) == webTestSessionID+".future-artifact" {
			return errors.New("flat-file removal failed")
		}
		return oldRemove(path)
	}
	t.Cleanup(func() { removeProjectSessionFile = oldRemove })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
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
		if id == webTestSessionID {
			t.Fatalf("webTestSessionID must not appear in deleted when its files could not be removed: %+v", resp)
		}
	}
	skippedCount := 0
	for _, sk := range resp.Skipped {
		if sk.ID == webTestSessionID {
			skippedCount++
		}
	}
	if skippedCount != 1 {
		t.Fatalf("want exactly 1 skipped entry for webTestSessionID, got %d: %+v", skippedCount, resp)
	}

	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
	if _, err := os.Stat(filepath.Join(sessDir, webTestSessionID+".meta.json")); err != nil {
		t.Fatalf(".meta.json must still exist after a failed removal: %v", err)
	}
}

func TestProjectDeletePreservesDecisionsWhenSessionDirectoryRemovalFails(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	oldRemove := removeProjectSessionDir
	removeProjectSessionDir = func(string) error { return errors.New("session directory removal failed") }
	t.Cleanup(func() { removeProjectSessionDir = oldRemove })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Deleted []string            `json:"deleted"`
		Skipped []projectDeleteSkip `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Deleted) != 0 || len(response.Skipped) != 1 || response.Skipped[0].ID != webTestSessionID {
		t.Fatalf("directory failure must skip the session: %+v", response)
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".api.jsonl")); err != nil {
		t.Fatalf("API log must remain after directory removal failure: %v", err)
	}
}

func TestProjectDeletePreservesDecisionsWhenAPILogRemovalFails(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	oldRemove := removeProjectSessionFile
	removeProjectSessionFile = func(path string) error {
		if filepath.Base(path) == webTestSessionID+".api.jsonl" {
			return errors.New("API log removal failed")
		}
		return oldRemove(path)
	}
	t.Cleanup(func() { removeProjectSessionFile = oldRemove })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Deleted []string            `json:"deleted"`
		Skipped []projectDeleteSkip `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Deleted) != 0 || len(response.Skipped) != 1 || response.Skipped[0].ID != webTestSessionID {
		t.Fatalf("API-log failure must skip the session: %+v", response)
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".api.jsonl")); err != nil {
		t.Fatalf("API log must remain after injected failure: %v", err)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s should already be gone before the final API-log failure, err=%v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID)); !os.IsNotExist(err) {
		t.Fatalf("session directory should already be gone before the final API-log failure, err=%v", err)
	}
}

func TestProjectDeleteRetainsSkippedDecisionsAndRemovesOnlyDeletedDecisions(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	deletedID := projectDeleteCanonicalSessionIDs[0]
	skippedID := projectDeleteCanonicalSessionIDs[1]
	writeSession(t, stateDir, deletedID, project.CanonicalPath)
	writeSession(t, stateDir, skippedID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, deletedID, skippedID)
	if err := archive.Set("session", "unrelated-session", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Set("session", "unrelated-session", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := archive.Set("project", "unrelated-project", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Set("project", "unrelated-project", true, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})

	checks := 0
	oldProjectSessionLive := projectSessionLive
	projectSessionLive = func(_ *hubcore.Roster, id string) bool {
		checks++
		return checks > 2 && id == skippedID
	}
	t.Cleanup(func() { projectSessionLive = oldProjectSessionLive })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body))
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Deleted []string            `json:"deleted"`
		Skipped []projectDeleteSkip `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Deleted) != 1 || response.Deleted[0] != deletedID || len(response.Skipped) != 1 || response.Skipped[0].ID != skippedID {
		t.Fatalf("partial deletion response=%+v", response)
	}
	assertArchiveDecisionAbsent(t, archive, "session", deletedID)
	assertProjectDeleteDecisionAbsent(t, dbPath, "session", deletedID)
	assertArchiveDecisionPresent(t, archive, "session", skippedID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", skippedID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
	assertArchiveDecisionPresent(t, archive, "session", "unrelated-session", true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", "unrelated-session", true)
	assertArchiveDecisionPresent(t, archive, "project", "unrelated-project", true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", "unrelated-project", true)
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", deletedID+".meta.json")); !os.IsNotExist(err) {
		t.Fatalf("successfully deleted session metadata survived, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", skippedID+".meta.json")); err != nil {
		t.Fatalf("skipped session metadata was removed: %v", err)
	}
}

func TestProjectDeleteReportsFavoriteStoreFailureAfterArtifactRemoval(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, webTestSessionID)
	favorite.SetFs(failingMkdirAllFS{Fs: afero.NewOsFs(), err: errors.New("favorite delete setup failure")})
	pokes := 0
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Past:          past,
		Archive:       archive,
		Favorite:      favorite,
		Roster:        hubcore.NewRosterWithEntries(),
		PokeAttention: func() { pokes++ },
	})
	defer hub.Close()
	past.SetOnChange(func() { notifyTreeChanged(web.appRPC) })
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	response, err := http.Post(hub.URL+"/api/project/delete", "application/json", newBody(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("favorite store failure status=%d", response.StatusCode)
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure.Error, "favorite store error: favorite delete setup failure") {
		t.Fatalf("favorite store failure was not reported: %s", failure.Error)
	}
	assertSingleNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
	if pokes != 1 {
		t.Fatalf("PokeAttention calls=%d, want exactly one after physical deletion", pokes)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed before reporting the store failure, err=%v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID)); !os.IsNotExist(err) {
		t.Fatalf("session directory should be removed before reporting the store failure, err=%v", err)
	}
	// Read the original database through a new store-independent SQL path: the
	// failing FavoriteStore must not make the retained row disappear or pretend
	// that the removed artifact was restored.
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
}

func TestProjectDeleteBroadcastsTreeChangedExactlyOnce(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	defer hub.Close()
	// Mirror runMain's composed wiring (main.go): PastIndex.Rebuild's own
	// onChange hook is the sole broadcast source for this path — the handler
	// no longer calls notifyTreeChanged directly (it would double-broadcast).
	past.SetOnChange(func() { notifyTreeChanged(web.appRPC) })
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	resp, err := http.Post(hub.URL+"/api/project/delete", "application/json", newBody(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	assertSingleNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}

// TestProjectDeleteDoesNotBroadcastWhenNothingRemoved covers the no-op path:
// every session in the target project gets skipped, so neither the artifact
// index nor any decision row changes.
func TestProjectDeleteDoesNotBroadcastWhenNothingRemoved(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
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

	// Force the project's one session to be skipped (becomes live mid-request)
	// rather than actually removed — same technique as
	// TestProjectDeleteSkipsSessionThatBecomesLive.
	checks := 0
	oldProjectSessionLive := projectSessionLive
	projectSessionLive = func(*hubcore.Roster, string) bool {
		checks++
		return checks > 1
	}
	t.Cleanup(func() { projectSessionLive = oldProjectSessionLive })

	body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
	resp, err := http.Post(hub.URL+"/api/project/delete", "application/json", newBody(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got struct {
		Deleted []string `json:"deleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Deleted) != 0 {
		t.Fatalf("expected nothing actually deleted (session skipped), got %+v", got.Deleted)
	}

	assertNoNotification(t, client, web.appRPC, appwire.NotifySerfTreeChanged)
}
