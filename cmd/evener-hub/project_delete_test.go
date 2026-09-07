package hub

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
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
	for name, params := range map[string]appwire.ProjectDeleteParams{
		"mismatch":   {Key: project.ID, WorkingDir: filepath.Join(root, "other")},
		"no-project": {Key: "no-project", WorkingDir: projectDir},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := dispatchProjectDelete(t, web, params)
			var wireErr appwire.WireError
			if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
				t.Fatalf("error = %v, want AppWire invalid params", err)
			}
		})
	}
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

func TestProjectDeleteRemovesPinsOnlyForDeletedSessions(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	deletedID, skippedID := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1]
	writeSession(t, stateDir, deletedID, project.CanonicalPath)
	writeSession(t, stateDir, skippedID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	pins := hubcore.NewPinSectionStore(dbPath)
	section, _, err := pins.CreateOrReuseAndAssign("Research", deletedID, timeNowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pins.Assign(section.ID, skippedID, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: past, PinSections: pins, Roster: hubcore.NewRosterWithEntries()})

	checks := 0
	oldProjectSessionLive := projectSessionLive
	projectSessionLive = func(_ *hubcore.Roster, id string) bool {
		checks++
		return checks > 2 && id == skippedID
	}
	t.Cleanup(func() { projectSessionLive = oldProjectSessionLive })
	if _, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	}); err != nil {
		t.Fatalf("dispatch project delete: %v", err)
	}
	assignments, err := pins.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := assignments[deletedID]; ok {
		t.Fatalf("deleted assignment survived: %+v", assignments)
	}
	if _, ok := assignments[skippedID]; !ok {
		t.Fatalf("skipped assignment removed: %+v", assignments)
	}
}

func TestProjectDeleteReportsPinStoreCleanupError(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "project-delete-0123456789")
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	pins := hubcore.NewPinSectionStore(dbPath)
	if _, _, err := pins.CreateOrReuseAndAssign("Research", webTestSessionID, timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	pins.SetFs(failingMkdirAllFS{Fs: afero.NewOsFs(), err: errors.New("forced pin cleanup failure")})
	web := NewWebServer(hubcore.WebConfig{Past: past, PinSections: pins, Roster: hubcore.NewRosterWithEntries()})
	_, err = dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInternalError || !strings.Contains(wireErr.Message, "pin section store error: forced pin cleanup failure") {
		t.Fatalf("cleanup failure = %v, want AppWire internal error", err)
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

	resp, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete: %v", err)
	}
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

	resp, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete: %v", err)
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

	_, err = dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInternalError {
		t.Fatalf("error = %v, want AppWire internal resolution failure", err)
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
	runProjectDeleteGit(t, dir, "-c", "user.name=evener-test", "-c", "user.email=evener-test@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
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
	_, err = dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: wrongDir})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("mismatch error = %v, want AppWire invalid params", err)
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
	_, err = dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error = %v, want AppWire conflict", err)
	}
	data, ok := wireErr.Data.(appwire.ProjectDeleteConflictData)
	if !ok || len(data.Live) != 1 || data.Live[0] != hubcore.ShortID(webTestSessionID) {
		t.Fatalf("conflict data = %#v, want live session details", wireErr.Data)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); os.IsNotExist(err) {
		t.Fatal("nothing should be removed when refused")
	}
	assertArchiveDecisionPresent(t, archive, "session", webTestSessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", webTestSessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
}

