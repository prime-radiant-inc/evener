package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestHubArchiveSetAppWirePersistsSessionDecision(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	var attentionPokes int
	web := NewWebServer(hubcore.WebConfig{
		Archive:       store,
		HubStateRoot:  t.TempDir(),
		Past:          hubcore.NewPastIndex(""),
		PokeAttention: func() { attentionPokes++ },
	})

	response, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "session-1",
		Archived: true,
	})
	if err != nil {
		t.Fatalf("dispatch archive set: %v", err)
	}
	if !response.OK {
		t.Fatalf("response = %+v, want success", response)
	}
	if attentionPokes != 1 {
		t.Fatalf("attention pokes = %d, want 1", attentionPokes)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-1"}] {
		t.Fatalf("session archive decision not persisted: %v", decisions)
	}
}

func TestHubArchiveSetAppWireValidatesAndUnarchivesProject(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(root, "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	})
	params := appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         project.ID,
		WorkingDir: project.CanonicalPath,
		Archived:   true,
	}
	if _, err := dispatchArchiveSet(t, web, params); err != nil {
		t.Fatalf("archive project: %v", err)
	}
	params.Archived = false
	if _, err := dispatchArchiveSet(t, web, params); err != nil {
		t.Fatalf("unarchive project: %v", err)
	}

	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if decisions[hubcore.ArchiveKey{Kind: "project", ID: project.ID}] {
		t.Fatalf("project decision remains archived: %v", decisions)
	}
}

func TestHubArchiveSetAppWireRejectsProjectPathMismatch(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{
		Archive:      hubcore.NewArchiveStore(filepath.Join(root, "archive.db")),
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	})

	_, err = dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         project.ID,
		WorkingDir: filepath.Join(root, "different"),
		Archived:   true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want AppWire error", err)
	}
	if wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d (%v)", wireErr.Code, appwire.CodeInvalidParams, wireErr)
	}
}

func dispatchArchiveSet(t *testing.T, web *WebServer, params appwire.ArchiveParams) (appwire.ArchiveResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal archive params: %v", err)
	}
	result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerArchiveSet,
		Params: raw,
	})
	if err != nil {
		return appwire.ArchiveResponse{}, err
	}
	response, ok := result.(appwire.ArchiveResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.ArchiveResponse", result)
	}
	return response, nil
}

// idleTimeoutRecordingDaemon runs one fixture daemon that records every
// evener/daemon/idle-timeout/set request it receives, answering with the
// resident lifecycle, or refusing the change when fail is set.
func idleTimeoutRecordingDaemon(t *testing.T, entry *rendezvous.Entry, fail bool) *idleTimeoutRecorder {
	t.Helper()
	return idleTimeoutFixtureDaemon(t, entry, func(appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error) {
		if fail {
			return appwire.DaemonIdleTimeoutSetResponse{}, appwire.Unavailable("fixture refuses the deadline change")
		}
		return appwire.DaemonIdleTimeoutSetResponse{Lifecycle: *residentLifecycleForTest()}, nil
	})
}

// idleTimeoutRecorder collects the requests one fixture daemon has answered.
// Handler writes and test reads both go through its lock: concurrent
// connections make an unlocked read of the collected slice a data race even
// when the dispatch's own roundtrip looks synchronized.
type idleTimeoutRecorder struct {
	mu  sync.Mutex
	got []appwire.DaemonIdleTimeoutSetParams
}

func (r *idleTimeoutRecorder) record(params appwire.DaemonIdleTimeoutSetParams) {
	r.mu.Lock()
	r.got = append(r.got, params)
	r.mu.Unlock()
}

// recorded returns a copy of every recorded request under the lock.
func (r *idleTimeoutRecorder) recorded() []appwire.DaemonIdleTimeoutSetParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]appwire.DaemonIdleTimeoutSetParams(nil), r.got...)
}

