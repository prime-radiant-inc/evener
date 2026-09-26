package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestForceStopInvalidatesQueuedOwnedChild(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	parent := buildRPCParentSession(t, stateDir)
	child := buildUpgradeDelegate(t, stateDir, parent)
	fork, err := agent.ForkSession(stateDir, parent, 1, "fork", "")
	if err != nil {
		t.Fatal(err)
	}
	locks := hubcore.NewResumeLocks()
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) { return nil, daemonprocess.ErrExited })}
	ctx := admitSessionConnection(t.Context(), cfg)
	epoch := sessionRequestRecoveryEpoch(ctx, cfg, "local:"+child, "")
	forkEpoch := sessionRequestRecoveryEpoch(ctx, cfg, "local:"+fork, "")
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir})
	if err := forceStopThread(context.Background(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil); err != nil {
		t.Fatal(err)
	}
	if err := sessionActionRecoveryError(ctx, cfg, "local:"+child, "", epoch); err == nil {
		t.Fatal("queued child action survived owner force stop")
	}
	if err := sessionActionRecoveryError(ctx, cfg, "local:"+fork, "", forkEpoch); err != nil {
		t.Fatalf("independent fork fenced: %v", err)
	}
	launches := 0
	cfg.Spawner = &fakeRPCSpawner{resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
		launches++
		if req.SessionID != child {
			t.Errorf("child redirected to %s", req.SessionID)
		}
		return rendezvous.Entry{}, errors.New("launcher observed")
	}}
	if _, err := hubThreadResume(ctx, cfg, nil, appwire.ThreadResumeParams{Ref: "local:" + child}); !isSessionRecoveryAdmissionError(err) {
		t.Fatalf("stale resume error=%v", err)
	}
	if launches != 0 {
		t.Fatal("stale resume launched")
	}
	fresh := admitSessionConnection(t.Context(), cfg)
	if err := sessionActionRecoveryError(fresh, cfg, "local:"+child, "", locks.RecoveryState(child).Epoch); err != nil {
		t.Fatalf("fresh child intent blocked: %v", err)
	}
	_, _ = hubThreadResume(fresh, cfg, nil, appwire.ThreadResumeParams{Ref: "local:" + child})
	if launches != 1 {
		t.Fatalf("fresh child resume launches=%d", launches)
	}
	if state := locks.RecoveryState(child); state.ResumeRequired || state.ResumeSessionID != "" {
		t.Fatalf("child acquired root resume authority: %+v", state)
	}

}

func TestForceStopFencesDelegateCreatedDuringTermination(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	parent := buildRPCParentSession(t, stateDir)
	locks := hubcore.NewResumeLocks()
	var events []string
	var child string
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			return &forceStopProcess{events: &events, onWait: func() { child = buildUpgradeDelegate(t, stateDir, parent) }}, nil
		})}
	old := admitSessionConnection(t.Context(), cfg)
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir})
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil); err != nil {
		t.Fatal(err)
	}
	if child == "" {
		t.Fatal("fixture did not create child")
	}
	if err := sessionConnectionRecoveryError(old, cfg, "local:"+child, ""); err == nil {
		t.Fatal("late child escaped recovery fence")
	}
}

func TestForceStopFencesDelegateAfterFailedExitConfirmation(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	parent := buildRPCParentSession(t, stateDir)
	locks := hubcore.NewResumeLocks()
	var events []string
	var child string
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			return &forceStopProcess{events: &events, waitErr: context.DeadlineExceeded, onWait: func() { child = buildUpgradeDelegate(t, stateDir, parent) }}, nil
		})}
	old := admitSessionConnection(t.Context(), cfg)
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir})
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil); err == nil {
		t.Fatal("expected exit confirmation failure")
	}
	if child == "" {
		t.Fatal("fixture did not create child")
	}
	if err := sessionConnectionRecoveryError(old, cfg, "local:"+child, ""); err == nil {
		t.Fatal("late child escaped recovery fence")
	}
}

