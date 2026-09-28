package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// A session that merely needs resume no longer refuses turn/start: sending a
// prompt folds the resume into the send, and the admitted turn/start runs that
// resume explicitly before its retry (app_rpc.go's resumeTurnStartThreadResume,
// selected by turnStartResumeExplicit). The carve-out is exactly turn/start and
// exactly ResumeRequired — a Stop still in flight keeps refusing every action,
// and so does every action other than turn/start while only the resume is
// pending.
//
// These tests drive the refusal at its source: sessionActionRecoveryError, read
// with the same request-admission context the hub stamps on a real request
// (admitSessionRecovery), so a passing turn/start here is the same admission the
// handler's own withDeletionTargetOwnership makes.
func resumeOnlyRecoveryConfig(t *testing.T) hubcore.WebConfig {
	t.Helper()
	cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
	return cfg
}

// stageResumeOnlyFence commits a force-stop obligation for id: Stopping returns
// to zero and ResumeRequired stays set, which is the "merely needs resume" state
// turn/start must now admit.
func stageResumeOnlyFence(t *testing.T, cfg hubcore.WebConfig, id string) {
	t.Helper()
	finish := cfg.ResumeLocks.BeginForceStop([]string{id})
	if err := cfg.ResumeLocks.PersistForceStop([]string{id}, id); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	state := cfg.ResumeLocks.RecoveryState(id)
	if !state.ResumeRequired || state.Stopping != 0 {
		t.Fatalf("fixture did not stage the resume-only state: %+v", state)
	}
}

func TestTurnStartAdmitsResumeRequiredButOtherActionsDoNot(t *testing.T) {
	cfg := resumeOnlyRecoveryConfig(t)
	const id = "resume-only-admission"
	stageResumeOnlyFence(t, cfg, id)
	ref := "local:" + id
	epoch := sessionRecoveryState(cfg, ref, "").Epoch

	turnCtx := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodTurnStart, appwire.TurnStartParams{Ref: ref, ClientMutationID: "resume-only-send"}))
	if err := sessionActionRecoveryError(turnCtx, cfg, ref, "", epoch); err != nil {
		t.Fatalf("turn/start refused while only ResumeRequired is set: %v", err)
	}

	steerCtx := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(
		appwire.NewIntID(2), appwire.MethodTurnSteer, appwire.TurnSteerParams{Ref: ref, ClientMutationID: "resume-only-steer"}))
	if err := sessionActionRecoveryError(steerCtx, cfg, ref, "", epoch); !isSessionRecoveryAdmissionError(err) {
		t.Fatalf("turn/steer admission = %v, want the explicit-resume refusal while ResumeRequired stands", err)
	}

	// The same fence on a different action that shares withDeletionTargetOwnership.
	queueCtx := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(
		appwire.NewIntID(3), appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: ref, ClientMutationID: "resume-only-queue"}))
	if err := sessionActionRecoveryError(queueCtx, cfg, ref, "", epoch); !isSessionRecoveryAdmissionError(err) {
		t.Fatalf("turn/queue admission = %v, want the explicit-resume refusal while ResumeRequired stands", err)
	}
}

func TestTurnStartStillRefusedWhileStopInFlight(t *testing.T) {
	cfg := resumeOnlyRecoveryConfig(t)
	const id = "stop-in-flight-admission"
	// BeginForceStop without Finish leaves the Stop fence up (Stopping > 0).
	finish := cfg.ResumeLocks.BeginForceStop([]string{id})
	t.Cleanup(func() { finish.Finish(false) })
	if state := cfg.ResumeLocks.RecoveryState(id); state.Stopping == 0 {
		t.Fatalf("fixture did not stage the in-flight Stop state: %+v", state)
	}
	ref := "local:" + id
	epoch := sessionRecoveryState(cfg, ref, "").Epoch

	turnCtx := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodTurnStart, appwire.TurnStartParams{Ref: ref, ClientMutationID: "send-during-stop"}))
	if err := sessionActionRecoveryError(turnCtx, cfg, ref, "", epoch); !isSessionRecoveryAdmissionError(err) {
		t.Fatalf("turn/start admission = %v, want the explicit-resume refusal while a Stop is in flight", err)
	}
}