// idleTimeoutFixtureDaemon runs one fixture daemon serving
// evener/daemon/idle-timeout/set through handle, recording every request it
// receives, and points entry at the fixture's endpoint.
func idleTimeoutFixtureDaemon(t *testing.T, entry *rendezvous.Entry, handle func(params appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error)) *idleTimeoutRecorder {
	t.Helper()
	rec := &idleTimeoutRecorder{}
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerDaemonIdleTimeoutSet, func(_ context.Context, params appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error) {
		rec.record(params)
		return handle(params)
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	return rec
}

// archiveTestHub builds a hub over one resident roster entry and returns the
// server pair plus the archive store, mirroring the daemon-action fixtures.
func archiveTestHub(t *testing.T, entry rendezvous.Entry, prober forceStopProberFunc) (*httptest.Server, *WebServer, *hubcore.ArchiveStore) {
	t.Helper()
	return archiveTestHubWithTimeout(t, entry, prober, 5*time.Minute)
}

// archiveTestHubWithTimeout is archiveTestHub with the Hub's configured daemon
// idle timeout varying per test.
func archiveTestHubWithTimeout(t *testing.T, entry rendezvous.Entry, prober forceStopProberFunc, idleTimeout time.Duration) (*httptest.Server, *WebServer, *hubcore.ArchiveStore) {
	t.Helper()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, entry)
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	archive := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		RunDir:            runDir,
		Roster:            roster,
		Archive:           archive,
		HubStateRoot:      t.TempDir(),
		Past:              hubcore.NewPastIndex(""),
		DaemonIdleTimeout: idleTimeout,
	})
	t.Cleanup(hub.Close)
	return hub, web, archive
}

func assertSessionArchived(t *testing.T, archive *hubcore.ArchiveStore, sessionID string, archived bool) {
	t.Helper()
	decisions, err := archive.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if decisions[hubcore.ArchiveKey{Kind: "session", ID: sessionID}] != archived {
		t.Fatalf("session %s archive decision = %v, want archived=%v", sessionID, decisions, archived)
	}
}

// TestArchiveSetRetargetsResidentDaemonIdleDeadline proves an explicit session
// archive decision reaches the session's resident daemon: archiving shortens
// its automatic idle-retirement deadline to the archived-session constant and
// unarchiving restores the Hub's configured timeout. The forwarded identity is
// the exact ownership fingerprint, so a replacement daemon refuses it.
func TestArchiveSetRetargetsResidentDaemonIdleDeadline(t *testing.T) {
	entry := residentEntryForTest(t, 4501)
	rec := idleTimeoutRecordingDaemon(t, &entry, false)
	_, web, archive := archiveTestHub(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: true,
	}); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	assertSessionArchived(t, archive, entry.SessionID, true)
	got := rec.recorded()
	if len(got) != 1 {
		t.Fatalf("resident daemon received %d idle-timeout sets, want 1", len(got))
	}
	if set := got[0]; set.TimeoutMillis != 60000 {
		t.Fatalf("archive TimeoutMillis = %d, want 60000", set.TimeoutMillis)
	} else if set.Identity.Generation != rendezvous.OwnershipFingerprint(entry) {
		t.Fatalf("forwarded identity generation = %q, want the exact ownership fingerprint", set.Identity.Generation)
	}

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: false,
	}); err != nil {
		t.Fatalf("unarchive session: %v", err)
	}
	assertSessionArchived(t, archive, entry.SessionID, false)
	got = rec.recorded()
	if len(got) != 2 {
		t.Fatalf("resident daemon received %d idle-timeout sets after unarchive, want 2", len(got))
	}
	if set := got[1]; set.TimeoutMillis != (5 * time.Minute).Milliseconds() {
		t.Fatalf("unarchive TimeoutMillis = %d, want the configured 300000", set.TimeoutMillis)
	}
}

// TestArchiveSetNeverLengthensShorterConfiguredDeadline proves the archived
// deadline is an upper bound, never an extension: a Hub configured with a
// sub-minute daemon idle timeout archives to that shorter timeout, not to the
// one-minute constant.
func TestArchiveSetNeverLengthensShorterConfiguredDeadline(t *testing.T) {
	entry := residentEntryForTest(t, 4507)
	rec := idleTimeoutRecordingDaemon(t, &entry, false)
	_, web, archive := archiveTestHubWithTimeout(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	}, 30*time.Second)

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: true,
	}); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	assertSessionArchived(t, archive, entry.SessionID, true)
	if got := rec.recorded(); len(got) != 1 || got[0].TimeoutMillis != (30*time.Second).Milliseconds() {
		t.Fatalf("daemon received %+v, want the configured 30000 — archiving must never lengthen the deadline", got)
	}
}