// TestProjectDeleteRemovesCrashedSessionAndStaleRendezvous is kata 8at6's
// core regression: a retained crash marker (LiveEntry.Crashed=true, written
// when Roster.Refresh confirms the daemon's PID is gone but its rendezvous
// file survived - see hubcore.Roster.Refresh) is historical error state, not
// a live daemon. Project deletion must still be able to acquire ownership and
// clean the session, including the stale rendezvous record the dead daemon
// left behind.
func TestProjectDeleteRemovesCrashedSessionAndStaleRendezvous(t *testing.T) {
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

	runDir := filepath.Join(root, "run")
	staleEntry := rendezvous.Entry{
		PID:       4242,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  webTestSessionID,
		SessionID: webTestSessionID,
	}
	writeRendezvous(t, runDir, staleEntry)
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     staleEntry,
		SessionID: webTestSessionID,
		Status:    "errored",
		Crashed:   true,
	})
	web := NewWebServer(hubcore.WebConfig{
		Past: past, Archive: archive, Favorite: favorite, Roster: roster, RunDir: runDir,
	})

	resp, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete with retained crash marker: %v", err)
	}
	if len(resp.Deleted) != 1 || resp.Deleted[0] != webTestSessionID || len(resp.Skipped) != 0 {
		t.Fatalf("crashed session must be deleted outright: %+v", resp)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed", suffix)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID)); !os.IsNotExist(err) {
		t.Fatal("per-session dir should be removed")
	}
	assertArchiveDecisionAbsent(t, archive, "session", webTestSessionID)
	assertProjectDeleteDecisionAbsent(t, dbPath, "session", webTestSessionID)
	if _, ok := past.Find(webTestSessionID); ok {
		t.Fatal("past index row should be removed")
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.SessionID == webTestSessionID || e.ThreadID == webTestSessionID {
			t.Fatalf("stale rendezvous record for %s should be removed, found %+v", webTestSessionID, e)
		}
	}
}

// TestProjectDeleteRefusesLiveSessionWhoseProbeTransientlyFails covers the
// other half of kata 8at6's predicate: a live PID whose /status probe merely
// timed out must NOT be mistaken for a crash. hubcore.Roster.Refresh carries
// the previous (non-crashed) entry forward whenever a probe fails but the
// PID is still alive (see its "keep-alive fallback" comment), so this drives
// a real Roster through that exact path with a fake Prober rather than
// asserting against a hand-built LiveEntry - the thing under test is that
// the crash-vs-live distinction survives the roster's own transient-failure
// handling, not just that projectSessionLive reads a Crashed field.
func TestProjectDeleteRefusesLiveSessionWhoseProbeTransientlyFails(t *testing.T) {
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

	runDir := filepath.Join(root, "run")
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       os.Getpid(),
		ThreadID:  webTestSessionID,
		SessionID: webTestSessionID,
	})
	prober := &fakeProber{sessionID: webTestSessionID, status: "active"}
	roster := hubcore.NewRoster(runDir, prober)
	roster.Refresh()
	if entry, ok := roster.Find(webTestSessionID); !ok || entry.Crashed {
		t.Fatalf("precondition: session must be live and not crashed after the first refresh, ok=%v crashed=%v", ok, entry.Crashed)
	}

	prober.shouldFail = true
	roster.Refresh() // the probe now fails, but the PID (this test process) is still alive
	if entry, ok := roster.Find(webTestSessionID); !ok || entry.Crashed {
		t.Fatalf("precondition: a transient probe failure on a live PID must not mark Crashed, ok=%v crashed=%v", ok, entry.Crashed)
	}

	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
	_, err = dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
		t.Fatalf("error = %v, want AppWire conflict for a live session whose probe merely timed out", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); os.IsNotExist(err) {
		t.Fatal("nothing should be removed when refused")
	}
}

