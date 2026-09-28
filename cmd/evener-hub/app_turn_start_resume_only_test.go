package hub

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A session that merely needs resume no longer refuses turn/start: sending a
// prompt folds the resume into the send (app_relay.go's prepareRelay auto-
// resumes a not-live session). The carve-out is exactly turn/start and exactly
// ResumeRequired — a Stop still in flight keeps refusing every action, and so
// does every action other than turn/start while only the resume is pending.
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
