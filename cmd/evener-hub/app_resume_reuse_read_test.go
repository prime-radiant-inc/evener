package hub

import (
	"context"
	"errors"
	"fmt"
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
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

// flippableReadProber confirms every entry except the one whose PID is
// registered as unreadable. A daemon that stays alive but stops answering for
// its session is exactly the probe shape that keeps a stale route in the
// roster: the process is alive, so a failed probe preserves the last confirmed
// entry instead of dropping it (#5530).
type flippableReadProber struct {
	sessionID          string
	failPID            atomic.Int64
	failReplacementPID atomic.Int64
}

func (p *flippableReadProber) Probe(entry rendezvous.Entry) hubcore.ProbeResult {
	if int(p.failPID.Load()) == entry.PID || int(p.failReplacementPID.Load()) == entry.PID {
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

// newUnreadableOwnerResumeFixture builds a primed roster whose only owner is a
// live daemon that answers thread/read with readMessage, plus the past session
// a resume needs. The caller installs its own spawner.
func newUnreadableOwnerResumeFixture(t *testing.T, project, readMessage string) (cfg hubcore.WebConfig, sessionID string) {
	t.Helper()
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", project)
	sessionID = buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	oldDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(oldDaemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable(readMessage)
	})
	oldHTTP := httptest.NewServer(http.HandlerFunc(oldDaemon.ServeWebSocket))
	t.Cleanup(oldHTTP.Close)

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

	cfg = hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      roster,
		Past:        past,
		ResumeLocks: hubcore.NewResumeLocks(),
	}
	return cfg, sessionID
}

// assertHubLaunchRefusal requires the structured hub-launch classification,
// not a rendered message.
func assertHubLaunchRefusal(t *testing.T, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error is not an appwire refusal: %T %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable || evenerErrorInfoFromData(wire.Data) != string(appwire.ErrorHubLaunch) {
		t.Fatalf("error classification = code %d info %q, want unavailable/hubLaunch",
			wire.Code, evenerErrorInfoFromData(wire.Data))
	}
}

// TestResumeReplacementFailureKeepsReadCauseAndIsNotRetried reproduces the two
// late review findings on the replacement fall-through: a refused launch must
// carry the read failure that caused it, and a client retry against the same
// unreadable owner must not launch another child for it. The causes are opaque
// fixture sentinels; the refusal shape is asserted by type and code.
func TestResumeReplacementFailureKeepsReadCauseAndIsNotRetried(t *testing.T) {
	readSentinel := "read-sentinel-" + t.Name()
	launchSentinel := "launch-sentinel-" + t.Name()
	cfg, sessionID := newUnreadableOwnerResumeFixture(t, "project-5530-failed-spawn-0000000000", readSentinel)
	spawnCalls := 0
	spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawnCalls++
		return rendezvous.Entry{}, fmt.Errorf("%s: %w", launchSentinel, llm.ErrAPILogTargetLocked)
	}}
	cfg.Spawner = spawner
	sources := newHubSourceRegistry(cfg)

	_, firstErr := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if firstErr == nil {
		t.Fatal("first resume unexpectedly succeeded")
	}
	if !strings.Contains(firstErr.Error(), readSentinel) {
		t.Fatalf("first resume lost the read cause: %v", firstErr)
	}
	if !strings.Contains(firstErr.Error(), launchSentinel) {
		t.Fatalf("first resume lost the launch cause: %v", firstErr)
	}
	assertHubLaunchRefusal(t, firstErr)
	if spawnCalls != 1 {
		t.Fatalf("first resume spawns=%d, want 1", spawnCalls)
	}

	_, secondErr := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if secondErr == nil {
		t.Fatal("second resume unexpectedly succeeded")
	}
	if !strings.Contains(secondErr.Error(), readSentinel) {
		t.Fatalf("second resume lost the read cause: %v", secondErr)
	}
	if !strings.Contains(secondErr.Error(), launchSentinel) {
		t.Fatalf("second resume lost the launch cause: %v", secondErr)
	}
	assertHubLaunchRefusal(t, secondErr)
	if spawnCalls != 1 {
		t.Fatalf("second resume spawned again for the same unreadable owner: spawns=%d, want 1", spawnCalls)
	}
}

