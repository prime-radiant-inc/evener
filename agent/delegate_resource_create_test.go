package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

func TestDelegateResourceCreate_IsolationFailurePublishesNothing(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected sandbox isolation failure")
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "sandbox_resolve" {
			return wantErr
		}
		return nil
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "remain unpublished",
		Sandbox:             "workspace-write",
		Background:          true,
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want isolation failure", result.Err)
	}
	root.delegateController.mu.Lock()
	defer root.delegateController.mu.Unlock()
	if len(root.delegateController.durable) != 0 || len(root.delegateController.live) != 0 || len(root.delegateController.reservations) != 0 {
		t.Fatalf("isolation failure published controller state: durable=%#v live=%#v reservations=%#v", root.delegateController.durable, root.delegateController.live, root.delegateController.reservations)
	}
	if root.delegateController.turnsInUse != 0 {
		t.Fatalf("isolation failure retained turn capacity = %d", root.delegateController.turnsInUse)
	}
}

func TestDelegateResourceCreate_StableRouteSkipsLegacyRetentionReservation(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	legacyReservationCalled := false
	root.cfg.testOnly.subagentReserveSlot = func(*Session) ([]*subagent, error) {
		legacyReservationCalled = true
		return nil, errors.New("legacy retained-session reservation invoked")
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "do not reclaim legacy sessions",
		Background:          true,
		DelegationAllowance: 0,
	})
	if legacyReservationCalled {
		t.Fatal("stable create invoked the legacy retained-session reservation seam")
	}
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
}

func TestDelegateResourceCreate_StableIdentityCommitsBeforeRuntimeLaunch(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected child construction failure")
	var committedID string
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point != "new_session" {
			return nil
		}
		root.delegateController.mu.Lock()
		defer root.delegateController.mu.Unlock()
		for id, aggregate := range root.delegateController.durable {
			if aggregate != nil && aggregate.CurrentRunOpen && aggregate.Generation == 1 {
				committedID = id
			}
		}
		return wantErr
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "commit before construction",
		Background:          true,
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want construction failure", result.Err)
	}
	if committedID == "" {
		t.Fatal("child construction began before a stable delegate generation was durable")
	}
	if result.DelegateID != committedID {
		t.Fatalf("returned delegate ID = %q, want committed stable ID %q", result.DelegateID, committedID)
	}
}

func TestDelegateResourceCreate_CommittedUpdatePrecedesConstruction(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	constructorEntered := make(chan struct{})
	releaseConstructor := make(chan struct{})
	createDone := make(chan delegateResult, 1)
	wantErr := errors.New("injected constructor barrier failure")
	var updatesMu sync.Mutex
	var updates []delegateUpdatePlan
	root.delegateController.mu.Lock()
	root.delegateController.emitUpdate = func(plan delegateUpdatePlan) {
		updatesMu.Lock()
		updates = append(updates, plan)
		updatesMu.Unlock()
	}
	root.delegateController.mu.Unlock()
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point != "new_session" {
			return nil
		}
		close(constructorEntered)
		<-releaseConstructor
		return wantErr
	}

	go func() {
		createDone <- root.createDelegate(context.Background(), delegateArgs{
			Task:                "publish before construction",
			Background:          true,
			DelegationAllowance: 0,
		})
	}()
	<-constructorEntered
	updatesMu.Lock()
	var committed delegateUpdatePlan
	if len(updates) != 0 {
		committed = updates[0]
	}
	updatesMu.Unlock()
	close(releaseConstructor)
	result := <-createDone
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want constructor failure", result.Err)
	}
	if len(committed.rows) != 1 {
		t.Fatalf("stable updates before construction = %#v, want one committed row", committed.rows)
	}
	row := committed.rows[0]
	if row.id != result.DelegateID || row.phase != delegatestore.PhaseRunning || row.lifecycle != delegateLifecycleRunning || !row.resumable {
		t.Fatalf("committed update = %#v, want running resumable delegate %q", row, result.DelegateID)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.Resumable {
		t.Fatalf("post-construction-failure aggregate = %#v, want closed and not resumable", aggregate)
	}
	if committed.rows[0].phase != delegatestore.PhaseRunning || !committed.rows[0].resumable {
		t.Fatalf("captured committed update changed after later failure: %#v", committed.rows[0])
	}
}

