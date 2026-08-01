package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

// sessionDeleteResponse mirrors handleAPISessionDelete's JSON envelope, which
// is deliberately the same shape handleAPIProjectDelete already returns
// (deleted/skipped) - see that handler's own doc comment - just for a target
// set of at most one.
type sessionDeleteResponse struct {
	Deleted []string            `json:"deleted"`
	Skipped []projectDeleteSkip `json:"skipped"`
}

func postSessionDelete(t *testing.T, web *WebServer, id string) (*httptest.ResponseRecorder, sessionDeleteResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:"+id+"/delete", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	var resp sessionDeleteResponse
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v, body=%s", err, rec.Body.String())
		}
	}
	return rec, resp
}

// TestSessionDeleteRemovesOnlyTarget covers n15j's verification #1: deleting
// one ended session from a project with an unrelated survivor removes only
// the target - siblings, their artifacts, and their decisions are untouched.
func TestSessionDeleteRemovesOnlyTarget(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	targetID := projectDeleteCanonicalSessionIDs[0]
	survivorID := projectDeleteCanonicalSessionIDs[1]
	writeSession(t, stateDir, targetID, project.CanonicalPath)
	writeSession(t, stateDir, survivorID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, targetID, survivorID)
	web := NewWebServer(hubcore.WebConfig{
		StateDir: root, Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries(),
	})

	rec, resp := postSessionDelete(t, web, targetID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(resp.Deleted) != 1 || resp.Deleted[0] != targetID || len(resp.Skipped) != 0 {
		t.Fatalf("want only the target deleted: %+v", resp)
	}

	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", targetID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("target %s should be removed", suffix)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", targetID)); !os.IsNotExist(err) {
		t.Fatal("target per-session dir should be removed")
	}
	assertArchiveDecisionAbsent(t, archive, "session", targetID)
	assertProjectDeleteDecisionAbsent(t, dbPath, "session", targetID)
	if _, ok := past.Find(targetID); ok {
		t.Fatal("target past index row should be removed")
	}

	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", survivorID+suffix)); err != nil {
			t.Fatalf("survivor %s must survive: %v", suffix, err)
		}
	}
	assertArchiveDecisionPresent(t, archive, "session", survivorID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", survivorID, true)
	if _, ok := past.Find(survivorID); !ok {
		t.Fatal("survivor past index row must remain")
	}
	// The project-level decision rows are shared by both sessions; a
	// single-session delete must never touch them (project delete's own
	// WholeProject-gated scrub does not apply here).
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
}

// TestSessionDeleteRefusesLiveTarget covers half of n15j's verification #2: a
// reachable live daemon refuses the delete with no partial artifact removal.
func TestSessionDeleteRefusesLiveTarget(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: webTestSessionID, Status: "active"})
	web := NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Roster: roster})

	rec, resp := postSessionDelete(t, web, webTestSessionID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(resp.Deleted) != 0 || len(resp.Skipped) != 1 || resp.Skipped[0].ID != webTestSessionID {
		t.Fatalf("live target must be refused via skipped, not deleted: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); err != nil {
		t.Fatalf("nothing should be removed when refused: %v", err)
	}
}

// TestSessionDeleteRefusesWhenAlreadyReserved covers the other half of
// n15j's verification #2: a session a concurrent resume already reserved
// (the API-log ownership gate) is refused with no partial artifact removal,
// even though the roster itself doesn't yet show it live.
func TestSessionDeleteRefusesWhenAlreadyReserved(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries()})

	resumeLogger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = resumeLogger.Close() })
	if err := resumeLogger.ReserveSession(webTestSessionID); err != nil {
		t.Fatalf("simulate a resume's reservation: %v", err)
	}

	rec, resp := postSessionDelete(t, web, webTestSessionID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(resp.Deleted) != 0 || len(resp.Skipped) != 1 || resp.Skipped[0].ID != webTestSessionID {
		t.Fatalf("reserved target must be refused via skipped, not deleted: %+v", resp)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+suffix)); err != nil {
			t.Fatalf("reserved session artifact %s was removed: %v", suffix, err)
		}
	}
}