// TestArchiveSetWithoutResidentDaemonPersistsDecision proves the nudge is
// best-effort: a session with no resident daemon archives fine, because the
// common archived session has nothing running to retarget.
func TestArchiveSetWithoutResidentDaemonPersistsDecision(t *testing.T) {
	archive := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		Archive:           archive,
		HubStateRoot:      t.TempDir(),
		Past:              hubcore.NewPastIndex(""),
		DaemonIdleTimeout: time.Hour,
	})
	t.Cleanup(hub.Close)
	sessionID := hubtest.SessionID(t)

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: sessionID, Archived: true,
	}); err != nil {
		t.Fatalf("archive session with no resident daemon: %v", err)
	}
	assertSessionArchived(t, archive, sessionID, true)
}

// TestArchiveSetSkipsIdleTimeoutForOlderProtocolDaemon proves the nudge never
// reaches a peer that cannot speak the current protocol: an older daemon keeps
// its configured deadline and the archive decision still persists.
func TestArchiveSetSkipsIdleTimeoutForOlderProtocolDaemon(t *testing.T) {
	entry := residentEntryForTest(t, 4502)
	entry.Protocol = "evener-appwire-v3"
	rec := idleTimeoutRecordingDaemon(t, &entry, false)
	_, web, archive := archiveTestHub(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: true,
	}); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	assertSessionArchived(t, archive, entry.SessionID, true)
	if got := rec.recorded(); len(got) != 0 {
		t.Fatalf("older-protocol daemon received %d idle-timeout sets, want 0", len(got))
	}
}

// TestArchiveSetNeverRetargetsClearedSuccessorsDaemon pins the nudge's
// session-scoped lookup. A daemon that survived thread/clear now serves the
// replacement session, and its stable WorkspaceRef still names the predecessor
// it was spawned for. Archiving the predecessor must not retarget that daemon —
// the decision belongs to the predecessor, whose own daemon no longer exists —
// while archiving the replacement, the daemon's current session, still does.
func TestArchiveSetNeverRetargetsClearedSuccessorsDaemon(t *testing.T) {
	entry := residentEntryForTest(t, 4512)
	predecessorID := entry.ThreadID
	entry.SessionID = hubtest.SessionID(t) // thread/clear advanced the current session
	rec := idleTimeoutRecordingDaemon(t, &entry, false)
	_, web, archive := archiveTestHub(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: predecessorID, Archived: true,
	}); err != nil {
		t.Fatalf("archive predecessor session: %v", err)
	}
	assertSessionArchived(t, archive, predecessorID, true)
	if got := rec.recorded(); len(got) != 0 {
		t.Fatalf("predecessor archive reached its former daemon %d times, want 0 — the daemon now belongs to the replacement", len(got))
	}

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: true,
	}); err != nil {
		t.Fatalf("archive replacement session: %v", err)
	}
	assertSessionArchived(t, archive, entry.SessionID, true)
	got := rec.recorded()
	if len(got) != 1 {
		t.Fatalf("replacement archive reached its daemon %d times, want 1", len(got))
	}
	if set := got[0]; set.Identity.Generation != rendezvous.OwnershipFingerprint(entry) {
		t.Fatalf("forwarded identity generation = %q, want the current ownership fingerprint", set.Identity.Generation)
	}
}

// TestArchiveSetProjectDecisionNeverTouchesDaemons proves a project archive
// decision, which covers many sessions, never retargets a resident daemon:
// shortening is the session decision's behavior alone.
func TestArchiveSetProjectDecisionNeverTouchesDaemons(t *testing.T) {
	entry := residentEntryForTest(t, 4503)
	rec := idleTimeoutRecordingDaemon(t, &entry, false)
	_, web, archive := archiveTestHub(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	})
	project, err := identifier.ResolveProject(entry.WorkingDir)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetProject, ID: project.ID, WorkingDir: entry.WorkingDir, Archived: true,
	}); err != nil {
		t.Fatalf("archive project: %v", err)
	}
	decisions, err := archive.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "project", ID: project.ID}] {
		t.Fatalf("project archive decision not persisted: %v", decisions)
	}
	if got := rec.recorded(); len(got) != 0 {
		t.Fatalf("project archive retargeted a daemon %d times, want 0", len(got))
	}
}