func TestDelegateResourceCreate_PostCommitConstructionFailureClosesResumability(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected permanent child construction failure")
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			return wantErr
		}
		return nil
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "fail after commit",
		Background:          true,
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want construction failure", result.Err)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.CurrentRunOpen || aggregate.Resumable {
		t.Fatalf("post-commit failure aggregate = %#v, want closed and not resumable", aggregate)
	}
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "construction_failed" {
		t.Fatalf("post-commit failure outcome = %#v, want failed/construction_failed", aggregate.LatestOutcome)
	}
	if aggregate.NotResumableReason != "construction_failed" {
		t.Fatalf("not-resumable reason = %q, want construction_failed", aggregate.NotResumableReason)
	}
}

func TestDelegateResourceCreate_PostCommitFailureRemainsInspectableAfterRestart(t *testing.T) {
	root, client, profile := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected permanent child construction failure")
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			return wantErr
		}
		return nil
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "remain inspectable",
		Background:          true,
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want construction failure", result.Err)
	}
	meta := root.Meta()
	if err := schema.SaveSessionMeta(root.stateDir, meta); err != nil {
		t.Fatal(err)
	}
	stateDir := root.stateDir
	workspace := root.currentEnv().WorkingDirectory()
	root.Close()

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer restored.Close()
	aggregate := delegateAggregateSnapshot(t, restored.delegateController, result.DelegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.Resumable || aggregate.NotResumableReason != "construction_failed" {
		t.Fatalf("restored failed delegate = %#v, want inspectable closed aggregate", aggregate)
	}
}

func TestDelegateResourceCreate_MissingRestoreInputsCloseResumabilityBeforeCleanup(t *testing.T) {
	root, client, profile := newDelegateResourceBootstrapSession(t)
	descriptor := task6DelegateDescriptor("missing restore transcript")
	descriptor.Isolation = "worktree"
	reservation, err := root.delegateController.ReserveCreate(rootDelegateActor(root.ID()), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	if err := os.MkdirAll(reservation.worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(reservation.worktreePath, "retained-artifact")
	if err := os.WriteFile(sentinelPath, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	meta := root.Meta()
	if err := schema.SaveSessionMeta(root.stateDir, meta); err != nil {
		t.Fatal(err)
	}
	stateDir := root.stateDir
	workspace := root.currentEnv().WorkingDirectory()
	root.Close()

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer restored.Close()
	aggregate := delegateAggregateSnapshot(t, restored.delegateController, started.lease.delegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.Resumable || aggregate.NotResumableReason != notResumableMissingChildSessionMeta {
		t.Fatalf("missing-input aggregate = %#v, want closed/%s", aggregate, notResumableMissingChildSessionMeta)
	}
	if got, err := os.ReadFile(sentinelPath); err != nil || string(got) != "retain" {
		t.Fatalf("artifact was cleaned before durable resumability closure: bytes=%q err=%v", got, err)
	}
}

func TestDelegateResourceCreate_ResumabilityAppendFailureDestroysNothing(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected permanent child construction failure")
	var artifactPath string
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point != "new_session" {
			return nil
		}
		root.delegateController.mu.Lock()
		for _, aggregate := range root.delegateController.durable {
			if aggregate != nil && aggregate.CurrentRunOpen {
				artifactPath = filepath.Join(root.stateDir, sessionsSubdir, aggregate.Descriptor.ChildSessionID+".transcript.jsonl")
				break
			}
		}
		root.delegateController.mu.Unlock()
		if artifactPath != "" {
			if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
				t.Fatalf("create artifact parent: %v", err)
			}
			if err := os.WriteFile(artifactPath, []byte("retained exact artifact"), 0o600); err != nil {
				t.Fatalf("create artifact: %v", err)
			}
		}
		if err := root.delegateController.store.Close(); err != nil {
			t.Fatalf("close delegate store: %v", err)
		}
		return wantErr
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "retain after append failure",
		Background:          true,
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) || !strings.Contains(result.Err.Error(), "store is closed") {
		t.Fatalf("createDelegate error = %v, want construction and closure-append failures", result.Err)
	}
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[result.DelegateID]
	live := root.delegateController.live[result.DelegateID]
	turnsInUse := root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseRunning || !aggregate.CurrentRunOpen {
		t.Fatalf("append-failed aggregate = %#v, want exact running generation retained", aggregate)
	}
	if live == nil || live.binding == nil || !live.recoveryRequired || turnsInUse != 1 {
		t.Fatalf("append-failed live state = %#v capacity=%d, want fenced binding and retained capacity", live, turnsInUse)
	}
	if got, err := os.ReadFile(artifactPath); err != nil || string(got) != "retained exact artifact" {
		t.Fatalf("append failure destroyed exact artifact: bytes=%q err=%v", got, err)
	}
}

func TestDelegateResourceCreate_StopBeforeAttachDisposesUnadoptedSession(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime, started, isolation, prepared := prepareCommittedUnadoptedDelegate(t, root, "stop before parent attach")
	_, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), started.lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	result := runtime.failCommittedStart(started, isolation, prepared, true, context.Canceled, "construction_failed")
	if result.DelegateID != started.lease.delegateID {
		t.Fatalf("failed create delegate ID = %q, want %q", result.DelegateID, started.lease.delegateID)
	}
	if got := root.subagents.get(prepared.sub.id); got != nil {
		t.Fatalf("stop-winning unadopted child was inserted into parent manager: result_err=%v disposition=%d child=%#v", result.Err, committedStartFailureDisposition(result.Err), got)
	}
	if got := prepared.sub.sess.State(); got != SessionClosed {
		t.Fatalf("stop-winning unadopted child state = %q, want %q", got, SessionClosed)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, started.lease.delegateID)
	if !aggregate.Resumable || aggregate.CurrentRunOpen {
		t.Fatalf("stop-winning delegate = %#v, want settled with durable resumability retained", aggregate)
	}
	root.delegateController.mu.Lock()
	live := root.delegateController.live[started.lease.delegateID]
	var resident *Session
	if live != nil {
		resident = live.runtime
	}
	root.delegateController.mu.Unlock()
	if resident != nil {
		t.Errorf("stop-winning delegate retained runtime %p in state %q, want no closed controller runtime", resident, resident.State())
	}

	if _, err := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController)); err != nil {
		t.Fatalf("complete stop: %v", err)
	}
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.ID()), started.lease.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart replacement: %v", err)
	}
	replacementStart, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart replacement: %v", err)
	}
	replacement := newTestSession(t)
	if err := root.delegateController.AttachRuntime(replacementStart.lease, replacement); err != nil {
		t.Fatalf("AttachRuntime replacement after stopped-start cleanup: %v", err)
	}
}