// TestResumeReplacementTemporaryFailureIsRetried proves a launch failure that
// is not an ownership refusal stays retryable: fixing its cause must let the
// next resume launch a replacement instead of replaying the cached failure.
func TestResumeReplacementTemporaryFailureIsRetried(t *testing.T) {
	readSentinel := "read-sentinel-" + t.Name()
	temporarySentinel := "temporary-launch-sentinel-" + t.Name()
	cfg, sessionID := newUnreadableOwnerResumeFixture(t, "project-5530-temporary-failure-0000000000", readSentinel)
	replacementWorkingDir := t.TempDir()

	replacementDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(replacementDaemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: liveProjectThread(sessionID, replacementWorkingDir)}, nil
	})
	replacementHTTP := httptest.NewServer(http.HandlerFunc(replacementDaemon.ServeWebSocket))
	t.Cleanup(replacementHTTP.Close)
	replacementEntry := rendezvous.Entry{
		PID:          108085,
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws" + strings.TrimPrefix(replacementHTTP.URL, "http"),
		SourceID:     appwire.LocalSourceID,
		ThreadID:     sessionID,
		SessionID:    sessionID,
		WorkspaceRef: appwire.Ref{SourceID: appwire.LocalSourceID, ThreadID: sessionID}.String(),
		WorkingDir:   replacementWorkingDir,
		StartedAt:    time.Now().UTC(),
	}
	spawnCalls := 0
	spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawnCalls++
		if spawnCalls == 1 {
			return rendezvous.Entry{}, errors.New(temporarySentinel)
		}
		writeRendezvous(t, cfg.RunDir, replacementEntry)
		return replacementEntry, nil
	}}
	cfg.Spawner = spawner
	sources := newHubSourceRegistry(cfg)

	_, firstErr := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if firstErr == nil {
		t.Fatal("first resume unexpectedly succeeded")
	}
	if !strings.Contains(firstErr.Error(), temporarySentinel) {
		t.Fatalf("first resume lost the temporary launch cause: %v", firstErr)
	}
	if !strings.Contains(firstErr.Error(), readSentinel) {
		t.Fatalf("first resume lost the read cause: %v", firstErr)
	}
	if spawnCalls != 1 {
		t.Fatalf("first resume spawns=%d, want 1", spawnCalls)
	}

	response, secondErr := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if secondErr != nil {
		t.Fatalf("temporary launch failure was not retried: %v", secondErr)
	}
	if spawnCalls != 2 {
		t.Fatalf("second resume spawns=%d, want 2", spawnCalls)
	}
	if response.Thread.ID != sessionID {
		t.Fatalf("resumed thread=%+v, want %s", response.Thread, sessionID)
	}
}

// TestResumeFailedConfirmationCompletesRetirementOnRetry reproduces the
// confirmation-failure gap: the first request launches a replacement but its
// post-launch confirmation fails, losing the superseded owner. The retry
// reuses the now-confirmed replacement, which must still retire the old
// rendezvous record and invalidate the relay that reaches the old daemon.
func TestResumeFailedConfirmationCompletesRetirementOnRetry(t *testing.T) {
	readSentinel := "read-sentinel-" + t.Name()
	confirmationSentinel := "confirmation-sentinel-" + t.Name()
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-5530-confirmation-retry-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	var oldUnreadable atomic.Bool
	oldDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(oldDaemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if oldUnreadable.Load() {
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable(readSentinel)
		}
		return appwire.ThreadReadResponse{Thread: liveProjectThread(sessionID, workingDir)}, nil
	})
	oldHTTP := httptest.NewServer(http.HandlerFunc(oldDaemon.ServeWebSocket))
	t.Cleanup(oldHTTP.Close)

	var replacementReads atomic.Int32
	replacementDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: appwire.LocalSourceID})
	appserver.HandleTyped(replacementDaemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if replacementReads.Add(1) == 1 {
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable(confirmationSentinel)
		}
		return appwire.ThreadReadResponse{Thread: liveProjectThread(sessionID, workingDir)}, nil
	})
	replacementHTTP := httptest.NewServer(http.HandlerFunc(replacementDaemon.ServeWebSocket))
	t.Cleanup(replacementHTTP.Close)

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
	roster.SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	prober.failPID.Store(int64(oldEntry.PID))

	spawnCalls := 0
	spawner := &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
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
	first, err := lease.Read(t.Context(), appwire.ThreadReadParams{Ref: canonicalRef.String(), Subscribe: true})
	if err != nil {
		t.Fatalf("establish canonical relay: %v", err)
	}
	if first.Handoff == nil || !first.Handoff.Commit() {
		t.Fatal("establish canonical relay: handoff did not commit")
	}

	// The old owner stops serving, and the replacement cannot be confirmed yet.
	oldUnreadable.Store(true)
	prober.failReplacementPID.Store(int64(replacementEntry.PID))
	if _, err := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID}); err == nil {
		t.Fatal("resume unexpectedly confirmed the replacement")
	}
	if spawnCalls != 1 {
		t.Fatalf("first resume spawns=%d, want 1", spawnCalls)
	}

	// Discovery can confirm the replacement now; the retry reuses it.
	prober.failReplacementPID.Store(0)
	response, err := resumeThreadLocked(context.Background(), cfg, sources, appwire.ThreadResumeParams{Session: sessionID})
	if err != nil {
		t.Fatalf("retry after failed confirmation: %v", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("retry spawned again: spawns=%d, want 1", spawnCalls)
	}
	if response.Thread.ID != sessionID {
		t.Fatalf("resumed thread=%+v, want %s", response.Thread, sessionID)
	}

	// The retry must finish the retirement the failed request could not.
	entries, err := rendezvous.ListStrict(runDir)
	if err != nil {
		t.Fatalf("list rendezvous: %v", err)
	}
	for _, entry := range entries {
		if entry.PID == oldEntry.PID {
			t.Fatalf("retry left the superseded owner's rendezvous record behind: %+v", entry)
		}
	}
	relayed, err := lease.Read(t.Context(), appwire.ThreadReadParams{Ref: canonicalRef.String(), Subscribe: true})
	if err != nil {
		t.Fatalf("relay still reaches the superseded daemon: %v", err)
	}
	if relayed.Handoff != nil {
		relayed.Handoff.Commit()
	}
	if relayed.Response.Thread.ID != sessionID {
		t.Fatalf("relayed thread=%+v, want %s", relayed.Response.Thread, sessionID)
	}
}
