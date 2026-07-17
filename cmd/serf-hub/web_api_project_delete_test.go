package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
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
	_ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	_ = archive.Set("session", webTestSessionID, true, time.Unix(1_700_000_000, 0))
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
	d, _ := archive.Decisions()
	if _, present := d[hubcore.ArchiveKey{Kind: "session", ID: webTestSessionID}]; present {
		t.Fatalf("session archive row should be scrubbed: %v", d)
	}
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
	if err := past.Rebuild(); err != nil {
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
	if err := past.Rebuild(); err != nil {
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
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	_ = past.Rebuild()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
	body := `{"key":"` + project.ID + `","working_dir":"` + wrongDir + `"}`
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
	_ = past.Rebuild()
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: webTestSessionID, Status: "active"})
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
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
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

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
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})

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
	if err := past.Rebuild(); err != nil {
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
	_ = past.Rebuild()
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	_ = archive.Set("session", webTestSessionID, true, time.Unix(1_700_000_000, 0))
	_ = favorite.Set("session", webTestSessionID, true, time.Unix(1_700_000_000, 0))
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

	ad, _ := archive.Decisions()
	if _, present := ad[hubcore.ArchiveKey{Kind: "session", ID: webTestSessionID}]; !present {
		t.Fatal("session archive row must survive a failed removal (retriable)")
	}
	fd, _ := favorite.Favorites()
	if _, present := fd[hubcore.ArchiveKey{Kind: "session", ID: webTestSessionID}]; !present {
		t.Fatal("session favorite row must survive a failed removal (retriable)")
	}
	if _, err := os.Stat(filepath.Join(sessDir, webTestSessionID+".meta.json")); err != nil {
		t.Fatalf(".meta.json must still exist after a failed removal: %v", err)
	}
}