// A spawned session writes its own daemon log under <run-dir>/logs
// (spawn_daemonlog.go). Nothing removed it, ever: rendezvous.List skips that
// subdirectory and hubcore.Roster prunes rendezvous entries only, so a machine
// that had spawned sessions for months kept every daemon log it ever wrote
// (kata dd8d). Deleting the session is the one moment the hub knows for
// certain that nobody owns the file, which makes it the only place it can go
// without inventing an age policy — an operator reads these after a crash.
//
// The unrelated session's log is the other half: this reaps the target's file
// and only the target's.
func TestSessionDeleteRemovesTheSessionsDaemonLog(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	targetID := projectDeleteCanonicalSessionIDs[0]
	survivorID := projectDeleteCanonicalSessionIDs[1]
	writeSession(t, stateDir, targetID, project.CanonicalPath)
	writeSession(t, stateDir, survivorID, project.CanonicalPath)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	runDir := filepath.Join(root, "run")
	logDir := filepath.Join(runDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetLog := filepath.Join(logDir, daemonLogName(targetID))
	survivorLog := filepath.Join(logDir, daemonLogName(survivorID))
	for _, path := range []string{targetLog, survivorLog} {
		if err := os.WriteFile(path, []byte("[serve] listening\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	web := NewWebServer(hubcore.WebConfig{
		StateDir: root, RunDir: runDir, Past: past, Roster: hubcore.NewRosterWithEntries(),
	})
	rec, resp := postSessionDelete(t, web, targetID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(resp.Deleted) != 1 || resp.Deleted[0] != targetID {
		t.Fatalf("session should have been deleted: %+v", resp)
	}
	if _, err := os.Stat(targetLog); !os.IsNotExist(err) {
		t.Fatalf("the deleted session's daemon log is still there (stat err=%v); nothing else will ever remove it", err)
	}
	if _, err := os.Stat(survivorLog); err != nil {
		t.Fatalf("an unrelated session's daemon log was removed: %v", err)
	}
}

// TestSessionDeleteRemovesCrashedSessionAndRendezvous covers n15j's
// verification #3, reusing kata 8at6's crash-vs-live predicate: a confirmed
// crash marker is deletable, and its stale rendezvous records go with it.
func TestSessionDeleteRemovesCrashedSessionAndRendezvous(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
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
		Entry: staleEntry, SessionID: webTestSessionID, Status: "errored", Crashed: true,
	})
	web := NewWebServer(hubcore.WebConfig{StateDir: root, RunDir: runDir, Past: past, Roster: roster})

	rec, resp := postSessionDelete(t, web, webTestSessionID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want a crash marker to be deletable", rec.Code, rec.Body.String())
	}
	if len(resp.Deleted) != 1 || resp.Deleted[0] != webTestSessionID || len(resp.Skipped) != 0 {
		t.Fatalf("crashed session must be deleted outright: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); !os.IsNotExist(err) {
		t.Fatal("crashed session metadata should be removed")
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.SessionID == webTestSessionID || e.ThreadID == webTestSessionID {
			t.Fatalf("stale rendezvous record should be removed, found %+v", e)
		}
	}
}

// TestSessionDeleteIsIdempotent covers n15j's verification #4: deleting twice
// is safe (the second call is a clean no-op, not an error), and an unrelated
// session's archive/favorite/index rows survive throughout.
func TestSessionDeleteIsIdempotent(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	targetID := projectDeleteCanonicalSessionIDs[0]
	unrelatedID := projectDeleteCanonicalSessionIDs[1]
	writeSession(t, stateDir, targetID, project.CanonicalPath)
	writeSession(t, stateDir, unrelatedID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	favorite := hubcore.NewFavoriteStore(dbPath)
	seedProjectDeleteDecisions(t, archive, favorite, project.ID, targetID, unrelatedID)
	web := NewWebServer(hubcore.WebConfig{
		StateDir: root, Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries(),
	})

	firstRec, firstResp := postSessionDelete(t, web, targetID)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first delete status=%d body=%s", firstRec.Code, firstRec.Body.String())
	}
	if len(firstResp.Deleted) != 1 || firstResp.Deleted[0] != targetID {
		t.Fatalf("first delete must remove the target: %+v", firstResp)
	}

	secondRec, secondResp := postSessionDelete(t, web, targetID)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second delete status=%d body=%s, want a clean no-op", secondRec.Code, secondRec.Body.String())
	}
	if len(secondResp.Deleted) != 0 || len(secondResp.Skipped) != 0 {
		t.Fatalf("repeated delete must be a safe no-op: %+v", secondResp)
	}

	assertArchiveDecisionPresent(t, archive, "session", unrelatedID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "session", unrelatedID, true)
	assertArchiveDecisionPresent(t, archive, "project", project.ID, true)
	assertProjectDeleteDecisionPresent(t, dbPath, "project", project.ID, true)
	if _, ok := past.Find(unrelatedID); !ok {
		t.Fatal("unrelated session's past index row must survive repeated deletion of a different session")
	}
}

// TestSessionDeleteRejectsRemoteSource covers the "never offer this for a
// remote-source thread" safety contract: a non-local ref is refused outright,
// never routed into local filesystem cleanup.
func TestSessionDeleteRejectsRemoteSource(t *testing.T) {
	root := t.TempDir()
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	web := NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries()})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/codex:"+webTestSessionID+"/delete", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("remote-source delete must be rejected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestSessionDeleteRejectsInvalidSessionID guards the "never infer a
// filesystem path from an unvalidated ID" contract: a well-formed ref
// (passes the dispatcher's own generic ref syntax, so it reaches this
// handler) whose session ID is not a real identifier.ValidateSessionID value
// must be rejected before any path is built from it, not swallowed into a
// silent no-op.
func TestSessionDeleteRejectsInvalidSessionID(t *testing.T) {
	root := t.TempDir()
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	web := NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries()})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/local:not-a-real-session-id/delete", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed session ID must be rejected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