func TestDelegateResourceCreate_StopSettlementTransfersRuntimeBeforeUpdateEmission(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime, started, isolation, prepared := prepareCommittedUnadoptedDelegate(t, root, "stop settlement runtime transfer")
	_, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), started.lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	updateEntered := make(chan struct{})
	releaseUpdate := make(chan struct{})
	var updateOnce sync.Once
	root.delegateController.mu.Lock()
	root.delegateController.emitUpdate = func(delegateUpdatePlan) {
		updateOnce.Do(func() { close(updateEntered) })
		<-releaseUpdate
	}
	root.delegateController.mu.Unlock()

	constructionErr := errors.New("construction failed after controller attachment")
	resultCh := make(chan delegateResult, 1)
	go func() {
		resultCh <- runtime.failCommittedStart(started, isolation, prepared, true, constructionErr, "construction_failed")
	}()
	<-updateEntered

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseUpdate) })
	}
	t.Cleanup(release)

	root.delegateController.mu.Lock()
	live := root.delegateController.live[started.lease.delegateID]
	var resident *Session
	if live != nil {
		resident = live.runtime
	}
	root.delegateController.mu.Unlock()
	if resident != nil {
		t.Errorf("controller retained stopped unadopted runtime %p before update emission, want close ownership already transferred", resident)
	}

	_, reconcileErr := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController))
	var reserveErr, commitErr, attachErr error
	var replacement *Session
	if reconcileErr == nil {
		var reservation *delegateStartReservation
		reservation, reserveErr = root.delegateController.ReserveStart(rootDelegateActor(root.ID()), started.lease.delegateID)
		if reserveErr == nil {
			var replacementStart delegateStartCommit
			replacementStart, commitErr = root.delegateController.CommitStart(reservation)
			if commitErr == nil {
				replacement = newTestSession(t)
				attachErr = root.delegateController.AttachRuntime(replacementStart.lease, replacement)
			}
		}
	}
	release()
	result := <-resultCh

	if reconcileErr != nil {
		t.Errorf("complete stop while cleanup blocked: %v", reconcileErr)
	}
	if reserveErr != nil {
		t.Errorf("ReserveStart replacement while cleanup blocked: %v", reserveErr)
	}
	if commitErr != nil {
		t.Errorf("CommitStart replacement while cleanup blocked: %v", commitErr)
	}
	if attachErr != nil {
		t.Errorf("AttachRuntime replacement while cleanup blocked: %v", attachErr)
	}
	if replacement == nil && reconcileErr == nil && reserveErr == nil && commitErr == nil {
		t.Error("replacement runtime was not constructed")
	}
	if !errors.Is(result.Err, constructionErr) {
		t.Errorf("failed create error = %v, want %v", result.Err, constructionErr)
	}
	if got := prepared.sub.sess.State(); got != SessionClosed {
		t.Errorf("stop-winning unadopted child state = %q after cleanup, want %q", got, SessionClosed)
	}
}