func TestForceStopFencesDelegateCreatedAfterFailedConfirmation(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	parent := buildRPCParentSession(t, stateDir)
	locks := hubcore.NewResumeLocks()
	var events []string
	var child string
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			return &forceStopProcess{events: &events, waitErr: context.DeadlineExceeded, onWait: func() {}}, nil
		})}
	old := admitSessionConnection(t.Context(), cfg)
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir})
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil); err == nil {
		t.Fatal("expected exit confirmation failure")
	}
	child = buildUpgradeDelegate(t, stateDir, parent)
	if child == "" {
		t.Fatal("fixture did not create child")
	}
	if err := sessionConnectionRecoveryError(old, cfg, "local:"+child, ""); err == nil {
		t.Fatal("late child escaped recovery fence")
	}
}

func TestForceStopConfirmsExitedOwnerWithTornDelegateTail(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	parent := buildRPCParentSession(t, stateDir)
	child := buildUpgradeDelegate(t, stateDir, parent)
	path := filepath.Join(stateDir, "sessions", parent, "delegates.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"events\":["); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	locks := hubcore.NewResumeLocks()
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) { return nil, daemonprocess.ErrExited })}
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir})
	for range 2 {
		old := admitSessionConnection(t.Context(), cfg)
		if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil); err != nil {
			t.Fatal(err)
		}
		if err := sessionConnectionRecoveryError(old, cfg, "local:"+child, ""); err == nil {
			t.Fatal("committed child escaped fence")
		}
	}
}

func TestForceStopResumedDelegateFencesHistoricalDescendants(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	root := buildRPCParentSession(t, stateDir)
	resumed := buildUpgradeDelegate(t, stateDir, root)
	seq := 1
	addDelegate := func(parent, parentEdge, edge string) string {
		t.Helper()
		child, err := agent.ForkSession(stateDir, parent, 1, "nested delegate", "")
		if err != nil {
			t.Fatal(err)
		}
		meta, err := schema.LoadSessionMeta(stateDir, child)
		if err != nil {
			t.Fatal(err)
		}
		meta.IsSubagent, meta.JobTreeRootSessionID = true, root
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
		seq++
		descriptor := map[string]any{"child_session_id": child, "transcript_ref": "local:" + child, "owner_session_id": root, "parent_delegate_id": parentEdge, "task": "nested", "agent_type": "explorer", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}
		batch, err := json.Marshal(map[string]any{"events": []map[string]any{{"kind": "delegate_created", "seq": seq, "delegate_id": edge, "created": map[string]any{"descriptor": descriptor}}}})
		if err != nil {
			t.Fatal(err)
		}
		journal, err := os.OpenFile(filepath.Join(stateDir, "sessions", root, "delegates.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.Write(append(batch, '\n')); err != nil {
			t.Fatal(err)
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
		return child
	}
	child := addDelegate(resumed, "dlg_upgrade", "dlg_nested")
	grandchild := addDelegate(child, "dlg_nested", "dlg_deep")
	sibling := addDelegate(root, "", "dlg_sibling")
	ids, err := agent.SessionOwnedDelegateIDs(t.Context(), stateDir, resumed)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{child, grandchild}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("resumed owner descendants=%v, want %v", ids, want)
	}
	locks := hubcore.NewResumeLocks()
	var events []string
	var late string
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return &forceStopProcess{events: &events, onWait: func() { late = addDelegate(child, "dlg_nested", "dlg_late") }}, nil
	})}
	old := admitSessionConnection(t.Context(), cfg)
	epochs := map[string]uint64{}
	for _, id := range want {
		epochs[id] = sessionRequestRecoveryEpoch(old, cfg, "local:"+id, "")
	}
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: resumed, ThreadID: resumed, StateDir: stateDir})
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + resumed}, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range want {
		if locks.RecoveryState(id).Epoch == epochs[id] {
			t.Fatal("historical descendant admission epoch did not advance")
		}
		if err := sessionActionRecoveryError(old, cfg, "local:"+id, "", epochs[id]); err == nil {
			t.Fatal("queued historical descendant survived force stop")
		}
	}
	if late == "" {
		t.Fatal("late descendant not created")
	}
	if err := sessionConnectionRecoveryError(old, cfg, "local:"+late, ""); err == nil {
		t.Fatal("late descendant escaped resumed owner fence")
	}
	for _, id := range []string{root, sibling} {
		if locks.RecoveryState(id).Epoch != 0 {
			t.Fatal("unrelated controller or sibling was fenced")
		}
		if err := sessionConnectionRecoveryError(old, cfg, "local:"+id, ""); err != nil {
			t.Fatalf("unrelated session rejected: %v", err)
		}
	}
}

