package hub

import (
	"context"
	"encoding/json"
	"errors"
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
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

func dispatchSessionDelete(t *testing.T, web *WebServer, params appwire.SessionDeleteParams) (appwire.SessionDeleteResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal session delete params: %v", err)
	}
	result, err := web.appRPC.Router().Dispatch(t.Context(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerSessionDelete,
		Params: raw,
	})
	if err != nil {
		return appwire.SessionDeleteResponse{}, err
	}
	response, ok := result.(appwire.SessionDeleteResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.SessionDeleteResponse", result)
	}
	return response, nil
}

func mustDeleteSession(t *testing.T, web *WebServer, id string) appwire.SessionDeleteResponse {
	t.Helper()
	response, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: "local:" + id})
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	return response
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

	resp := mustDeleteSession(t, web, targetID)
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

	resp := mustDeleteSession(t, web, webTestSessionID)
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

	resp := mustDeleteSession(t, web, webTestSessionID)
	if len(resp.Deleted) != 0 || len(resp.Skipped) != 1 || resp.Skipped[0].ID != webTestSessionID {
		t.Fatalf("reserved target must be refused via skipped, not deleted: %+v", resp)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+suffix)); err != nil {
			t.Fatalf("reserved session artifact %s was removed: %v", suffix, err)
		}
	}
}

// Deleting a session removes only that session's daemon log.
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
	resp := mustDeleteSession(t, web, targetID)
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

	resp := mustDeleteSession(t, web, webTestSessionID)
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

	firstResp := mustDeleteSession(t, web, targetID)
	if len(firstResp.Deleted) != 1 || firstResp.Deleted[0] != targetID {
		t.Fatalf("first delete must remove the target: %+v", firstResp)
	}

	secondResp := mustDeleteSession(t, web, targetID)
	if len(secondResp.Deleted) != 0 || len(secondResp.Skipped) != 0 {
		t.Fatalf("repeated delete must be a safe no-op: %+v", secondResp)
	}
	if secondResp.Deleted == nil || secondResp.Skipped == nil {
		t.Fatalf("repeated delete must preserve array response fields: %+v", secondResp)
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

	_, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: "remote:" + webTestSessionID})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("remote-source delete error = %v, want invalid params", err)
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

	_, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: "local:not-a-real-session-id"})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("malformed session ID error = %v, want invalid params", err)
	}
}

func TestSessionDeleteRejectsMalformedRef(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})

	_, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("empty-ref delete error = %v, want invalid params", err)
	}
}

func TestSessionDeleteFailsWithoutPastIndex(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{})

	_, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: "local:" + webTestSessionID})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInternalError {
		t.Fatalf("missing-past delete error = %v, want internal error", err)
	}
}

func TestSessionDeletePreservesSuccessAfterNavigationFailure(t *testing.T) {
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
	failingSource := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	failingSource.err = errors.New("capture failed")
	web.navigation = newTestNavigationService(t, failingSource)

	response, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: "local:" + webTestSessionID})
	if err != nil || len(response.Deleted) != 1 || response.Deleted[0] != webTestSessionID || len(response.Skipped) != 0 {
		t.Fatalf("committed deletion outcome: response=%+v error=%v", response, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); !os.IsNotExist(err) {
		t.Fatalf("cleanup must remain committed when the navigation receipt fails: %v", err)
	}
	if _, ok := past.Find(webTestSessionID); ok {
		t.Fatal("past index must reflect committed cleanup when the navigation receipt fails")
	}
}

func TestSessionDeleteRemovesPinAssignmentButKeepsEmptySection(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	targetID := projectDeleteCanonicalSessionIDs[0]
	writeSession(t, stateDir, targetID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	pinStore := hubcore.NewPinSectionStore(dbPath)
	section, _, err := pinStore.CreateOrReuseAndAssign("Research", targetID, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		StateDir: root, Past: past, PinSections: pinStore, Roster: hubcore.NewRosterWithEntries(),
	})

	response := mustDeleteSession(t, web, targetID)
	if len(response.Deleted) != 1 {
		t.Fatalf("delete response = %+v", response)
	}
	pins, err := pinStore.Assignments()
	if err != nil || len(pins) != 0 {
		t.Fatalf("pins = %+v, %v", pins, err)
	}
	sections, err := pinStore.Sections()
	if err != nil || len(sections) != 1 || sections[0].ID != section.ID || sections[0].MemberCount != 0 {
		t.Fatalf("sections = %+v, %v", sections, err)
	}
}