func TestDelegateResourceCreate_CloseAfterDrainRefusesFailedStartRetention(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime, started, isolation, prepared := prepareCommittedUnadoptedDelegate(t, root, "close after manager drain")
	root.subagents.drainForClose()
	if err := root.delegateController.store.Close(); err != nil {
		t.Fatalf("close delegate store: %v", err)
	}

	constructionErr := errors.New("construction failed after controller attachment")
	result := runtime.failCommittedStart(started, isolation, prepared, true, constructionErr, "construction_failed")
	if !errors.Is(result.Err, constructionErr) || !strings.Contains(result.Err.Error(), "store is closed") {
		t.Fatalf("failed create error = %v, want construction and append failures", result.Err)
	}
	if got := root.subagents.get(prepared.sub.id); got != nil {
		t.Fatalf("late failed-start retention escaped the close drain: %#v", got)
	}
	if got := prepared.sub.sess.State(); got != SessionClosed {
		t.Fatalf("late failed-start candidate state = %q, want %q", got, SessionClosed)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, started.lease.delegateID)
	root.delegateController.mu.Lock()
	live := root.delegateController.live[started.lease.delegateID]
	root.delegateController.mu.Unlock()
	if aggregate.Phase != delegatestore.PhaseRunning || !aggregate.CurrentRunOpen || !aggregate.Resumable || live == nil || !live.recoveryRequired {
		t.Fatalf("append-failed delegate = aggregate %#v live %#v, want fenced exact generation", aggregate, live)
	}
	for _, path := range []string{
		started.transcriptPath,
		filepath.Join(root.stateDir, sessionsSubdir, prepared.sub.id+".meta.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("durable isolation artifact %s was not retained: %v", path, err)
		}
	}
}