// TestArchiveSetPersistsWhenIdleTimeoutNudgeFails proves the daemon nudge is
// subordinate to the decision: a daemon that refuses (or a peer lost mid-race)
// never fails the archive response or the persisted decision.
func TestArchiveSetPersistsWhenIdleTimeoutNudgeFails(t *testing.T) {
	entry := residentEntryForTest(t, 4504)
	idleTimeoutRecordingDaemon(t, &entry, true)
	_, web, archive := archiveTestHub(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: true,
	}); err != nil {
		t.Fatalf("archive session over a refusing daemon: %v", err)
	}
	assertSessionArchived(t, archive, entry.SessionID, true)
}

// TestArchiveSetBoundedWhenDaemonNudgeWedges proves the best-effort nudge is
// time-bounded: a daemon that accepts the connection but never answers cannot
// hang the archive response, and the decision still persists once the bound
// expires.
func TestArchiveSetBoundedWhenDaemonNudgeWedges(t *testing.T) {
	orig := archivedDaemonNudgeTimeout
	archivedDaemonNudgeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { archivedDaemonNudgeTimeout = orig })

	entry := residentEntryForTest(t, 4505)
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerDaemonIdleTimeoutSet, func(context.Context, appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error) {
		<-released
		return appwire.DaemonIdleTimeoutSetResponse{}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	_, web, archive := archiveTestHub(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	})

	type archiveOutcome struct {
		resp appwire.ArchiveResponse
		err  error
	}
	done := make(chan archiveOutcome, 1)
	go func() {
		raw, err := json.Marshal(appwire.ArchiveParams{Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: true})
		if err != nil {
			done <- archiveOutcome{err: err}
			return
		}
		result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
			ID:     appwire.NewIntID(1),
			Method: appwire.MethodEvenerArchiveSet,
			Params: raw,
		})
		resp, _ := result.(appwire.ArchiveResponse)
		done <- archiveOutcome{resp: resp, err: err}
	}()
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("archive session: %v", out.err)
		}
		if !out.resp.OK {
			t.Fatalf("archive response = %+v, want OK", out.resp)
		}
	case <-time.After(3 * time.Second): // TRIPWIRE: the bound is 100ms; a wedge past 3s means the nudge is unbounded.
		t.Fatal("archive response wedged behind the unresponsive daemon nudge")
	}
	assertSessionArchived(t, archive, entry.SessionID, true)
}