// TestProjectDeleteRefreshesRosterAndBustsTreeMemo pins the post-delete
// freshness contract: a successful project delete must (1) refresh the
// in-memory roster BEFORE poking the attention watcher, so the immediate
// follow-up navigation read issued by the frontend is served from a roster that
// already dropped the deleted sessions instead of ghost rows until the next
// 5s tick, and (2) bump the shared InputsVersion so the tree memo is busted
// even when the past-index rebuild reports no delta.
func TestProjectDeleteRefreshesRosterAndBustsTreeMemo(t *testing.T) {
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

	// A retained crash marker unblocks deletion (kata 8at6) while still being
	// listed by the roster snapshot the tree is built from.
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{PID: 4242, ThreadID: webTestSessionID, SessionID: webTestSessionID},
		SessionID: webTestSessionID,
		Status:    "errored",
		Crashed:   true,
	})

	var events []string
	prevRefresh := hubRosterRefresh
	hubRosterRefresh = func(ctx context.Context, r *hubcore.Roster) error {
		events = append(events, "roster-refresh")
		return prevRefresh(ctx, r)
	}
	t.Cleanup(func() { hubRosterRefresh = prevRefresh })

	inputs := &hubcore.InputsVersion{}
	web := NewWebServer(hubcore.WebConfig{
		Past: past, Roster: roster, Inputs: inputs,
		PokeAttention: func() { events = append(events, "poke") },
	})
	before := inputs.Load()

	if _, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	}); err != nil {
		t.Fatalf("dispatch project delete: %v", err)
	}

	if _, ok := roster.Find(webTestSessionID); ok {
		t.Fatal("deleted session must be gone from the roster snapshot right after the delete response")
	}
	if got := inputs.Load(); got <= before {
		t.Fatalf("InputsVersion=%d, want a bump past %d so the tree memo is busted", got, before)
	}
	if len(events) < 2 || events[0] != "roster-refresh" || events[1] != "poke" {
		t.Fatalf("events=%v, want the roster refreshed before PokeAttention", events)
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

	resp, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete: %v", err)
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

	resp, deleteErr := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if reserveErr != nil {
		t.Fatalf("resume reservation: %v", reserveErr)
	}
	if deleteErr != nil {
		t.Fatalf("dispatch project delete: %v", deleteErr)
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

	if _, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	}); err != nil {
		t.Fatalf("dispatch project delete: %v", err)
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

	resp, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete: %v", err)
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

func TestProjectDeleteDeletionStateResumesAfterRestart(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "work")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("stop deletion after durable fence")
	oldRemove := removeProjectSessionFile
	removeProjectSessionFile = func(path string) error {
		if filepath.Base(path) == webTestSessionID+".future-artifact" {
			return injected
		}
		return oldRemove(path)
	}
	t.Cleanup(func() { removeProjectSessionFile = oldRemove })

	cfg := hubcore.WebConfig{
		HubStateRoot: root,
		StateDir:     root,
		Past:         past,
		Roster:       hubcore.NewRosterWithEntries(),
	}
	web := NewWebServer(cfg)
	if _, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	}); err != nil {
		t.Fatalf("first project delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); err != nil {
		t.Fatalf("failed cleanup removed later session state: %v", err)
	}
	deletionStatePath := filepath.Join(root, "deletions", "state.json")
	raw, err := os.ReadFile(deletionStatePath)
	if err != nil {
		t.Fatalf("read durable deleting state: %v", err)
	}
	if !strings.Contains(string(raw), `"state":"deleting"`) {
		t.Fatalf("durable state after cleanup failure = %s, want deleting", raw)
	}

	removeProjectSessionFile = oldRemove
	restoredPast := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := restoredPast.Rebuild(); err != nil {
		t.Fatal(err)
	}
	_ = NewWebServer(hubcore.WebConfig{
		HubStateRoot: root,
		StateDir:     root,
		Past:         restoredPast,
		Roster:       hubcore.NewRosterWithEntries(),
	})
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); !os.IsNotExist(err) {
		t.Fatalf("startup did not resume deleting target cleanup: %v", err)
	}
	raw, err = os.ReadFile(deletionStatePath)
	if err != nil {
		t.Fatalf("read durable deleted state: %v", err)
	}
	if !strings.Contains(string(raw), `"state":"deleted"`) {
		t.Fatalf("durable state after resumed cleanup = %s, want deleted", raw)
	}
}

func TestProjectDeleteRequestResumesCommittedDeletionWithoutPastEntry(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "work")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubStateRoot: root,
		StateDir:     root,
		Past:         past,
		Roster:       hubcore.NewRosterWithEntries(),
	})
	oldRemoveDir := removeProjectSessionDir
	removeProjectSessionDir = func(string) error {
		return errors.New("stop after flat session cleanup")
	}
	t.Cleanup(func() { removeProjectSessionDir = oldRemoveDir })

	params := appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: project.CanonicalPath}
	if _, err := dispatchProjectDelete(t, web, params); err != nil {
		t.Fatalf("first project delete: %v", err)
	}
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, ok := past.Find(webTestSessionID); ok {
		t.Fatal("flat cleanup left deleted session in Past")
	}

	removeProjectSessionDir = oldRemoveDir
	if _, err := dispatchProjectDelete(t, web, params); err != nil {
		t.Fatalf("retry project delete: %v", err)
	}
	store, err := hubcore.NewDeletionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := store.TargetState(localAppRef(webTestSessionID), webTestSessionID)
	if !ok || state != hubcore.DeletionStateDeleted {
		t.Fatalf("retry deletion state = %q, %v, want deleted", state, ok)
	}
}