func prepareCommittedUnadoptedDelegate(t *testing.T, root *Session, task string) (delegateRuntime, delegateStartCommit, delegateIsolation, *preparedSubagentRun) {
	t.Helper()
	runtime := delegateRuntime{owner: root}
	ctx := context.Background()
	args := delegateArgs{Task: task, Background: true, DelegationAllowance: 0}
	selection, err := root.selectSubagentModel(ctx, args.Model, args.AgentType)
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	descriptor, project, err := runtime.describe(ctx, args, task, "", nil, selection)
	if err != nil {
		t.Fatalf("describe delegate: %v", err)
	}
	reservation, err := root.delegateController.ReserveCreate(rootDelegateActor(root.ID()), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	isolation, err := runtime.prepareIsolation(ctx, reservation, project, nil)
	if err != nil {
		t.Fatalf("prepareIsolation: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	prepared, err := runtime.construct(ctx, args, selection, started, isolation)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	t.Cleanup(prepared.disposeUnadopted)
	if err := root.delegateController.AttachRuntime(started.lease, prepared.sub.sess); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	return runtime, started, isolation, prepared
}

func TestDelegateResourceCreate_DescendantEventCallbackSurvivesSpawnConfig(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	observed := make(chan events.SessionEvent, 1)
	var callbackMu sync.Mutex
	targetSessionID := ""
	root.SetDescendantEventFunc(func(event events.SessionEvent) {
		callbackMu.Lock()
		target := targetSessionID
		callbackMu.Unlock()
		warning, ok := event.Data.(events.WarningData)
		if event.Kind == events.EventWarning && event.SessionID == target && ok && warning.Message == "task6 descendant sentinel" {
			observed <- event
		}
	})
	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "preserve descendant callback",
		Background:          true,
		DelegationAllowance: 0,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	children := root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("tracked child count = %d, want 1", len(children))
	}
	child := children[0]
	if child.descendantEvent == nil {
		t.Fatal("child lost root descendant-event callback")
	}
	callbackMu.Lock()
	targetSessionID = child.ID()
	callbackMu.Unlock()
	want := events.SessionEvent{Kind: events.EventWarning, SessionID: child.ID(), Data: events.WarningData{Message: "task6 descendant sentinel"}}
	child.descendantEvent(want)
	select {
	case got := <-observed:
		if got.Kind != want.Kind || got.SessionID != want.SessionID {
			t.Fatalf("descendant callback event = %#v, want %#v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("descendant callback did not receive child event")
	}
}

func TestDelegateResourceCreate_ChildTranscriptIsPreseededBeforeRun(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	adapter := newTask6TranscriptBarrierAdapter()
	t.Cleanup(adapter.releaseRun)
	client := llm.NewClient()
	client.Register(adapter)
	root, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)

	const task = "preseed this exact task"
	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                task,
		Background:          true,
		DelegationAllowance: 0,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	var childID string
	select {
	case childID = <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not reached")
	}
	_, entries, _, err := readTranscript(filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl"))
	if err != nil {
		t.Fatalf("read child transcript at provider boundary: %v", err)
	}
	matches := 0
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnUserInput && entry.Turn.Message.Text() == task {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("child transcript at provider boundary has %d exact user inputs, want 1: %#v", matches, entries)
	}
	adapter.releaseRun()
}

func TestDelegateResourceCreate_InputTranscriptAppendRunsAfterControllerUnlock(t *testing.T) {
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, controller, "dlg_target", "")
	started, _ := commitAttachedDelegateControllerStart(t, controller, "dlg_target")
	claim, err := controller.BeginStartInput(started.lease)
	if err != nil {
		t.Fatalf("begin initial transcript input: %v", err)
	}
	unlocked := controller.mu.TryLock()
	if unlocked {
		controller.mu.Unlock()
	}
	if !unlocked {
		t.Fatal("initial transcript append ran while the delegate controller mutex was held")
	}
	if _, err := controller.CompleteStartInput(claim, true, delegateFinish{}); err != nil {
		t.Fatalf("complete initial transcript input: %v", err)
	}
}

func TestDelegateResourceCreate_RegisteredToolReturnsOnlyStableDelegateIdentity(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	result := executeTask6RegisteredDelegate(t, context.Background(), root, "registered stable identity", 0)

	delegateID, _ := result["delegate_id"].(string)
	if err := identifier.ValidateDelegateID(delegateID); err != nil {
		t.Fatalf("registered delegate_id = %q: %v", delegateID, err)
	}
	childSessionID, _ := result["child_session_id"].(string)
	if err := identifier.ValidateSessionID(childSessionID); err != nil {
		t.Fatalf("registered child_session_id = %q: %v", childSessionID, err)
	}
	if transcriptRef, _ := result["transcript_ref"].(string); transcriptRef != encodeRef("", childSessionID) {
		t.Fatalf("registered transcript_ref = %q, want child transcript reference", transcriptRef)
	}
	if got := result["type"]; got != "delegate" {
		t.Fatalf("registered type = %#v, want delegate", got)
	}
	for _, forbidden := range []string{
		"job_id", "started_job_id", "latest_job_id", "current_job_id", "activation_job_id",
		"output", "truncated", "structured_result", "running_in_background", "timed_out",
	} {
		if value, exists := result[forbidden]; exists {
			t.Fatalf("registered create returned activation field %q=%#v", forbidden, value)
		}
	}
}

func TestDelegateResourceCreate_RegisteredToolUsesRootController(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantID := identifier.MustNewDelegateID()
	root.delegateController.mu.Lock()
	root.delegateController.newDelegateID = func() string { return wantID }
	root.delegateController.mu.Unlock()

	result := executeTask6RegisteredDelegate(t, context.Background(), root, "registered root controller", 0)
	if got, _ := result["delegate_id"].(string); got != wantID {
		t.Fatalf("registered delegate_id = %q, want controller-minted %q", got, wantID)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, wantID)
	if aggregate.Descriptor.OwnerSessionID != root.ID() {
		t.Fatalf("registered owner session = %q, want root %q", aggregate.Descriptor.OwnerSessionID, root.ID())
	}
	for _, record := range root.jobManager.list(listFilter{IncludeNested: true}) {
		if record.Type == jobstore.JobDelegate {
			t.Fatalf("registered stable create wrote delegate JobRecord %#v", record)
		}
	}
}

func TestDelegateResourceCreate_RegisteredNestedCreateUsesCurrentLease(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	parentResult := executeTask6RegisteredDelegate(t, context.Background(), root, "registered parent", 1)
	parentID, _ := parentResult["delegate_id"].(string)
	parentChildID, _ := parentResult["child_session_id"].(string)

	root.delegateController.mu.Lock()
	parentLive := root.delegateController.live[parentID]
	if parentLive == nil || parentLive.binding == nil {
		root.delegateController.mu.Unlock()
		t.Fatalf("registered parent %q has no live generation binding", parentID)
	}
	parentLease := parentLive.binding.lease
	root.delegateController.mu.Unlock()
	parent := root.subagents.get(parentChildID)
	if parent == nil || parent.sess == nil {
		t.Fatalf("registered parent child session %q is not retained", parentChildID)
	}
	leaseContext := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, parentLease)
	childResult := executeTask6RegisteredDelegate(t, leaseContext, parent.sess, "registered nested child", 0)
	childID, _ := childResult["delegate_id"].(string)

	aggregate := delegateAggregateSnapshot(t, root.delegateController, childID)
	if aggregate.Descriptor.ParentDelegateID != parentID {
		t.Fatalf("nested parent delegate = %q, want current lease owner %q", aggregate.Descriptor.ParentDelegateID, parentID)
	}
	if aggregate.Descriptor.OwnerSessionID != root.ID() {
		t.Fatalf("nested owner session = %q, want root %q", aggregate.Descriptor.OwnerSessionID, root.ID())
	}
}

func executeTask6RegisteredDelegate(t *testing.T, ctx context.Context, session *Session, task string, allowance int) map[string]any {
	t.Helper()
	arguments := map[string]any{"task": task}
	if allowance != 0 {
		arguments["delegation_allowance"] = allowance
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	call := session.reg.ExecuteCall(ctx, session.currentEnv(), llm.ToolCallData{
		ID:        "task6-registered-" + strings.ReplaceAll(task, " ", "-"),
		Name:      "delegate",
		Arguments: raw,
	})
	if call.IsError {
		t.Fatalf("registered delegate returned error: %s", call.Output)
	}
	var result map[string]any
	if err := json.Unmarshal(toolResultJSON(call), &result); err != nil {
		t.Fatalf("decode registered delegate output %q: %v", call.Output, err)
	}
	return result
}

func task6DelegateDescriptor(task string) delegatestore.Descriptor {
	return delegatestore.Descriptor{
		Task:              task,
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		Resumable:         true,
	}
}

func delegateAggregateSnapshot(t *testing.T, controller *delegateTreeController, delegateID string) delegatestore.Aggregate {
	t.Helper()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	aggregate := controller.durable[delegateID]
	if aggregate == nil {
		t.Fatalf("delegate %q is absent from stable controller", delegateID)
	}
	return *aggregate
}

type task6TranscriptBarrierAdapter struct {
	entered     chan string
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newTask6TranscriptBarrierAdapter() *task6TranscriptBarrierAdapter {
	return &task6TranscriptBarrierAdapter{
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
}

func (a *task6TranscriptBarrierAdapter) Name() string { return "openai" }

func (a *task6TranscriptBarrierAdapter) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	a.enteredOnce.Do(func() { a.entered <- request.SessionID })
	select {
	case <-a.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Provider: "openai", Model: request.Model, Message: llm.Assistant("done")}, nil
}

func (a *task6TranscriptBarrierAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *task6TranscriptBarrierAdapter) releaseRun() {
	a.releaseOnce.Do(func() { close(a.release) })
}