func TestForceStopFailedTerminationBlocksFreshDescendantActions(t *testing.T) {
	for _, failure := range []string{"kill", "wait", "late"} {
		t.Run(failure, func(t *testing.T) {
			stateDir, runDir, recoveryDir := t.TempDir(), t.TempDir(), t.TempDir()
			parent := buildRPCParentSession(t, stateDir)
			child := ""
			if failure != "late" {
				child = buildUpgradeDelegate(t, stateDir, parent)
			}
			locks, err := hubcore.NewPersistentResumeLocks(recoveryDir)
			if err != nil {
				t.Fatal(err)
			}
			var events []string
			process := &forceStopProcess{events: &events}
			if failure == "kill" {
				process.killErr = errors.New("signal failed")
			} else {
				process.waitErr = context.DeadlineExceeded
			}
			cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks,
				DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) { return process, nil })}
			writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir})
			if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil); err == nil {
				t.Fatal("expected failed termination")
			}
			if failure == "late" {
				child = buildUpgradeDelegate(t, stateDir, parent)
			}
			for _, restart := range []bool{false, true} {
				if restart {
					cfg.ResumeLocks, err = hubcore.NewPersistentResumeLocks(recoveryDir)
					if err != nil {
						t.Fatal(err)
					}
				}
				fresh := admitSessionConnection(t.Context(), cfg)
				called := false
				_, err := withSessionActionOwnership(fresh, cfg, "local:"+child, "", func() (bool, error) { called = true; return true, nil })
				if called || !isSessionRecoveryAdmissionError(err) {
					t.Fatalf("restart=%v descendant action ran=%v error=%v", restart, called, err)
				}
				if state := cfg.ResumeLocks.RecoveryState(child); state.ResumeSessionID != "" {
					t.Fatalf("child acquired root identity: %+v", state)
				}
			}
			process.killErr, process.waitErr = nil, nil
			if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil); err != nil {
				t.Fatal(err)
			}
			cfg.ResumeLocks, err = hubcore.NewPersistentResumeLocks(recoveryDir)
			if err != nil {
				t.Fatal(err)
			}
			fresh := admitSessionConnection(t.Context(), cfg)
			if err := sessionActionRecoveryError(fresh, cfg, "local:"+child, "", cfg.ResumeLocks.RecoveryState(child).Epoch); err != nil {
				t.Fatalf("confirmed stop still blocks child: %v", err)
			}
			if !cfg.ResumeLocks.RecoveryState(parent).ResumeRequired {
				t.Fatal("exit confirmation acknowledged root Resume")
			}

		})
	}
}