// The wire bit the client keys Send on. applyThreadResumeRequirement overlays
// ResumeRequired (and clears send) for FOUR different recovery causes, but the
// hub's turn/start carve-out admits only the one whose resume a send can fold
// into itself: ResumeRequired set, no Stop draining, the exit confirmed, and no
// connection fence. ResumeOnlyFoldable is true exactly there and false for the
// other three, which is what keeps a client from offering a Send the hub will
// refuse.
func TestApplyThreadResumeRequirementStampsResumeOnlyFoldable(t *testing.T) {
	t.Run("a confirmed stopped exit with the requirement set is foldable", func(t *testing.T) {
		cfg := resumeOnlyRecoveryConfig(t)
		const id = "foldable-confirmed-exit"
		stageResumeOnlyFenceConfirmedExit(t, cfg.ResumeLocks, id)
		ctx := admitSessionConnection(t.Context(), cfg)

		thread := applyThreadResumeRequirement(ctx, cfg, "local:"+id, "", appwire.Thread{})
		if !thread.Evener.ResumeRequired || thread.Evener.Capabilities.Send {
			t.Fatalf("overlay = %+v, want ResumeRequired set with send cleared", thread.Evener)
		}
		if !thread.Evener.ResumeOnlyFoldable {
			t.Fatalf("ResumeOnlyFoldable = false, want true for a confirmed stopped exit with the requirement set")
		}
	})

	t.Run("a Stop still draining is not foldable", func(t *testing.T) {
		cfg := resumeOnlyRecoveryConfig(t)
		const id = "foldable-stop-in-flight"
		finish := cfg.ResumeLocks.BeginForceStop([]string{id})
		t.Cleanup(func() { finish.Finish(false) })
		if state := cfg.ResumeLocks.RecoveryState(id); state.Stopping == 0 {
			t.Fatalf("fixture did not stage the in-flight Stop state: %+v", state)
		}
		ctx := admitSessionConnection(t.Context(), cfg)

		thread := applyThreadResumeRequirement(ctx, cfg, "local:"+id, "", appwire.Thread{})
		if !thread.Evener.ResumeRequired {
			t.Fatalf("overlay = %+v, want ResumeRequired set while a Stop drains", thread.Evener)
		}
		if thread.Evener.ResumeOnlyFoldable {
			t.Fatalf("ResumeOnlyFoldable = true while Stopping > 0, want false")
		}
	})

	t.Run("an unconfirmed force-stop exit is not foldable", func(t *testing.T) {
		cfg := resumeOnlyRecoveryConfig(t)
		const id = "foldable-unconfirmed-exit"
		stageResumeOnlyFence(t, cfg, id)
		ctx := admitSessionConnection(t.Context(), cfg)

		thread := applyThreadResumeRequirement(ctx, cfg, "local:"+id, "", appwire.Thread{})
		if !thread.Evener.ResumeRequired {
			t.Fatalf("overlay = %+v, want ResumeRequired set for the unconfirmed exit", thread.Evener)
		}
		if thread.Evener.ResumeOnlyFoldable {
			t.Fatalf("ResumeOnlyFoldable = true while ExitConfirmed is false, want false")
		}
	})

	t.Run("the connection-recovery fence is not foldable", func(t *testing.T) {
		cfg := resumeOnlyRecoveryConfig(t)
		const id = "foldable-stale-connection"
		// A connection admitted before the recovery is stale once the recovery
		// advances the sequence (sessionConnectionRecoveryError's freshness rule).
		staleCtx := admitSessionConnection(t.Context(), cfg)
		stageResumeOnlyFenceConfirmedExit(t, cfg.ResumeLocks, id)

		if err := sessionConnectionRecoveryError(staleCtx, cfg, "local:"+id, ""); err == nil {
			t.Fatalf("fixture did not stage the connection fence")
		}
		thread := applyThreadResumeRequirement(staleCtx, cfg, "local:"+id, "", appwire.Thread{})
		if !thread.Evener.ResumeRequired {
			t.Fatalf("overlay = %+v, want ResumeRequired set under the connection fence", thread.Evener)
		}
		if thread.Evener.ResumeOnlyFoldable {
			t.Fatalf("ResumeOnlyFoldable = true under the connection-recovery fence, want false")
		}
	})
}