// TestArchiveSetSerializesConcurrentDecisions proves the durable decision and
// its daemon nudge are serialized per session: two connections racing archive
// and unarchive land their nudges in the order the decisions persisted, so the
// last durable decision is also the last deadline the daemon applied.
func TestArchiveSetSerializesConcurrentDecisions(t *testing.T) {
	entry := residentEntryForTest(t, 4506)
	entered := make(chan appwire.DaemonIdleTimeoutSetParams, 1)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	rec := idleTimeoutFixtureDaemon(t, &entry, func(params appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error) {
		// The first request parks until released; later ones answer at once.
		select {
		case entered <- params:
			<-releaseFirst
		default:
		}
		return appwire.DaemonIdleTimeoutSetResponse{Lifecycle: *residentLifecycleForTest()}, nil
	})
	_, web, archive := archiveTestHub(t, entry, func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusIdle}
	})
	dispatch := func(params appwire.ArchiveParams) (appwire.ArchiveResponse, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			return appwire.ArchiveResponse{}, err
		}
		result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
			ID:     appwire.NewIntID(1),
			Method: appwire.MethodEvenerArchiveSet,
			Params: raw,
		})
		if err != nil {
			return appwire.ArchiveResponse{}, err
		}
		resp, _ := result.(appwire.ArchiveResponse)
		return resp, nil
	}

	type outcome struct {
		resp appwire.ArchiveResponse
		err  error
	}
	archiveDone := make(chan outcome, 1)
	go func() {
		resp, err := dispatch(appwire.ArchiveParams{Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: true})
		archiveDone <- outcome{resp: resp, err: err}
	}()
	// The archive decision has persisted and its nudge is parked in the daemon.
	if parked := <-entered; parked.TimeoutMillis != 60000 {
		t.Fatalf("first nudge = %d ms, want the archived 60000", parked.TimeoutMillis)
	}
	unarchiveDone := make(chan outcome, 1)
	go func() {
		resp, err := dispatch(appwire.ArchiveParams{Kind: appwire.ArchiveTargetSession, ID: entry.SessionID, Archived: false})
		unarchiveDone <- outcome{resp: resp, err: err}
	}()
	select {
	case <-unarchiveDone:
		t.Fatal("unarchive completed while the archive decision still held the session")
	case <-time.After(200 * time.Millisecond): // TRIPWIRE: the archive holds the session's alias; the unarchive must queue behind it.
	}
	release()

	for name, done := range map[string]chan outcome{"archive": archiveDone, "unarchive": unarchiveDone} {
		select {
		case out := <-done:
			if out.err != nil {
				t.Fatalf("%s: %v", name, out.err)
			}
			if !out.resp.OK {
				t.Fatalf("%s response = %+v, want OK", name, out.resp)
			}
		case <-time.After(10 * time.Second): // TRIPWIRE: both dispatches are released by close(releaseFirst).
			t.Fatalf("%s never completed after release", name)
		}
	}
	assertSessionArchived(t, archive, entry.SessionID, false)
	want := []int64{60000, (5 * time.Minute).Milliseconds()}
	var applied []int64
	for _, set := range rec.recorded() {
		applied = append(applied, set.TimeoutMillis)
	}
	if !slices.Equal(applied, want) {
		t.Fatalf("daemon applied deadlines %v, want persist order %v", applied, want)
	}
}

// A remote project's working directory does not exist on the controller, so the
// archive must not resolve it against the controller's filesystem. Each host's
// decision is keyed by its own source, distinct from the controller key.
func TestHubArchiveSetAppWireKeysNonLocalProjectBySource(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:          store,
		HubStateRoot:     t.TempDir(),
		Past:             hubcore.NewPastIndex(""),
		RemoteHosts:      []hostreg.Host{{Name: "host-a"}, {Name: "host-b"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID

	for _, host := range []string{"host-a", "host-b"} {
		if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
			Kind:     appwire.ArchiveTargetProject,
			ID:       projectID,
			Source:   host,
			Archived: true,
		}); err != nil {
			t.Fatalf("archive remote project on %s: %v", host, err)
		}
	}

	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"host-a", "host-b"} {
		if !decisions[hubcore.ArchiveKey{Kind: "project", ID: projectID, Source: host}] {
			t.Fatalf("decision for %s not persisted under its own source: %v", host, decisions)
		}
	}
	if decisions[hubcore.ArchiveKey{Kind: "project", ID: projectID}] {
		t.Fatalf("remote decision leaked onto the controller key: %v", decisions)
	}
}

// The optional non-local WorkingDir is cross-checked against the identity the
// host already reported, never resolved locally: a mismatched path is rejected
// with the same message the local path uses.
func TestHubArchiveSetAppWireCrossChecksRemoteWorkingDir(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Threads: []appwire.Thread{{
			ID:          "t1",
			SessionID:   "t1",
			Source:      "host-a",
			ProjectID:   projectID,
			ProjectPath: "/srv/remote/project",
		}},
	})
	web := NewWebServer(hubcore.WebConfig{
		Archive:           hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db")),
		RemoteThreadCache: cache,
		HubStateRoot:      t.TempDir(),
		Past:              hubcore.NewPastIndex(""),
		RemoteHosts:       []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient:  unusedRemoteHostClient,
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         projectID,
		Source:     "host-a",
		WorkingDir: "/srv/remote/project",
		Archived:   true,
	}); err != nil {
		t.Fatalf("archive remote project with the reported path: %v", err)
	}

	_, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         projectID,
		Source:     "host-a",
		WorkingDir: "/srv/elsewhere",
		Archived:   true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %v, want InvalidParams for a mismatched remote workingDir", err)
	}
	if !strings.Contains(err.Error(), "project ID does not match workingDir") {
		t.Fatalf("error = %q, want the project/workingDir mismatch message", err)
	}
}