// TestForceStopRefusalRejectsDescendantFences is the Medium RoboRev reported
// against the descendant fence lifecycle: fenceDescendants always finished
// normally, even when the request was refused before any cancellation (an
// identity or deletion conflict under the fence), so the refusal advanced
// descendant admission epochs and connection sequences and invalidated
// existing clients although the refusal canceled nothing. A refusal that has
// canceled nothing must reject the descendant fences it installed; they finish
// normally only once the stop committed or termination was attempted.
func TestForceStopRefusalRejectsDescendantFences(t *testing.T) {
	for _, refusal := range []string{"deletion conflict", "identity conflict"} {
		t.Run(refusal, func(t *testing.T) {
			stateDir, runDir := t.TempDir(), t.TempDir()
			parent := buildRPCParentSession(t, stateDir)
			child := buildUpgradeDelegate(t, stateDir, parent)
			entry := rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir, StartedAt: time.Now()}
			writeRendezvous(t, runDir, entry)
			locks := hubcore.NewResumeLocks()
			store, err := hubcore.NewDeletionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			original := deletionTargetState
			calls := 0
			deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
				calls++
				if refusal == "deletion conflict" {
					// The request's entry fence check passes; the deletion record
					// is published before the validation that runs under the alias
					// reservations, after the descendant fences exist.
					return hubcore.DeletionStateDeleting, calls > 1
				}
				// The identity revalidation under the fence must observe a
				// replacement claim: rewrite the entry's identity after the
				// descendant fences exist, before the revalidation.
				if calls == 2 {
					entry.StartedAt = entry.StartedAt.Add(time.Minute)
					writeRendezvous(t, runDir, entry)
				}
				return hubcore.DeletionStateDeleting, false
			}
			defer func() { deletionTargetState = original }()
			var events []string
			cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks, DeletionStore: store,
				DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
					events = append(events, "open")
					return &forceStopProcess{events: &events}, nil
				})}
			params := appwire.ThreadForceStopParams{Ref: "local:" + parent}
			if refusal == "identity conflict" {
				expected := daemonIdentity(entry)
				params.ExpectedDaemon = &expected
			}
			before := locks.RecoveryState(child)
			stopped := forceStopThread(t.Context(), cfg, params, nil)
			if stopped == nil {
				t.Fatal("force stop with a pre-cancellation refusal succeeded")
			}
			if refusal == "deletion conflict" && !isTargetDeletedError(stopped) {
				t.Fatalf("force stop error = %v, want the target-deleted refusal", stopped)
			}
			if after := locks.RecoveryState(child); after != before {
				t.Fatalf("refused force stop left the descendant fence applied: before=%+v, after=%+v", before, after)
			}
			if slices.Contains(events, "kill") {
				t.Fatalf("a refused force stop reached process control: %v", events)
			}
		})
	}
}

// TestForceStopPostDrainRefusalRejectsPreCancellationDescendantFences is the
// Medium RoboRev reported against the post-drain refusal path: refuseStop
// rejected only the main recovery fence when the drain canceled nothing, so
// the descendant fences installed before the cancellation stayed advanced and
// a refused stop permanently staled existing descendant clients — their Epoch
// and connection-level recovery sequence kept the advance the refusal should
// have rolled back, exactly as TestForceStopRefusedAfterUncanceledDrainRollsBackFence
// pins for the main fence. A post-drain refusal that canceled nothing must
// reject the fences installed before the drain alongside its own; fences the
// post-termination scan installs later stay non-rejected.
func TestForceStopPostDrainRefusalRejectsPreCancellationDescendantFences(t *testing.T) {
	stateDir, runDir := t.TempDir(), t.TempDir()
	parent := buildRPCParentSession(t, stateDir)
	child := buildUpgradeDelegate(t, stateDir, parent)
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: parent, ThreadID: parent, StateDir: stateDir, StartedAt: time.Now()})
	locks := hubcore.NewResumeLocks()
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := deletionTargetState
	calls := 0
	// The request's entry fence check (call 1) and the pre-cancellation check
	// under the alias reservations (call 2) pass; the deletion publishes after
	// the uncanceled drain, so the post-ownership re-validation (call 3)
	// refuses through refuseStop.
	deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
		calls++
		return hubcore.DeletionStateDeleting, calls > 2
	}
	defer func() { deletionTargetState = original }()
	var events []string
	cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, ResumeLocks: locks, DeletionStore: store,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			events = append(events, "open")
			return &forceStopProcess{events: &events}, nil
		})}
	beforeParent, beforeChild := locks.RecoveryState(parent), locks.RecoveryState(child)
	stopped := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + parent}, nil)
	if stopped == nil {
		t.Fatal("force stop with a post-drain deletion succeeded")
	}
	if !isTargetDeletedError(stopped) {
		t.Fatalf("force stop error = %v, want the target-deleted refusal", stopped)
	}
	if after := locks.RecoveryState(child); after != beforeChild {
		t.Fatalf("refused force stop left the descendant fence applied: before=%+v, after=%+v", beforeChild, after)
	}
	if after := locks.RecoveryState(parent); after != beforeParent {
		t.Fatalf("refused force stop left the recovery fence applied: before=%+v, after=%+v", beforeParent, after)
	}
	if slices.Contains(events, "kill") {
		t.Fatalf("a refused force stop reached process control: %v", events)
	}
}
