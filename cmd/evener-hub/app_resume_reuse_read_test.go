package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// flippableReadProber confirms every entry except the one whose PID is
// registered as unreadable. A daemon that stays alive but stops answering for
// its session is exactly the probe shape that keeps a stale route in the
// roster: the process is alive, so a failed probe preserves the last confirmed
// entry instead of dropping it (#5530).
type flippableReadProber struct {
	sessionID string
	failPID   atomic.Int64
}

func (p *flippableReadProber) Probe(entry rendezvous.Entry) hubcore.ProbeResult {
	if int(p.failPID.Load()) == entry.PID {
		return hubcore.ProbeResult{}
	}
	return hubcore.ProbeResult{SessionID: p.sessionID, Status: appwire.ThreadStatusIdle, OK: true}
}

// TestResumeUnreadableOwnerSpawnsReplacement reproduces the resume fail-loop
// from #5530: the roster names a live daemon, its thread/read is unavailable,
// and the hub must not keep reusing that route. It falls through to one
// replacement spawn, and the replacement becomes the routed owner.
func TestResumeUnreadableOwnerSpawnsReplacement(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-5530-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	// The wedged owner: alive enough for discovery, but its read is
	// unavailable, which is the shape that failed 190 times in the incident.
	// It serves reads until the relay below is established, then stops.
	var oldUnreadable atomic.Bool
	oldDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(oldDaemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if !oldUnreadable.Load() {
			return appwire.ThreadReadResponse{Thread: liveProjectThread(sessionID, workingDir)}, nil
		}
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread history is closed")
	})
	oldHTTP := httptest.NewServer(http.HandlerFunc(oldDaemon.ServeWebSocket))
	defer oldHTTP.Close()

	// The replacement serves the resumed session.
	replacementDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(replacementDaemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: liveProjectThread(sessionID, workingDir)}, nil
	})
	replacementHTTP := httptest.NewServer(http.HandlerFunc(replacementDaemon.ServeWebSocket))
	defer replacementHTTP.Close()

	started := time.Now().UTC().Add(-time.Minute)
	oldEntry := rendezvous.Entry{
		PID:          108084,
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + strings.TrimPrefix(oldHTTP.URL, "http"),
		SourceID:     appwire.LocalSourceID,
		ThreadID:     sessionID,
		SessionID:    sessionID,
		WorkspaceRef: appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: sessionID}.String(),
		WorkingDir:   workingDir,
		StartedAt:    started,
	}
	replacementEntry := rendezvous.Entry{
		PID:          108085,
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + strings.TrimPrefix(replacementHTTP.URL, "http"),
		SourceID:     appwire.LocalSourceID,
		ThreadID:     sessionID,
		SessionID:    sessionID,
		WorkspaceRef: appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: sessionID}.String(),
		WorkingDir:   workingDir,
		StartedAt:    started.Add(30 * time.Second),
	}

	runDir := t.TempDir()
	writeRendezvous(t, runDir, oldEntry)
	prober := &flippableReadProber{sessionID: sessionID}
	roster := hubcore.NewRoster(runDir, prober)
	// Synthetic PIDs must not depend on whether they exist on this host: the
	// process must read as alive so a failed probe preserves the stale route.
	roster.SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	if _, ok := liveDaemonForThread(roster, sessionID); !ok {
		t.Fatal("fixture lost the confirmed owner before the resume")
	}

	// The daemon stops answering for its session while its process stays alive.
	prober.failPID.Store(int64(oldEntry.PID))

	spawnCalls := 0
	spawner := &fakeRPCSpawner{resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
		if req.SessionID != sessionID {
			t.Fatalf("replacement resume session=%q, want %q", req.SessionID, sessionID)
		}
		spawnCalls++
		writeRendezvous(t, runDir, replacementEntry)
		return replacementEntry, nil
	}}
	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		Spawner:     spawner,
		Past:        past,
		ResumeLocks: hubcore.NewResumeLocks(),
	}

	// One persistent registry for the relay and the resume. Its relay session is
	// keyed by the canonical ref the replacement also uses, so a retained
	// connection must be invalidated after the swap.
	sources := newHubSourceRegistry(cfg)
	relaySource, ok := sources.Source(appwire.LocalSourceID)
	if !ok {
		t.Fatal("persistent registry has no local source")
	}
	relaySessions, ok := relaySource.(appsource.RelaySessionSource)
	if !ok {
		t.Fatal("local source has no relay session API")
	}
	canonicalRef := appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: sessionID}
	lease, err := relaySessions.AcquireRelaySession(canonicalRef)
	if err != nil {
		t.Fatalf("acquire canonical relay: %v", err)
	}
	defer lease.Close()
	relayCtx := t.Context()
	first, err := lease.Read(relayCtx, appwire.ThreadReadParams{Ref: canonicalRef.String(), Subscribe: true})
	if err != nil {
		t.Fatalf("establish canonical relay: %v", err)
	}
	if first.Handoff == nil || !first.Handoff.Commit() {
		t.Fatal("establish canonical relay: handoff did not commit")
	}
	oldUnreadable.Store(true)

	response, err := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if err != nil {
		t.Fatalf("resume reused the unreadable owner instead of spawning a replacement: %v", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("replacement spawns=%d, want 1", spawnCalls)
	}
	if response.Thread.ID != sessionID || response.Thread.Evener.Ref != "local:"+sessionID {
		t.Fatalf("resumed thread=%+v, want the replacement's thread for %s", response.Thread, sessionID)
	}
	live, ok := liveDaemonForThread(roster, sessionID)
	if !ok || live.PID != replacementEntry.PID {
		t.Fatalf("live owner=(pid %d, ok=%v), want the replacement pid %d", live.PID, ok, replacementEntry.PID)
	}

	// The replacement is now the confirmed owner: a later resume of the same
	// session must reuse it rather than falling through to another spawn.
	if _, err := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID}); err != nil {
		t.Fatalf("resume after recovery: %v", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("replacement spawns after recovery=%d, want 1", spawnCalls)
	}

	// A hub restart rebuilds the roster from the run directory. A surviving
	// record for the unreadable owner becomes an unconfirmed claim on that fresh
	// roster, and lookupDaemonOwner checks unconfirmed claims before the healthy
	// replacement, so the session must keep working across the restart.
	restartedRoster := hubcore.NewRoster(runDir, prober)
	restartedRoster.SetProcessAlive(func(int) bool { return true })
	restartedRoster.Refresh()
	restartedCfg := cfg
	restartedCfg.Roster = restartedRoster
	if _, err := resumeThreadLocked(context.Background(), restartedCfg, newHubSourceRegistry(restartedCfg), appwire.ThreadResumeParams{Session: sessionID}); err != nil {
		t.Fatalf("resume after hub restart: %v", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("replacement spawns after hub restart=%d, want 1", spawnCalls)
	}

	// The cached relay connection belonged to the superseded daemon. A
	// subscribed read through the same lease must now reach the replacement.
	second, err := lease.Read(relayCtx, appwire.ThreadReadParams{Ref: canonicalRef.String(), Subscribe: true})
	if err != nil {
		t.Fatalf("subscribed read after replacement reused the superseded relay: %v", err)
	}
	if second.Handoff != nil {
		second.Handoff.Commit()
	}
	if second.Response.Thread.ID != sessionID {
		t.Fatalf("subscribed read thread=%+v, want the replacement's thread for %s", second.Response.Thread, sessionID)
	}
}

// TestResumeReplacementFailureKeepsReadCauseAndIsNotRetried reproduces the two
// late review findings on the replacement fall-through: a refused launch must
// carry the read failure that caused it, and a client retry against the same
// unreadable owner must not launch another child for it.
func TestResumeReplacementFailureKeepsReadCauseAndIsNotRetried(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-5530-failed-spawn-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	oldDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(oldDaemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread history is closed")
	})
	oldHTTP := httptest.NewServer(http.HandlerFunc(oldDaemon.ServeWebSocket))
	defer oldHTTP.Close()

	oldEntry := rendezvous.Entry{
		PID:          108084,
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + strings.TrimPrefix(oldHTTP.URL, "http"),
		SourceID:     appwire.LocalSourceID,
		ThreadID:     sessionID,
		SessionID:    sessionID,
		WorkspaceRef: appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: sessionID}.String(),
		WorkingDir:   workingDir,
		StartedAt:    time.Now().UTC().Add(-time.Minute),
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, oldEntry)
	prober := &flippableReadProber{sessionID: sessionID}
	roster := hubcore.NewRoster(runDir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	prober.failPID.Store(int64(oldEntry.PID))

	spawnCalls := 0
	launchFailure := errors.New("evener serve: session " + sessionID + " is already running; send work to the live session or fork it")
	spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawnCalls++
		return rendezvous.Entry{}, launchFailure
	}}
	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		Spawner:     spawner,
		Past:        past,
		ResumeLocks: hubcore.NewResumeLocks(),
	}
	sources := newHubSourceRegistry(cfg)

	_, firstErr := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if firstErr == nil {
		t.Fatal("first resume unexpectedly succeeded")
	}
	if !strings.Contains(firstErr.Error(), "thread history is closed") {
		t.Fatalf("first resume lost the read cause: %v", firstErr)
	}
	if !strings.Contains(firstErr.Error(), "already running") {
		t.Fatalf("first resume lost the launch cause: %v", firstErr)
	}
	if spawnCalls != 1 {
		t.Fatalf("first resume spawns=%d, want 1", spawnCalls)
	}

	_, secondErr := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if secondErr == nil {
		t.Fatal("second resume unexpectedly succeeded")
	}
	if !strings.Contains(secondErr.Error(), "thread history is closed") {
		t.Fatalf("second resume lost the read cause: %v", secondErr)
	}
	if spawnCalls != 1 {
		t.Fatalf("second resume spawned again for the same unreadable owner: spawns=%d, want 1", spawnCalls)
	}
}