// A non-local archive source must name a configured host. An unknown source
// would persist a successful but permanently inert decision row that no read
// path addresses.
func TestHubArchiveSetAppWireRejectsUnknownSource(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:          store,
		HubStateRoot:     t.TempDir(),
		Past:             hubcore.NewPastIndex(""),
		RemoteHosts:      []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID

	_, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetProject,
		ID:       projectID,
		Source:   "host-b",
		Archived: true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %v, want InvalidParams for an unknown source", err)
	}
	if !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("error = %q, want the unknown-source message", err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("unknown source wrote a decision: %v", decisions)
	}

	// A whitespace variant still names the configured host: normalization trims
	// it before validation and keying.
	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetProject,
		ID:       projectID,
		Source:   "  host-a  ",
		Archived: true,
	}); err != nil {
		t.Fatalf("archive with a padded configured source: %v", err)
	}
	decisions, err = store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "project", ID: projectID, Source: "host-a"}] {
		t.Fatalf("padded source did not normalize to host-a: %v", decisions)
	}
}

// A configured host is only a registered source when the hub wired a remote
// client: newHubSourceRegistry skips cfg.RemoteHosts entirely when
// RemoteHostClient is nil, so accepting its name here would persist a decision
// no source, and no navigation row, can ever address. The same request against a
// wired hub succeeds.
func TestHubArchiveSetRejectsConfiguredHostWithoutRemoteClient(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID
	params := appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetProject,
		ID:       projectID,
		Source:   "host-a",
		Archived: true,
	}

	unwired := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
		RemoteHosts:  []hostreg.Host{{Name: "host-a"}},
	})
	_, err := dispatchArchiveSet(t, unwired, params)
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("error = %v, want InvalidParams for an unregistered source", err)
	}

	wired := NewWebServer(hubcore.WebConfig{
		Archive:          store,
		HubStateRoot:     t.TempDir(),
		Past:             hubcore.NewPastIndex(""),
		RemoteHosts:      []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient: unusedRemoteHostClient,
	})
	if _, err := dispatchArchiveSet(t, wired, params); err != nil {
		t.Fatalf("archive against a wired hub: %v", err)
	}
}

// A session archive carries no source dimension: the session ID is already its
// host-qualified ref, so a non-local source must be rejected instead of
// silently writing an inert controller-key row.
func TestHubArchiveSetAppWireRejectsSessionSource(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
		RemoteHosts:  []hostreg.Host{{Name: "host-a"}},
	})
	_, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "session-1",
		Source:   "host-a",
		Archived: true,
	})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %v, want InvalidParams for a session source", err)
	}
	if !strings.Contains(err.Error(), "source is not supported for session archive") {
		t.Fatalf("error = %q, want the session-source message", err)
	}
}

// A "local:thread" ref addresses the controller's own row. Both spellings must
// land on the single key the local rows and every stored local decision use, so
// a client that sends the canonical ref neither misses the row nor splits the
// decision into a second inert key.
func TestHubArchiveSetAppWireNormalizesLocalSessionRef(t *testing.T) {
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:      store,
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	})

	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "  local:session-1  ",
		Archived: true,
	}); err != nil {
		t.Fatalf("archive local session by ref: %v", err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-1"}] {
		t.Fatalf("local ref did not normalize onto the bare session key: %v", decisions)
	}
	if decisions[hubcore.ArchiveKey{Kind: "session", ID: "local:session-1"}] {
		t.Fatalf("local ref was stored under a spelling no local row is read by: %v", decisions)
	}

	// The bare spelling unarchives the row the ref spelling archived.
	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "session-1",
		Archived: false,
	}); err != nil {
		t.Fatalf("unarchive bare session id: %v", err)
	}
	decisions, err = store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-1"}] {
		t.Fatalf("bare-ID unarchive did not clear the decision the ref set: %v", decisions)
	}
}