// TestTurnStartDispatchesThroughResumeOnlyFence proves the carve-out is not just
// sessionActionRecoveryError's shape: the whole turn/start handler runs to the
// relay with ResumeRequired still set, i.e. the send is admitted and proceeds.
func TestTurnStartDispatchesThroughResumeOnlyFence(t *testing.T) {
	cfg := resumeOnlyRecoveryConfig(t)
	const id = "resume-only-dispatch"
	stageResumeOnlyFence(t, cfg, id)
	ref := "local:" + id

	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() { resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume })

	started := 0
	source := &scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:        id,
			SessionID: id,
			Source:    "local",
			Evener:    appwire.EvenerThread{Ref: ref, Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			started++
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_after_resume"}}, nil
		},
	}
	resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
		return source, nil
	}
	resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		t.Fatal("turn/start under the resume-only fence should not need the explicit resume retry")
		return appwire.ThreadResumeResponse{}, nil
	}

	server := newHubAppServer(cfg, appsource.NewRegistry())
	params := appwire.TurnStartParams{
		Ref:              ref,
		ClientMutationID: "resume-only-dispatch",
		Input:            []appwire.InputItem{{Type: "text", Text: "just send it"}},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	ctx := admitSessionConnection(admitSessionRecovery(t.Context(), cfg,
		appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodTurnStart, params)), cfg)
	resp, err := server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: appwire.MethodTurnStart, Params: raw})
	if err != nil {
		t.Fatalf("turn/start refused though only a resume was pending: %v", err)
	}
	if started != 1 {
		t.Fatalf("source.StartTurn calls = %d, want 1 (the send reached the relay)", started)
	}
	startResp, ok := resp.(appwire.TurnStartResponse)
	if !ok || startResp.Turn.ID != "turn_after_resume" {
		t.Fatalf("turn/start response = %#v, want the source's turn", resp)
	}
}

// stageResumeOnlyFenceConfirmedExit is stageResumeOnlyFence plus a proven
// process exit, the shape a completed Force stop leaves: ResumeRequired set,
// Stopping back to zero, ExitConfirmed true. A real resume launch requires the
// confirmed exit (resumeThreadLockedLaunch refuses an unconfirmed one), so the
// end-to-end test below needs it; the admission-only tests above do not.
func stageResumeOnlyFenceConfirmedExit(t *testing.T, locks *hubcore.ResumeLocks, id string) {
	t.Helper()
	finish := locks.BeginForceStop([]string{id})
	if err := locks.PersistForceStop([]string{id}, id); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	if err := locks.ConfirmForceStop(id); err != nil {
		t.Fatal(err)
	}
	state := locks.RecoveryState(id)
	if !state.ResumeRequired || state.Stopping != 0 || !state.ExitConfirmed {
		t.Fatalf("fixture did not stage the confirmed resume-only state: %+v", state)
	}
}

// TestTurnStartResumeOnlyFenceLaunchesDaemonAndDeliversTurn is the end-to-end
// shape the resume-only carve-out exists for, and the reviewer's High finding
// made into a test: a genuinely NOT-LIVE local session under the ResumeRequired
// fence. Sending a prompt must be admitted past sessionActionRecoveryError, the
// folded resume must run as an EXPLICIT resume (the automatic path refuses
// while ResumeRequired stands, so the prompt would otherwise never be
// delivered), and the prompt must reach the launched daemon.
//
// Unlike TestTurnStartDispatchesThroughResumeOnlyFence this drives a real
// spawner, roster and daemon - resolveTurnStartSource is not stubbed - so it
// exercises the resume path itself. With the resume left automatic the launch
// is refused ("session recovery requires a fresh explicit thread/resume") and
// the prompt is never delivered.
func TestTurnStartResumeOnlyFenceLaunchesDaemonAndDeliversTurn(t *testing.T) {
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	ref := "local:" + sessionID

	locks := hubcore.NewResumeLocks()
	stageResumeOnlyFenceConfirmedExit(t, locks, sessionID)

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, Capabilities: appwire.ThreadCapabilities{Send: true}}}}, nil
	})
	var gotPrompt string
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		gotPrompt = inputTextForTest(params.Input)
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_after_resume"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	resumeCalls := 0
	spawner := &fakeRPCSpawner{
		resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumeCalls++
			if req.WorkingDir != workingDir {
				t.Fatalf("resume request=%+v", req)
			}
			entry := rendezvous.Entry{
				PID:        108,
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws" + daemonHTTP.URL[len("http"):],
				SourceID:   "local",
				ThreadID:   sessionID,
				SessionID:  sessionID,
				WorkingDir: workingDir,
			}
			writeRendezvous(t, runDir, entry)
			return entry, nil
		},
	}
	roster := hubcore.NewRoster(runDir, nil)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Spawner: spawner, Past: past, ResumeLocks: locks})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: sessionID, Ref: ref, Input: []appwire.InputItem{{Type: "text", Text: "resume work"}}}); err != nil {
		t.Fatalf("turn/start under the resume-only fence: %v", err)
	}
	if gotPrompt != "resume work" {
		t.Fatalf("prompt=%q, want the send delivered after the folded resume", gotPrompt)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume launches=%d, want 1", resumeCalls)
	}
	if state := locks.RecoveryState(sessionID); state.ResumeRequired {
		t.Fatalf("the folded resume did not clear the fence: %+v", state)
	}
}