func TestSessionDeleteRetryScrubsPinAfterArtifactsAreAlreadyGone(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
	targetID := projectDeleteCanonicalSessionIDs[0]
	writeSession(t, stateDir, targetID, project.CanonicalPath)
	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	pinStore := hubcore.NewPinSectionStore(dbPath)
	if _, _, err := pinStore.CreateOrReuseAndAssign("Research", targetID, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	dbSnapshot, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		StateDir: root, Past: past, PinSections: pinStore, Roster: hubcore.NewRosterWithEntries(),
	})

	oldRemove := removeProjectSessionFile
	t.Cleanup(func() { removeProjectSessionFile = oldRemove })
	corrupted := false
	removeProjectSessionFile = func(path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		if !corrupted && filepath.Base(path) == targetID+".meta.json" {
			corrupted = true
			return os.WriteFile(dbPath, []byte("not a sqlite database"), 0o600)
		}
		return nil
	}

	_, firstErr := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: "local:" + targetID})
	var wireErr appwire.WireError
	if !errors.As(firstErr, &wireErr) || wireErr.Code != appwire.CodeInternalError {
		t.Fatalf("first delete error = %v, want internal pin-store failure", firstErr)
	}
	if !corrupted {
		t.Fatal("test did not inject the pin-store failure after artifact removal")
	}
	if _, ok := past.Find(targetID); ok {
		t.Fatal("first delete should have rebuilt Past after removing the artifacts")
	}
	if err := os.WriteFile(dbPath, dbSnapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	removeProjectSessionFile = oldRemove

	secondResp := mustDeleteSession(t, web, targetID)
	if len(secondResp.Deleted) != 0 || len(secondResp.Skipped) != 0 {
		t.Fatalf("retry should preserve the idempotent no-op response: %+v", secondResp)
	}
	pins, err := pinStore.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins[targetID]; ok {
		t.Fatalf("retry left the durable pin behind: %+v", pins)
	}
}

// TestSessionDeleteRefreshesRosterAndBustsTreeMemo mirrors
// TestProjectDeleteRefreshesRosterAndBustsTreeMemo for the single-session
// handler: a successful delete must refresh the roster before PokeAttention
// (no ghost rows in the immediate follow-up navigation read) and bump the
// shared InputsVersion so the tree memo is busted even without a past-index
// delta.
func TestSessionDeleteRefreshesRosterAndBustsTreeMemo(t *testing.T) {
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
		StateDir: root, Past: past, Roster: roster, Inputs: inputs,
		PokeAttention: func() { events = append(events, "poke") },
	})
	before := inputs.Load()

	resp := mustDeleteSession(t, web, webTestSessionID)
	if len(resp.Deleted) != 1 || resp.Deleted[0] != webTestSessionID {
		t.Fatalf("session should have been deleted: %+v", resp)
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

func TestSessionDeletePreservesUnconfirmedDaemonState(t *testing.T) {
	for _, identity := range []string{"known", "unidentified"} {
		t.Run(identity, func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "projects", "delete-uncertain-0123456789")
			writeSession(t, stateDir, webTestSessionID, root)
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: os.Getpid(), SessionID: webTestSessionID, ThreadID: webTestSessionID}
			if identity == "unidentified" {
				entry.SessionID = ""
				entry.ThreadID = ""
			}
			writeRendezvous(t, runDir, entry)
			roster := hubcore.NewRoster(runDir, &fakeProber{shouldFail: true})
			roster.Refresh()
			if len(roster.UnconfirmedEntries()) != 1 {
				t.Fatal("missing unconfirmed claim")
			}
			web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster, RunDir: runDir})
			response, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(webTestSessionID)})
			if len(response.Deleted) > 0 {
				t.Fatalf("deleted an unconfirmed live session: %+v", response)
			}
			if _, statErr := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); statErr != nil {
				t.Fatalf("session state removed despite unconfirmed owner: %v (response error: %v)", statErr, err)
			}
			if err := rendezvous.Remove(runDir, entry.PID); err != nil {
				t.Fatal(err)
			}
			roster.Refresh()
			response, err = dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(webTestSessionID)})
			if err != nil || len(response.Deleted) != 1 {
				t.Fatalf("deletion stayed blocked after ownership cleared: response=%+v error=%v", response, err)
			}
		})
	}
}

func TestFailedInitialRosterScanBlocksNavigationAndDeletion(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "delete-uncertain-0123456789")
	writeSession(t, stateDir, webTestSessionID, root)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	claim := filepath.Join(runDir, "1.json")
	if err := os.WriteFile(claim, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRoster(runDir, &fakeProber{})
	roster.Refresh()
	web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster, RunDir: runDir})
	assertNavigationOwnershipReadOnly(t, web)
	response, deleteErr := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(webTestSessionID)})
	if len(response.Deleted) > 0 {
		t.Error("deleted session after failed discovery")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", webTestSessionID+".meta.json")); err != nil {
		t.Fatalf("state removed after failed discovery: %v (delete error: %v)", err, deleteErr)
	}
	if err := os.Remove(claim); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if _, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now()); err != nil {
		t.Fatal(err)
	}
	response, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(webTestSessionID)})
	if err != nil || len(response.Deleted) != 1 {
		t.Fatalf("recovered deletion=%+v error=%v", response, err)
	}
}