func TestProjectDeleteDeletionStateResumesEveryCleanupArtifact(t *testing.T) {
	for _, step := range []string{
		"session-directory",
		"mutation",
		"queue",
		"task",
		"rendezvous",
		"api-log",
		"past-index",
	} {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			projectDir := filepath.Join(root, "work")
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}
			project, err := identifier.ResolveProject(projectDir)
			if err != nil {
				t.Fatal(err)
			}
			stateDir := filepath.Join(root, "projects", project.ID)
			writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
			for _, kind := range []string{"mutations", "queues", "tasks"} {
				if err := os.MkdirAll(filepath.Join(stateDir, kind), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stateDir, kind, webTestSessionID+".json"), []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runDir := filepath.Join(root, "run")
			writeRendezvous(t, runDir, rendezvous.Entry{
				PID:       4242,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws://127.0.0.1:1/rpc",
				SourceID:  "local",
				ThreadID:  webTestSessionID,
				SessionID: webTestSessionID,
			})
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}

			oldRemoveFile := removeProjectSessionFile
			oldRemoveDir := removeProjectSessionDir
			oldRemoveRendezvous := removeProjectSessionRendezvousEntry
			oldRebuildPast := rebuildProjectDeletionPast
			t.Cleanup(func() {
				removeProjectSessionFile = oldRemoveFile
				removeProjectSessionDir = oldRemoveDir
				removeProjectSessionRendezvousEntry = oldRemoveRendezvous
				rebuildProjectDeletionPast = oldRebuildPast
			})
			failStep := true
			injected := errors.New("stop deletion at " + step)
			removeProjectSessionFile = func(path string) error {
				if failStep {
					switch step {
					case "mutation":
						if path == filepath.Join(stateDir, "mutations", webTestSessionID+".json") {
							return injected
						}
					case "queue":
						if path == filepath.Join(stateDir, "queues", webTestSessionID+".json") {
							return injected
						}
					case "task":
						if path == filepath.Join(stateDir, "tasks", webTestSessionID+".json") {
							return injected
						}
					case "api-log":
						if path == filepath.Join(stateDir, "sessions", webTestSessionID+".api.jsonl") {
							return injected
						}
					}
				}
				return oldRemoveFile(path)
			}
			removeProjectSessionDir = func(path string) error {
				if failStep && step == "session-directory" {
					return injected
				}
				return oldRemoveDir(path)
			}
			removeProjectSessionRendezvousEntry = func(dir string, pid int) error {
				if failStep && step == "rendezvous" {
					return injected
				}
				return oldRemoveRendezvous(dir, pid)
			}
			rebuildProjectDeletionPast = func(past *hubcore.PastIndex) (bool, error) {
				if failStep && step == "past-index" {
					return false, injected
				}
				return oldRebuildPast(past)
			}

			cfg := hubcore.WebConfig{
				HubStateRoot: root,
				StateDir:     root,
				RunDir:       runDir,
				Past:         past,
				Roster:       hubcore.NewRosterWithEntries(),
			}
			web := NewWebServer(cfg)
			_, deleteErr := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
				Key:        project.ID,
				WorkingDir: project.CanonicalPath,
			})
			if step == "past-index" {
				var wireErr appwire.WireError
				if !errors.As(deleteErr, &wireErr) || wireErr.Code != appwire.CodeInternalError {
					t.Fatalf("first delete error = %v, want AppWire internal error", deleteErr)
				}
			} else if deleteErr != nil {
				t.Fatalf("first project delete: %v", deleteErr)
			}
			store, err := hubcore.NewDeletionStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if state, ok := store.TargetState(localAppRef(webTestSessionID), webTestSessionID); !ok || state != hubcore.DeletionStateDeleting {
				t.Fatalf("failed %s cleanup state = %q, %v, want deleting", step, state, ok)
			}

			failStep = false
			restoredPast := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := restoredPast.Rebuild(); err != nil {
				t.Fatal(err)
			}
			_ = NewWebServer(hubcore.WebConfig{
				HubStateRoot: root,
				StateDir:     root,
				RunDir:       runDir,
				Past:         restoredPast,
				Roster:       hubcore.NewRosterWithEntries(),
			})
			store, err = hubcore.NewDeletionStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if state, ok := store.TargetState(localAppRef(webTestSessionID), webTestSessionID); !ok || state != hubcore.DeletionStateDeleted {
				t.Fatalf("resumed %s cleanup state = %q, %v, want deleted", step, state, ok)
			}
		})
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

	response, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete: %v", err)
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

	response, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete: %v", err)
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

	response, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("dispatch project delete: %v", err)
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
		HubStateRoot:  root,
		StateDir:      root,
		Past:          past,
		Archive:       archive,
		Favorite:      favorite,
		Roster:        hubcore.NewRosterWithEntries(),
		PokeAttention: func() { pokes++ },
	})
	defer hub.Close()
	past.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, err = client.ProjectDelete(context.Background(), appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInternalError || !strings.Contains(wireErr.Message, "favorite store error: favorite delete setup failure") {
		t.Fatalf("favorite store failure = %v, want AppWire internal error", err)
	}
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

	favorite.SetFs(afero.NewOsFs())
	if err := web.resumeProjectDeletions(); err != nil {
		t.Fatalf("resume deletion after favorite recovery: %v", err)
	}
	store, err := hubcore.NewDeletionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if state, ok := store.TargetState(localAppRef(webTestSessionID), webTestSessionID); !ok || state != hubcore.DeletionStateDeleted {
		t.Fatalf("decision retry state = %q, %v, want deleted", state, ok)
	}
	assertProjectDeleteDecisionAbsent(t, dbPath, "session", webTestSessionID)
	assertProjectDeleteDecisionAbsent(t, dbPath, "project", project.ID)
}