// A remote session's tree row carries its canonical ref as the node ID, and the
// Live tier filters on exactly that identity. Archiving the ref must therefore
// clear the host's row: the bare session ID the rail sends today is the local
// identity and reaches no remote row.
func TestHubArchiveSetAppWireArchivesRemoteSessionByRef(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	projectID := identifier.ProjectFromCanonicalPath("/srv/remote/project").ID
	// A current timestamp keeps the row in the Current tier: an age-based
	// auto-archive would clear it from the Live tier for reasons of its own.
	now := time.Now().Unix()
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Threads: []appwire.Thread{{
			ID:          "t1",
			SessionID:   "t1",
			Source:      "host-a",
			CWD:         "/srv/remote/project",
			ProjectID:   projectID,
			ProjectPath: "/srv/remote/project",
			CreatedAt:   now,
			UpdatedAt:   now,
			Status:      appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:      appwire.EvenerThread{Ref: "host-a:t1"},
		}},
	})
	store := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
	web := NewWebServer(hubcore.WebConfig{
		Archive:           store,
		RemoteThreadCache: cache,
		HubStateRoot:      t.TempDir(),
		Past:              hubcore.NewPastIndex(""),
		RemoteHosts:       []hostreg.Host{{Name: "host-a"}},
		RemoteHostClient:  unusedRemoteHostClient,
	})
	liveTree := func(t *testing.T) hubcore.Tree {
		t.Helper()
		metas, live, projects := web.navigationTreeInputs(context.Background())
		decisions, err := store.Decisions()
		if err != nil {
			t.Fatal(err)
		}
		return hubcore.BuildTreeWithProjects(metas, live, decisions, projects)
	}

	if live := liveTree(t).Live; len(live) != 1 || live[0].ID != "host-a:t1" {
		t.Fatalf("live = %#v, want host-a's remote row before the archive", live)
	}
	if _, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "host-a:t1",
		Archived: true,
	}); err != nil {
		t.Fatalf("archive remote session by ref: %v", err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "session", ID: "host-a:t1"}] {
		t.Fatalf("remote session decision not keyed by its ref: %v", decisions)
	}
	if live := liveTree(t).Live; len(live) != 0 {
		t.Fatalf("live = %#v, want the archived remote row cleared from the Live tier", live)
	}
}

// TestValidateDecisionSourcePrefersLiveRegistry pins the round-2 medium: an
// archive or favorite decision may name a host added at runtime, because the
// live registry — not only the configured entries — is the authority a
// decision source validates against. The configured entries remain the
// fallback when no registry is threaded.
func TestValidateDecisionSourcePrefersLiveRegistry(t *testing.T) {
	reg, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	if err := reg.Add(hostreg.Host{Name: "side", SSH: "s.example"}); err != nil {
		t.Fatalf("reg.Add: %v", err)
	}
	refuse := func(context.Context, string) (*appwire.Client, error) {
		return nil, errors.New("test dial refused")
	}
	cfg := hubcore.WebConfig{RemoteHostClient: refuse, RemoteHostRegistry: reg}
	if err := validateDecisionSource(cfg, "side"); err != nil {
		t.Fatalf("validateDecisionSource(side) = %v, want nil: a runtime-added host is a valid decision source", err)
	}
	if err := validateDecisionSource(cfg, "m4"); err != nil {
		t.Fatalf("validateDecisionSource(m4) = %v, want nil", err)
	}
	if err := validateDecisionSource(cfg, "ghost"); err == nil {
		t.Fatal("validateDecisionSource(ghost) accepted, want refusal")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
	fallback := hubcore.WebConfig{RemoteHostClient: refuse, RemoteHosts: []hostreg.Host{{Name: "m4"}}}
	if err := validateDecisionSource(fallback, "m4"); err != nil {
		t.Fatalf("validateDecisionSource(m4, fallback) = %v, want nil", err)
	}
	if err := validateDecisionSource(fallback, "side"); err == nil {
		t.Fatal("validateDecisionSource(side, fallback) accepted, want refusal: without a registry the configured entries are the set")
	}
	if err := validateDecisionSource(hubcore.WebConfig{}, "m4"); err == nil {
		t.Fatal("validateDecisionSource(m4, no client) accepted, want refusal: no remote source can exist")
	}
}