func TestSessionDeletePreservesDaemonOwnedDelegates(t *testing.T) {
	for _, status := range []string{appwire.ThreadStatusActive, appwire.ThreadStatusRestartRequired, "unconfirmed"} {
		for _, nested := range []bool{false, true} {
			name := status + "/direct"
			if nested {
				name = status + "/nested"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				projectDir := filepath.Join(root, "project")
				if err := os.MkdirAll(projectDir, 0755); err != nil {
					t.Fatal(err)
				}
				stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
				rootID, parentID, childID := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1], projectDeleteCanonicalSessionIDs[2]
				writeSession(t, stateDir, rootID, projectDir)
				events := []map[string]any{}
				addChild := func(id, parent, parentDelegate, delegate string) {
					writeSession(t, stateDir, id, projectDir)
					meta, err := schema.LoadSessionMeta(stateDir, id)
					if err != nil {
						t.Fatal(err)
					}
					meta.ParentSessionID, meta.JobTreeRootSessionID, meta.IsSubagent = parent, rootID, true
					if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
						t.Fatal(err)
					}
					descriptor := map[string]any{"owner_session_id": rootID, "child_session_id": id, "parent_delegate_id": parentDelegate, "transcript_ref": localAppRef(id), "task": "delete ownership", "agent_type": "explorer", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}
					events = append(events, map[string]any{"kind": "delegate_created", "seq": len(events) + 1, "delegate_id": delegate, "created": map[string]any{"descriptor": descriptor}})
				}
				if nested {
					addChild(parentID, rootID, "", "dlg_parent")
					addChild(childID, parentID, "dlg_parent", "dlg_child")
				} else {
					addChild(childID, rootID, "", "dlg_child")
				}
				batch, err := json.Marshal(map[string]any{"events": events})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stateDir, "sessions", rootID, "delegates.jsonl"), append(append([]byte("{\"version\":1}\n"), batch...), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
				if _, err := past.Rebuild(); err != nil {
					t.Fatal(err)
				}
				roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: rootID, Status: status})
				if status == "unconfirmed" {
					peer := httptest.NewServer(http.NotFoundHandler())
					defer peer.Close()
					runDir := t.TempDir()
					writeRendezvous(t, runDir, rendezvous.Entry{PID: os.Getpid(), SessionID: rootID, ThreadID: rootID, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http")})
					roster = hubcore.NewRoster(runDir, &hubcore.StatusProber{})
					roster.Refresh()
					if !unconfirmedDaemonForThread(roster, rootID) {
						t.Fatal("fixture has no unresolved owner")
					}
				}
				web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
				response, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(childID)})
				if len(response.Deleted) != 0 {
					t.Errorf("deleted owned child: %+v", response)
				}
				if err == nil && len(response.Skipped) != 1 {
					t.Errorf("missing ownership refusal: %+v", response)
				}
				if status != "unconfirmed" && (err != nil || len(response.Skipped) != 1 || response.Skipped[0].Reason != "resumed live") {
					t.Errorf("verified owner not classified live: response=%+v error=%v", response, err)
				}
				for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".api.jsonl"} {
					if _, err := os.Stat(filepath.Join(stateDir, "sessions", childID+suffix)); err != nil {
						t.Errorf("owned child artifact %s: %v", suffix, err)
					}
				}
			})
		}
	}
}

func TestSessionDeleteAllowsIndependentForkOfLiveDaemon(t *testing.T) {
	for _, status := range []string{appwire.ThreadStatusActive, appwire.ThreadStatusRestartRequired} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			projectDir := filepath.Join(root, "project")
			if err := os.MkdirAll(projectDir, 0755); err != nil {
				t.Fatal(err)
			}
			stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
			rootID, forkID := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1]
			writeSession(t, stateDir, rootID, projectDir)
			writeSession(t, stateDir, forkID, projectDir)
			meta, err := schema.LoadSessionMeta(stateDir, forkID)
			if err != nil {
				t.Fatal(err)
			}
			meta.ParentSessionID = rootID
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatal(err)
			}
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: rootID, Status: status})
			web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster})
			response, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(forkID)})
			if err != nil || len(response.Deleted) != 1 || response.Deleted[0] != forkID {
				t.Fatalf("fork deletion=%+v error=%v", response, err)
			}
			if _, err := os.Stat(filepath.Join(stateDir, "sessions", rootID+".meta.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionDeleteRetainedDelegateAfterParentDeletion(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "upgrade-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	childID := buildUpgradeDelegate(t, stateDir, parentID)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRoster(t.TempDir(), &hubcore.StatusProber{})
	roster.Refresh()
	spawned := false
	cfg := hubcore.WebConfig{Past: past, Roster: roster, ResumeLocks: hubcore.NewResumeLocks(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawned = true
		return rendezvous.Entry{}, errors.New("spawn sentinel")
	}}}
	web := NewWebServer(cfg)
	response, err := dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(parentID)})
	if err != nil || len(response.Deleted) != 1 {
		t.Fatalf("delete parent: %+v %v", response, err)
	}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: localAppRef(childID), IncludeTurns: true}); err != nil {
		t.Fatalf("read retained child: %v", err)
	}
	_, _ = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Session: childID})
	if !spawned {
		t.Fatal("retained child could not reach resume launcher")
	}
	response, err = dispatchSessionDelete(t, web, appwire.SessionDeleteParams{Ref: localAppRef(childID)})
	if err != nil || len(response.Deleted) != 1 {
		t.Fatalf("delete child: %+v %v", response, err)
	}
}