func TestProjectDeleteDoesNotScrubProjectRowsAfterPastSnapshotRacesWithRebuild(t *testing.T) {
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
	sessionID := projectDeleteCanonicalSessionIDs[0]
	writeSession(t, stateDir, sessionID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, sessionID)
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

	inputs := &hubcore.InputsVersion{}
	past.SetOnChange(inputs.Bump)
	oldBuild := hubBuildNavigationTree
	interleaveObserved := false
	hubBuildNavigationTree = func(metas []schema.SessionMeta, live []hubcore.LiveEntry, decisions map[hubcore.ArchiveKey]bool, projects map[string]identifier.Project) hubcore.Tree {
		tree := oldBuild(metas, live, decisions, projects)
		if err := os.Remove(filepath.Join(stateDir, "sessions", sessionID+".meta.json")); err != nil {
			t.Fatalf("remove session metadata during snapshot interleave: %v", err)
		}
		if _, err := past.Rebuild(); err != nil {
			t.Fatalf("rebuild during snapshot interleave: %v", err)
		}
		interleaveObserved = true
		return tree
	}
	t.Cleanup(func() { hubBuildNavigationTree = oldBuild })

	web := NewWebServer(hubcore.WebConfig{
		Past:     past,
		Archive:  archive,
		Favorite: favorite,
		Roster:   hubcore.NewRosterWithEntries(),
		Inputs:   inputs,
	})
	if _, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	}); err != nil {
		t.Fatalf("dispatch project delete: %v", err)
	}
	if !interleaveObserved {
		t.Fatal("snapshot/rebuild interleave did not execute")
	}
	assertArchiveDecisionPresent(t, archive, "session", sessionID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", sessionID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
	assertArchiveDecisionPresent(t, archive, "session", "unrelated-session", true)
	assertArchiveDecisionPresent(t, archive, "project", "unrelated-project", true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", "unrelated-session", true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", "unrelated-project", true)
	for _, suffix := range []string{".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", sessionID+suffix)); err != nil {
			t.Fatalf("remaining session artifact %s was touched: %v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", sessionID)); err != nil {
		t.Fatalf("remaining session directory was touched: %v", err)
	}
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
	past.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
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

	got, err := client.ProjectDelete(context.Background(), appwire.ProjectDeleteParams{
		Key:        project.ID,
		WorkingDir: project.CanonicalPath,
	})
	if err != nil {
		t.Fatalf("project delete: %v", err)
	}
	if len(got.Deleted) != 0 {
		t.Fatalf("expected nothing actually deleted (session skipped), got %+v", got.Deleted)
	}
}
